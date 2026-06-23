// Package api exposes the Sluice query engine as an HTTP/JSON service:
// submit SQL, get results or a cost-annotated plan, list tables, check health,
// and read quota. Requests authenticate with an X-API-Key header that maps to a
// tenant; per-tenant quotas come from config.
//
// This phase builds the service, auth, and endpoints. Charging a query's
// estimated cost against the limiter on /query (cost-based throttling) is wired
// in Phase 8; here /quota reports status read-only.
package api

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/PG1204/sluice/limiter"
)

// Quota mirrors limiter.Quota in the config file format.
type Quota struct {
	Rate  float64 `json:"rate"`
	Burst int64   `json:"burst"`
}

// KeyConfig is one API key's settings: the tenant it authenticates as and an
// optional quota override (otherwise the default quota applies).
type KeyConfig struct {
	Tenant string `json:"tenant"`
	Quota  *Quota `json:"quota,omitempty"`
}

// Config is the service configuration, loaded from a JSON file.
type Config struct {
	// DataDir is the directory of CSV/Parquet table files.
	DataDir string `json:"data_dir"`
	// DefaultQuota applies to any tenant without an explicit quota.
	DefaultQuota Quota `json:"default_quota"`
	// APIKeys maps an API key string to its configuration.
	APIKeys map[string]KeyConfig `json:"api_keys"`
	// CostPerToken is the cost-to-token exchange rate: one token is charged per
	// this many estimated cost units (rounded up, minimum one token per query).
	// Larger values make queries cheaper in tokens; quotas are denominated in
	// tokens. Defaults to DefaultCostPerToken when unset.
	CostPerToken float64 `json:"cost_per_token"`
}

// DefaultCostPerToken is the cost-to-token rate used when config leaves it
// unset: 10 cost units per token.
const DefaultCostPerToken = 10.0

// DefaultConfig returns a ready-to-run dev configuration: the given data
// directory, a modest default quota, and a single demo key. It lets the server
// start without a config file for local use.
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:      dataDir,
		DefaultQuota: Quota{Rate: 10, Burst: 100},
		CostPerToken: DefaultCostPerToken,
		APIKeys: map[string]KeyConfig{
			"dev-key": {Tenant: "dev"},
		},
	}
}

// LoadConfig reads and validates a JSON config file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("config: data_dir is required")
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("config: at least one api key is required")
	}
	for key, kc := range c.APIKeys {
		if kc.Tenant == "" {
			return fmt.Errorf("config: api key %q has no tenant", key)
		}
	}
	return nil
}

// limiterConfig derives the limiter configuration from the API config: the
// default quota plus any per-key quota overrides, keyed by tenant.
func (c Config) limiterConfig() limiter.Config {
	lc := limiter.Config{
		Default:   limiter.Quota{Rate: c.DefaultQuota.Rate, Burst: c.DefaultQuota.Burst},
		PerTenant: make(map[string]limiter.Quota),
	}
	for _, kc := range c.APIKeys {
		if kc.Quota != nil {
			lc.PerTenant[kc.Tenant] = limiter.Quota{Rate: kc.Quota.Rate, Burst: kc.Quota.Burst}
		}
	}
	return lc
}

// keyToTenant builds the API-key -> tenant lookup used for authentication.
func (c Config) keyToTenant() map[string]string {
	m := make(map[string]string, len(c.APIKeys))
	for key, kc := range c.APIKeys {
		m[key] = kc.Tenant
	}
	return m
}
