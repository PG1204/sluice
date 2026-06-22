package limiter

import (
	"context"
	"sync"
	"time"
)

// SlidingWindow is an in-memory, cost-aware sliding-window-counter limiter — the
// configurable alternative to the token bucket. Each tenant may spend up to
// Limit cost units per Window.
//
// It approximates a true sliding log cheaply: it keeps only the current and
// previous fixed windows' totals and weights the previous one by how far the
// current window has progressed. This avoids the fixed-window edge burst (where
// 2x the limit can pass across a boundary) without storing every request's
// timestamp.
type SlidingWindow struct {
	window    time.Duration
	limit     int64
	perTenant map[string]int64
	clock     Clock

	mu      sync.Mutex
	tenants map[string]*windowState
}

type windowState struct {
	start    time.Time // start of the current window
	current  int64     // cost accumulated in the current window
	previous int64     // cost accumulated in the immediately previous window
}

// SlidingOption configures a SlidingWindow.
type SlidingOption func(*SlidingWindow)

// WithSlidingClock overrides the time source (for tests).
func WithSlidingClock(c Clock) SlidingOption {
	return func(s *SlidingWindow) { s.clock = c }
}

// WithTenantLimit sets a per-tenant cost limit, overriding the default.
func WithTenantLimit(tenant string, limit int64) SlidingOption {
	return func(s *SlidingWindow) { s.perTenant[tenant] = limit }
}

// NewSlidingWindow creates a sliding-window limiter allowing defaultLimit cost
// units per window.
func NewSlidingWindow(window time.Duration, defaultLimit int64, opts ...SlidingOption) *SlidingWindow {
	s := &SlidingWindow{
		window:    window,
		limit:     defaultLimit,
		perTenant: make(map[string]int64),
		clock:     systemClock,
		tenants:   make(map[string]*windowState),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *SlidingWindow) limitFor(tenant string) int64 {
	if l, ok := s.perTenant[tenant]; ok {
		return l
	}
	return s.limit
}

// Allow implements Limiter.
func (s *SlidingWindow) Allow(_ context.Context, tenantID string, cost int64) (Result, error) {
	if cost < 0 {
		return Result{}, errNegativeCost
	}
	limit := s.limitFor(tenantID)
	now := s.clock()

	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.tenants[tenantID]
	if !ok {
		st = &windowState{start: now}
		s.tenants[tenantID] = st
	}
	s.roll(st, now)

	// Weighted estimate of usage over the trailing window.
	elapsed := now.Sub(st.start)
	weight := 1.0 - float64(elapsed)/float64(s.window)
	if weight < 0 {
		weight = 0
	}
	estimated := float64(st.previous)*weight + float64(st.current)

	remaining := limit - int64(estimated)
	if estimated+float64(cost) > float64(limit) {
		if remaining < 0 {
			remaining = 0
		}
		return Result{Allowed: false, Remaining: remaining, RetryAfter: s.window - elapsed}, nil
	}

	st.current += cost
	remaining = limit - int64(estimated) - cost
	if remaining < 0 {
		remaining = 0
	}
	return Result{Allowed: true, Remaining: remaining}, nil
}

// roll advances the window state to the window containing now: one step forward
// carries the current count into previous; a longer gap clears both.
func (s *SlidingWindow) roll(st *windowState, now time.Time) {
	elapsed := now.Sub(st.start)
	switch {
	case elapsed < s.window:
		return // still in the current window
	case elapsed < 2*s.window:
		st.previous = st.current
		st.current = 0
		st.start = st.start.Add(s.window)
	default:
		st.previous = 0
		st.current = 0
		st.start = now
	}
}
