package logical

import (
	"fmt"
	"testing"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/parser"
	"github.com/PG1204/sluice/engine/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mapCatalog is an in-memory Catalog for tests.
type mapCatalog map[string]storage.Schema

func (m mapCatalog) TableSchema(name string) (storage.Schema, error) {
	s, ok := m[name]
	if !ok {
		return storage.Schema{}, fmt.Errorf("no such table %q", name)
	}
	return s, nil
}

func testCatalog() Catalog {
	return mapCatalog{
		"users": {Fields: []storage.Field{
			{Name: "id", Type: storage.TypeInt64},
			{Name: "name", Type: storage.TypeString},
			{Name: "active", Type: storage.TypeBool},
		}},
		"orders": {Fields: []storage.Field{
			{Name: "id", Type: storage.TypeInt64},
			{Name: "user_id", Type: storage.TypeInt64},
			{Name: "amount", Type: storage.TypeFloat64, Nullable: true},
			{Name: "status", Type: storage.TypeString},
		}},
	}
}

func mustPlan(t *testing.T, sql string) Plan {
	t.Helper()
	stmt, err := parser.Parse(sql)
	require.NoError(t, err, "parse")
	sel, ok := stmt.(*ast.SelectStatement)
	require.True(t, ok)
	p, err := Build(sel, testCatalog())
	require.NoError(t, err, "build plan")
	return p
}

func planErr(t *testing.T, sql string) error {
	t.Helper()
	stmt, err := parser.Parse(sql)
	require.NoError(t, err, "parse")
	_, err = Build(stmt.(*ast.SelectStatement), testCatalog())
	return err
}

func TestPlan_ScanAndProject(t *testing.T) {
	p := mustPlan(t, "SELECT id, name FROM users")
	proj, ok := p.(*Project)
	require.True(t, ok, "root should be Project")
	_, ok = proj.Input.(*Scan)
	require.True(t, ok, "Project input should be Scan")

	assert.Equal(t, []string{"id", "name"}, proj.Schema().Names())
	assert.Equal(t, storage.TypeInt64, proj.Schema().Fields[0].Type)
	assert.Equal(t, storage.TypeString, proj.Schema().Fields[1].Type)
}

func TestPlan_SelectStarExpands(t *testing.T) {
	p := mustPlan(t, "SELECT * FROM users")
	proj := p.(*Project)
	assert.Equal(t, []string{"id", "name", "active"}, proj.Schema().Names())
}

func TestPlan_Filter(t *testing.T) {
	p := mustPlan(t, "SELECT id FROM users WHERE active")
	proj := p.(*Project)
	filter, ok := proj.Input.(*Filter)
	require.True(t, ok, "expected Filter under Project")
	_, ok = filter.Input.(*Scan)
	assert.True(t, ok)
}

func TestPlan_Join(t *testing.T) {
	p := mustPlan(t, "SELECT users.name, orders.amount FROM users JOIN orders ON users.id = orders.user_id")
	proj := p.(*Project)
	join, ok := proj.Input.(*Join)
	require.True(t, ok, "expected Join under Project")
	assert.Equal(t, ast.InnerJoin, join.JoinType)
	// Join output schema is the concatenation of both tables (7 columns).
	assert.Len(t, join.Schema().Fields, 7)
	// Output column names are the bare column names (the qualifier selects the
	// source, it isn't part of the output name).
	assert.Equal(t, []string{"name", "amount"}, proj.Schema().Names())
}

func TestPlan_AggregateGroupBy(t *testing.T) {
	p := mustPlan(t, "SELECT status, COUNT(*) FROM orders GROUP BY status")
	proj := p.(*Project)
	agg, ok := proj.Input.(*Aggregate)
	require.True(t, ok, "expected Aggregate under Project")

	require.Len(t, agg.GroupBy, 1)
	require.Len(t, agg.Aggregates, 1)
	assert.Equal(t, "COUNT(*)", agg.Aggregates[0].Name)
	assert.Equal(t, storage.TypeInt64, agg.Aggregates[0].Type)

	// Aggregate output: group key then aggregate.
	assert.Equal(t, []string{"status", "COUNT(*)"}, agg.Schema().Names())
	assert.Equal(t, storage.TypeString, agg.Schema().Fields[0].Type)
	assert.Equal(t, storage.TypeInt64, agg.Schema().Fields[1].Type)
}

func TestPlan_GlobalAggregate(t *testing.T) {
	p := mustPlan(t, "SELECT COUNT(*) FROM orders")
	agg := p.(*Project).Input.(*Aggregate)
	assert.Empty(t, agg.GroupBy)
	assert.Len(t, agg.Aggregates, 1)
}

func TestPlan_AggregateResultTypes(t *testing.T) {
	tests := []struct {
		sql  string
		want storage.Type
	}{
		{"SELECT SUM(amount) FROM orders", storage.TypeFloat64},  // float in -> float
		{"SELECT SUM(user_id) FROM orders", storage.TypeInt64},   // int in -> int
		{"SELECT AVG(user_id) FROM orders", storage.TypeFloat64}, // avg is always float
		{"SELECT MIN(amount) FROM orders", storage.TypeFloat64},
		{"SELECT MAX(status) FROM orders", storage.TypeString},
		{"SELECT COUNT(amount) FROM orders", storage.TypeInt64},
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			p := mustPlan(t, tt.sql)
			agg := p.(*Project).Input.(*Aggregate)
			assert.Equal(t, tt.want, agg.Aggregates[0].Type)
		})
	}
}

func TestPlan_SortAndLimit(t *testing.T) {
	p := mustPlan(t, "SELECT amount FROM orders ORDER BY amount DESC LIMIT 5")
	limit, ok := p.(*Limit)
	require.True(t, ok, "root should be Limit")
	assert.Equal(t, int64(5), limit.Count)

	sort, ok := limit.Input.(*Sort)
	require.True(t, ok, "Limit input should be Sort")
	require.Len(t, sort.Keys, 1)
	assert.True(t, sort.Keys[0].Desc)
}

func TestPlan_OrderByAlias(t *testing.T) {
	// ORDER BY references a SELECT alias that is an aggregate.
	p := mustPlan(t, "SELECT status, COUNT(*) AS c FROM orders GROUP BY status ORDER BY c DESC")
	sort, ok := p.(*Sort)
	require.True(t, ok, "root should be Sort")
	assert.Equal(t, "c", sort.Keys[0].Expr.String())
}

func TestPlan_ValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		msgSubstr string
	}{
		{"unknown table", "SELECT id FROM ghosts", "table"},
		{"unknown column", "SELECT bogus FROM users", "unknown column"},
		{"ambiguous column", "SELECT id FROM users JOIN orders ON users.id = orders.user_id", "ambiguous"},
		{"where not boolean", "SELECT id FROM users WHERE name", "must be boolean"},
		{"incomparable types", "SELECT id FROM users WHERE name > 5", "cannot compare"},
		{"arithmetic on string", "SELECT id FROM users WHERE id + name > 0", "requires numbers"},
		{"unknown function", "SELECT foo(id) FROM users", `unknown function "foo"`},
		{"aggregate in where", "SELECT id FROM users WHERE COUNT(*) > 1", "not allowed"},
		{"star with group by", "SELECT * FROM orders GROUP BY status", "'*' cannot be used"},
		{"non-grouped column", "SELECT id, COUNT(*) FROM orders GROUP BY status", "must appear in GROUP BY"},
		{"join on not boolean", "SELECT users.id FROM users JOIN orders ON users.name", "must be boolean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := planErr(t, tt.sql)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.msgSubstr)
		})
	}
}

func TestExplain_FullQuery(t *testing.T) {
	p := mustPlan(t,
		"SELECT u.name, COUNT(*) AS orders FROM users u JOIN orders o ON u.id = o.user_id "+
			"WHERE o.amount > 100 GROUP BY u.name ORDER BY orders DESC LIMIT 5")

	want := "" +
		"Limit: 5\n" +
		"  Sort: orders DESC\n" +
		"    Project: u.name, COUNT(*) AS orders\n" +
		"      Aggregate: group by u.name; aggs COUNT(*)\n" +
		"        Filter: o.amount > 100\n" +
		"          Join INNER on u.id = o.user_id\n" +
		"            Scan: users AS u\n" +
		"            Scan: orders AS o\n"
	assert.Equal(t, want, Explain(p))
}

func TestExplain_SimplePlan(t *testing.T) {
	p := mustPlan(t, "SELECT id, name FROM users WHERE id > 10")
	want := "" +
		"Project: id, name\n" +
		"  Filter: id > 10\n" +
		"    Scan: users\n"
	assert.Equal(t, want, Explain(p))
}
