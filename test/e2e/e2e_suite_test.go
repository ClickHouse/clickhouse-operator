package e2e

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	clickhousecomv1alpha1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	namespace     = "clickhouse-operator-system"
	testNamespace = "clickhouse-operator-test"
)

var (
	cancelLogs     context.CancelFunc
	k8sClient      client.Client
	config         *rest.Config
	defaultStorage = corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
		},
	}
)

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)

	_, _ = fmt.Fprintf(GinkgoWriter, "Starting clickhouse-operator suite\n")

	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	var err error
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	Expect(clickhousecomv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(certv1.AddToScheme(scheme.Scheme)).To(Succeed())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(config, client.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())

	By("installing the cert-manager")
	Expect(testutil.InstallCertManager(ctx)).To(Succeed())

	// By("installing prometheus operator")
	// Expect(utils.InstallPrometheusOperator()).To(Succeed())

	By("creating manager namespace")
	cmd := exec.Command("kubectl", "create", "ns", namespace)
	_, _ = testutil.Run(cmd)

	By("creating test namespace")
	cmd = exec.Command("kubectl", "create", "ns", testNamespace)
	_, _ = testutil.Run(cmd)

	var controllerPodName string

	// projectimage stores the name of the image used in the example
	var projectimage = "ghcr.io/clickhouse/clickhouse-operator:v0.0.1"

	By("building the manager(Operator) image")
	cmd = exec.Command("make", "docker-build", "IMG="+projectimage)
	_, err = testutil.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("loading the manager(Operator) image on Kind")
	err = testutil.LoadImageToKindClusterWithName(ctx, projectimage)
	Expect(err).NotTo(HaveOccurred())

	// In kind cert-manager root CA may not be injected at this moment.
	By("deploying the controller-manager")
	Eventually(func() error {
		cmd = exec.Command("make", "deploy", "IMG="+projectimage)
		_, err = testutil.Run(cmd)
		if err != nil {
			return fmt.Errorf("deploy controller-manager: %w", err)
		}

		return nil
	}, 2*time.Minute, time.Second).Should(Succeed())

	cmd = exec.Command("kubectl", "wait", "deployment.apps/clickhouse-operator-controller-manager",
		"--for", "condition=Available",
		"--namespace", namespace,
		"--timeout", "5m",
	)
	_, err = testutil.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("validating that the controller-manager pod is running as expected")
	verifyControllerUp := func() error {
		// Get pod name
		cmd = exec.Command("kubectl", "get",
			"pods", "-l", "control-plane=controller-manager",
			"-o", "go-template={{ range .items }}"+
				"{{ if not .metadata.deletionTimestamp }}"+
				"{{ .metadata.name }}"+
				"{{ \"\\n\" }}{{ end }}{{ end }}",
			"-n", namespace,
		)

		podOutput, err := testutil.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		podNames := testutil.GetNonEmptyLines(string(podOutput))
		if len(podNames) != 1 {
			return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
		}
		controllerPodName = podNames[0]
		Expect(controllerPodName).Should(ContainSubstring("controller-manager"))
		logsCtx, cancel := context.WithCancel(context.Background())
		cancelLogs = cancel
		Expect(testutil.CapturePodLogs(logsCtx, config, namespace, controllerPodName)).To(Succeed())

		// Validate pod status
		cmd = exec.Command("kubectl", "get",
			"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
			"-n", namespace,
		)
		status, err := testutil.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		if string(status) != "Running" {
			return fmt.Errorf("controller pod in %s status", status)
		}
		return nil
	}
	Eventually(verifyControllerUp, time.Minute*2, time.Second).Should(Succeed())

	// Ensure webhook is accepting admission requests by issuing a dry-run create that must be rejected
	By("waiting for webhook to accept admission requests")
	Eventually(func(ctx context.Context) error {
		cr := &clickhousecomv1alpha1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      "webhook-probe",
			},
		}

		// Use dry-run so nothing is persisted
		err := k8sClient.Create(ctx, cr, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err == nil {
			return errors.New("unexpected success creating object, webhook not engaged yet")
		}
		if !strings.Contains(err.Error(), "spec.keeperClusterRef") {
			return fmt.Errorf("webhook not ready or different error: %w", err)
		}
		return nil
	}, 2*time.Minute, 2*time.Second).WithContext(ctx).Should(Succeed())
})

var _ = AfterSuite(func(ctx context.Context) {
	cancelLogs()

	// By("uninstalling the Prometheus manager bundle")
	// utils.UninstallPrometheusOperator()

	By("removing manager namespace")
	_, _ = testutil.Run(exec.CommandContext(ctx, "kubectl", "delete", "ns", namespace))

	By("removing test namespace")
	_, _ = testutil.Run(exec.CommandContext(ctx, "kubectl", "delete", "ns", testNamespace))
})

func CheckPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

func CheckReplicaUpdated(ctx context.Context, cfgName string, cfgRev string, stsName string, stsRev string) bool {
	var configmap corev1.ConfigMap
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: testNamespace,
		Name:      cfgName,
	}, &configmap); err != nil {
		return false
	}

	if controllerutil.GetSpecHashFromObject(&configmap) != cfgRev {
		return false
	}

	var sts appsv1.StatefulSet
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: testNamespace,
		Name:      stsName,
	}, &sts); err != nil {
		return false
	}

	return controllerutil.GetSpecHashFromObject(&sts) == stsRev
}
