//go:build integration

// These tests need a real Redis. Run them with: make test-integration
// (or `go test -tags=integration ./limiter/`). They are skipped by the default
// `go test ./...` run. Point them at a non-default server with REDIS_ADDR.

package limiter

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return rdb
}

func TestRedis_AllowAndDeny(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	ctx := context.Background()
	require.NoError(t, rdb.Del(ctx, keyPrefix+"t1").Err())

	clk := newFakeClock()
	lim := NewRedis(rdb, Config{Default: Quota{Rate: 10, Burst: 10}}, WithRedisClock(clk.now))

	res, err := lim.Allow(ctx, "t1", 10)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.EqualValues(t, 0, res.Remaining)

	res, err = lim.Allow(ctx, "t1", 1)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Greater(t, res.RetryAfter, time.Duration(0))

	// Refill after advancing the (injected) clock by 1s.
	clk.advance(time.Second)
	res, err = lim.Allow(ctx, "t1", 5)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

// TestRedis_AtomicUnderConcurrency proves the Lua script enforces the quota
// across many concurrent clients: with the clock frozen, exactly Burst unit
// requests are admitted.
func TestRedis_AtomicUnderConcurrency(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	ctx := context.Background()
	require.NoError(t, rdb.Del(ctx, keyPrefix+"hammer").Err())

	clk := newFakeClock()
	const burst = 100
	lim := NewRedis(rdb, Config{Default: Quota{Rate: 0, Burst: burst}}, WithRedisClock(clk.now))

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := lim.Allow(ctx, "hammer", 1); err == nil && res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, burst, allowed.Load())
}
