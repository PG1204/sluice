package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlidingWindow_AdmitsUpToLimit(t *testing.T) {
	clk := newFakeClock()
	sw := NewSlidingWindow(time.Second, 10, WithSlidingClock(clk.now))
	ctx := context.Background()

	r, err := sw.Allow(ctx, "t1", 6)
	require.NoError(t, err)
	assert.True(t, r.Allowed)
	assert.EqualValues(t, 4, r.Remaining)

	r, _ = sw.Allow(ctx, "t1", 4)
	assert.True(t, r.Allowed)

	// Limit reached within the window.
	r, _ = sw.Allow(ctx, "t1", 1)
	assert.False(t, r.Allowed)
	assert.Greater(t, r.RetryAfter, time.Duration(0))
}

func TestSlidingWindow_RollsOver(t *testing.T) {
	clk := newFakeClock()
	sw := NewSlidingWindow(time.Second, 10, WithSlidingClock(clk.now))
	ctx := context.Background()

	r, _ := sw.Allow(ctx, "t1", 10)
	require.True(t, r.Allowed)
	r, _ = sw.Allow(ctx, "t1", 1)
	require.False(t, r.Allowed)

	// Advance two full windows: history clears, full allowance returns.
	clk.advance(2 * time.Second)
	r, _ = sw.Allow(ctx, "t1", 10)
	assert.True(t, r.Allowed)
}

func TestSlidingWindow_WeightsPreviousWindow(t *testing.T) {
	clk := newFakeClock()
	sw := NewSlidingWindow(time.Second, 10, WithSlidingClock(clk.now))
	ctx := context.Background()

	// Fill the first window completely.
	r, _ := sw.Allow(ctx, "t1", 10)
	require.True(t, r.Allowed)

	// Move 50% into the next window: previous window still counts at ~50%
	// weight (≈5 used), so a cost of 6 should be rejected but 5 admitted.
	clk.advance(1500 * time.Millisecond)
	r, _ = sw.Allow(ctx, "t1", 6)
	assert.False(t, r.Allowed, "weighted previous window (~5) + 6 > 10")
	r, _ = sw.Allow(ctx, "t1", 5)
	assert.True(t, r.Allowed)
}

func TestSlidingWindow_PerTenantLimit(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 5, WithTenantLimit("vip", 100))
	ctx := context.Background()

	r, _ := sw.Allow(ctx, "vip", 80)
	assert.True(t, r.Allowed)
	r, _ = sw.Allow(ctx, "free", 80)
	assert.False(t, r.Allowed, "free tenant limited to 5")
}

func TestSlidingWindow_NegativeCostErrors(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 5)
	_, err := sw.Allow(context.Background(), "t1", -1)
	assert.Error(t, err)
}

// Both implementations must satisfy the Limiter interface.
var (
	_ Limiter = (*TokenBucket)(nil)
	_ Limiter = (*SlidingWindow)(nil)
	_ Limiter = (*Redis)(nil)
)
