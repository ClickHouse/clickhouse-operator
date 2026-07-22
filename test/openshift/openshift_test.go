package openshift

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	catalogManifests = `apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: clickhouse-operator-catalog
  namespace: %[1]s
spec:
  sourceType: grpc
  image: %[2]s
  displayName: ClickHouse Operator Catalog
---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: clickhouse-operator
  namespace: %[1]s
spec:
  targetNamespaces:
    - %[1]s
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: clickhouse-operator
  namespace: %[1]s
spec:
  channel: %[3]s
  name: clickhouse-operator
  source: clickhouse-operator-catalog
  sourceNamespace: %[1]s
  installPlanApproval: Automatic
`
)

var (
	k8sClient      client.Client
	config         *rest.Config
	namespace      = "e2e-" + testutil.CurrentSpecHash()
	versionEntries []any
)

func TestOpenShift(t *testing.T) {
	RegisterFailHandler(Fail)

	versions := []string{"latest"}
	if v := os.Getenv("CLICKHOUSE_VERSION"); v != "" {
		versions = strings.Split(v, ",")
	}

	for _, v := range versions {
		versionEntries = append(versionEntries, Entry("version: "+v, v))
	}

	RunSpecs(t, "openshift suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	var err error

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	Expect(v1.AddToScheme(scheme.Scheme)).To(Succeed())
	k8sClient, err = client.New(config, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	By("waiting for project API service")
	Expect(testutil.MustRun(ctx, "oc", "wait",
		"--for=jsonpath={.status.conditions[?(@.type=='Available')].status}=True",
		"apiservice", "v1.project.openshift.io", "--timeout=5m",
	)).To(Succeed())

	By("creating project")
	Eventually(func(g Gomega) {
		if _, err := testutil.Run(exec.CommandContext(ctx, "oc", "get", "project", namespace)); err == nil {
			return
		}

		out, err := testutil.Run(exec.CommandContext(ctx, "oc", "adm", "new-project", namespace))
		g.Expect(err).NotTo(HaveOccurred(), string(out))
	}, "5m", "5s").Should(Succeed())

	DeferCleanup(func(ctx context.Context) {
		_, _ = testutil.Run(exec.CommandContext(ctx, "oc", "delete", "project", namespace,
			"--ignore-not-found", "--wait=false"))
	})

	By("waiting for SCC uid-range annotation")
	Eventually(func(g Gomega) {
		var ns corev1.Namespace
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, &ns)).To(Succeed())
		_, ok := ns.Annotations["openshift.io/sa.scc.uid-range"]
		g.Expect(ok).To(BeTrue(), "waiting for SCC uid-range annotation")
	}, "5m", "5s").Should(Succeed())

	manifests := fmt.Sprintf(catalogManifests,
		namespace,
		envOrDefault("OPENSHIFT_CATALOG_IMAGE", "ghcr.io/clickhouse/clickhouse-operator-catalog:latest"),
		envOrDefault("OPENSHIFT_CHANNEL", "fast-v0"),
	)

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifests)
	out, err := testutil.Run(cmd)
	Expect(err).ToNot(HaveOccurred(), string(out))

	DeferCleanup(func(ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "delete", "-f", "-")
		cmd.Stdin = strings.NewReader(manifests)
		out, err := testutil.Run(cmd)
		Expect(err).ToNot(HaveOccurred(), string(out))
	})
	DeferCleanup(func(ctx context.Context) {
		_ = testutil.UninstallCRDs(ctx)
	})

	By("waiting for CatalogSource to be READY")
	Eventually(func(g Gomega) {
		out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "get", "catalogsource",
			"clickhouse-operator-catalog", "-n", namespace,
			"-o", "jsonpath={.status.connectionState.lastObservedState}"))
		g.Expect(err).ToNot(HaveOccurred(), string(out))
		g.Expect(strings.TrimSpace(string(out))).To(Equal("READY"))
	}, "3m", "5s").Should(Succeed())

	By("waiting for ClusterServiceVersion to succeed")
	Eventually(func(g Gomega) {
		out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "get", "csv",
			"-n", namespace, "--no-headers", "-o", "custom-columns=PHASE:.status.phase"))
		g.Expect(err).ToNot(HaveOccurred(), string(out))
		g.Expect(strings.TrimSpace(string(out))).To(Equal("Succeeded"))
	}, "5m", "5s").Should(Succeed())

	By("waiting controller to be ready")
	Expect(testutil.MustRun(ctx, "kubectl", "wait", "-n", namespace, "--timeout=2m",
		"--for=condition=Available", "deployment/clickhouse-operator-controller-manager")).To(Succeed())
})

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var _ = JustAfterEach(func(ctx context.Context) {
	if !CurrentSpecReport().Failed() {
		return
	}

	for _, resource := range []string{"catalogsources", "subscriptions", "installplans", "clusterserviceversions"} {
		out, _ := testutil.Run(exec.CommandContext(ctx, "kubectl", "get", resource,
			"-n", namespace, "-o", "yaml"))
		AddReportEntry("=== "+resource, string(out))
	}

	catalogLog, _ := testutil.Run(exec.CommandContext(ctx, "kubectl", "logs",
		"-n", "openshift-operator-lifecycle-manager", "-l", "app=catalog-operator", "--tail=100"))
	AddReportEntry("=== catalog-operator log", string(catalogLog))

	testutil.DumpNamespaceDiagnostics(ctx, config, k8sClient, namespace, "report")
})

var _ = Describe("OLM deployment on OpenShift", func() {
	It("should deploy cluster with default settings", func(ctx context.Context) {
		keeper := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "keeper",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{Tag: testutil.BaseVersion},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) { _ = k8sClient.Delete(ctx, &keeper) })

		By("Waiting for KeeperCluster to be ready")
		Expect(testutil.MustRun(ctx, "kubectl", "-n", namespace, "wait", "--timeout=15m", "--for=condition=Ready",
			"keepercluster/"+keeper.Name)).To(Succeed())

		ch := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "ch",
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:         new(int32(1)),
				KeeperClusterRef: v1.KeeperClusterReference{Name: keeper.Name},
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{Tag: testutil.BaseVersion},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &ch)).To(Succeed())
		DeferCleanup(func(ctx context.Context) { _ = k8sClient.Delete(ctx, &ch) })

		By("Waiting for ClickHouse to be ready")
		Expect(testutil.MustRun(ctx, "kubectl", "-n", namespace, "wait", "--timeout=15m", "--for=condition=Ready",
			"clickhousecluster/"+ch.Name)).To(Succeed())
	})
})
