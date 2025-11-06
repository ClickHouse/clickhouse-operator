/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	clickhousecomv1alpha1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	namespace     = "clickhouse-operator-system"
	testNamespace = "clickhouse-operator-test"
)

var ctx context.Context
var cancel context.CancelFunc
var k8sClient client.Client
var config *rest.Config
var defaultStorage = corev1.PersistentVolumeClaimSpec{
	AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
	Resources: corev1.VolumeResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceStorage: resource.MustParse("1Gi"),
		},
	},
}

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting clickhouse-operator suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	var err error
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.Background())

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
	Expect(utils.InstallCertManager()).To(Succeed())

	// By("installing prometheus operator")
	// Expect(utils.InstallPrometheusOperator()).To(Succeed())

	By("creating manager namespace")
	cmd := exec.Command("kubectl", "create", "ns", namespace)
	_, _ = utils.Run(cmd)

	By("creating test namespace")
	cmd = exec.Command("kubectl", "create", "ns", testNamespace)
	_, _ = utils.Run(cmd)

	var controllerPodName string

	// projectimage stores the name of the image used in the example
	var projectimage = "clickhouse.com/clickhouse-operator:v0.0.1"

	By("building the manager(Operator) image")
	cmd = exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectimage))
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("loading the the manager(Operator) image on Kind")
	err = utils.LoadImageToKindClusterWithName(projectimage)
	Expect(err).NotTo(HaveOccurred())

	// In kind cert-manager root CA may not be injected at this moment.
	By("deploying the controller-manager")
	Eventually(func() error {
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectimage))
		_, err = utils.Run(cmd)
		return err
	}, 2*time.Minute, time.Second).Should(Succeed())

	cmd = exec.Command("kubectl", "wait", "deployment.apps/clickhouse-operator-controller-manager",
		"--for", "condition=Available",
		"--namespace", namespace,
		"--timeout", "5m",
	)
	_, err = utils.Run(cmd)
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

		podOutput, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		podNames := utils.GetNonEmptyLines(string(podOutput))
		if len(podNames) != 1 {
			return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
		}
		controllerPodName = podNames[0]
		Expect(controllerPodName).Should(ContainSubstring("controller-manager"))
		Expect(utils.CapturePodLogs(ctx, config, namespace, controllerPodName)).To(Succeed())

		// Validate pod status
		cmd = exec.Command("kubectl", "get",
			"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
			"-n", namespace,
		)
		status, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		if string(status) != "Running" {
			return fmt.Errorf("controller pod in %s status", status)
		}
		return nil
	}
	Eventually(verifyControllerUp, time.Minute*2, time.Second).Should(Succeed())

	// Ensure webhook is accepting admission requests by issuing a dry-run create that must be rejected
	By("waiting for webhook to accept admission requests")
	Eventually(func() error {
		cr := &clickhousecomv1alpha1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      "webhook-probe",
			},
		}

		// Use dry-run so nothing is persisted
		err := k8sClient.Create(ctx, cr, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err == nil {
			return fmt.Errorf("unexpected success creating object, webhook not engaged yet")
		}
		if !strings.Contains(err.Error(), "spec.keeperClusterRef") {
			return fmt.Errorf("webhook not ready or different error: %v", err)
		}
		return nil
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
})

var _ = AfterSuite(func() {
	cancel()

	// By("uninstalling the Prometheus manager bundle")
	// utils.UninstallPrometheusOperator()

	By("removing manager namespace")
	cmd := exec.Command("kubectl", "delete", "ns", namespace)
	_, _ = utils.Run(cmd)

	By("removing test namespace")
	cmd = exec.Command("kubectl", "delete", "ns", testNamespace)
	_, _ = utils.Run(cmd)
})
