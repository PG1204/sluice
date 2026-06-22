package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the Redis limiter's pure logic (result parsing, argument
// validation) without needing a running Redis. The end-to-end Redis round trip
// is covered by the build-tagged integration tests in redis_test.go.

func TestParseScriptResult(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		res, err := parseScriptResult([]any{int64(1), int64(7), int64(0)})
		require.NoError(t, err)
		assert.Equal(t, Result{Allowed: true, Remaining: 7}, res)
	})
	t.Run("denied with retry", func(t *testing.T) {
		res, err := parseScriptResult([]any{int64(0), int64(0), int64(250)})
		require.NoError(t, err)
		assert.False(t, res.Allowed)
		assert.Equal(t, 250*time.Millisecond, res.RetryAfter)
	})
	t.Run("wrong arity", func(t *testing.T) {
		_, err := parseScriptResult([]any{int64(1), int64(2)})
		assert.Error(t, err)
	})
	t.Run("non-integer reply", func(t *testing.T) {
		_, err := parseScriptResult([]any{"nope", int64(2), int64(3)})
		assert.Error(t, err)
	})
}

func TestToInt64(t *testing.T) {
	got, err := toInt64(int64(5))
	require.NoError(t, err)
	assert.EqualValues(t, 5, got)

	got, err = toInt64(int(9))
	require.NoError(t, err)
	assert.EqualValues(t, 9, got)

	_, err = toInt64("x")
	assert.Error(t, err)
}

func TestRedis_NegativeCostErrorsBeforeRedis(t *testing.T) {
	// nil client is fine: the cost check happens before any Redis call.
	lim := NewRedis(nil, Config{Default: Quota{Rate: 1, Burst: 1}})
	_, err := lim.Allow(context.Background(), "t1", -1)
	assert.Error(t, err)
}

func TestRedis_QuotaResolution(t *testing.T) {
	cfg := Config{
		Default:   Quota{Rate: 1, Burst: 1},
		PerTenant: map[string]Quota{"vip": {Rate: 100, Burst: 100}},
	}
	assert.Equal(t, Quota{Rate: 100, Burst: 100}, cfg.quotaFor("vip"))
	assert.Equal(t, Quota{Rate: 1, Burst: 1}, cfg.quotaFor("someone-else"))
}
