package logical

import (
	"strconv"
	"strings"

	"github.com/PG1204/sluice/engine/ast"
)

// Explain renders a logical plan as an indented tree, root at the top and
// inputs nested beneath. It is the EXPLAIN output and the primary debugging
// aid for planning; Phase 5 extends each line with cost estimates.
//
// Example:
//
//	Limit: 5
//	  Sort: orders DESC
//	    Project: u.name, COUNT(*) AS orders
//	      Aggregate: group by u.name; aggs COUNT(*)
//	        Filter: o.amount > 100
//	          Join INNER on u.id = o.user_id
//	            Scan: users AS u
//	            Scan: orders AS o
func Explain(p Plan) string {
	var b strings.Builder
	explainInto(&b, p, 0)
	return b.String()
}

func explainInto(b *strings.Builder, p Plan, depth int) {
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(p.describe())
	b.WriteByte('\n')
	for _, child := range p.Children() {
		explainInto(b, child, depth+1)
	}
}

// --- describe() per node ---

func (s *Scan) describe() string {
	if s.Alias != "" {
		return "Scan: " + s.Table + " AS " + s.Alias
	}
	return "Scan: " + s.Table
}

func (f *Filter) describe() string {
	return "Filter: " + f.Predicate.String()
}

func (p *Project) describe() string {
	parts := make([]string, len(p.Items))
	for i, it := range p.Items {
		if it.Alias != "" {
			parts[i] = it.Expr.String() + " AS " + it.Alias
		} else {
			parts[i] = it.Expr.String()
		}
	}
	prefix := "Project: "
	if p.Distinct {
		prefix = "Project (DISTINCT): "
	}
	return prefix + strings.Join(parts, ", ")
}

func (j *Join) describe() string {
	return "Join " + joinTypeWord(j.JoinType) + " on " + j.On.String()
}

func (a *Aggregate) describe() string {
	aggs := make([]string, len(a.Aggregates))
	for i, ag := range a.Aggregates {
		aggs[i] = ag.Call.String()
	}
	if len(a.GroupBy) == 0 {
		return "Aggregate: aggs " + strings.Join(aggs, ", ")
	}
	groups := make([]string, len(a.GroupBy))
	for i, g := range a.GroupBy {
		groups[i] = g.String()
	}
	return "Aggregate: group by " + strings.Join(groups, ", ") + "; aggs " + strings.Join(aggs, ", ")
}

func (s *Sort) describe() string {
	parts := make([]string, len(s.Keys))
	for i, k := range s.Keys {
		if k.Desc {
			parts[i] = k.Expr.String() + " DESC"
		} else {
			parts[i] = k.Expr.String()
		}
	}
	return "Sort: " + strings.Join(parts, ", ")
}

func (l *Limit) describe() string {
	return "Limit: " + strconv.FormatInt(l.Count, 10)
}

// joinTypeWord renders the join type as a bare keyword for EXPLAIN.
func joinTypeWord(t ast.JoinType) string {
	switch t {
	case ast.LeftJoin:
		return "LEFT"
	case ast.RightJoin:
		return "RIGHT"
	case ast.FullJoin:
		return "FULL"
	default:
		return "INNER"
	}
}
