package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestLoadConfig(t *testing.T) {
	path := writeConfig(t, `{
		"data_dir": "/data",
		"default_quota": {"rate": 5, "burst": 50},
		"api_keys": {
			"k1": {"tenant": "acme", "quota": {"rate": 100, "burst": 1000}},
			"k2": {"tenant": "free"}
		}
	}`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "/data", cfg.DataDir)

	// Auth lookup and limiter config are derived correctly.
	assert.Equal(t, map[string]string{"k1": "acme", "k2": "free"}, cfg.keyToTenant())

	lc := cfg.limiterConfig()
	assert.EqualValues(t, 50, lc.Default.Burst)
	assert.EqualValues(t, 1000, lc.PerTenant["acme"].Burst, "per-key quota override")
	_, hasFree := lc.PerTenant["free"]
	assert.False(t, hasFree, "free tenant has no override, falls back to default")
}

func TestLoadConfig_Errors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"bad JSON", `{"data_dir":`},
		{"missing data_dir", `{"api_keys":{"k":{"tenant":"t"}}}`},
		{"no api keys", `{"data_dir":"/d","api_keys":{}}`},
		{"key without tenant", `{"data_dir":"/d","api_keys":{"k":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tt.body))
			assert.Error(t, err)
		})
	}

	_, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	assert.Error(t, err, "missing file")
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("./somedata")
	assert.Equal(t, "./somedata", cfg.DataDir)
	require.NoError(t, cfg.validate())
	assert.Contains(t, cfg.keyToTenant(), "dev-key")
}
