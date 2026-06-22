package optimizer

import (
	"context"
	"math"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/logical"
)

// Tunable estimation constants. These are heuristics, not measurements — the
// point is relative ordering of plans, not absolute accuracy. Named so the
// model has no magic numbers and the assumptions are visible.
const (
	defaultEqSelectivity    = 0.10    // col = const, when distinct count unknown
	defaultRangeSelectivity = 1.0 / 3 // col < const, when range unknown
	defaultPredSelectivity  = 0.25    // any predicate shape we don't model
	scanCostPerCell         = 1.0     // scan cost per row * column read
	rowCostUnit             = 1.0     // per-input-row cost for filter/project/aggregate
	hashBuildCostWeight     = 2.0     // building a hash table costs more than probing...
	hashProbeCostWeight     = 1.0     // ...so building the smaller side is cheaper
	sortCostWeight          = 1.0     // multiplies n*log2(n)
	limitCostPerRow         = 1.0
)

// Estimate is the analyzer's verdict for one plan node.
type Estimate struct {
	Rows      int64   // estimated output rows
	Cost      float64 // cost of this operator alone
	TotalCost float64 // cost of this node plus its whole subtree

	cols map[string]ColumnStats // output column stats, for downstream selectivity
}

// Analysis holds per-node estimates for a plan.
type Analysis struct {
	est map[logical.Plan]*Estimate
}

// Of returns the estimate for a node (zero value if absent).
func (a *Analysis) Of(p logical.Plan) Estimate {
	if e, ok := a.est[p]; ok {
		return *e
	}
	return Estimate{}
}

// TotalCost is the whole query's estimated cost: the single number the rate
// limiter charges. It is the root node's subtree cost.
func (a *Analysis) TotalCost(root logical.Plan) float64 {
	return a.Of(root).TotalCost
}

// Analyze walks a plan and estimates the cardinality and cost of every node.
func Analyze(ctx context.Context, plan logical.Plan, p *Provider) (*Analysis, error) {
	a := &Analysis{est: make(map[logical.Plan]*Estimate)}
	if _, err := a.analyze(ctx, plan, p); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Analysis) analyze(ctx context.Context, plan logical.Plan, p *Provider) (*Estimate, error) {
	var (
		est *Estimate
		err error
	)
	switch n := plan.(type) {
	case *logical.Scan:
		est, err = a.analyzeScan(ctx, n, p)
	case *logical.Filter:
		est, err = a.analyzeFilter(ctx, n, p)
	case *logical.Project:
		est, err = a.analyzeProject(ctx, n, p)
	case *logical.Join:
		est, err = a.analyzeJoin(ctx, n, p)
	case *logical.Aggregate:
		est, err = a.analyzeAggregate(ctx, n, p)
	case *logical.Sort:
		est, err = a.analyzeChild1(ctx, n, n.Input, p, a.sortEstimate)
	case *logical.Limit:
		est, err = a.analyzeChild1(ctx, n, n.Input, p, func(child *Estimate) *Estimate {
			rows := min64(child.Rows, n.Count)
			cost := float64(rows) * limitCostPerRow
			return &Estimate{Rows: rows, Cost: cost, TotalCost: cost + child.TotalCost, cols: child.cols}
		})
	default:
		est, err = &Estimate{Rows: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	a.est[plan] = est
	return est, nil
}

func (a *Analysis) analyzeScan(ctx context.Context, s *logical.Scan, p *Provider) (*Estimate, error) {
	stats, err := p.TableStats(ctx, s.Table)
	if err != nil {
		return nil, err
	}
	// Output stats restricted to the (possibly projected) output columns.
	cols := make(map[string]ColumnStats)
	for _, f := range s.Schema().Fields {
		if cs, ok := stats.Columns[f.Name]; ok {
			cols[f.Name] = cs
		}
	}
	numCols := len(s.Schema().Fields)
	cost := float64(stats.RowCount) * float64(numCols) * scanCostPerCell
	return &Estimate{Rows: stats.RowCount, Cost: cost, TotalCost: cost, cols: cols}, nil
}

func (a *Analysis) analyzeFilter(ctx context.Context, f *logical.Filter, p *Provider) (*Estimate, error) {
	child, err := a.analyze(ctx, f.Input, p)
	if err != nil {
		return nil, err
	}
	sel := selectivity(f.Predicate, child.cols)
	rows := floorRows(float64(child.Rows) * sel)
	cost := float64(child.Rows) * rowCostUnit
	return &Estimate{Rows: rows, Cost: cost, TotalCost: cost + child.TotalCost, cols: child.cols}, nil
}

func (a *Analysis) analyzeProject(ctx context.Context, pr *logical.Project, p *Provider) (*Estimate, error) {
	child, err := a.analyze(ctx, pr.Input, p)
	if err != nil {
		return nil, err
	}
	// Output column stats: carry through stats for plain column references.
	cols := make(map[string]ColumnStats)
	for _, it := range pr.Items {
		if id, ok := it.Expr.(*ast.Identifier); ok {
			if cs, ok := child.cols[id.Name]; ok {
				cols[it.Name] = cs
				continue
			}
		}
		cols[it.Name] = ColumnStats{DistinctCount: child.Rows} // computed column: unknown
	}

	rows := child.Rows
	if pr.Distinct {
		rows = min64(rows, distinctProduct(cols, child.Rows))
	}
	cost := float64(child.Rows) * rowCostUnit
	return &Estimate{Rows: rows, Cost: cost, TotalCost: cost + child.TotalCost, cols: cols}, nil
}

func (a *Analysis) analyzeJoin(ctx context.Context, j *logical.Join, p *Provider) (*Estimate, error) {
	left, err := a.analyze(ctx, j.Left, p)
	if err != nil {
		return nil, err
	}
	right, err := a.analyze(ctx, j.Right, p)
	if err != nil {
		return nil, err
	}

	// Equi-join cardinality: |L|*|R| / max(distinct join keys). Falls back to a
	// foreign-key-like estimate (max of the two sides) when keys are unknown.
	denom := joinKeyNDV(j.On, left.cols, right.cols)
	if denom <= 0 {
		denom = float64(max64(left.Rows, right.Rows))
	}
	if denom < 1 {
		denom = 1
	}
	rows := floorRows(float64(left.Rows) * float64(right.Rows) / denom)

	// The physical operator builds the RIGHT input and probes with the LEFT, so
	// the right side's size dominates cost — building the smaller side wins.
	cost := float64(right.Rows)*hashBuildCostWeight + float64(left.Rows)*hashProbeCostWeight

	cols := make(map[string]ColumnStats, len(left.cols)+len(right.cols))
	for k, v := range left.cols {
		cols[k] = v
	}
	for k, v := range right.cols {
		cols[k] = v
	}
	return &Estimate{Rows: rows, Cost: cost, TotalCost: cost + left.TotalCost + right.TotalCost, cols: cols}, nil
}

func (a *Analysis) analyzeAggregate(ctx context.Context, agg *logical.Aggregate, p *Provider) (*Estimate, error) {
	child, err := a.analyze(ctx, agg.Input, p)
	if err != nil {
		return nil, err
	}

	groups := int64(1) // global aggregate => one row
	if len(agg.GroupBy) > 0 {
		groups = distinctProductOfExprs(agg.GroupBy, child.cols, child.Rows)
	}

	cols := make(map[string]ColumnStats)
	for _, g := range agg.GroupBy {
		if id, ok := g.(*ast.Identifier); ok {
			if cs, ok := child.cols[id.Name]; ok {
				cols[id.Name] = cs
			}
		}
	}
	for _, ag := range agg.Aggregates {
		cols[ag.Name] = ColumnStats{DistinctCount: groups}
	}

	cost := float64(child.Rows) * rowCostUnit
	return &Estimate{Rows: groups, Cost: cost, TotalCost: cost + child.TotalCost, cols: cols}, nil
}

// analyzeChild1 is a helper for single-child passthrough operators.
func (a *Analysis) analyzeChild1(ctx context.Context, node, child logical.Plan, p *Provider, combine func(*Estimate) *Estimate) (*Estimate, error) {
	c, err := a.analyze(ctx, child, p)
	if err != nil {
		return nil, err
	}
	return combine(c), nil
}

func (a *Analysis) sortEstimate(child *Estimate) *Estimate {
	n := float64(child.Rows)
	cost := 0.0
	if n > 1 {
		cost = n * math.Log2(n) * sortCostWeight
	}
	return &Estimate{Rows: child.Rows, Cost: cost, TotalCost: cost + child.TotalCost, cols: child.cols}
}

// --- selectivity ---

// selectivity estimates the fraction of rows a predicate keeps, in [0, 1].
func selectivity(pred ast.Expression, cols map[string]ColumnStats) float64 {
	switch e := pred.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case ast.OpAnd: // independence assumption
			return selectivity(e.Left, cols) * selectivity(e.Right, cols)
		case ast.OpOr:
			l, r := selectivity(e.Left, cols), selectivity(e.Right, cols)
			return l + r - l*r
		case ast.OpEq:
			return clamp01(eqSelectivity(e.Left, e.Right, cols))
		case ast.OpNeq:
			return clamp01(1 - eqSelectivity(e.Left, e.Right, cols))
		case ast.OpLt, ast.OpLte, ast.OpGt, ast.OpGte:
			return clamp01(rangeSelectivity(e.Op, e.Left, e.Right, cols))
		}
	case *ast.UnaryExpr:
		if e.Op == ast.OpNot {
			return clamp01(1 - selectivity(e.Operand, cols))
		}
	}
	return defaultPredSelectivity
}

// eqSelectivity estimates "col = const" as 1/distinct(col) when known. The
// literal may be of any type (a string equality is just as selective as a
// numeric one), so this uses the broader column-vs-literal match.
func eqSelectivity(left, right ast.Expression, cols map[string]ColumnStats) float64 {
	if col, ok := columnVsLiteral(left, right); ok {
		if cs, ok := cols[col.Name]; ok && cs.DistinctCount > 0 {
			return 1 / float64(cs.DistinctCount)
		}
	}
	return defaultEqSelectivity
}

// columnVsLiteral matches "column <op> literal" in either order, for any
// literal type.
func columnVsLiteral(left, right ast.Expression) (*ast.Identifier, bool) {
	if id, ok := left.(*ast.Identifier); ok && isLiteral(right) {
		return id, true
	}
	if id, ok := right.(*ast.Identifier); ok && isLiteral(left) {
		return id, true
	}
	return nil, false
}

func isLiteral(e ast.Expression) bool {
	switch e.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.BooleanLiteral, *ast.NullLiteral:
		return true
	default:
		return false
	}
}

// rangeSelectivity estimates an inequality using the column's [min, max] range.
func rangeSelectivity(op ast.Operator, left, right ast.Expression, cols map[string]ColumnStats) float64 {
	col, lit, ok := colAndLiteral(left, right)
	if !ok {
		return defaultRangeSelectivity
	}
	cs, ok := cols[col.Name]
	if !ok || !cs.Numeric || cs.Max <= cs.Min {
		return defaultRangeSelectivity
	}
	// Normalize so the column is on the left of the operator.
	effOp := op
	if _, isLit := numericLiteral(left); isLit {
		effOp = flipOp(op)
	}
	span := cs.Max - cs.Min
	switch effOp {
	case ast.OpLt, ast.OpLte:
		return (lit - cs.Min) / span
	default: // OpGt, OpGte
		return (cs.Max - lit) / span
	}
}

// colAndLiteral matches a "column <op> numeric-literal" pair in either order.
func colAndLiteral(left, right ast.Expression) (*ast.Identifier, float64, bool) {
	if id, ok := left.(*ast.Identifier); ok {
		if v, ok := numericLiteral(right); ok {
			return id, v, true
		}
	}
	if id, ok := right.(*ast.Identifier); ok {
		if v, ok := numericLiteral(left); ok {
			return id, v, true
		}
	}
	return nil, 0, false
}

func numericLiteral(e ast.Expression) (float64, bool) {
	switch n := e.(type) {
	case *ast.IntegerLiteral:
		return float64(n.Value), true
	case *ast.FloatLiteral:
		return n.Value, true
	default:
		return 0, false
	}
}

func flipOp(op ast.Operator) ast.Operator {
	switch op {
	case ast.OpLt:
		return ast.OpGt
	case ast.OpLte:
		return ast.OpGte
	case ast.OpGt:
		return ast.OpLt
	case ast.OpGte:
		return ast.OpLte
	default:
		return op
	}
}

// joinKeyNDV returns the largest distinct count among the equi-join key columns
// (the denominator in the join cardinality formula), or 0 if none are known.
func joinKeyNDV(on ast.Expression, leftCols, rightCols map[string]ColumnStats) float64 {
	best := 0.0
	for _, eq := range conjunctEqualities(on) {
		li, lok := eq.Left.(*ast.Identifier)
		ri, rok := eq.Right.(*ast.Identifier)
		if !lok || !rok {
			continue
		}
		for _, cand := range []struct {
			name string
			cols map[string]ColumnStats
		}{{li.Name, leftCols}, {li.Name, rightCols}, {ri.Name, leftCols}, {ri.Name, rightCols}} {
			if cs, ok := cand.cols[cand.name]; ok && float64(cs.DistinctCount) > best {
				best = float64(cs.DistinctCount)
			}
		}
	}
	return best
}

// conjunctEqualities flattens an AND-tree into its equality leaves.
func conjunctEqualities(e ast.Expression) []*ast.BinaryExpr {
	be, ok := e.(*ast.BinaryExpr)
	if !ok {
		return nil
	}
	switch be.Op {
	case ast.OpAnd:
		return append(conjunctEqualities(be.Left), conjunctEqualities(be.Right)...)
	case ast.OpEq:
		return []*ast.BinaryExpr{be}
	default:
		return nil
	}
}

// --- small numeric helpers ---

func distinctProductOfExprs(exprs []ast.Expression, cols map[string]ColumnStats, cap int64) int64 {
	product := int64(1)
	for _, e := range exprs {
		d := int64(1)
		if id, ok := e.(*ast.Identifier); ok {
			if cs, ok := cols[id.Name]; ok && cs.DistinctCount > 0 {
				d = cs.DistinctCount
			}
		}
		product *= d
		if product >= cap {
			return cap
		}
	}
	return min64(product, cap)
}

func distinctProduct(cols map[string]ColumnStats, cap int64) int64 {
	product := int64(1)
	for _, cs := range cols {
		d := cs.DistinctCount
		if d < 1 {
			d = 1
		}
		product *= d
		if product >= cap {
			return cap
		}
	}
	return min64(product, cap)
}

func floorRows(f float64) int64 {
	r := int64(f)
	if r < 1 {
		return 1
	}
	return r
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
