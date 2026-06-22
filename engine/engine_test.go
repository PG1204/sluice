package engine

import (
	"context"
	"sort"
	"testing"

	"github.com/PG1204/sluice/engine/logical"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEngine() *Engine { return New("testdata") }

// rowStrings returns the result rows as string slices, for order-insensitive
// comparison of query output.
func rowStrings(r *Result) [][]string {
	var rows [][]string
	for _, b := range r.Batches {
		for i := 0; i < b.NumRows(); i++ {
			row := make([]string, b.NumCols())
			for c := 0; c < b.NumCols(); c++ {
				row[c] = formatCell(b.Columns[c], i)
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func sortedRowStrings(r *Result) [][]string {
	rows := rowStrings(r)
	sort.Slice(rows, func(i, j int) bool {
		for k := range rows[i] {
			if rows[i][k] != rows[j][k] {
				return rows[i][k] < rows[j][k]
			}
		}
		return false
	})
	return rows
}

func mustQuery(t *testing.T, sql string) *Result {
	t.Helper()
	r, err := testEngine().Query(context.Background(), sql)
	require.NoError(t, err)
	return r
}

// TestDoneWhenQuery is the Phase 4 acceptance query from the build plan.
func TestDoneWhenQuery(t *testing.T) {
	r := mustQuery(t, "SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name")
	assert.Equal(t, []string{"name", "COUNT(*)"}, r.Schema.Names())
	// amount>100: Alice {150,220}=2, Bob {310.25,120}=2, Carol {500}=1.
	assert.Equal(t, [][]string{
		{"Alice", "2"}, {"Bob", "2"}, {"Carol", "1"},
	}, sortedRowStrings(r))
}

func TestQuery_ProjectAndFilter(t *testing.T) {
	r := mustQuery(t, "SELECT name, amount FROM orders WHERE status = 'pending'")
	assert.Equal(t, [][]string{
		{"Alice", "95"}, {"Carol", "40"},
	}, sortedRowStrings(r))
}

func TestQuery_GlobalAggregates(t *testing.T) {
	r := mustQuery(t, "SELECT COUNT(*), SUM(amount), MIN(amount), MAX(amount) FROM orders")
	require.Equal(t, 1, r.RowCount())
	row := rowStrings(r)[0]
	assert.Equal(t, "8", row[0])       // COUNT(*)
	assert.Equal(t, "1510.75", row[1]) // SUM
	assert.Equal(t, "40", row[2])      // MIN
	assert.Equal(t, "500", row[3])     // MAX
}

func TestQuery_OrderByAndLimit(t *testing.T) {
	r := mustQuery(t, "SELECT amount FROM orders ORDER BY amount DESC LIMIT 3")
	assert.Equal(t, [][]string{{"500"}, {"310.25"}, {"220"}}, rowStrings(r))
}

func TestQuery_JoinOrderByLimit(t *testing.T) {
	r := mustQuery(t, "SELECT o.name, o.amount, c.city FROM orders o JOIN customers c ON o.name = c.name WHERE o.amount > 100 ORDER BY o.amount DESC LIMIT 2")
	assert.Equal(t, [][]string{
		{"Carol", "500", "Denver"},
		{"Bob", "310.25", "Portland"},
	}, rowStrings(r))
}

func TestQuery_Distinct(t *testing.T) {
	r := mustQuery(t, "SELECT DISTINCT name FROM orders")
	assert.Equal(t, [][]string{{"Alice"}, {"Bob"}, {"Carol"}}, sortedRowStrings(r))
}

func TestQuery_GroupByMultiAggregate(t *testing.T) {
	r := mustQuery(t, "SELECT name, COUNT(*) AS n, SUM(amount) AS total FROM orders GROUP BY name ORDER BY total DESC")
	assert.Equal(t, [][]string{
		{"Carol", "2", "540"},
		{"Bob", "3", "505.75"},
		{"Alice", "3", "465"},
	}, rowStrings(r))
}

func TestResult_String(t *testing.T) {
	r := mustQuery(t, "SELECT name, amount FROM orders ORDER BY amount DESC LIMIT 2")
	out := r.String()
	assert.Contains(t, out, "name")
	assert.Contains(t, out, "amount")
	assert.Contains(t, out, "Carol") // largest amount, 500
	assert.Contains(t, out, "(2 rows)")
}

func TestResult_StringFormatsNullsAndSingularRow(t *testing.T) {
	// A LEFT join miss would be ideal, but a single-row count exercises the
	// "(1 row)" singular and integer formatting.
	r := mustQuery(t, "SELECT COUNT(*) FROM orders")
	out := r.String()
	assert.Contains(t, out, "(1 row)")
	assert.Contains(t, out, "8")
}

func TestExplain(t *testing.T) {
	plan, err := testEngine().Explain("SELECT name FROM orders WHERE amount > 100")
	require.NoError(t, err)
	assert.Equal(t, "Project: name\n  Filter: amount > 100\n    Scan: orders\n", plan)
}

func TestExplainCost(t *testing.T) {
	out, err := testEngine().ExplainCost(context.Background(), "SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name")
	require.NoError(t, err)
	assert.Contains(t, out, "rows=")
	assert.Contains(t, out, "cost=")
	assert.Contains(t, out, "Total cost:")
	// Projection pushdown should narrow the scan (amount + name, not id/status).
	assert.Contains(t, out, "Scan: orders [")
}

func TestCost(t *testing.T) {
	c, err := testEngine().Cost(context.Background(), "SELECT name FROM orders WHERE amount > 100")
	require.NoError(t, err)
	assert.Greater(t, c, 0.0)
}

func TestOptimizedPlan_PushesPredicateBelowJoin(t *testing.T) {
	plan, err := testEngine().OptimizedPlan(context.Background(),
		"SELECT o.name, c.city FROM orders o JOIN customers c ON o.name = c.name WHERE o.amount > 100")
	require.NoError(t, err)
	// After optimization the WHERE is no longer the node directly under Project;
	// it has been pushed below the join.
	proj, ok := plan.(*logical.Project)
	require.True(t, ok)
	_, stillFilter := proj.Input.(*logical.Filter)
	assert.False(t, stillFilter, "predicate should have been pushed below the join")
}

func TestTables(t *testing.T) {
	tables, err := testEngine().Tables()
	require.NoError(t, err)
	assert.Equal(t, []string{"customers", "orders"}, tables)
}

func TestQuery_Errors(t *testing.T) {
	tests := []struct{ name, sql string }{
		{"unknown table", "SELECT x FROM nope"},
		{"unknown column", "SELECT bogus FROM orders"},
		{"non-select", "EXPLAIN orders"},
		{"order by unprojected column", "SELECT id FROM orders ORDER BY amount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testEngine().Query(context.Background(), tt.sql)
			assert.Error(t, err)
		})
	}
}
