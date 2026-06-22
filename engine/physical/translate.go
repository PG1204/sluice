package physical

import (
	"fmt"
	"strings"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/logical"
	"github.com/PG1204/sluice/engine/storage"
)

// Build translates a validated logical plan into an executable physical plan.
//
// The translation is naive (one physical operator per logical operator), as the
// build plan specifies — the optimizer in Phase 5 is what makes plans good.
// While walking the tree it threads a layout (columns with their source
// qualifiers) so each expression can be bound to concrete column positions.
func Build(plan logical.Plan, opener TableOpener) (Operator, error) {
	b := &builder{opener: opener}
	op, _, err := b.build(plan)
	return op, err
}

type builder struct {
	opener TableOpener
}

func (b *builder) build(p logical.Plan) (Operator, layout, error) {
	switch n := p.(type) {
	case *logical.Scan:
		return b.buildScan(n)
	case *logical.Filter:
		return b.buildFilter(n)
	case *logical.Project:
		return b.buildProject(n)
	case *logical.Join:
		return b.buildJoin(n)
	case *logical.Aggregate:
		return b.buildAggregate(n)
	case *logical.Sort:
		return b.buildSort(n)
	case *logical.Limit:
		return b.buildLimit(n)
	default:
		return nil, layout{}, fmt.Errorf("cannot execute logical node %T", p)
	}
}

func (b *builder) buildScan(n *logical.Scan) (Operator, layout, error) {
	qualifier := n.Alias
	if qualifier == "" {
		qualifier = n.Table
	}

	// Scan.Schema() is the projected subset (or the full table). Build the
	// layout from it and, when narrowed, the indices that select those columns
	// out of each full source batch.
	out := n.Schema()
	cols := make([]colInfo, len(out.Fields))
	for i, f := range out.Fields {
		cols[i] = colInfo{qualifier: qualifier, name: f.Name, typ: f.Type}
	}

	var indices []int
	if n.Projection != nil {
		indices = make([]int, len(out.Fields))
		for i, of := range out.Fields {
			for ti, tf := range n.TableSchema.Fields {
				if tf.Name == of.Name {
					indices[i] = ti
					break
				}
			}
		}
	}

	return NewSeqScan(b.opener, n.Table, out, indices), layout{cols: cols}, nil
}

func (b *builder) buildFilter(n *logical.Filter) (Operator, layout, error) {
	child, childLayout, err := b.build(n.Input)
	if err != nil {
		return nil, layout{}, err
	}
	predicate, err := compileExpr(n.Predicate, childLayout)
	if err != nil {
		return nil, layout{}, err
	}
	return NewFilter(child, predicate), childLayout, nil
}

func (b *builder) buildProject(n *logical.Project) (Operator, layout, error) {
	child, childLayout, err := b.build(n.Input)
	if err != nil {
		return nil, layout{}, err
	}

	exprs := make([]evalExpr, len(n.Items))
	cols := make([]colInfo, len(n.Items))
	for i, it := range n.Items {
		e, err := compileExpr(it.Expr, childLayout)
		if err != nil {
			return nil, layout{}, err
		}
		exprs[i] = e
		// Projection output drops source qualifiers; columns are referred to by
		// their output name downstream.
		cols[i] = colInfo{name: it.Name, typ: it.Type}
	}

	op := NewProjection(child, exprs, n.OutSchema, n.Distinct)
	return op, layout{cols: cols}, nil
}

func (b *builder) buildJoin(n *logical.Join) (Operator, layout, error) {
	if n.JoinType == ast.RightJoin || n.JoinType == ast.FullJoin {
		return nil, layout{}, fmt.Errorf("%s joins are not supported yet", joinWord(n.JoinType))
	}

	left, leftLayout, err := b.build(n.Left)
	if err != nil {
		return nil, layout{}, err
	}
	right, rightLayout, err := b.build(n.Right)
	if err != nil {
		return nil, layout{}, err
	}

	leftKeys, rightKeys, err := extractJoinKeys(n.On, leftLayout, rightLayout)
	if err != nil {
		return nil, layout{}, err
	}

	combined := layout{cols: append(append([]colInfo{}, leftLayout.cols...), rightLayout.cols...)}
	op := NewHashJoin(left, right, leftKeys, rightKeys, n.JoinType, n.OutSchema, len(leftLayout.cols))
	return op, combined, nil
}

func (b *builder) buildAggregate(n *logical.Aggregate) (Operator, layout, error) {
	child, childLayout, err := b.build(n.Input)
	if err != nil {
		return nil, layout{}, err
	}

	groupBy := make([]evalExpr, len(n.GroupBy))
	cols := make([]colInfo, 0, len(n.GroupBy)+len(n.Aggregates))
	for i, g := range n.GroupBy {
		e, err := compileExpr(g, childLayout)
		if err != nil {
			return nil, layout{}, err
		}
		groupBy[i] = e
		cols = append(cols, groupKeyColInfo(g, n.OutSchema.Fields[i].Type))
	}

	specs := make([]aggSpec, len(n.Aggregates))
	for i, agg := range n.Aggregates {
		spec, err := compileAggregate(agg, childLayout)
		if err != nil {
			return nil, layout{}, err
		}
		specs[i] = spec
		cols = append(cols, colInfo{name: agg.Name, typ: agg.Type})
	}

	op := NewHashAggregate(child, groupBy, specs, n.OutSchema)
	return op, layout{cols: cols}, nil
}

func (b *builder) buildSort(n *logical.Sort) (Operator, layout, error) {
	child, childLayout, err := b.build(n.Input)
	if err != nil {
		return nil, layout{}, err
	}
	keys := make([]sortKey, len(n.Keys))
	for i, k := range n.Keys {
		e, err := compileExpr(k.Expr, childLayout)
		if err != nil {
			return nil, layout{}, fmt.Errorf("ORDER BY %s: %w (only selected columns and aliases can be ordered by)", k.Expr.String(), err)
		}
		keys[i] = sortKey{expr: e, desc: k.Desc}
	}
	return NewSort(child, keys), childLayout, nil
}

func (b *builder) buildLimit(n *logical.Limit) (Operator, layout, error) {
	child, childLayout, err := b.build(n.Input)
	if err != nil {
		return nil, layout{}, err
	}
	return NewLimit(child, n.Count), childLayout, nil
}

// groupKeyColInfo derives the layout column for a GROUP BY key so downstream
// projections can still reference it (qualified, if it was an identifier).
func groupKeyColInfo(g ast.Expression, t storage.Type) colInfo {
	if id, ok := g.(*ast.Identifier); ok {
		return colInfo{qualifier: id.Table, name: id.Name, typ: t}
	}
	return colInfo{name: g.String(), typ: t}
}

// compileAggregate turns a logical aggregate into an executable spec, compiling
// its argument (if any) against the input layout.
func compileAggregate(agg logical.AggregateExpr, in layout) (aggSpec, error) {
	name := strings.ToUpper(agg.Call.Name)

	if name == "COUNT" {
		if _, isStar := agg.Call.Args[0].(*ast.Star); isStar {
			return aggSpec{kind: aggCountStar, resultType: agg.Type}, nil
		}
	}

	arg, err := compileExpr(agg.Call.Args[0], in)
	if err != nil {
		return aggSpec{}, err
	}

	kind, ok := map[string]aggKind{
		"COUNT": aggCount,
		"SUM":   aggSum,
		"AVG":   aggAvg,
		"MIN":   aggMin,
		"MAX":   aggMax,
	}[name]
	if !ok {
		return aggSpec{}, fmt.Errorf("unknown aggregate %q", agg.Call.Name)
	}
	return aggSpec{kind: kind, arg: arg, resultType: agg.Type}, nil
}

// extractJoinKeys decomposes an equi-join ON condition into aligned left/right
// key expressions. The condition must be one or more equalities (ANDed), each
// comparing a left-input expression with a right-input expression.
func extractJoinKeys(on ast.Expression, leftL, rightL layout) (leftKeys, rightKeys []evalExpr, err error) {
	eqs, err := splitEqualities(on)
	if err != nil {
		return nil, nil, err
	}
	for _, eq := range eqs {
		lk, rk, err := bindEquality(eq, leftL, rightL)
		if err != nil {
			return nil, nil, err
		}
		leftKeys = append(leftKeys, lk)
		rightKeys = append(rightKeys, rk)
	}
	return leftKeys, rightKeys, nil
}

// splitEqualities flattens an AND-tree of equality predicates into a list,
// erroring on any non-equality (hash join handles only equi-joins).
func splitEqualities(e ast.Expression) ([]*ast.BinaryExpr, error) {
	be, ok := e.(*ast.BinaryExpr)
	if !ok {
		return nil, fmt.Errorf("hash join requires an equality condition, got %s", e.String())
	}
	switch be.Op {
	case ast.OpAnd:
		left, err := splitEqualities(be.Left)
		if err != nil {
			return nil, err
		}
		right, err := splitEqualities(be.Right)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
	case ast.OpEq:
		return []*ast.BinaryExpr{be}, nil
	default:
		return nil, fmt.Errorf("hash join supports only '=' conditions (optionally ANDed), got %s", be.Op)
	}
}

// bindEquality binds one equality's sides to the left and right inputs,
// returning (leftKey, rightKey) regardless of which way it was written.
func bindEquality(eq *ast.BinaryExpr, leftL, rightL layout) (evalExpr, evalExpr, error) {
	if lk, e1 := compileExpr(eq.Left, leftL); e1 == nil {
		if rk, e2 := compileExpr(eq.Right, rightL); e2 == nil {
			return lk, rk, nil
		}
	}
	if rk, e1 := compileExpr(eq.Left, rightL); e1 == nil {
		if lk, e2 := compileExpr(eq.Right, leftL); e2 == nil {
			return lk, rk, nil
		}
	}
	return nil, nil, fmt.Errorf("join condition %s must compare a left-side column with a right-side column", eq.String())
}

func joinWord(t ast.JoinType) string {
	switch t {
	case ast.RightJoin:
		return "RIGHT"
	case ast.FullJoin:
		return "FULL"
	case ast.LeftJoin:
		return "LEFT"
	default:
		return "INNER"
	}
}
