package helm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

var k8sClient client.Client

// TestHelm Run helm tests using the Ginkgo runner.
func TestHelm(t *testing.T) {
	RegisterFailHandler(Fail)

	_, _ = fmt.Fprintf(GinkgoWriter, "Starting clickhouse-operator suite\n")

	RunSpecs(t, "e2e suite")
}

var _ = SynchronizedBeforeSuite(func(ctx context.Context) {
	Expect(testutil.InstallCertManager(ctx)).To(Succeed())
	runCmd(ctx, "helm",
		"upgrade", "--install", "prometheus", "-n", "prometheus", "--create-namespace",
		"oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack",
		"--set", "alertmanager.enabled=false",
		"--set", "pushgateway.enabled=false",
		"--set", "nodeExporter.enabled=false",
		"--set", "grafana.enabled=false",
		"--set", "kube-state-metrics.enabled=false",
		"--set", "server.enabled=false",
	)

	By("installing operator with default values to provide CRDs")
	runCmd(ctx, "helm", "upgrade", "--install", "clickhouse-operator", "../../dist/chart",
		"-n", "clickhouse-operator-system", "--create-namespace")
}, func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	Expect(v1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(certv1.AddToScheme(scheme.Scheme)).To(Succeed())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(config, client.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())
})

var _ = SynchronizedAfterSuite(func() {}, func(ctx context.Context) {
	By("uninstalling operator")
	runCmd(ctx, "helm", "uninstall", "clickhouse-operator", "-n", "clickhouse-operator-system")
})

var _ = DescribeTable("Helm Deployment", Label("helm"), func(ctx context.Context, name string, values map[string]any) {
	namespace := "clickhouse-operator-" + name
	values["watchNamespaces"] = []string{namespace}
	values["crd"] = map[string]any{
		"enable": false,
	}

	valuesFile, err := os.CreateTemp("", "clickhouse-operator-values-*.yaml")
	Expect(err).ToNot(HaveOccurred())
	By("Creating temporary values file")

	valuesData, err := yaml.Marshal(values)
	Expect(err).ToNot(HaveOccurred())
	_, err = valuesFile.Write(valuesData)
	Expect(err).ToNot(HaveOccurred())
	Expect(valuesFile.Close()).To(Succeed())

	By("Installing clickhouse-operator with helm")
	runCmd(ctx, "helm", "install", namespace, "../../dist/chart", "-n", namespace,
		"--create-namespace", "--values", valuesFile.Name())

	DeferCleanup(func(ctx context.Context) {
		By("Uninstalling clickhouse-operator with helm")
		runCmd(ctx, "helm", "uninstall", namespace, "-n", namespace)

		By("Deleting test namespace")
		runCmd(ctx, "kubectl", "delete", "ns", namespace)
	})

	By("Waiting for clickhouse-operator deployment to be ready")
	runCmd(ctx, "kubectl", "wait", "-n", namespace, "--timeout=120s", "--for=condition=Available",
		fmt.Sprintf("deployment/%s-controller-manager", namespace))

	keeper := v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "test",
		},
		Spec: v1.KeeperClusterSpec{
			Replicas: new(int32(1)),
		},
	}
	Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())

	By("Waiting for KeeperCluster to be ready")
	runCmd(ctx, "kubectl", "-n", namespace, "wait", "--timeout=120s", "--for=condition=Ready", "keepercluster/test")
},
	Entry("Default values", "default", map[string]any{}),
	Entry("Without webhook", "webhookless", map[string]any{
		"webhook": map[string]any{
			"enable": false,
		},
	}),
	Entry("Custom certificate issuer", "custom-issuer", map[string]any{
		"certManager": map[string]any{
			"issuerRef": map[string]any{
				"name": "custom-issuer",
				"kind": "Issuer",
			},
		},
		"extraManifests": []string{
			`apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
    name: custom-issuer
spec:
    selfSigned: {}`,
		},
	}),
	Entry("With secure metrics service monitor", "secure-metrics", map[string]any{
		"metrics": map[string]any{
			"enable": true,
			"secure": true,
		},
		"prometheus": map[string]any{
			"service_monitor": true,
		},
	}),
)

func runCmd(ctx context.Context, name string, args ...string) {
	out, err := testutil.Run(exec.CommandContext(ctx, name, args...))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), string(out))
}
