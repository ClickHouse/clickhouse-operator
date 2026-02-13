package environment

import (
	"context"
	"testing"

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
		EnableWebhooks: true,
		WatchNamespace: nil,
	}),
	Entry("explicit enabled webhook", map[string]string{
		"ENABLE_WEBHOOKS": "true",
	}, Environment{
		EnableWebhooks: true,
	}),
	Entry("explicit disabled webhook", map[string]string{
		"ENABLE_WEBHOOKS": "false",
	}, Environment{
		EnableWebhooks: false,
	}),
	Entry("parse single namespace", map[string]string{
		"WATCH_NAMESPACE": "target_namespace",
	}, Environment{
		EnableWebhooks: true,
		WatchNamespace: []string{"target_namespace"},
	}),
	Entry("parse multiple namespace", map[string]string{
		"WATCH_NAMESPACE": "target,namespace",
	}, Environment{
		EnableWebhooks: true,
		WatchNamespace: []string{"target", "namespace"},
	}),
	Entry("empty namespace behaves as not set", map[string]string{
		"WATCH_NAMESPACE": "",
	}, Environment{
		EnableWebhooks: true,
	}),
)
