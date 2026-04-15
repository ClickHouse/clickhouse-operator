package deploy

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	testRepo  = "localhost/clickhouse-operator"
	testTag   = "test"
	testImage = testRepo + ":" + testTag

	defaultVersion = "latest"
	reportDir      = "report"
)

var (
	//go:embed olm_manifests.yaml.tmpl
	olmManifests string

	config               *rest.Config
	k8sClient            client.Client
	currentTestNamespace string
	versionEntries       []any
)

// TestDeploy runs deployment tests using the Ginkgo runner.
func TestDeploy(t *testing.T) {
	RegisterFailHandler(Fail)

	versions := []string{defaultVersion}
	if vers := os.Getenv("CLICKHOUSE_VERSION"); vers != "" {
		versions = strings.Split(vers, ",")
	}

	for _, version := range versions {
		versionEntries = append(versionEntries, Entry("version: "+version, version))
	}

	GinkgoWriter.Printf("Starting clickhouse-operator deploy suite\n")

	RunSpecs(t, "deploy suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	By("building manager binary")
	runCmd(ctx, "make", "build-linux-manager")

	By("building operator image")
	runCmd(ctx, "docker", "build", "-f", "dev.Dockerfile", "-t", testImage, ".")

	By("loading operator image to kind")
	runCmd(ctx, "kind", "load", "docker-image", testImage)

	By("installing the cert-manager")
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

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")

	var err error

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	dc, err := discovery.NewDiscoveryClientForConfig(config)
	Expect(err).NotTo(HaveOccurred())
	serverVersion, err := dc.ServerVersion()
	Expect(err).NotTo(HaveOccurred())
	By("running on Kubernetes " + serverVersion.GitVersion)

	Expect(v1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(certv1.AddToScheme(scheme.Scheme)).To(Succeed())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(config, client.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())
})

var _ = JustAfterEach(func(ctx context.Context) {
	report := CurrentSpecReport()
	if !report.Failed() || currentTestNamespace == "" {
		return
	}

	ns := currentTestNamespace
	currentTestNamespace = ""

	testutil.DumpNamespaceDiagnostics(ctx, config, k8sClient, ns, reportDir)
})

var _ = Describe("Manifests deployment", Ordered, Label("manifest"), func() {
	namespace := "clickhouse-operator-system"

	BeforeAll(func(ctx context.Context) {
		currentTestNamespace = namespace

		By("building installer manifest")
		runCmd(ctx, "make", "build-installer", "IMG="+testImage)

		By("applying installer manifest")
		runCmd(ctx, "kubectl", "apply", "-f", "dist/install.yaml")

		DeferCleanup(func(ctx context.Context) {
			By("removing installer manifest resources")
			runCmd(ctx, "kubectl", "delete", "--ignore-not-found", "-f", "dist/install.yaml")
		})

		By("Waiting controller to be ready")
		Eventually(func(g Gomega) {
			out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "wait", "-n", namespace,
				"--timeout=120s", "--for=condition=Available", "deployment/clickhouse-operator-controller-manager"))
			g.Expect(err).ToNot(HaveOccurred(), string(out))
		}, "2m", "100ms").Should(Succeed())
	})

	testDeployment(namespace)
})

var _ = Describe("OLM deployment", Ordered, Label("olm"), func() {
	namespace := "clickhouse-operator-olm"

	BeforeAll(func(ctx context.Context) {
		currentTestNamespace = namespace

		By("installing operator-sdk")
		runCmd(ctx, "make", "operator-sdk")

		out, err := testutil.Run(exec.CommandContext(ctx, "make", "operator-sdk-path"))
		Expect(err).ToNot(HaveOccurred(), string(out))

		// Extract the path from make output, skipping make[N] directory messages.
		var operatorSDK string
		for line := range strings.SplitSeq(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && strings.HasPrefix(line, "/") && strings.HasSuffix(line, "operator-sdk") {
				operatorSDK = line

				break
			}
		}

		Expect(operatorSDK).ToNot(BeEmpty(), "operator-sdk path not found in output: %s", string(out))

		By("installing OLM")

		if _, err := testutil.Run(exec.CommandContext(ctx, operatorSDK, "olm", "status")); err != nil {
			// Clean up any leftover OLM resources from a previous run
			_, _ = testutil.Run(exec.CommandContext(ctx, operatorSDK, "olm", "uninstall", "--timeout", "1m"))
			runCmd(ctx, operatorSDK, "olm", "install", "--timeout", "5m")
		}

		DeferCleanup(func(ctx context.Context) {
			By("uninstalling OLM")
			runCmd(ctx, operatorSDK, "olm", "uninstall", "--timeout", "5m")
		})

		By("creating test namespace")
		testutil.EnsureNamespace(ctx, k8sClient, namespace)

		By("building OLM bundle")
		runCmd(ctx, "make", "bundle", "IMG="+testImage)

		By("creating catalog and subscription")

		resources := templateTestResources(ctx, namespace)

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				AddReportEntry("OLM resources", resources)
			}
		})

		cmd := exec.CommandContext(ctx, "kubectl", "create", "-f", "-")
		cmd.Stdin = strings.NewReader(resources)
		out, err = testutil.Run(cmd)
		Expect(err).ToNot(HaveOccurred(), string(out))

		DeferCleanup(func(ctx context.Context) {
			By("cleaning up CRDs left by OLM deployment")

			_ = testutil.UninstallCRDs(ctx)
		})

		DeferCleanup(func(ctx context.Context) {
			if !CurrentSpecReport().Failed() {
				return
			}

			By("dumping OLM state for debugging")

			for _, resource := range []string{"catalogsources", "subscriptions", "installplans", "clusterserviceversions"} {
				out, _ := testutil.Run(exec.CommandContext(ctx, "kubectl", "get", resource,
					"-n", namespace, "-o", "wide"))
				AddReportEntry("=== OLM "+resource, string(out))
			}
		})

		By("waiting for catalog server to be ready")
		runCmd(ctx, "kubectl", "wait", "-n", namespace, "--timeout=120s",
			"--for=condition=Ready", "pod/test-catalog-server")

		By("waiting for ClusterServiceVersion to succeed")
		Eventually(func(g Gomega) {
			out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "get", "csv",
				"-n", namespace, "--no-headers", "-o", "custom-columns=PHASE:.status.phase"))
			g.Expect(err).ToNot(HaveOccurred(), string(out))
			g.Expect(strings.TrimSpace(string(out))).To(Equal("Succeeded"), "CSV phase: %s", strings.TrimSpace(string(out)))
		}, "5m", "5s").Should(Succeed())

		By("Waiting controller to be ready")
		Eventually(func(g Gomega) {
			out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "wait", "-n", namespace,
				"--timeout=120s", "--for=condition=Available", "deployment/clickhouse-operator-controller-manager"))
			g.Expect(err).ToNot(HaveOccurred(), string(out))
		}, "2m", "100ms").Should(Succeed())
	})

	testDeployment(namespace)
})

var _ = Describe("Helm deployment", Ordered, Label("helm"), func() {
	DescribeTableSubtree("with", func(name string, values map[string]any) {
		namespace := "clickhouse-operator-" + name
		BeforeAll(func(ctx context.Context) {
			currentTestNamespace = namespace
			values["watchNamespaces"] = []string{namespace}
			values["crd"] = map[string]any{
				"enable": true,
				"keep":   false,
			}
			values["manager"] = map[string]any{
				"image": map[string]any{
					"repository": testRepo,
					"tag":        testTag,
					"pullPolicy": "Never",
				},
			}

			valuesFile, err := os.CreateTemp("", "clickhouse-operator-values-*.yaml")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = os.Remove(valuesFile.Name()) })
			By("Creating temporary values file")

			valuesData, err := yaml.Marshal(values)
			Expect(err).ToNot(HaveOccurred())
			_, err = valuesFile.Write(valuesData)
			Expect(err).ToNot(HaveOccurred())
			Expect(valuesFile.Close()).To(Succeed())

			By("Installing clickhouse-operator with helm")
			runCmd(ctx, "helm", "install", namespace, "dist/chart", "-n", namespace,
				"--create-namespace", "--values", valuesFile.Name())

			DeferCleanup(func(ctx context.Context) {
				By("Uninstalling clickhouse-operator with helm")
				runCmd(ctx, "helm", "uninstall", namespace, "-n", namespace)

				By("Deleting test namespace")
				runCmd(ctx, "kubectl", "delete", "ns", namespace)
			})

			By("Waiting controller to be ready")
			Eventually(func(g Gomega) {
				out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "wait", "-n", namespace,
					"--timeout=120s", "--for=condition=Available", "deployment/"+namespace+"-controller-manager"))
				g.Expect(err).ToNot(HaveOccurred(), string(out))
			}, "2m", "100ms").Should(Succeed())
		})

		testDeployment(namespace)
	},
		Entry("default values", "default", map[string]any{}),
		Entry("disabled webhook", "webhookless", map[string]any{
			"webhook": map[string]any{
				"enable": false,
			},
		}),
		Entry("custom certificate issuer", "custom-issuer", map[string]any{
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
		Entry("secure metrics service monitor", "secure-metrics", map[string]any{
			"metrics": map[string]any{
				"enable": true,
				"secure": true,
			},
			"prometheus": map[string]any{
				"service_monitor": true,
			},
		}),
	)
})

func testDeployment(namespace string) {
	body := func(ctx context.Context, version string) {
		keeper := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "keeper-" + version,
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: version,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			_ = k8sClient.Delete(ctx, &keeper)
		})

		By("Waiting for KeeperCluster to be ready")
		runCmd(ctx, "kubectl", "-n", namespace, "wait", "--timeout=5m", "--for=condition=Ready",
			"keepercluster/"+keeper.Name)

		ch := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "ch-" + version,
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas: new(int32(1)),
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeper.Name,
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: version,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &ch)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			_ = k8sClient.Delete(ctx, &ch)
		})

		By("Waiting for ClickHouse to be ready")
		runCmd(ctx, "kubectl", "-n", namespace, "wait", "--timeout=5m", "--for=condition=Ready",
			"clickhousecluster/"+ch.Name)
	}

	tableArgs := make([]any, 1, len(versionEntries)+1)
	tableArgs[0] = body
	DescribeTable("should successfully work with", append(tableArgs, versionEntries...)...)
}

func runCmd(ctx context.Context, name string, args ...string) {
	out, err := testutil.Run(exec.CommandContext(ctx, name, args...))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), string(out))
}

func templateTestResources(ctx context.Context, namespace string) string {
	projectDir, err := testutil.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())

	By("installing opm")
	runCmd(ctx, "make", "opm")

	opm := filepath.Join(projectDir, "bin", "opm")

	// Render bundle directory into FBC JSON
	By("rendering catalog with opm")

	renderCmd := exec.CommandContext(ctx, opm, "render", filepath.Join(projectDir, "bundle"))
	bundleBlob, err := renderCmd.Output()
	Expect(err).NotTo(HaveOccurred())

	bundle := map[string]any{}
	Expect(json.Unmarshal(bundleBlob, &bundle)).To(Succeed())
	bundleBlob, err = json.Marshal(bundle)
	Expect(err).NotTo(HaveOccurred())

	tmpl, err := template.New("olm").Parse(olmManifests)
	Expect(err).NotTo(HaveOccurred())

	result := strings.Builder{}
	Expect(tmpl.Execute(&result, map[string]any{
		"namespace":  namespace,
		"bundleName": bundle["name"],
		"bundle":     string(bundleBlob),
	})).To(Succeed())

	return result.String()
}
