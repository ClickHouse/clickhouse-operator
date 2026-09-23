package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/zapr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/ClickHouse/clickhouse-operator/internal/app"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
	"github.com/ClickHouse/clickhouse-operator/internal/version"
)

var setupLog = ctrl.Log.WithName("setup")

func main() {
	if err := run(); err != nil {
		// The logger is not initialized on early failures; duplicate to stderr so the error is never lost.
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		setupLog.Error(err, "startup failed")
		os.Exit(1)
	}
}

func run() error {
	settings := app.Defaults()
	if err := app.LoadEnv(context.Background(), &settings); err != nil {
		return fmt.Errorf("load operator settings: %w", err)
	}

	settings.BindFlags(flag.CommandLine)

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	logger := zap.NewRaw(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(zapr.NewLogger(logger))

	if err := settings.Validate(); err != nil {
		return fmt.Errorf("validate operator settings: %w", err)
	}

	config, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("get kubeconfig: %w", err)
	}

	config.UserAgent = version.BuildUserAgent()

	mgr, err := app.New(app.Components{
		RestConfig: config,
		Logger:     controllerutil.NewLogger(logger),
		Fetcher:    upgrade.NewURLFetcher(),
	}, settings)
	if err != nil {
		return fmt.Errorf("build operator: %w", err)
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
