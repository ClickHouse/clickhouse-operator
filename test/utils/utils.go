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

package utils

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/clickhouse-operator/internal/util"
	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive,staticcheck
	. "github.com/onsi/gomega"    //nolint:golint,revive,staticcheck
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	prometheusOperatorVersion = "v0.87.0"
	prometheusOperatorURL     = "https://github.com/prometheus-operator/prometheus-operator/" +
		"releases/download/%s/bundle.yaml"

	certmanagerVersion = "v1.18.2"
	certmanagerURLTmpl = "https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml"
)

func warnError(err error) {
	_, _ = fmt.Fprintf(GinkgoWriter, "warning: %v\n", err)
}

// InstallPrometheusOperator installs the prometheus Operator to be used to export the enabled metrics.
func InstallPrometheusOperator() error {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.Command("kubectl", "create", "-f", url)
	_, err := Run(cmd)
	return err
}

// Run executes the provided command within this context.
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}

	return output, nil
}

// UninstallPrometheusOperator uninstalls the prometheus.
func UninstallPrometheusOperator() {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// InstallCertManager installs the cert manager bundle.
func InstallCertManager() error {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
	cmd := exec.Command("kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	// Wait for cert-manager-webhook to be ready, which can take time if cert-manager
	// was re-installed after uninstalling on a cluster.
	cmd = exec.Command("kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "5m",
	)

	_, err := Run(cmd)
	return err
}

// LoadImageToKindClusterWithName loads a local docker image to the kind cluster.
func LoadImageToKindClusterWithName(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	return err
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is.
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.ReplaceAll(wd, "/test/e2e", "")
	return wd, nil
}

func GetFreePort() (int, error) {
	a, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", a)
	if err != nil {
		return 0, err
	}
	return l.Addr().(*net.TCPAddr).Port, l.Close()
}

func WaitReplicaCount(ctx context.Context, k8sClient client.Client, namespace, app string, replicas int) error {
	var pods corev1.PodList
	for {
		if err := k8sClient.List(ctx, &pods,
			client.InNamespace(namespace), client.MatchingLabels{util.LabelAppKey: app}); err != nil {
			return fmt.Errorf("list app=%s pods failed: %w", app, err)
		}
		if len(pods.Items) == replicas {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %d replicas of %s, got %d", replicas, app, len(pods.Items))
		case <-time.After(time.Second):
			continue
		}
	}
}

type ForwardedCluster struct {
	PodToAddr map[*corev1.Pod]string
	cancel    context.CancelFunc
}

func NewForwardedCluster(ctx context.Context, config *rest.Config,
	namespace, app string, port uint16,
) (*ForwardedCluster, error) {
	ctx, cancel := context.WithCancel(ctx)

	cluster := &ForwardedCluster{
		cancel: cancel,
	}
	if err := cluster.forwardNodes(ctx, config, namespace, app, port); err != nil {
		cancel()
		return nil, fmt.Errorf("forwarding nodes failed: %w", err)
	}

	return cluster, nil
}

func (c *ForwardedCluster) Close() {
	c.cancel()
}

func (c *ForwardedCluster) forwardNodes(ctx context.Context, config *rest.Config,
	namespace, app string, servicePort uint16,
) error {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("unable to create k8s client: %w", err)
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", util.LabelAppKey, app),
	})
	if err != nil {
		return fmt.Errorf("list app %s pods failed: %w", app, err)
	}

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return fmt.Errorf("unable to create k8sround tripper: %w", err)
	}

	c.PodToAddr = make(map[*corev1.Pod]string, len(pods.Items))
	for _, pod := range pods.Items {
		reqURL := clientset.CoreV1().
			RESTClient().
			Post().
			Resource("pods").
			Namespace(namespace).
			Name(pod.Name).
			SubResource("portforward").URL()

		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)

		port, err := GetFreePort()
		if err != nil {
			return fmt.Errorf("failed to get free port: %w", err)
		}

		readyCh := make(chan struct{})
		portforwardErr := make(chan error)
		forwarder, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", port, servicePort)},
			ctx.Done(), readyCh, GinkgoWriter, GinkgoWriter,
		)
		if err != nil {
			return fmt.Errorf("k8s: unable to start port forwarding: %w", err)
		}

		go func() {
			err = forwarder.ForwardPorts()
			if err != nil {
				portforwardErr <- fmt.Errorf("failed to port-forward: %w", err)
			}
		}()

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for port-forwarding to be ready")
		case err := <-portforwardErr:
			c.cancel()
			return fmt.Errorf("port-forwarding error: %w", err)
		case <-readyCh:
		}

		c.PodToAddr[&pod] = fmt.Sprintf("127.0.0.1:%d", port)
	}

	return nil
}

func CapturePodLogs(ctx context.Context, config *rest.Config, namespace, pod string) error {
	if ctx.Done() == nil {
		return fmt.Errorf("context is not cancellable")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("unable to create k8s client: %w", err)
	}

	res, err := clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		return fmt.Errorf("failed to get logs for pod %s in namespace %s: %w", pod, namespace, err)
	}

	go func() {
		defer func() {
			_ = res.Close()
		}()

		buffer := make([]byte, 4096)
		c := 0
		for {
			for c, err = res.Read(buffer); err == nil && c > 0; c, err = res.Read(buffer) {
				GinkgoWriter.Printf(string(buffer[:c]))
			}

			if err != nil {
				GinkgoWriter.Printf("error while reading pod %s:%s logs: %v\n", namespace, pod, err)
				return
			}

			select {
			case <-ctx.Done():
				GinkgoWriter.Printf("context cancelled, stopping log capture for pod %s:%s\n", namespace, pod)
				return
			case <-time.After(time.Millisecond * 100):
			}
		}
	}()

	return nil
}

func SetupCA(ctx context.Context, k8sClient client.Client, namespace string, suffix uint32) {
	ssIssuer := certv1.ClusterIssuer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("issuer-%d", suffix),
		},
		Spec: certv1.IssuerSpec{
			IssuerConfig: certv1.IssuerConfig{
				SelfSigned: &certv1.SelfSignedIssuer{},
			},
		},
	}
	By("creating self-signed issuer")
	Expect(k8sClient.Create(ctx, &ssIssuer)).To(Succeed())
	DeferCleanup(func() {
		if err := k8sClient.Delete(ctx, &ssIssuer); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete self-signed issuer: %v\n", err)
		}
	})

	caCert := certv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("ca-cert-%d", suffix),
		},
		Spec: certv1.CertificateSpec{
			IssuerRef: cmmeta.ObjectReference{
				Kind: "ClusterIssuer",
				Name: ssIssuer.Name,
			},
			IsCA:       true,
			CommonName: fmt.Sprintf("ca-cert-%d", suffix),
			SecretName: fmt.Sprintf("ca-cert-%d", suffix),
		},
	}
	By("creating CA cert")
	Expect(k8sClient.Create(ctx, &caCert)).To(Succeed())
	DeferCleanup(func() {
		if err := k8sClient.Delete(ctx, &caCert); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete CA certificate: %v\n", err)
		}
	})

	issuer := certv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("issuer-%d", suffix),
		},
		Spec: certv1.IssuerSpec{
			IssuerConfig: certv1.IssuerConfig{
				CA: &certv1.CAIssuer{
					SecretName: caCert.Spec.SecretName,
				},
			},
		},
	}
	By("creating Issuer")
	Expect(k8sClient.Create(ctx, &issuer)).To(Succeed())
	DeferCleanup(func() {
		if err := k8sClient.Delete(ctx, &issuer); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete CA issuer: %v\n", err)
		}
	})
}
