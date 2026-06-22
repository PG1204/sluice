package optimizer

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/logical"
	"github.com/PG1204/sluice/engine/parser"
	"github.com/PG1204/sluice/engine/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- in-memory test database (Catalog + TableOpener over fixed batches) ---

type table struct {
	schema storage.Schema
	batch  *storage.Batch
}

type memDB map[string]table

func (m memDB) TableSchema(name string) (storage.Schema, error) {
	t, ok := m[name]
	if !ok {
		return storage.Schema{}, fmt.Errorf("no table %q", name)
	}
	return t.schema, nil
}

func (m memDB) Open(_ context.Context, name string) (storage.DataSource, error) {
	t, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("no table %q", name)
	}
	return &oneBatchSource{schema: t.schema, batch: t.batch}, nil
}

type oneBatchSource struct {
	schema storage.Schema
	batch  *storage.Batch
	done   bool
}

func (s *oneBatchSource) Schema() storage.Schema { return s.schema }
func (s *oneBatchSource) Close() error           { return nil }
func (s *oneBatchSource) Next(context.Context) (*storage.Batch, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return s.batch, nil
}

func intCol(vs ...int64) storage.Column {
	b := storage.NewColumnBuilder[int64](storage.TypeInt64)
	for _, v := range vs {
		b.Append(v)
	}
	return b.Build()
}

func floatCol(vs ...float64) storage.Column {
	b := storage.NewColumnBuilder[float64](storage.TypeFloat64)
	for _, v := range vs {
		b.Append(v)
	}
	return b.Build()
}

func strCol(vs ...string) storage.Column {
	b := storage.NewColumnBuilder[string](storage.TypeString)
	for _, v := range vs {
		b.Append(v)
	}
	return b.Build()
}

// testDB has orders (8 rows, name has 3 distinct) and customers (3 rows).
func testDB() memDB {
	ordersSchema := storage.Schema{Fields: []storage.Field{
		{Name: "id", Type: storage.TypeInt64},
		{Name: "name", Type: storage.TypeString},
		{Name: "amount", Type: storage.TypeFloat64},
		{Name: "status", Type: storage.TypeString},
	}}
	custSchema := storage.Schema{Fields: []storage.Field{
		{Name: "name", Type: storage.TypeString},
		{Name: "city", Type: storage.TypeString},
		{Name: "tier", Type: storage.TypeString},
	}}
	return memDB{
		"orders": {schema: ordersSchema, batch: &storage.Batch{Schema: ordersSchema, Columns: []storage.Column{
			intCol(1, 2, 3, 4, 5, 6, 7, 8),
			strCol("Alice", "Bob", "Alice", "Carol", "Bob", "Alice", "Carol", "Bob"),
			floatCol(150, 75, 220, 40, 310, 95, 500, 120),
			strCol("paid", "paid", "paid", "pending", "paid", "pending", "paid", "paid"),
		}}},
		"customers": {schema: custSchema, batch: &storage.Batch{Schema: custSchema, Columns: []storage.Column{
			strCol("Alice", "Bob", "Carol"),
			strCol("Seattle", "Portland", "Denver"),
			strCol("gold", "silver", "gold"),
		}}},
	}
}

func buildPlan(t *testing.T, db memDB, sql string) logical.Plan {
	t.Helper()
	stmt, err := parser.Parse(sql)
	require.NoError(t, err)
	p, err := logical.Build(stmt.(*ast.SelectStatement), db)
	require.NoError(t, err)
	return p
}

// findJoin returns the first Join in a plan (depth-first), or nil.
func findJoin(p logical.Plan) *logical.Join {
	if j, ok := p.(*logical.Join); ok {
		return j
	}
	for _, c := range p.Children() {
		if j := findJoin(c); j != nil {
			return j
		}
	}
	return nil
}

// findScan returns the first Scan of the named table.
func findScan(p logical.Plan, table string) *logical.Scan {
	if s, ok := p.(*logical.Scan); ok && s.Table == table {
		return s
	}
	for _, c := range p.Children() {
		if s := findScan(c, table); s != nil {
			return s
		}
	}
	return nil
}

// --- tests ---

func TestStats_ComputesAndCaches(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	ctx := context.Background()

	s, err := p.TableStats(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, int64(8), s.RowCount)
	assert.Equal(t, int64(3), s.Columns["name"].DistinctCount, "Alice/Bob/Carol")
	assert.Equal(t, int64(8), s.Columns["id"].DistinctCount)
	amount := s.Columns["amount"]
	assert.True(t, amount.Numeric)
	assert.Equal(t, 40.0, amount.Min)
	assert.Equal(t, 500.0, amount.Max)

	// Second call returns the cached pointer (no recompute).
	s2, err := p.TableStats(ctx, "orders")
	require.NoError(t, err)
	assert.Same(t, s, s2)
}

func TestCardinality_FilterSelectivity(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	plan := buildPlan(t, db, "SELECT name FROM orders WHERE name = 'Alice'")
	a, err := Analyze(context.Background(), plan, p)
	require.NoError(t, err)

	// Equality on a 3-distinct column: 8 * 1/3 ≈ 2 rows after the filter.
	filter := findFilter(plan)
	require.NotNil(t, filter)
	assert.Equal(t, int64(2), a.Of(filter).Rows)
}

func TestCardinality_GroupByGroups(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	plan := buildPlan(t, db, "SELECT name, COUNT(*) FROM orders GROUP BY name")
	a, err := Analyze(context.Background(), plan, p)
	require.NoError(t, err)

	agg := findAggregate(plan)
	require.NotNil(t, agg)
	assert.Equal(t, int64(3), a.Of(agg).Rows, "3 distinct names => 3 groups")
}

func TestCost_IsPositiveAndTotalsSubtree(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	plan := buildPlan(t, db, "SELECT name FROM orders WHERE amount > 100")
	a, err := Analyze(context.Background(), plan, p)
	require.NoError(t, err)
	assert.Greater(t, a.TotalCost(plan), 0.0)
}

func TestPredicatePushdown_MovesFilterBelowJoin(t *testing.T) {
	db := testDB()
	plan := buildPlan(t, db, "SELECT o.name FROM orders o JOIN customers c ON o.name = c.name WHERE o.amount > 100")

	optimized, err := PredicatePushdown{}.Apply(context.Background(), plan, nil)
	require.NoError(t, err)

	join := findJoin(optimized)
	require.NotNil(t, join)
	_, ok := join.Left.(*logical.Filter)
	assert.True(t, ok, "the orders-side predicate should sit below the join, on the left input")
}

func TestProjectionPushdown_NarrowsScan(t *testing.T) {
	db := testDB()
	plan := buildPlan(t, db, "SELECT name FROM orders WHERE amount > 100")

	optimized, err := ProjectionPushdown{}.Apply(context.Background(), plan, nil)
	require.NoError(t, err)

	scan := findScan(optimized, "orders")
	require.NotNil(t, scan)
	// Needs the selected column (name) and the filter column (amount); drops id, status.
	assert.ElementsMatch(t, []string{"name", "amount"}, scan.Projection)
}

func TestProjectionPushdown_StarKeepsAllColumns(t *testing.T) {
	db := testDB()
	plan := buildPlan(t, db, "SELECT * FROM orders")
	optimized, err := ProjectionPushdown{}.Apply(context.Background(), plan, nil)
	require.NoError(t, err)
	assert.Nil(t, findScan(optimized, "orders").Projection, "SELECT * needs every column")
}

func TestJoinReorder_BuildsSmallerSide(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	ctx := context.Background()
	// customers (3) is written on the left, orders (8) on the right.
	plan := buildPlan(t, db, "SELECT c.name FROM customers c JOIN orders o ON c.name = o.name")

	optimized, err := JoinReorder{}.Apply(ctx, plan, p)
	require.NoError(t, err)

	join := findJoin(optimized)
	require.NotNil(t, join)
	leftRows, _ := rowsOf(ctx, join.Left, p)
	rightRows, _ := rowsOf(ctx, join.Right, p)
	assert.LessOrEqual(t, rightRows, leftRows, "smaller input must be on the right (build) side")
}

func TestOptimize_BeatsNaive(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	ctx := context.Background()
	sql := "SELECT o.name, c.city FROM orders o JOIN customers c ON o.name = c.name WHERE o.amount > 100"

	naive := buildPlan(t, db, sql)
	naiveA, err := Analyze(ctx, naive, p)
	require.NoError(t, err)

	optimized, err := Optimize(ctx, naive, p)
	require.NoError(t, err)
	optA, err := Analyze(ctx, optimized, p)
	require.NoError(t, err)

	assert.Less(t, optA.TotalCost(optimized), naiveA.TotalCost(naive),
		"optimized plan should cost less than the naive plan")
}

func TestCardinality_DistinctSortLimit(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	ctx := context.Background()

	distinct := buildPlan(t, db, "SELECT DISTINCT name FROM orders")
	da, err := Analyze(ctx, distinct, p)
	require.NoError(t, err)
	assert.Equal(t, int64(3), da.Of(distinct).Rows, "3 distinct names")

	limited := buildPlan(t, db, "SELECT amount FROM orders ORDER BY amount LIMIT 3")
	la, err := Analyze(ctx, limited, p)
	require.NoError(t, err)
	assert.Equal(t, int64(3), la.Of(limited).Rows, "LIMIT caps rows")
	assert.Greater(t, la.TotalCost(limited), 0.0)
}

func TestSelectivity_OrNotAndFlippedRange(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	ctx := context.Background()

	// Each of these exercises a different selectivity branch; they must analyze
	// without error and keep at least one estimated row.
	for _, sql := range []string{
		"SELECT id FROM orders WHERE amount > 100 OR amount < 50",
		"SELECT id FROM orders WHERE NOT (status = 'paid')",
		"SELECT id FROM orders WHERE 100 < amount", // literal on the left -> flipOp
		"SELECT id FROM orders WHERE status != 'paid'",
	} {
		t.Run(sql, func(t *testing.T) {
			plan := buildPlan(t, db, sql)
			a, err := Analyze(ctx, plan, p)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, a.Of(plan).Rows, int64(1))
		})
	}
}

func TestRuleNames(t *testing.T) {
	assert.Equal(t, "predicate-pushdown", PredicatePushdown{}.Name())
	assert.Equal(t, "projection-pushdown", ProjectionPushdown{}.Name())
	assert.Equal(t, "join-reorder", JoinReorder{}.Name())
}

func TestExplainCost_Output(t *testing.T) {
	db := testDB()
	p := NewProvider(db)
	out, err := ExplainCost(context.Background(), buildPlan(t, db, "SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name"), p)
	require.NoError(t, err)
	assert.Contains(t, out, "rows=")
	assert.Contains(t, out, "cost=")
	assert.Contains(t, out, "Total cost:")
}

// findFilter / findAggregate locate nodes for assertions.
func findFilter(p logical.Plan) *logical.Filter {
	if f, ok := p.(*logical.Filter); ok {
		return f
	}
	for _, c := range p.Children() {
		if f := findFilter(c); f != nil {
			return f
		}
	}
	return nil
}

func findAggregate(p logical.Plan) *logical.Aggregate {
	if a, ok := p.(*logical.Aggregate); ok {
		return a
	}
	for _, c := range p.Children() {
		if a := findAggregate(c); a != nil {
			return a
		}
	}
	return nil
}
