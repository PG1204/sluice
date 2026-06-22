package limiter

import (
	"context"
	"math"
	"sync"
	"time"
)

// Why a token bucket (over leaky bucket or fixed window)? It admits short
// bursts up to Burst while bounding the sustained rate to Rate — a good fit for
// query traffic, which is spiky. Fixed windows allow double-rate bursts at
// window edges; leaky buckets smooth output but don't model "saved up" credit.
// See docs/decisions for the full rationale.

// tokenBucketStep applies one token-bucket check: it refills the bucket for the
// time elapsed since last, then admits the request if enough tokens are
// present. It is pure (no clock, no locks) so it can be unit-tested exhaustively
// and so the Go and Redis/Lua implementations share identical semantics.
//
// It returns the Result and the bucket's new token level. A rejected request
// consumes nothing (the bucket is only refilled, not debited).
func tokenBucketStep(tokens float64, last, now time.Time, q Quota, cost int64) (Result, float64) {
	elapsed := now.Sub(last).Seconds()
	if elapsed < 0 {
		elapsed = 0 // clock went backwards; don't refill negatively
	}
	tokens = math.Min(float64(q.Burst), tokens+elapsed*q.Rate)

	if float64(cost) <= tokens {
		tokens -= float64(cost)
		return Result{Allowed: true, Remaining: int64(tokens)}, tokens
	}

	// Not enough tokens: estimate when the shortfall will have refilled.
	var retry time.Duration
	if q.Rate > 0 {
		need := float64(cost) - tokens
		retry = time.Duration(need / q.Rate * float64(time.Second))
	}
	return Result{Allowed: false, Remaining: int64(tokens), RetryAfter: retry}, tokens
}

// TokenBucket is an in-memory, multi-tenant token-bucket limiter. It needs no
// external services, which makes it ideal for unit/concurrency tests and
// single-process use; the Redis limiter is its distributed counterpart.
type TokenBucket struct {
	cfg   Config
	clock Clock

	mu      sync.Mutex
	buckets map[string]*bucketState
}

type bucketState struct {
	tokens float64
	last   time.Time
}

// Option configures an in-memory limiter.
type Option func(*TokenBucket)

// WithClock overrides the time source (for tests).
func WithClock(c Clock) Option {
	return func(tb *TokenBucket) { tb.clock = c }
}

// NewTokenBucket creates an in-memory token-bucket limiter.
func NewTokenBucket(cfg Config, opts ...Option) *TokenBucket {
	tb := &TokenBucket{
		cfg:     cfg,
		clock:   systemClock,
		buckets: make(map[string]*bucketState),
	}
	for _, opt := range opts {
		opt(tb)
	}
	return tb
}

// Allow implements Limiter. A new tenant starts with a full bucket.
func (tb *TokenBucket) Allow(_ context.Context, tenantID string, cost int64) (Result, error) {
	if cost < 0 {
		return Result{}, errNegativeCost
	}
	q := tb.cfg.quotaFor(tenantID)
	now := tb.clock()

	tb.mu.Lock()
	defer tb.mu.Unlock()

	b, ok := tb.buckets[tenantID]
	if !ok {
		b = &bucketState{tokens: float64(q.Burst), last: now}
		tb.buckets[tenantID] = b
	}

	res, newTokens := tokenBucketStep(b.tokens, b.last, now, q, cost)
	b.tokens = newTokens
	b.last = now
	return res, nil
}
