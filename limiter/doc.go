// Package limiter is Sluice's cost-aware rate limiter — a standalone component
// with no dependency on the engine.
//
// All limiters implement the Limiter interface: Allow(ctx, tenantID, cost). The
// cost parameter is the point — callers pass a query's estimated cost (from the
// engine's optimizer), so expensive queries draw down quota faster than cheap
// ones, rather than every request counting as 1.
//
// Three implementations share that interface:
//   - TokenBucket: in-memory token bucket (tokenbucket.go) — burst-tolerant,
//     no external services; ideal for tests and single-process use.
//   - SlidingWindow: in-memory sliding-window counter (sliding.go) — the
//     configurable alternative algorithm.
//   - Redis: distributed token bucket (redis.go) — shares one bucket per tenant
//     across many processes, with the refill-and-consume step run atomically as
//     a Lua script on the server.
package limiter
