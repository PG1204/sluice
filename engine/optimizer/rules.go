package optimizer

import (
	"context"
	"strings"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/logical"
	"github.com/PG1204/sluice/engine/storage"
)

// PredicatePushdown moves WHERE conjuncts below an INNER join, onto whichever
// side supplies all of their columns. Filtering before the join shrinks the
// inputs the join must process — usually the single biggest win available.
// Only INNER joins are rewritten: pushing a predicate onto the null-supplying
// side of an outer join would change its result.
type PredicatePushdown struct{}

func (PredicatePushdown) Name() string { return "predicate-pushdown" }

func (PredicatePushdown) Apply(_ context.Context, plan logical.Plan, _ *Provider) (logical.Plan, error) {
	return mapBottomUp(plan, func(n logical.Plan) logical.Plan {
		filter, ok := n.(*logical.Filter)
		if !ok {
			return n
		}
		join, ok := filter.Input.(*logical.Join)
		if !ok || join.JoinType != ast.InnerJoin {
			return n
		}
		return splitFilterThroughJoin(filter.Predicate, join)
	}), nil
}

// splitFilterThroughJoin routes each conjunct of pred to the join side that owns
// all its columns, leaving the rest as a residual filter above the join.
func splitFilterThroughJoin(pred ast.Expression, join *logical.Join) logical.Plan {
	leftNames := nameSet(join.Left.Schema())
	rightNames := nameSet(join.Right.Schema())

	var leftPreds, rightPreds, residual []ast.Expression
	for _, c := range splitConjuncts(pred) {
		refs := columnRefs(c)
		switch {
		case len(refs) == 0:
			residual = append(residual, c) // constant predicate: nothing to gain
		case subset(refs, leftNames) && !subset(refs, rightNames):
			leftPreds = append(leftPreds, c)
		case subset(refs, rightNames) && !subset(refs, leftNames):
			rightPreds = append(rightPreds, c)
		default:
			residual = append(residual, c) // spans both sides (the join predicate itself, etc.)
		}
	}

	newJoin := &logical.Join{
		Left:      wrapFilter(join.Left, leftPreds),
		Right:     wrapFilter(join.Right, rightPreds),
		JoinType:  join.JoinType,
		On:        join.On,
		OutSchema: join.OutSchema,
	}
	return wrapFilter(newJoin, residual)
}

// wrapFilter wraps plan in a Filter of the ANDed predicates, or returns plan
// unchanged when there are none.
func wrapFilter(plan logical.Plan, preds []ast.Expression) logical.Plan {
	if len(preds) == 0 {
		return plan
	}
	return &logical.Filter{Input: plan, Predicate: andAll(preds)}
}

// ProjectionPushdown narrows each Scan to only the columns the query actually
// uses, so less data flows up the plan (and, with a column-aware cost model,
// scans get cheaper). With a Parquet source this would push down to read fewer
// columns from disk; here it narrows the scan's output.
type ProjectionPushdown struct{}

func (ProjectionPushdown) Name() string { return "projection-pushdown" }

func (ProjectionPushdown) Apply(_ context.Context, plan logical.Plan, _ *Provider) (logical.Plan, error) {
	refs := collectColumnUsage(plan)
	return mapBottomUp(plan, func(n logical.Plan) logical.Plan {
		scan, ok := n.(*logical.Scan)
		if !ok {
			return n
		}
		return projectScan(scan, refs)
	}), nil
}

// projectScan computes the columns a scan must output given how the query uses
// it, returning a copy with Projection set (or the original if it needs all).
func projectScan(scan *logical.Scan, refs *columnUsage) *logical.Scan {
	rel := scan.Table
	if scan.Alias != "" {
		rel = scan.Alias
	}
	if refs.starAll || refs.starQualified[strings.ToLower(rel)] {
		return scan // SELECT * over this table: needs everything
	}

	needed := make(map[string]bool)
	for name := range refs.qualified[strings.ToLower(rel)] {
		needed[name] = true
	}
	// Unqualified references could belong to this table if it has the column.
	for _, f := range scan.TableSchema.Fields {
		if refs.bare[strings.ToLower(f.Name)] {
			needed[strings.ToLower(f.Name)] = true
		}
	}

	var projection []string
	for _, f := range scan.TableSchema.Fields {
		if needed[strings.ToLower(f.Name)] {
			projection = append(projection, f.Name)
		}
	}
	// Keep at least one column so row count is preserved (e.g. COUNT(*) refers
	// to no column), and skip the rewrite when every column is needed anyway.
	if len(projection) == len(scan.TableSchema.Fields) {
		return scan
	}
	if len(projection) == 0 {
		projection = []string{scan.TableSchema.Fields[0].Name}
	}

	narrowed := *scan
	narrowed.Projection = projection
	return &narrowed
}

// JoinReorder puts the smaller estimated input on the build (right) side of
// each INNER hash join, which the cost model rewards because building a hash
// table costs more than probing it.
type JoinReorder struct{}

func (JoinReorder) Name() string { return "join-reorder" }

func (JoinReorder) Apply(ctx context.Context, plan logical.Plan, p *Provider) (logical.Plan, error) {
	return reorderJoins(ctx, plan, p)
}

func reorderJoins(ctx context.Context, plan logical.Plan, p *Provider) (logical.Plan, error) {
	children := plan.Children()
	rewritten := make([]logical.Plan, len(children))
	for i, c := range children {
		r, err := reorderJoins(ctx, c, p)
		if err != nil {
			return nil, err
		}
		rewritten[i] = r
	}
	plan = replaceChildren(plan, rewritten)

	join, ok := plan.(*logical.Join)
	if !ok || join.JoinType != ast.InnerJoin {
		return plan, nil
	}

	leftRows, err := rowsOf(ctx, join.Left, p)
	if err != nil {
		return nil, err
	}
	rightRows, err := rowsOf(ctx, join.Right, p)
	if err != nil {
		return nil, err
	}
	if leftRows >= rightRows {
		return join, nil // smaller side already on the right (build side)
	}

	// Swap so the smaller input builds. Output column order follows the new
	// left++right; downstream references resolve by name, so results are equal
	// (only SELECT * column order can differ).
	swapped := &logical.Join{
		Left:      join.Right,
		Right:     join.Left,
		JoinType:  join.JoinType,
		On:        join.On,
		OutSchema: concatSchema(join.Right.Schema(), join.Left.Schema()),
	}
	return swapped, nil
}

// --- expression / schema helpers ---

// columnUsage records how columns are referenced across a whole plan, for
// projection pushdown. Keys are lower-cased for case-insensitive matching.
type columnUsage struct {
	qualified     map[string]map[string]bool // relation -> set of column names
	bare          map[string]bool            // unqualified column names
	starAll       bool                       // an unqualified "*" appeared
	starQualified map[string]bool            // relations with a "t.*"
}

func newColumnUsage() *columnUsage {
	return &columnUsage{
		qualified:     make(map[string]map[string]bool),
		bare:          make(map[string]bool),
		starQualified: make(map[string]bool),
	}
}

// collectColumnUsage walks every expression in the plan and records column and
// wildcard references.
func collectColumnUsage(plan logical.Plan) *columnUsage {
	u := newColumnUsage()
	var visitPlan func(logical.Plan)
	visitPlan = func(p logical.Plan) {
		for _, e := range planExpressions(p) {
			recordRefs(e, u)
		}
		for _, c := range p.Children() {
			visitPlan(c)
		}
	}
	visitPlan(plan)
	return u
}

// planExpressions returns the expressions a node references directly.
func planExpressions(p logical.Plan) []ast.Expression {
	switch n := p.(type) {
	case *logical.Filter:
		return []ast.Expression{n.Predicate}
	case *logical.Join:
		return []ast.Expression{n.On}
	case *logical.Project:
		exprs := make([]ast.Expression, len(n.Items))
		for i, it := range n.Items {
			exprs[i] = it.Expr
		}
		return exprs
	case *logical.Aggregate:
		exprs := append([]ast.Expression{}, n.GroupBy...)
		for _, ag := range n.Aggregates {
			exprs = append(exprs, ag.Call)
		}
		return exprs
	case *logical.Sort:
		exprs := make([]ast.Expression, len(n.Keys))
		for i, k := range n.Keys {
			exprs[i] = k.Expr
		}
		return exprs
	default:
		return nil
	}
}

// recordRefs walks an expression, recording identifiers and wildcards.
func recordRefs(e ast.Expression, u *columnUsage) {
	switch n := e.(type) {
	case *ast.Identifier:
		if n.Table != "" {
			rel := strings.ToLower(n.Table)
			if u.qualified[rel] == nil {
				u.qualified[rel] = make(map[string]bool)
			}
			u.qualified[rel][strings.ToLower(n.Name)] = true
		} else {
			u.bare[strings.ToLower(n.Name)] = true
		}
	case *ast.Star:
		if n.Table == "" {
			u.starAll = true
		} else {
			u.starQualified[strings.ToLower(n.Table)] = true
		}
	case *ast.UnaryExpr:
		recordRefs(n.Operand, u)
	case *ast.BinaryExpr:
		recordRefs(n.Left, u)
		recordRefs(n.Right, u)
	case *ast.FunctionCall:
		for _, a := range n.Args {
			// The "*" in COUNT(*) counts rows; it is not a column wildcard and
			// must not force the scan to keep every column.
			if _, isStar := a.(*ast.Star); isStar {
				continue
			}
			recordRefs(a, u)
		}
	}
}

// columnRefs returns the set of (bare, lower-cased) column names in an
// expression — used to decide which join side a predicate belongs to.
func columnRefs(e ast.Expression) map[string]bool {
	out := make(map[string]bool)
	var walk func(ast.Expression)
	walk = func(e ast.Expression) {
		switch n := e.(type) {
		case *ast.Identifier:
			out[strings.ToLower(n.Name)] = true
		case *ast.UnaryExpr:
			walk(n.Operand)
		case *ast.BinaryExpr:
			walk(n.Left)
			walk(n.Right)
		case *ast.FunctionCall:
			for _, a := range n.Args {
				walk(a)
			}
		}
	}
	walk(e)
	return out
}

// nameSet returns the lower-cased set of a schema's column names.
func nameSet(s storage.Schema) map[string]bool {
	out := make(map[string]bool, len(s.Fields))
	for _, f := range s.Fields {
		out[strings.ToLower(f.Name)] = true
	}
	return out
}

// splitConjuncts flattens an AND-tree into its conjuncts.
func splitConjuncts(e ast.Expression) []ast.Expression {
	if be, ok := e.(*ast.BinaryExpr); ok && be.Op == ast.OpAnd {
		return append(splitConjuncts(be.Left), splitConjuncts(be.Right)...)
	}
	return []ast.Expression{e}
}

// andAll folds predicates into a left-deep AND chain.
func andAll(preds []ast.Expression) ast.Expression {
	result := preds[0]
	for _, p := range preds[1:] {
		result = &ast.BinaryExpr{Op: ast.OpAnd, Left: result, Right: p}
	}
	return result
}

// subset reports whether every name in refs is present in names.
func subset(refs, names map[string]bool) bool {
	for r := range refs {
		if !names[r] {
			return false
		}
	}
	return true
}
