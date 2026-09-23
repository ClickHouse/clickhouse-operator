package app

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sethvargo/go-envconfig"
)

func TestSettings(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Settings Suite")
}

var _ = DescribeTable("Environment variables parsing",
	func(ctx context.Context, vars map[string]string, expected Settings) {
		var result Settings
		Expect(envconfig.ProcessWith(ctx, &envconfig.Config{
			Target:   &result,
			Lookuper: envconfig.MapLookuper(vars),
		})).To(Succeed())
		Expect(result).To(BeEquivalentTo(expected))
	},
	Entry("default values", nil, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		WatchNamespace:          nil,
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("explicit enabled webhook", map[string]string{
		"ENABLE_WEBHOOKS": "true",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("explicit disabled webhook", map[string]string{
		"ENABLE_WEBHOOKS": "false",
	}, Settings{
		EnableWebhooks:          false,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("explicit disabled PDB management", map[string]string{
		"ENABLE_PDB": "false",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               false,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("parse single namespace", map[string]string{
		"WATCH_NAMESPACE": "target_namespace",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		WatchNamespace:          []string{"target_namespace"},
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("parse multiple namespace", map[string]string{
		"WATCH_NAMESPACE": "target,namespace",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		WatchNamespace:          []string{"target", "namespace"},
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("empty namespace behaves as not set", map[string]string{
		"WATCH_NAMESPACE": "",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		ResyncPeriod:            30 * time.Second,
	}),
	Entry("explicit resync period", map[string]string{
		"RESYNC_PERIOD": "5m",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
		ResyncPeriod:            5 * time.Minute,
	}),
	Entry("explicit max concurrent reconciles", map[string]string{
		"MAX_CONCURRENT_RECONCILES": "1",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		ResyncPeriod:            30 * time.Second,
		MaxConcurrentReconciles: 1,
		VersionUpdateInterval:   24 * time.Hour,
	}),
	Entry("zero resync period disables periodic reconciliation", map[string]string{
		"RESYNC_PERIOD": "0",
	}, Settings{
		EnableWebhooks:          true,
		EnablePDB:               true,
		EnableNetworkPolicy:     true,
		MaxConcurrentReconciles: 4,
		VersionUpdateInterval:   24 * time.Hour,
	}),
)

var _ = DescribeTable(
	"Version update env parsing",
	func(ctx context.Context, vars map[string]string, interval time.Duration, disabled bool) {
		var result Settings
		Expect(envconfig.ProcessWith(ctx, &envconfig.Config{
			Target:   &result,
			Lookuper: envconfig.MapLookuper(vars),
		})).To(Succeed())
		Expect(result.VersionUpdateInterval).To(Equal(interval))
		Expect(result.DisableVersionUpdateChecks).To(Equal(disabled))
	},
	Entry("defaults", nil, 24*time.Hour, false),
	Entry("explicit interval", map[string]string{"VERSION_UPDATE_INTERVAL": "1h"}, time.Hour, false),
	Entry("disabled checks", map[string]string{"DISABLE_VERSION_UPDATE_CHECKS": "true"}, 24*time.Hour, true),
)

var _ = DescribeTable(
	"Settings validation",
	func(mutate func(*Settings), wantError string) {
		settings := Defaults()
		settings.MaxConcurrentReconciles = 4
		settings.VersionUpdateInterval = 24 * time.Hour
		mutate(&settings)

		err := settings.Validate()
		if wantError != "" {
			Expect(err).To(MatchError(wantError))
		} else {
			Expect(err).ToNot(HaveOccurred())
		}
	},
	Entry("should accept the defaults", func(*Settings) {}, ""),
	Entry("should reject zero max concurrent reconciles",
		func(s *Settings) { s.MaxConcurrentReconciles = 0 },
		"MAX_CONCURRENT_RECONCILES must be greater than 0"),
	Entry("should reject negative max concurrent reconciles",
		func(s *Settings) { s.MaxConcurrentReconciles = -1 },
		"MAX_CONCURRENT_RECONCILES must be greater than 0"),
	Entry("should reject zero version update interval if checks are enabled",
		func(s *Settings) { s.VersionUpdateInterval = 0 },
		"--version-update-interval must be greater than 0 when version update checks are enabled"),
	Entry("should reject negative version update interval if checks are enabled",
		func(s *Settings) { s.VersionUpdateInterval = -time.Second },
		"--version-update-interval must be greater than 0 when version update checks are enabled"),
	Entry("should allow zero version update interval if checks are disabled",
		func(s *Settings) { s.VersionUpdateInterval = 0; s.DisableVersionUpdateChecks = true },
		""),
)
