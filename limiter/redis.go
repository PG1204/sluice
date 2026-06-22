package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is a distributed token-bucket limiter backed by Redis. It is the
// production counterpart to the in-memory TokenBucket: multiple API server
// instances share one bucket per tenant, so a tenant's quota is enforced across
// the whole fleet, not per process.
//
// The refill-and-consume step runs as a single Lua script. Why Lua? It executes
// atomically on the Redis server, so the read-modify-write of the bucket can't
// interleave between concurrent callers — no distributed lock, no race, one
// round trip. The script mirrors tokenBucketStep exactly.
type Redis struct {
	rdb    redis.Scripter
	cfg    Config
	clock  Clock
	script *redis.Script
}

// tokenBucketScript performs an atomic refill-and-consume. It returns
// {allowed (0|1), remaining tokens, retry-after ms}.
//
// KEYS[1] = bucket key
// ARGV    = rate (tokens/sec), burst (capacity), now (ms), cost
var tokenBucketScript = redis.NewScript(`
local key   = KEYS[1]
local rate  = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now   = tonumber(ARGV[3])
local cost  = tonumber(ARGV[4])

local state  = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(state[1])
local ts     = tonumber(state[2])
if tokens == nil then
  tokens = burst
  ts = now
end

local elapsed = math.max(0, now - ts) / 1000.0
tokens = math.min(burst, tokens + elapsed * rate)

local allowed = 0
local retry = 0
if cost <= tokens then
  allowed = 1
  tokens = tokens - cost
elseif rate > 0 then
  retry = math.ceil(((cost - tokens) / rate) * 1000)
end

redis.call('HMSET', key, 'tokens', tokens, 'ts', now)
-- Expire idle buckets once they would have refilled to full, to bound memory.
local ttl = math.ceil((burst / math.max(rate, 0.0001)) * 1000) + 1000
redis.call('PEXPIRE', key, ttl)

return {allowed, math.floor(tokens), retry}
`)

// NewRedis creates a Redis-backed token-bucket limiter using the given client.
func NewRedis(rdb redis.Scripter, cfg Config, opts ...RedisOption) *Redis {
	r := &Redis{rdb: rdb, cfg: cfg, clock: systemClock, script: tokenBucketScript}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RedisOption configures a Redis limiter.
type RedisOption func(*Redis)

// WithRedisClock overrides the time source (for tests).
func WithRedisClock(c Clock) RedisOption {
	return func(r *Redis) { r.clock = c }
}

// Allow implements Limiter, running the atomic bucket update on the server.
func (r *Redis) Allow(ctx context.Context, tenantID string, cost int64) (Result, error) {
	if cost < 0 {
		return Result{}, errNegativeCost
	}
	q := r.cfg.quotaFor(tenantID)
	now := r.clock().UnixMilli()

	out, err := r.script.Run(ctx, r.rdb, []string{keyPrefix + tenantID}, q.Rate, q.Burst, now, cost).Slice()
	if err != nil {
		return Result{}, fmt.Errorf("limiter: redis eval: %w", err)
	}
	return parseScriptResult(out)
}

// parseScriptResult converts the Lua reply {allowed, remaining, retry_ms} into
// a Result. Split out from Allow so it can be unit-tested without Redis.
func parseScriptResult(out []any) (Result, error) {
	if len(out) != 3 {
		return Result{}, fmt.Errorf("limiter: unexpected script result %v", out)
	}
	allowed, err := toInt64(out[0])
	if err != nil {
		return Result{}, err
	}
	remaining, err := toInt64(out[1])
	if err != nil {
		return Result{}, err
	}
	retryMs, err := toInt64(out[2])
	if err != nil {
		return Result{}, err
	}
	return Result{
		Allowed:    allowed == 1,
		Remaining:  remaining,
		RetryAfter: time.Duration(retryMs) * time.Millisecond,
	}, nil
}

// toInt64 coerces a Lua/Redis numeric reply to int64.
func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("limiter: expected integer reply, got %T", v)
	}
}
