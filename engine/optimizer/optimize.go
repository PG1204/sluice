package optimizer

import (
	"context"

	"github.com/PG1204/sluice/engine/logical"
	"github.com/PG1204/sluice/engine/storage"
)

// Rule is a cost-reducing plan rewrite. Each rule takes a plan and returns an
// equivalent one; the driver applies them in sequence.
type Rule interface {
	Name() string
	Apply(ctx context.Context, plan logical.Plan, p *Provider) (logical.Plan, error)
}

// DefaultRules is the optimization pipeline, in dependency order:
//   - predicate pushdown first, so filters shrink inputs before anything else;
//   - projection pushdown next, computing needed columns given the new shape;
//   - join reordering last, choosing build sides from the resulting estimates.
func DefaultRules() []Rule {
	return []Rule{PredicatePushdown{}, ProjectionPushdown{}, JoinReorder{}}
}

// Optimize applies the default rule pipeline to a logical plan.
func Optimize(ctx context.Context, plan logical.Plan, p *Provider) (logical.Plan, error) {
	var err error
	for _, rule := range DefaultRules() {
		if plan, err = rule.Apply(ctx, plan, p); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// mapBottomUp rebuilds a plan, applying fn to each node after its children have
// been rewritten. Rules that are pure structural transforms use it.
func mapBottomUp(plan logical.Plan, fn func(logical.Plan) logical.Plan) logical.Plan {
	children := plan.Children()
	if len(children) > 0 {
		rewritten := make([]logical.Plan, len(children))
		for i, c := range children {
			rewritten[i] = mapBottomUp(c, fn)
		}
		plan = replaceChildren(plan, rewritten)
	}
	return fn(plan)
}

// replaceChildren returns a shallow copy of plan with its child inputs swapped
// for the given ones. It is the structural backbone of plan rewriting.
func replaceChildren(plan logical.Plan, children []logical.Plan) logical.Plan {
	switch n := plan.(type) {
	case *logical.Filter:
		return &logical.Filter{Input: children[0], Predicate: n.Predicate}
	case *logical.Project:
		return &logical.Project{Input: children[0], Items: n.Items, Distinct: n.Distinct, OutSchema: n.OutSchema}
	case *logical.Aggregate:
		return &logical.Aggregate{Input: children[0], GroupBy: n.GroupBy, Aggregates: n.Aggregates, OutSchema: n.OutSchema}
	case *logical.Sort:
		return &logical.Sort{Input: children[0], Keys: n.Keys}
	case *logical.Limit:
		return &logical.Limit{Input: children[0], Count: n.Count}
	case *logical.Join:
		// Recompute the output schema from the (possibly rewritten) children:
		// projection pushdown can narrow a child, which must be reflected here
		// or the stored concat goes stale.
		return &logical.Join{
			Left:      children[0],
			Right:     children[1],
			JoinType:  n.JoinType,
			On:        n.On,
			OutSchema: concatSchema(children[0].Schema(), children[1].Schema()),
		}
	default: // Scan has no children
		return plan
	}
}

// concatSchema appends two schemas, the layout of a join's output.
func concatSchema(a, b storage.Schema) storage.Schema {
	fields := make([]storage.Field, 0, len(a.Fields)+len(b.Fields))
	fields = append(fields, a.Fields...)
	fields = append(fields, b.Fields...)
	return storage.Schema{Fields: fields}
}

// rowsOf estimates the output row count of a subtree (used by join reordering).
func rowsOf(ctx context.Context, plan logical.Plan, p *Provider) (int64, error) {
	a, err := Analyze(ctx, plan, p)
	if err != nil {
		return 0, err
	}
	return a.Of(plan).Rows, nil
}
