package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	clickhousecomv1alpha1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/environment"
	"github.com/ClickHouse/clickhouse-operator/internal/version"
	whchv1 "github.com/ClickHouse/clickhouse-operator/internal/webhook/v1alpha1"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/zapr"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clickhousecomv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	if err := run(); err != nil {
		setupLog.Error(err, "startup failed")
		os.Exit(1)
	}
}

func run() error {
	var (
		metricsAddr                                      string
		metricsCertPath, metricsCertName, metricsCertKey string
		webhookCertPath, webhookCertName, webhookCertKey string
		enableLeaderElection                             bool
		probeAddr                                        string
		secureMetrics                                    bool
		enableHTTP2                                      bool
		tlsOpts                                          []func(*tls.Config)
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	logger := zap.NewRaw(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(zapr.NewLogger(logger))

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")

		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	config, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("get kubeconfig: %w", err)
	}

	config.UserAgent = version.BuildUserAgent()

	mgrOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "d4ceba06.clickhouse.com",
	}

	env, err := environment.GetEnvironment(context.Background())
	if err != nil {
		return fmt.Errorf("load manager : %w", err)
	}

	if len(env.WatchNamespace) > 0 {
		setupLog.Info("Watching namespaces", "namespaces", env.WatchNamespace)

		mgrOptions.Cache.DefaultNamespaces = make(map[string]cache.Config, len(env.WatchNamespace))
		for _, ns := range env.WatchNamespace {
			mgrOptions.Cache.DefaultNamespaces[ns] = cache.Config{}
		}
	} else {
		setupLog.Info("Watching all namespaces")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOptions)
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	zapLogger := controllerutil.NewLogger(logger)

	if err = keeper.SetupWithManager(mgr, zapLogger); err != nil {
		return fmt.Errorf("unable to setup KeeperCluster controller: %w", err)
	}

	if err = clickhouse.SetupWithManager(mgr, zapLogger); err != nil {
		return fmt.Errorf("unable to setup ClickHouseCluster controller: %w", err)
	}

	// +kubebuilder:scaffold:builder

	if env.EnableWebhooks {
		if err = whchv1.SetupKeeperWebhookWithManager(mgr, zapLogger); err != nil {
			return fmt.Errorf("unable to setup KeeperCluster webhook: %w", err)
		}

		if err = whchv1.SetupClickHouseWebhookWithManager(mgr, zapLogger); err != nil {
			return fmt.Errorf("unable to setup ClickHouseCluster webhook: %w", err)
		}
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to setup healthz checker: %w", err)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to setup readyz checker: %w", err)
	}

	setupLog.Info("starting manager",
		"version", version.Version,
		"gitCommitHash", version.GitCommitHash,
		"buildTime", version.BuildTime,
	)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	return nil
}
