package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a controllable time source for deterministic refill/expiry tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestTokenBucketStep(t *testing.T) {
	t0 := time.Unix(0, 0)
	q := Quota{Rate: 10, Burst: 10}

	tests := []struct {
		name        string
		tokens      float64
		elapsed     time.Duration
		cost        int64
		wantAllowed bool
		wantTokens  float64
	}{
		{"full bucket admits", 10, 0, 4, true, 6},
		{"exact balance admits", 3, 0, 3, true, 0},
		{"insufficient rejects", 2, 0, 5, false, 2},
		{"refill over time", 0, 500 * time.Millisecond, 5, true, 0}, // 0 + 0.5s*10 = 5, -5 = 0
		{"refill caps at burst", 0, 10 * time.Second, 1, true, 9},   // capped to 10, -1
		{"cost above burst rejects", 10, 0, 20, false, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, newTokens := tokenBucketStep(tt.tokens, t0, t0.Add(tt.elapsed), q, tt.cost)
			assert.Equal(t, tt.wantAllowed, res.Allowed)
			assert.InDelta(t, tt.wantTokens, newTokens, 1e-9)
		})
	}
}

func TestTokenBucketStep_RetryAfter(t *testing.T) {
	t0 := time.Unix(0, 0)
	// Empty bucket, need 1 token at 10 tokens/sec => 100ms to refill.
	res, _ := tokenBucketStep(0, t0, t0, Quota{Rate: 10, Burst: 10}, 1)
	assert.False(t, res.Allowed)
	assert.InDelta(t, float64(100*time.Millisecond), float64(res.RetryAfter), float64(time.Millisecond))
}

func TestTokenBucket_RefillWithClock(t *testing.T) {
	clk := newFakeClock()
	tb := NewTokenBucket(Config{Default: Quota{Rate: 10, Burst: 10}}, WithClock(clk.now))
	ctx := context.Background()

	// Drain the full bucket.
	res, err := tb.Allow(ctx, "t1", 10)
	require.NoError(t, err)
	require.True(t, res.Allowed)

	// Immediately denied — empty.
	res, _ = tb.Allow(ctx, "t1", 1)
	assert.False(t, res.Allowed)
	assert.Greater(t, res.RetryAfter, time.Duration(0))

	// After 1s, fully refilled.
	clk.advance(time.Second)
	res, _ = tb.Allow(ctx, "t1", 10)
	assert.True(t, res.Allowed)
}

func TestTokenBucket_PerTenantIsolationAndQuota(t *testing.T) {
	tb := NewTokenBucket(Config{
		Default:   Quota{Rate: 1, Burst: 1},
		PerTenant: map[string]Quota{"vip": {Rate: 100, Burst: 100}},
	})
	ctx := context.Background()

	// Draining one tenant doesn't affect another.
	r, _ := tb.Allow(ctx, "free", 1)
	require.True(t, r.Allowed)
	r, _ = tb.Allow(ctx, "free", 1)
	assert.False(t, r.Allowed, "free tenant burst is 1")

	r, _ = tb.Allow(ctx, "vip", 50)
	assert.True(t, r.Allowed, "vip has its own, larger bucket")
	assert.EqualValues(t, 50, r.Remaining)
}

func TestTokenBucket_NegativeCostErrors(t *testing.T) {
	tb := NewTokenBucket(Config{Default: Quota{Rate: 1, Burst: 1}})
	_, err := tb.Allow(context.Background(), "t1", -1)
	assert.Error(t, err)
}

// TestTokenBucket_ConcurrentEnforcement hammers one tenant from many goroutines
// with the clock frozen (no refill); exactly Burst unit-cost requests may pass.
// Run with -race to catch data races.
func TestTokenBucket_ConcurrentEnforcement(t *testing.T) {
	clk := newFakeClock()
	const burst = 100
	tb := NewTokenBucket(Config{Default: Quota{Rate: 0, Burst: burst}}, WithClock(clk.now))
	ctx := context.Background()

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, _ := tb.Allow(ctx, "t1", 1); res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, burst, allowed.Load(), "exactly Burst requests admitted, no more")
}
