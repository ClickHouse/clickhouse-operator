package testutil

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/sethvargo/go-envconfig"
)

// ShardingConfig assigns specs to CI shards by a deterministic hash of the spec name.
type ShardingConfig struct {
	// Shard is "index/total" (e.g. "2/4"); empty runs every spec.
	Shard string `env:"E2E_SHARD"`

	Index int
	Total int
}

// Load reads the sharding configuration from the environment.
func (c *ShardingConfig) Load() error {
	if err := envconfig.Process(context.Background(), c); err != nil {
		return fmt.Errorf("load sharding config from env: %w", err)
	}

	if c.Shard == "" {
		return nil
	}

	idx, total, ok := strings.Cut(c.Shard, "/")
	index, indexErr := strconv.Atoi(idx)
	totalN, totalErr := strconv.Atoi(total)

	if !ok || indexErr != nil || totalErr != nil {
		return fmt.Errorf("invalid E2E_SHARD %q, want \"index/total\" e.g. \"2/4\"", c.Shard)
	}

	c.Index, c.Total = index, totalN
	if c.Total < 1 || c.Index < 1 || c.Index > c.Total {
		return fmt.Errorf("invalid shard %d/%d", c.Index, c.Total)
	}

	return nil
}

// Enabled reports whether the given spec belongs to this shard.
func (c *ShardingConfig) Enabled(spec string) bool {
	if c.Total <= 1 {
		return true
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(spec))
	// Mask to 31 bits so the hash fits a signed int on every platform.
	hash := int(h.Sum32() & 0x7fffffff)

	return hash%c.Total+1 == c.Index
}
