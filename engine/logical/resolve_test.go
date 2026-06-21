package logical

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/parser"
	"github.com/PG1204/sluice/engine/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlan_QualifiedStar(t *testing.T) {
	p := mustPlan(t, "SELECT u.* FROM users u")
	assert.Equal(t, []string{"id", "name", "active"}, p.(*Project).Schema().Names())
}

func TestPlan_Distinct(t *testing.T) {
	p := mustPlan(t, "SELECT DISTINCT status FROM orders")
	proj := p.(*Project)
	assert.True(t, proj.Distinct)
	assert.Contains(t, Explain(p), "Project (DISTINCT):")
}

func TestPlan_BooleanLogicInWhere(t *testing.T) {
	// Exercises NOT, AND, OR, unary minus, and arithmetic in one predicate.
	p := mustPlan(t, "SELECT id FROM orders WHERE NOT (amount > 0) AND user_id > 0 OR id < -5")
	_, ok := p.(*Project).Input.(*Filter)
	assert.True(t, ok)
}

func TestPlan_JoinTypeWordsInExplain(t *testing.T) {
	tests := []struct {
		sql  string
		word string
	}{
		{"SELECT users.id FROM users JOIN orders ON users.id = orders.user_id", "Join INNER"},
		{"SELECT users.id FROM users LEFT JOIN orders ON users.id = orders.user_id", "Join LEFT"},
		{"SELECT users.id FROM users RIGHT JOIN orders ON users.id = orders.user_id", "Join RIGHT"},
		{"SELECT users.id FROM users FULL JOIN orders ON users.id = orders.user_id", "Join FULL"},
	}
	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			assert.Contains(t, Explain(mustPlan(t, tt.sql)), tt.word)
		})
	}
}

func TestPlan_OrderByUnselectedColumn(t *testing.T) {
	// ORDER BY a column that isn't in the SELECT list resolves against the
	// input scope, not the projection.
	p := mustPlan(t, "SELECT id FROM orders ORDER BY amount")
	sort, ok := p.(*Sort)
	require.True(t, ok)
	assert.Equal(t, "amount", sort.Keys[0].Expr.String())
}

func TestPlan_AggregateArgTypeErrors(t *testing.T) {
	tests := []struct {
		sql       string
		msgSubstr string
	}{
		{"SELECT SUM(status) FROM orders", "SUM requires a number"},
		{"SELECT AVG(status) FROM orders", "AVG requires a number"},
		{"SELECT MIN(active) FROM users", "orderable"},
		{"SELECT SUM(amount, id) FROM orders", "exactly one argument"},
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			err := planErr(t, tt.sql)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.msgSubstr)
		})
	}
}

// TestPlan_WithRegistryCatalog wires the planner to a real storage Registry via
// NewRegistryCatalog, covering the production catalog path end to end.
func TestPlan_WithRegistryCatalog(t *testing.T) {
	dir := t.TempDir()
	csv := "id,name\n1,Alice\n2,Bob\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "people.csv"), []byte(csv), 0o644))

	cat := NewRegistryCatalog(storage.NewRegistry(dir))
	stmt, err := parser.Parse("SELECT name FROM people WHERE id > 0")
	require.NoError(t, err)

	p, err := Build(stmt.(*ast.SelectStatement), cat)
	require.NoError(t, err)
	assert.Equal(t, []string{"name"}, p.Schema().Names())

	// And a missing table surfaces through the adapter.
	bad, _ := parser.Parse("SELECT x FROM missing")
	_, err = Build(bad.(*ast.SelectStatement), cat)
	assert.Error(t, err)
}
