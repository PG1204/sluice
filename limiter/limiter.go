package limiter

import (
	"context"
	"errors"
	"time"
)

// errNegativeCost is returned when a caller passes a negative cost.
var errNegativeCost = errors.New("limiter: cost must not be negative")

// Result is the outcome of a rate-limit check.
type Result struct {
	// Allowed reports whether the request may proceed.
	Allowed bool
	// Remaining is the tokens left in the tenant's bucket after the check
	// (floored to a whole number).
	Remaining int64
	// RetryAfter, when not allowed, is how long until enough tokens accrue for
	// the request to succeed. Zero when allowed.
	RetryAfter time.Duration
}

// Quota is a tenant's token-bucket configuration.
type Quota struct {
	// Rate is the number of tokens refilled per second (the sustained rate).
	Rate float64
	// Burst is the bucket capacity (the most tokens that can accumulate, and so
	// the largest single request that can ever be admitted).
	Burst int64
}

// Config holds quotas per tenant with a fallback default, so each API key can
// have its own limit while unconfigured tenants still get sensible bounds.
type Config struct {
	Default   Quota
	PerTenant map[string]Quota
}

// QuotaFor returns the quota for a tenant, falling back to the default.
func (c Config) QuotaFor(tenant string) Quota {
	if q, ok := c.PerTenant[tenant]; ok {
		return q
	}
	return c.Default
}

// Limiter decides whether a tenant may spend a given cost. The cost parameter
// is what makes Sluice's limiting cost-aware: callers pass the query's
// estimated cost (from the optimizer), not a flat 1-per-request. Implementations
// are safe for concurrent use.
type Limiter interface {
	// Allow attempts to consume cost tokens from tenantID's bucket, reporting
	// whether it succeeded along with the remaining balance and retry hint.
	Allow(ctx context.Context, tenantID string, cost int64) (Result, error)
}

// Clock returns the current time; injectable so tests can advance time
// deterministically instead of sleeping.
type Clock func() time.Time

// systemClock is the default wall-clock.
func systemClock() time.Time { return time.Now() }

// keyPrefix namespaces limiter state (e.g. in Redis) to avoid collisions.
const keyPrefix = "sluice:rl:"
