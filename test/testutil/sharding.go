package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sethvargo/go-envconfig"
)

// ShardingConfig assigns specs to CI shards. Two modes are supported:
//
//   - Round-robin (new): set E2E_SHARD to "index/total" (e.g. "2/4"). Specs are
//     spread by a deterministic hash of the spec name, so every shard derives the
//     same assignment independently and each spec runs on exactly one shard — no
//     pre-computed plan. Takes precedence when E2E_SHARD is set.
//
//   - Plan-based / "smart" (legacy): set E2E_SHARD_INDEX + E2E_SHARD_TOTAL +
//     E2E_SHARD_PLAN (a JSON spec->shard map from the e2e-shard-planner). Used
//     when E2E_SHARD is empty; this is what .github/workflows/ci.yaml drives.
//
// Duration-weighted sharding can later feed the plan-based mode (or replace the
// round-robin hash) using per-spec timings collected by main CI.
type ShardingConfig struct {
	// Round-robin mode: "index/total". Takes precedence when non-empty.
	Shard string `env:"E2E_SHARD"`

	// Plan-based (legacy) mode.
	Index    int    `env:"E2E_SHARD_INDEX, default=0"`
	Total    int    `env:"E2E_SHARD_TOTAL, default=0"`
	PlanPath string `env:"E2E_SHARD_PLAN"`

	roundRobin       bool
	shardAssignments map[string]int
}

// Load reads the sharding configuration from the environment (and, in plan-based
// mode, the plan file).
func (c *ShardingConfig) Load() error {
	if err := envconfig.Process(context.Background(), c); err != nil {
		return fmt.Errorf("load sharding config from env: %w", err)
	}

	// Round-robin mode takes precedence over the legacy plan env vars.
	if c.Shard != "" {
		idx, total, ok := strings.Cut(c.Shard, "/")
		indexErr := error(nil)
		totalErr := error(nil)
		c.Index, indexErr = strconv.Atoi(idx)
		c.Total, totalErr = strconv.Atoi(total)
		if !ok || indexErr != nil || totalErr != nil {
			return fmt.Errorf("invalid E2E_SHARD %q, want \"index/total\" e.g. \"2/4\"", c.Shard)
		}
		if c.Total < 1 || c.Index < 1 || c.Index > c.Total {
			return fmt.Errorf("invalid shard %d/%d", c.Index, c.Total)
		}
		c.roundRobin = true
		return nil
	}

	// Legacy plan-based mode.
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

	if c.roundRobin {
		h := fnv.New32a()
		_, _ = h.Write([]byte(spec))
		shard := int(h.Sum32()%uint32(c.Total)) + 1
		return shard == c.Index, nil
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
