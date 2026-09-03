package environment

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sethvargo/go-envconfig"
)

func TestTypes(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Environment Suite")
}

var _ = DescribeTable("Environment variables parsing",
	func(ctx context.Context, vars map[string]string, expected Environment) {
		var result Environment
		Expect(envconfig.ProcessWith(ctx, &envconfig.Config{
			Target:   &result,
			Lookuper: envconfig.MapLookuper(vars),
		})).To(Succeed())
		Expect(result).To(BeEquivalentTo(expected))
	},
	Entry("default values", nil, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		WatchNamespace:      nil,
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("explicit enabled webhook", map[string]string{
		"ENABLE_WEBHOOKS": "true",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("explicit disabled webhook", map[string]string{
		"ENABLE_WEBHOOKS": "false",
	}, Environment{
		EnableWebhooks:      false,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("explicit disabled PDB management", map[string]string{
		"ENABLE_PDB": "false",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           false,
		EnableNetworkPolicy: true,
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("parse single namespace", map[string]string{
		"WATCH_NAMESPACE": "target_namespace",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		WatchNamespace:      []string{"target_namespace"},
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("parse multiple namespace", map[string]string{
		"WATCH_NAMESPACE": "target,namespace",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		WatchNamespace:      []string{"target", "namespace"},
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("empty namespace behaves as not set", map[string]string{
		"WATCH_NAMESPACE": "",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		ResyncPeriod:        30 * time.Second,
	}),
	Entry("explicit resync period", map[string]string{
		"RESYNC_PERIOD": "5m",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
		ResyncPeriod:        5 * time.Minute,
	}),
	Entry("zero resync period disables periodic reconciliation", map[string]string{
		"RESYNC_PERIOD": "0",
	}, Environment{
		EnableWebhooks:      true,
		EnablePDB:           true,
		EnableNetworkPolicy: true,
	}),
)
