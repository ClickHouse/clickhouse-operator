package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/sethvargo/go-envconfig"
)

// Settings holds the operator configuration from command-line flags and environment variables.
// Behavior knobs are environment-sourced so OLM subscription config and helm envOverrides can inject them;
// process wiring stays on kubebuilder-scaffold command-line flags. Flags take precedence over environment.
type Settings struct {
	// Reconciliation behavior.
	EnableWebhooks             bool          `env:"ENABLE_WEBHOOKS, default=true"`
	EnablePDB                  bool          `env:"ENABLE_PDB, default=true"`
	EnableNetworkPolicy        bool          `env:"ENABLE_NETWORK_POLICY, default=true"`
	WatchNamespace             []string      `env:"WATCH_NAMESPACE"`
	ResyncPeriod               time.Duration `env:"RESYNC_PERIOD, default=30s"`
	MaxConcurrentReconciles    int           `env:"MAX_CONCURRENT_RECONCILES, default=4"`
	VersionUpdateInterval      time.Duration `env:"VERSION_UPDATE_INTERVAL, default=24h"`
	DisableVersionUpdateChecks bool          `env:"DISABLE_VERSION_UPDATE_CHECKS, default=false"`

	// Manager process wiring, command-line flags only.
	MetricsAddr          string
	ProbeAddr            string
	EnableLeaderElection bool
	SecureMetrics        bool
	EnableHTTP2          bool
	MetricsCertPath      string
	MetricsCertName      string
	MetricsCertKey       string
	WebhookCertPath      string
	WebhookCertName      string
	WebhookCertKey       string
}

// Defaults returns Settings pre-filled with the defaults of the flag-only fields.
func Defaults() Settings {
	return Settings{
		MetricsAddr:     "0",
		ProbeAddr:       ":8081",
		SecureMetrics:   true,
		MetricsCertName: "tls.crt",
		MetricsCertKey:  "tls.key",
		WebhookCertName: "tls.crt",
		WebhookCertKey:  "tls.key",
	}
}

// BindFlags registers the command-line flags on the given FlagSet, using the current values as defaults.
func (s *Settings) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.MetricsAddr, "metrics-bind-address", s.MetricsAddr, "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.StringVar(&s.ProbeAddr, "health-probe-bind-address", s.ProbeAddr, "The address the probe endpoint binds to.")
	fs.BoolVar(&s.EnableLeaderElection, "leader-elect", s.EnableLeaderElection,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.BoolVar(&s.SecureMetrics, "metrics-secure", s.SecureMetrics,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&s.WebhookCertPath, "webhook-cert-path", s.WebhookCertPath,
		"The directory that contains the webhook certificate.")
	fs.StringVar(&s.WebhookCertName, "webhook-cert-name", s.WebhookCertName, "The name of the webhook certificate file.")
	fs.StringVar(&s.WebhookCertKey, "webhook-cert-key", s.WebhookCertKey, "The name of the webhook key file.")
	fs.StringVar(&s.MetricsCertPath, "metrics-cert-path", s.MetricsCertPath,
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&s.MetricsCertName, "metrics-cert-name", s.MetricsCertName,
		"The name of the metrics server certificate file.")
	fs.StringVar(&s.MetricsCertKey, "metrics-cert-key", s.MetricsCertKey, "The name of the metrics server key file.")
	fs.BoolVar(&s.EnableHTTP2, "enable-http2", s.EnableHTTP2,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers.")
	fs.DurationVar(&s.VersionUpdateInterval, "version-update-interval", s.VersionUpdateInterval,
		"Interval for updating ClickHouse versions list.")
	fs.BoolVar(&s.DisableVersionUpdateChecks, "disable-version-update-checks", s.DisableVersionUpdateChecks,
		"If set, the operator will not check for ClickHouse updates and notify about outdated versions.")
}

// LoadEnv fills the environment-backed fields; call before flag parsing so explicit flags win.
func LoadEnv(ctx context.Context, s *Settings) error {
	if err := envconfig.Process(ctx, s); err != nil {
		return fmt.Errorf("process environment variables: %w", err)
	}

	return nil
}

// Validate checks the configuration invariants across all sources.
func (s *Settings) Validate() error {
	if s.MaxConcurrentReconciles <= 0 {
		return errors.New("MAX_CONCURRENT_RECONCILES must be greater than 0")
	}

	if !s.DisableVersionUpdateChecks && s.VersionUpdateInterval <= 0 {
		return errors.New("--version-update-interval must be greater than 0 when version update checks are enabled")
	}

	return nil
}
