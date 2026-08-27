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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/discovery"

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

	env                  *testutil.Env
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
	Expect(testutil.MustRun(ctx, "make", "build-linux-manager")).To(Succeed())

	By("building operator image")
	Expect(testutil.MustRun(ctx, "docker", "build", "-f", "dev.Dockerfile", "-t", testImage, ".")).To(Succeed())

	By("loading operator image to kind")
	Expect(testutil.MustRun(ctx, "kind", "load", "docker-image", testImage)).To(Succeed())

	By("installing the cert-manager")
	Expect(testutil.InstallCertManager(ctx)).To(Succeed())

	By("installing the NetworkPolicy controller")
	Expect(testutil.InstallNetworkPolicyController(ctx)).To(Succeed())

	Expect(testutil.MustRun(ctx, "helm",
		"upgrade", "--install", "prometheus", "-n", "prometheus", "--create-namespace",
		"oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack",
		"--set", "alertmanager.enabled=false",
		"--set", "pushgateway.enabled=false",
		"--set", "nodeExporter.enabled=false",
		"--set", "grafana.enabled=false",
		"--set", "kube-state-metrics.enabled=false",
		"--set", "server.enabled=false",
	)).To(Succeed())

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	env = testutil.SetupEnv(testutil.SetupOptions{CertManagerScheme: true})
	k8sClient = env.Client

	dc, err := discovery.NewDiscoveryClientForConfig(env.Config)
	Expect(err).NotTo(HaveOccurred())
	serverVersion, err := dc.ServerVersion()
	Expect(err).NotTo(HaveOccurred())
	By("running on Kubernetes " + serverVersion.GitVersion)
})

var _ = JustAfterEach(func(ctx context.Context) {
	report := CurrentSpecReport()
	if !report.Failed() || currentTestNamespace == "" {
		return
	}

	ns := currentTestNamespace
	currentTestNamespace = ""

	testutil.DumpNamespaceDiagnostics(ctx, env, ns, reportDir)
})

var _ = Describe("Manifests deployment", Ordered, ContinueOnFailure, Label("manifest"), func() {
	namespace := "clickhouse-operator-system"

	BeforeAll(func(ctx context.Context) {
		currentTestNamespace = namespace

		By("building installer manifest")
		Expect(testutil.MustRun(ctx, "make", "build-installer", "IMG="+testImage)).To(Succeed())

		By("applying installer manifest")
		Expect(testutil.MustRun(ctx, "kubectl", "apply", "--server-side", "--force-conflicts",
			"-f", "dist/install.yaml")).To(Succeed())

		DeferCleanup(func(ctx context.Context) {
			By("removing installer manifest resources")
			Expect(testutil.MustRun(ctx, "kubectl", "delete", "--ignore-not-found", "-f", "dist/install.yaml")).To(Succeed())
		})

		By("Waiting controller to be ready")
		env.WaitDeploymentAvailable(ctx, namespace, "clickhouse-operator-controller-manager", 2*time.Minute)
	})

	testDeployment(namespace)
})

var _ = Describe("OLM deployment", Ordered, ContinueOnFailure, Label("olm"), func() {
	namespace := "clickhouse-operator-olm"

	BeforeAll(func(ctx context.Context) {
		currentTestNamespace = namespace

		By("installing operator-sdk")

		out, err := testutil.Run(exec.CommandContext(ctx, "make", "-s", "operator-sdk-path"))
		Expect(err).ToNot(HaveOccurred(), string(out))
		Expect(out).ToNot(BeEmpty(), "operator-sdk path not found in output: %s", string(out))

		operatorSDK := strings.TrimSpace(string(out))

		By("installing OLM")

		if _, err := testutil.Run(exec.CommandContext(ctx, operatorSDK, "olm", "status")); err != nil {
			// Clean up any leftover OLM resources from a previous run
			_, _ = testutil.Run(exec.CommandContext(ctx, operatorSDK, "olm", "uninstall", "--timeout", "1m"))
			Expect(testutil.MustRun(ctx, operatorSDK, "olm", "install", "--timeout", "5m")).To(Succeed())
		}

		DeferCleanup(func(ctx context.Context) {
			By("uninstalling OLM")
			Expect(testutil.MustRun(ctx, operatorSDK, "olm", "uninstall", "--timeout", "5m")).To(Succeed())
		})

		By("creating test namespace")
		testutil.EnsureNamespace(ctx, env, namespace)

		// Enforce upstream Pod Security Admission at "restricted" level on the OLM test namespace.
		By("labeling test namespace with PSA enforce=restricted")
		Expect(testutil.MustRun(ctx, "kubectl", "label", "ns", namespace, "--overwrite",
			"pod-security.kubernetes.io/enforce=restricted",
			"pod-security.kubernetes.io/enforce-version=latest")).To(Succeed())

		By("building OLM bundle")
		Expect(testutil.MustRun(ctx, "make", "bundle", "IMG="+testImage)).To(Succeed())

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
		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "test-catalog-server"}, &pod)).To(Succeed())
			g.Expect(testutil.CheckPodReady(&pod)).To(BeTrue())
		}, "2m", testutil.PollInterval).Should(Succeed())

		By("waiting for ClusterServiceVersion to succeed")
		Eventually(func(g Gomega) {
			out, err := testutil.Run(exec.CommandContext(ctx, "kubectl", "get", "csv",
				"-n", namespace, "--no-headers", "-o", "custom-columns=PHASE:.status.phase"))
			g.Expect(err).ToNot(HaveOccurred(), string(out))
			g.Expect(strings.TrimSpace(string(out))).To(Equal("Succeeded"), "CSV phase: %s", strings.TrimSpace(string(out)))
		}, "5m", "5s").Should(Succeed())

		By("Waiting controller to be ready")
		env.WaitDeploymentAvailable(ctx, namespace, "clickhouse-operator-controller-manager", 2*time.Minute)
	})

	testDeployment(namespace)
})

var _ = Describe("Helm deployment", Ordered, ContinueOnFailure, Label("helm"), func() {
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
			Expect(testutil.MustRun(ctx, "helm", "install", namespace, "dist/chart", "-n", namespace,
				"--create-namespace", "--values", valuesFile.Name())).To(Succeed())

			DeferCleanup(func(ctx context.Context) {
				By("Uninstalling clickhouse-operator with helm")
				Expect(testutil.MustRun(ctx, "helm", "uninstall", namespace, "-n", namespace)).To(Succeed())

				By("Deleting test namespace")
				Expect(testutil.MustRun(ctx, "kubectl", "delete", "ns", namespace)).To(Succeed())
			})

			By("Waiting controller to be ready")
			env.WaitDeploymentAvailable(ctx, namespace, namespace+"-controller-manager", 2*time.Minute)
		})

		testHelmCluster(namespace)
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

var _ = Describe("Operator upgrade", Ordered, ContinueOnFailure, Label("upgrade"), func() {
	const (
		namespace      = "clickhouse-operator-upgrade"
		releaseName    = namespace
		releasedChart  = "oci://ghcr.io/clickhouse/clickhouse-operator-helm"
		deploymentName = namespace + "-controller-manager"
		keeperName     = "keeper"
		chName         = "ch"
		version        = testutil.UpdateVersion
	)

	helmArgs := []string{"-n", namespace,
		"--set", "controller.watchNamespaces={" + namespace + "}",
		"--set", "crd.enable=true", "--set", "crd.keep=false",
	}

	storage := &corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
		},
	}

	BeforeAll(func(ctx context.Context) {
		currentTestNamespace = namespace

		fromVersion := latestReleasedVersion(ctx)

		By("installing the released operator " + fromVersion + " via Helm")
		Expect(testutil.MustRun(ctx, "helm", append([]string{"install", releaseName, releasedChart,
			"--version", fromVersion, "--create-namespace"}, helmArgs...)...,
		)).To(Succeed())

		DeferCleanup(func(ctx context.Context) {
			By("uninstalling the operator and cleaning up")

			_ = testutil.MustRun(ctx, "helm", "uninstall", releaseName, "-n", namespace)
			_ = testutil.MustRun(ctx, "kubectl", "delete", "ns", namespace, "--ignore-not-found")
			_ = testutil.UninstallCRDs(ctx)
		})

		By("waiting for the released operator to be ready")
		env.WaitDeploymentAvailable(ctx, namespace, deploymentName, 2*time.Minute)
	})

	It("should reconcile cluster after upgrade without data loss", func(ctx context.Context) {
		dialer := env.Dialer

		keeperCR := testutil.NewKeeperCluster(namespace, keeperName).
			WithReplicas(3).
			WithStorage(*storage).
			WithTag(version).
			Cluster()
		chCR := testutil.NewClickHouseCluster(namespace, chName).
			WithReplicas(2).
			WithStorage(*storage).
			WithKeeper(keeperName).
			WithTag(version).
			Cluster()

		By("deploying keeper and clickhouse on the released operator")

		Expect(k8sClient.Create(ctx, &keeperCR)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &keeperCR)).To(Succeed())
		})
		env.WaitClusterReady(ctx, &keeperCR, 5*time.Minute)
		Expect(k8sClient.Create(ctx, &chCR)).To(Succeed())
		env.WaitClusterReady(ctx, &chCR, 5*time.Minute)

		By("writing test data", func() {
			// A freshly formed keeper ensemble re-elects for a while, so retry through transient leader loss.
			Eventually(func(g Gomega) {
				keeperClient, err := testutil.NewKeeperClient(ctx, dialer, &keeperCR)
				g.Expect(err).NotTo(HaveOccurred())

				defer keeperClient.Close()

				g.Expect(keeperClient.CheckWrite(0)).To(Succeed())
				g.Expect(keeperClient.CheckRead(0)).To(Succeed())
			}, "2m", "5s").Should(Succeed())

			chClient, err := testutil.NewClickHouseClient(ctx, dialer, &chCR)
			Expect(err).NotTo(HaveOccurred())

			defer chClient.Close()

			Expect(chClient.CreateDatabase(ctx)).To(Succeed())
			Expect(chClient.CheckWrite(ctx, 0)).To(Succeed())
			Expect(chClient.CheckRead(ctx, 0)).To(Succeed())
		})

		By("upgrading the operator to the local build")

		Expect(testutil.MustRun(ctx, "helm", append([]string{"upgrade", releaseName, "dist/chart",
			"--set", "manager.image.repository=" + testRepo,
			"--set", "manager.image.tag=" + testTag,
			"--set", "manager.image.pullPolicy=Never",
		}, helmArgs...)...)).To(Succeed())
		env.WaitDeploymentAvailable(ctx, namespace, deploymentName, 3*time.Minute)

		By("updating keeper and verifying it stays writable", func() {
			Expect(k8sClient.Get(ctx, keeperCR.NamespacedName(), &keeperCR)).To(Succeed())
			keeperCR.Spec.Annotations = map[string]string{"e2e.clickhouse.com/upgrade": "reconciled"}
			Expect(k8sClient.Update(ctx, &keeperCR)).To(Succeed())
			Eventually(func(g Gomega) {
				var cluster v1.KeeperCluster
				g.Expect(k8sClient.Get(ctx, keeperCR.NamespacedName(), &cluster)).To(Succeed())
				g.Expect(cluster.Status.ObservedGeneration).To(Equal(cluster.Generation))
				g.Expect(cluster.Status.CurrentRevision).To(Equal(cluster.Status.UpdateRevision))
				g.Expect(cluster.Status.ReadyReplicas).To(Equal(cluster.Replicas()))
				g.Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, v1.ConditionTypeReady)).To(BeTrue())
			}, "10m", "5s").Should(Succeed())

			// Keeper re-elects after the rolling restart, so retry through transient leader loss.
			Eventually(func(g Gomega) {
				keeperClient, err := testutil.NewKeeperClient(ctx, dialer, &keeperCR)
				g.Expect(err).NotTo(HaveOccurred())

				defer keeperClient.Close()

				g.Expect(keeperClient.CheckRead(0)).To(Succeed())
				g.Expect(keeperClient.CheckWrite(1)).To(Succeed())
				g.Expect(keeperClient.CheckRead(1)).To(Succeed())
			}, "2m", "5s").Should(Succeed())
		})

		By("updating clickhouse and verifying its health", func() {
			Expect(k8sClient.Get(ctx, chCR.NamespacedName(), &chCR)).To(Succeed())
			chCR.Spec.Annotations = map[string]string{"e2e.clickhouse.com/upgrade": "reconciled"}
			Expect(k8sClient.Update(ctx, &chCR)).To(Succeed())
			Eventually(func(g Gomega) {
				var cluster v1.ClickHouseCluster
				g.Expect(k8sClient.Get(ctx, chCR.NamespacedName(), &cluster)).To(Succeed())
				g.Expect(cluster.Status.ObservedGeneration).To(Equal(cluster.Generation))
				g.Expect(cluster.Status.CurrentRevision).To(Equal(cluster.Status.UpdateRevision))
				g.Expect(cluster.Status.ReadyReplicas).To(Equal(cluster.Replicas() * cluster.Shards()))
				g.Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, v1.ConditionTypeReady)).To(BeTrue())
			}, "10m", "5s").Should(Succeed())

			chClient, err := testutil.NewClickHouseClient(ctx, dialer, &chCR)
			Expect(err).NotTo(HaveOccurred())

			defer chClient.Close()

			Expect(chClient.CheckRead(ctx, 0)).To(Succeed())
			Expect(chClient.CheckWrite(ctx, 1)).To(Succeed())
			Expect(chClient.CheckRead(ctx, 1)).To(Succeed())
		})
	})
})

// testHelmCluster validates the Helm based deployment using the clickhouse-cluster-helm chart to deploy sample cluster.
func testHelmCluster(namespace string) {
	body := func(ctx context.Context, version string) {
		releaseName := "cluster-" + version
		chName := "ch-" + version
		keeperName := "keeper-" + version

		By("Installing clickhouse-cluster chart")
		Expect(testutil.MustRun(ctx, "helm", "install", releaseName, "dist/chart-cluster", "-n", namespace,
			"--set", "clickhouse.meta.name="+chName,
			"--set", "keeper.meta.name="+keeperName,
			"--set", "clickhouse.spec.replicas=1",
			"--set", "keeper.spec.replicas=1",
			"--set-string", "imageTag="+version,
		)).To(Succeed())

		DeferCleanup(func(ctx context.Context) {
			By("Uninstalling clickhouse-cluster chart")
			Expect(testutil.MustRun(ctx, "helm", "uninstall", releaseName, "-n", namespace)).To(Succeed())
		})

		By("Waiting for KeeperCluster to be ready")
		env.WaitClusterReady(ctx, &v1.KeeperCluster{
			Namespace: namespace, Name: keeperName,
		}, 5*time.Minute)

		By("Waiting for ClickHouse to be ready")
		env.WaitClusterReady(ctx, &v1.ClickHouseCluster{
			Namespace: namespace, Name: chName,
		}, 5*time.Minute)
	}

	tableArgs := make([]any, 1, len(versionEntries)+1)
	tableArgs[0] = body
	DescribeTable("cluster chart should successfully deploy", append(tableArgs, versionEntries...)...)
}

func testDeployment(namespace string) {
	body := func(ctx context.Context, version string) {
		keeper := testutil.NewKeeperCluster(namespace, "keeper-"+version).
			WithTag(version).
			WithNetworkPolicy(v1.NetworkPolicyEnabled).
			Cluster()
		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			_ = k8sClient.Delete(ctx, &keeper)
		})

		By("Waiting for KeeperCluster to be ready")
		env.WaitClusterReady(ctx, &keeper, 5*time.Minute)

		ch := testutil.NewClickHouseCluster(namespace, "ch-"+version).
			WithKeeper(keeper.Name).
			WithTag(version).
			WithNetworkPolicy(v1.NetworkPolicyEnabled).
			Cluster()
		Expect(k8sClient.Create(ctx, &ch)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			_ = k8sClient.Delete(ctx, &ch)
		})

		By("Waiting for ClickHouse to be ready")
		env.WaitClusterReady(ctx, &ch, 5*time.Minute)
	}

	tableArgs := make([]any, 1, len(versionEntries)+1)
	tableArgs[0] = body
	DescribeTable("should successfully work with", append(tableArgs, versionEntries...)...)
}

func templateTestResources(ctx context.Context, namespace string) string {
	projectDir, err := testutil.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())

	By("installing opm")
	Expect(testutil.MustRun(ctx, "make", "opm")).To(Succeed())

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

func latestReleasedVersion(ctx context.Context) string {
	if v := os.Getenv("UPGRADE_FROM_VERSION"); v != "" {
		return strings.TrimPrefix(v, "v")
	}

	out, err := testutil.Run(exec.CommandContext(ctx, "git", "tag", "--list",
		"v[0-9]*.[0-9]*.[0-9]*", "--sort=-v:refname"))
	Expect(err).NotTo(HaveOccurred(), string(out))

	tags := strings.Fields(string(out))
	Expect(tags).NotTo(BeEmpty(), "no release tags found to upgrade from")

	return strings.TrimPrefix(tags[0], "v")
}
