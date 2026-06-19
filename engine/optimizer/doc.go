// Package optimizer estimates query cost and rewrites plans. It holds the
// cardinality estimator, the cost model, and rule-based rewrites (predicate
// pushdown, projection pushdown, join reordering).
//
// The total cost it produces is what the rate limiter charges per query —
// this package is the novel core of Sluice.
//
// Implemented in Phase 5.
package optimizer
