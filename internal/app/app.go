package app

import (
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	clickhousecomv1alpha1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
	whchv1 "github.com/ClickHouse/clickhouse-operator/internal/webhook/v1alpha1"
)

// Below the deployment's terminationGracePeriodSeconds so the post-drain lease release still happens before SIGKILL.
const shutdownTimeout = 8 * time.Second

// Components holds the runtime objects the operator is built from, replaceable in tests.
type Components struct {
	RestConfig *rest.Config
	Logger     controllerutil.Logger
	Dialer     controllerutil.DialContextFunc
	Fetcher    upgrade.Fetcher
}

// NewScheme returns the runtime scheme with all types the operator works with.
func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clickhousecomv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme

	return scheme
}

// New builds the controller manager with all operator components wired according to the settings.
// The caller owns starting it.
func New(components Components, settings Settings) (ctrl.Manager, error) {
	var tlsOpts []func(*tls.Config)
	if !settings.EnableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			components.Logger.Info("disabling http/2")

			c.NextProtos = []string{"http/1.1"}
		})
	}

	webhookServerOptions := webhook.Options{
		TLSOpts: tlsOpts,
	}

	if len(settings.WebhookCertPath) > 0 {
		components.Logger.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", settings.WebhookCertPath,
			"webhook-cert-name", settings.WebhookCertName, "webhook-cert-key", settings.WebhookCertKey)

		webhookServerOptions.CertDir = settings.WebhookCertPath
		webhookServerOptions.CertName = settings.WebhookCertName
		webhookServerOptions.KeyName = settings.WebhookCertKey
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   settings.MetricsAddr,
		SecureServing: settings.SecureMetrics,
		TLSOpts:       tlsOpts,
	}

	if settings.SecureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if len(settings.MetricsCertPath) > 0 {
		components.Logger.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", settings.MetricsCertPath,
			"metrics-cert-name", settings.MetricsCertName, "metrics-cert-key", settings.MetricsCertKey)

		metricsServerOptions.CertDir = settings.MetricsCertPath
		metricsServerOptions.CertName = settings.MetricsCertName
		metricsServerOptions.KeyName = settings.MetricsCertKey
	}

	timeout := shutdownTimeout
	mgrOptions := ctrl.Options{
		Scheme:                        NewScheme(),
		Metrics:                       metricsServerOptions,
		WebhookServer:                 webhook.NewServer(webhookServerOptions),
		HealthProbeBindAddress:        settings.ProbeAddr,
		LeaderElection:                settings.EnableLeaderElection,
		LeaderElectionID:              "d4ceba06.clickhouse.com",
		LeaderElectionReleaseOnCancel: true,
		GracefulShutdownTimeout:       &timeout,
	}

	if len(settings.WatchNamespace) > 0 {
		components.Logger.Info("Watching namespaces", "namespaces", settings.WatchNamespace)

		mgrOptions.Cache.DefaultNamespaces = make(map[string]cache.Config, len(settings.WatchNamespace))
		for _, ns := range settings.WatchNamespace {
			mgrOptions.Cache.DefaultNamespaces[ns] = cache.Config{}
		}
	} else {
		components.Logger.Info("Watching all namespaces")
	}

	mgr, err := ctrl.NewManager(components.RestConfig, mgrOptions)
	if err != nil {
		return nil, fmt.Errorf("create manager: %w", err)
	}

	if err := controllerutil.DetectOpenShift(components.RestConfig); err != nil {
		components.Logger.Error(err, "OpenShift detection failed; falling back to vanilla defaults")
	}

	components.Logger.Info("platform detected", "openshift", controllerutil.IsOpenShift())

	var upgradeChecker *upgrade.Checker
	if !settings.DisableVersionUpdateChecks {
		if components.Fetcher == nil {
			return nil, errors.New("components.Fetcher is required when version update checks are enabled")
		}

		updater := upgrade.NewReleaseUpdater(components.Fetcher, settings.VersionUpdateInterval, components.Logger)
		if err := mgr.Add(updater); err != nil {
			return nil, fmt.Errorf("add release updater to manager: %w", err)
		}

		upgradeChecker = upgrade.NewChecker(updater)
	}

	deps := chctrl.Dependencies{Logger: components.Logger, Checker: upgradeChecker, Dialer: components.Dialer}
	controllerSettings := chctrl.Settings{
		EnablePDB:               settings.EnablePDB,
		EnableNetworkPolicy:     settings.EnableNetworkPolicy,
		ResyncPeriod:            settings.ResyncPeriod,
		MaxConcurrentReconciles: settings.MaxConcurrentReconciles,
	}

	if err := keeper.SetupWithManager(mgr, deps, controllerSettings); err != nil {
		return nil, fmt.Errorf("setup KeeperCluster controller: %w", err)
	}

	if err := clickhouse.SetupWithManager(mgr, deps, controllerSettings); err != nil {
		return nil, fmt.Errorf("setup ClickHouseCluster controller: %w", err)
	}

	if settings.EnableWebhooks {
		if err := whchv1.SetupKeeperWebhookWithManager(mgr, components.Logger); err != nil {
			return nil, fmt.Errorf("setup KeeperCluster webhook: %w", err)
		}

		if err := whchv1.SetupClickHouseWebhookWithManager(mgr, components.Logger); err != nil {
			return nil, fmt.Errorf("setup ClickHouseCluster webhook: %w", err)
		}
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("setup healthz checker: %w", err)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("setup readyz checker: %w", err)
	}

	return mgr, nil
}
