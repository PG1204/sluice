// Package limiter is Sluice's distributed, cost-aware rate limiter. It is a
// standalone component (no dependency on the engine) implementing a token
// bucket backed by Redis, with atomic consume operations via Lua scripts.
//
// Its consume API takes a cost, not just a count: consume(tenantID, cost).
// That cost comes from the engine's optimizer, which is what makes Sluice's
// throttling cost-aware rather than request-count based.
//
// Implemented in Phase 6.
package limiter
