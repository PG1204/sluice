package api

import (
	"strconv"
	"sync"
)

// maxRecentQueries bounds the live query feed retained for the dashboard.
const maxRecentQueries = 100

// QueryEvent is one observed query, recorded for the dashboard's live feed.
type QueryEvent struct {
	RequestID     string  `json:"request_id"`
	Time          string  `json:"time"` // RFC3339
	Tenant        string  `json:"tenant"`
	SQL           string  `json:"sql"`
	Outcome       string  `json:"outcome"` // ok | throttled | error
	EstimatedCost float64 `json:"estimated_cost"`
	Tokens        int64   `json:"tokens"`
	Rows          int64   `json:"rows"`
	LatencyMs     int64   `json:"latency_ms"`
}

// TenantUsage aggregates one tenant's activity.
type TenantUsage struct {
	Tenant         string `json:"tenant"`
	Queries        int64  `json:"queries"`
	Throttled      int64  `json:"throttled"`
	TokensConsumed int64  `json:"tokens_consumed"`
}

// CostBucket is one bar of the cost-distribution histogram.
type CostBucket struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// StatsSnapshot is the dashboard payload returned by GET /stats.
type StatsSnapshot struct {
	TotalQueries   int64         `json:"total_queries"`
	TotalThrottled int64         `json:"total_throttled"`
	Tenants        []TenantUsage `json:"tenants"`
	CostBuckets    []CostBucket  `json:"cost_buckets"`
	Recent         []QueryEvent  `json:"recent"`
}

// costBucketBounds are the upper edges of the cost histogram; the final bucket
// catches everything above the last bound.
var costBucketBounds = []float64{10, 50, 100, 500}

// Collector accumulates query observability data in memory for the dashboard.
// It is safe for concurrent use. State is process-local and lossy by design
// (a bounded feed) — Prometheus is the durable metrics path; this powers the
// live UI.
type Collector struct {
	mu          sync.Mutex
	recent      []QueryEvent
	tenants     map[string]*TenantUsage
	costCounts  []int64
	totalQuery  int64
	totalThrott int64
}

// NewCollector creates an empty collector.
func NewCollector() *Collector {
	return &Collector{
		tenants:    make(map[string]*TenantUsage),
		costCounts: make([]int64, len(costBucketBounds)+1),
	}
}

// Record adds one query event to the rolling stats.
func (c *Collector) Record(ev QueryEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.recent = append(c.recent, ev)
	if len(c.recent) > maxRecentQueries {
		c.recent = c.recent[len(c.recent)-maxRecentQueries:]
	}

	t, ok := c.tenants[ev.Tenant]
	if !ok {
		t = &TenantUsage{Tenant: ev.Tenant}
		c.tenants[ev.Tenant] = t
	}
	t.Queries++
	t.TokensConsumed += ev.Tokens
	c.totalQuery++
	if ev.Outcome == outcomeThrottled {
		t.Throttled++
		c.totalThrott++
	}

	c.costCounts[costBucketIndex(ev.EstimatedCost)]++
}

// Snapshot returns a consistent copy of the current stats for serialization.
// Recent queries are returned newest-first.
func (c *Collector) Snapshot() StatsSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snap := StatsSnapshot{
		TotalQueries:   c.totalQuery,
		TotalThrottled: c.totalThrott,
		Tenants:        make([]TenantUsage, 0, len(c.tenants)),
		CostBuckets:    make([]CostBucket, len(c.costCounts)),
		Recent:         make([]QueryEvent, 0, len(c.recent)),
	}
	for _, t := range c.tenants {
		snap.Tenants = append(snap.Tenants, *t)
	}
	for i, count := range c.costCounts {
		snap.CostBuckets[i] = CostBucket{Label: costBucketLabel(i), Count: count}
	}
	for i := len(c.recent) - 1; i >= 0; i-- {
		snap.Recent = append(snap.Recent, c.recent[i])
	}
	return snap
}

// costBucketIndex returns the histogram bucket a cost falls into.
func costBucketIndex(cost float64) int {
	for i, bound := range costBucketBounds {
		if cost <= bound {
			return i
		}
	}
	return len(costBucketBounds) // overflow bucket
}

// costBucketLabel renders a human-readable label for bucket i.
func costBucketLabel(i int) string {
	switch {
	case i == 0:
		return "0–" + ftoa(costBucketBounds[0])
	case i < len(costBucketBounds):
		return ftoa(costBucketBounds[i-1]) + "–" + ftoa(costBucketBounds[i])
	default:
		return ftoa(costBucketBounds[len(costBucketBounds)-1]) + "+"
	}
}

// ftoa formats a bucket bound compactly (no trailing zeros).
func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
