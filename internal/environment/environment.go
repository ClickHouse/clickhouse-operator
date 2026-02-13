package environment

import (
	"context"
	"fmt"

	"github.com/sethvargo/go-envconfig"
)

// Environment holds all environment variables for the application.
type Environment struct {
	EnableWebhooks bool     `env:"ENABLE_WEBHOOKS, default=true"`
	WatchNamespace []string `env:"WATCH_NAMESPACE"`
}

// GetEnvironment processes environment variables and returns an Environment struct.
func GetEnvironment(ctx context.Context) (Environment, error) {
	var env Environment
	if err := envconfig.Process(ctx, &env); err != nil {
		return Environment{}, fmt.Errorf("process environment variables: %w", err)
	}

	return env, nil
}
