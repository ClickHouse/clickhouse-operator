package testutil

import (
	"context"
	"crypto/md5" //nolint:gosec
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	certmanagerVersion = "v1.19.2"
	certmanagerURLTmpl = "https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml"

	networkPolicyControllerVersion = "v1.1.1"
	networkPolicyControllerURLTmpl = "https://raw.githubusercontent.com" +
		"/kubernetes-sigs/kube-network-policies/%s/install.yaml"

	logTailLines  = 10
	BaseVersion   = "26.3.21.7"
	UpdateVersion = "26.7.5.10"
)

// CurrentSpecHash returns a stable hash for the currently running Ginkgo spec.
func CurrentSpecHash() string {
	hash := md5.Sum([]byte(CurrentSpecReport().FullText())) //nolint:gosec
	return hex.EncodeToString(hash[:8])
}

// Run executes the provided command within this context.
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		GinkgoWriter.Printf("chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	GinkgoWriter.Printf("running: %s\n", command)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%w) %s", command, err, string(output))
	}

	return output, nil
}

// MustRun executes the provided command and fails the test if it returns an error.
func MustRun(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)

	ret, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("cmd %q failed with error: (%w)\n%s", cmd.String(), err, ret)
	}

	return nil
}

// InstallCRDs installs the CRDs into the cluster using make install.
func InstallCRDs(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "make", "install")
	_, err := Run(cmd)

	return err
}

// UninstallCRDs removes the CRDs from the cluster using make uninstall.
func UninstallCRDs(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "make", "uninstall", "ignore-not-found=true")
	_, err := Run(cmd)

	return err
}

// InstallNetworkPolicyController installs kube-network-policies so the default kind CNI enforces NetworkPolicies.
func InstallNetworkPolicyController(ctx context.Context) error {
	url := fmt.Sprintf(networkPolicyControllerURLTmpl, networkPolicyControllerVersion)

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}

	cmd = exec.CommandContext(ctx, "kubectl", "rollout", "status", "daemonset/kube-network-policies",
		"--namespace", "kube-system",
		"--timeout", "2m",
	)

	_, err := Run(cmd)

	return err
}

// InstallCertManager installs the cert manager bundle.
func InstallCertManager(ctx context.Context) error {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	// Wait for cert-manager-webhook to be ready, which can take time if cert-manager
	// was re-installed after uninstalling on a cluster.
	cmd = exec.CommandContext(ctx, "kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "10m",
	)

	_, err := Run(cmd)

	return err
}

// GetProjectDir will return the directory where the project is by walking
// up from the current working directory until it finds a go.mod file.
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, fmt.Errorf("get project dir: %w", err)
	}

	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return wd, fmt.Errorf("could not find project root (go.mod) from %s", wd)
		}

		dir = parent
	}
}

// SetupCA sets up a self-signed CA issuer and a CA certificate in the given namespace.
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
	DeferCleanup(func(ctx context.Context) {
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
			IssuerRef: cmmeta.IssuerReference{
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
	DeferCleanup(func(ctx context.Context) {
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
	DeferCleanup(func(ctx context.Context) {
		if err := k8sClient.Delete(ctx, &issuer); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete CA issuer: %v\n", err)
		}
	})
}

// PreloadImages pulls each image from the registry and loads it into the kind cluster.
// All images are processed in parallel.
func PreloadImages(ctx context.Context, images []string) *errgroup.Group {
	g, ctx := errgroup.WithContext(ctx)

	for _, image := range images {
		g.Go(func() error {
			// Remove any cached manifest-index
			_ = exec.CommandContext(ctx, "docker", "image", "rm", image).Run()

			By("pulling image:" + image)

			pull := exec.CommandContext(ctx, "docker", "pull", "--platform", "linux/"+runtime.GOARCH, image)
			if out, err := pull.CombinedOutput(); err != nil {
				return fmt.Errorf("docker pull %s: %w\n%s", image, err, out)
			}

			By("loading image into kind: " + image)

			load := exec.CommandContext(ctx, "kind", "load", "docker-image", image)
			if out, err := load.CombinedOutput(); err != nil {
				return fmt.Errorf("kind load %s: %w\n%s", image, err, out)
			}

			return nil
		})
	}

	return g
}

// TestNamespace returns the deterministic namespace name unique for every test.
func TestNamespace() string {
	return "e2e-" + CurrentSpecHash()
}

// EnsureNamespace ensures the test namespace is created and active.
func EnsureNamespace(ctx context.Context, env *Env, name string) {
	cli := env.Client

	DeferCleanup(func(ctx context.Context) {
		ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

		err := cli.Get(ctx, types.NamespacedName{Name: name}, &ns)
		if err != nil {
			return
		}

		if err := cli.Delete(ctx, &ns); err != nil {
			GinkgoWriter.Printf("failed to delete namespace %s: %v\n", name, err)
		}
	})

	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := cli.Get(ctx, types.NamespacedName{Name: name}, &ns)

	if k8serrors.IsNotFound(err) {
		ExpectWithOffset(1, cli.Create(ctx, &ns)).To(Succeed())
		return
	}

	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	if ns.Status.Phase != corev1.NamespaceTerminating {
		return
	}

	Eventually(func() bool {
		err := cli.Get(ctx, types.NamespacedName{Name: name}, &ns)
		return k8serrors.IsNotFound(err)
	}, "5s", "100ms").Should(BeTrue())

	ExpectWithOffset(1, cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
}

// EnsureTestNamespace ensures that unique per test namespace is created and returns its name.
func EnsureTestNamespace(ctx context.Context, env *Env) string {
	ns := TestNamespace()
	EnsureNamespace(ctx, env, ns)
	return ns
}
