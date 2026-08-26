package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

// SetupOptions configures the shared suite bootstrap.
type SetupOptions struct {
	// CertManagerScheme registers cert-manager types into the scheme.
	CertManagerScheme bool
	// RequireExplicitImageTag rejects cluster CR creation without an image tag.
	RequireExplicitImageTag bool
}

// SetupEnv resolves kubeconfig, registers schemes and builds the suite Env.
func SetupEnv(opts SetupOptions) *Env {
	GinkgoHelper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	Expect(v1.AddToScheme(scheme.Scheme)).To(Succeed())
	// +kubebuilder:scaffold:scheme

	if opts.CertManagerScheme {
		Expect(certv1.AddToScheme(scheme.Scheme)).To(Succeed())
	}

	baseClient, err := client.NewWithWatch(config, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	cli := client.Client(baseClient)
	if opts.RequireExplicitImageTag {
		cli = interceptor.NewClient(baseClient, interceptor.Funcs{Create: requireExplicitImageTag})
	}

	return &Env{Client: cli, Config: config, Dialer: NewPortForwardDialer(config)}
}

func requireExplicitImageTag(
	ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption,
) error {
	switch cr := obj.(type) {
	case *v1.ClickHouseCluster:
		if cr.Spec.ContainerTemplate.Image.Tag == "" {
			return fmt.Errorf("refusing to create ClickHouseCluster %q without an explicit image tag", cr.Name)
		}
	case *v1.KeeperCluster:
		if cr.Spec.ContainerTemplate.Image.Tag == "" {
			return fmt.Errorf("refusing to create KeeperCluster %q without an explicit image tag", cr.Name)
		}
	}

	if err := cli.Create(ctx, obj, opts...); err != nil {
		return fmt.Errorf("create %T: %w", obj, err)
	}

	return nil
}
