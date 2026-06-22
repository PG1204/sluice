package optimizer

import (
	"context"
	"fmt"
	"strings"

	"github.com/PG1204/sluice/engine/logical"
)

// ExplainCost renders a plan as an indented tree annotated with each operator's
// estimated output rows and cost, plus the total query cost at the bottom. This
// is the `EXPLAIN COST` output and the human-readable face of the cost model.
//
// Example:
//
//	Project: name, COUNT(*)  (rows=2 cost=8.0)
//	  Aggregate: group by name; aggs COUNT(*)  (rows=2 cost=8.0)
//	    Filter: amount > 100  (rows=2 cost=8.0)
//	      Scan: orders  (rows=8 cost=32.0)
//	Total cost: 56.0
func ExplainCost(ctx context.Context, plan logical.Plan, p *Provider) (string, error) {
	analysis, err := Analyze(ctx, plan, p)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	explainInto(&b, plan, analysis, 0)
	fmt.Fprintf(&b, "Total cost: %.1f\n", analysis.TotalCost(plan))
	return b.String(), nil
}

func explainInto(b *strings.Builder, plan logical.Plan, a *Analysis, depth int) {
	est := a.Of(plan)
	b.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(b, "%s  (rows=%d cost=%.1f)\n", plan.Describe(), est.Rows, est.Cost)
	for _, child := range plan.Children() {
		explainInto(b, child, a, depth+1)
	}
}
