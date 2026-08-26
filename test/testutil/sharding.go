package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sethvargo/go-envconfig"
)

// ShardingConfig assigns specs to CI shards from a pre-computed plan.
type ShardingConfig struct {
	Index    int    `env:"E2E_SHARD_INDEX, default=0"`
	Total    int    `env:"E2E_SHARD_TOTAL, default=0"`
	PlanPath string `env:"E2E_SHARD_PLAN"`

	shardAssignments map[string]int
}

// Load reads the sharding configuration from the environment and the plan file.
func (c *ShardingConfig) Load() error {
	if err := envconfig.Process(context.Background(), c); err != nil {
		return fmt.Errorf("load sharding config from env: %w", err)
	}

	if c.Total <= 1 {
		return nil
	}

	if c.PlanPath == "" {
		return errors.New("sharding plan path must be set if more than 1 shard")
	}

	if c.Index < 1 || c.Index > c.Total {
		return fmt.Errorf("invalid shard index %d, should be between 1 and %d", c.Index, c.Total)
	}

	data, err := os.ReadFile(filepath.Clean(c.PlanPath))
	if err != nil {
		return fmt.Errorf("read e2e shard plan: %w", err)
	}

	if err = json.Unmarshal(data, &c.shardAssignments); err != nil {
		return fmt.Errorf("decode e2e shard plan: %w", err)
	}

	return nil
}

// Enabled reports whether the given spec belongs to this shard.
func (c *ShardingConfig) Enabled(spec string) (bool, error) {
	if c.Total <= 1 {
		return true, nil
	}

	shard, ok := c.shardAssignments[spec]
	if !ok {
		return false, fmt.Errorf("test %q is not assigned in sharding plan", spec)
	}

	if shard < 1 || shard > c.Total {
		return false, fmt.Errorf("invalid assignment for %q", spec)
	}

	return shard == c.Index, nil
}
