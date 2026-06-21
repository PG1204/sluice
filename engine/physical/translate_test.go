package physical

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

// memSource is an in-memory DataSource for the translate tests.
type memSource struct {
	schema storage.Schema
	batch  *storage.Batch
	done   bool
}

func (m *memSource) Schema() storage.Schema { return m.schema }
func (m *memSource) Close() error           { return nil }
func (m *memSource) Next(context.Context) (*storage.Batch, error) {
	if m.done {
		return nil, io.EOF
	}
	m.done = true
	return m.batch, nil
}

// tableData defines an in-memory table.
type tableData struct {
	schema storage.Schema
	rows   [][]Value
}

// memCatalog is both a logical.Catalog and a physical.TableOpener over a fixed
// set of in-memory tables, so a query can be planned and executed end to end
// inside this package's coverage.
type memCatalog map[string]tableData

func (m memCatalog) TableSchema(name string) (storage.Schema, error) {
	td, ok := m[name]
	if !ok {
		return storage.Schema{}, fmt.Errorf("no table %q", name)
	}
	return td.schema, nil
}

func (m memCatalog) Open(_ context.Context, name string) (storage.DataSource, error) {
	td, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("no table %q", name)
	}
	return &memSource{schema: td.schema, batch: makeBatch(td.schema, td.rows)}, nil
}

func ordersCatalog() memCatalog {
	orders := storage.Schema{Fields: []storage.Field{
		{Name: "id", Type: storage.TypeInt64},
		{Name: "name", Type: storage.TypeString},
		{Name: "amount", Type: storage.TypeFloat64},
	}}
	customers := storage.Schema{Fields: []storage.Field{
		{Name: "name", Type: storage.TypeString},
		{Name: "city", Type: storage.TypeString},
	}}
	return memCatalog{
		"orders": {schema: orders, rows: [][]Value{
			{Int(1), Str("Alice"), Float(150)},
			{Int(2), Str("Bob"), Float(75)},
			{Int(3), Str("Alice"), Float(220)},
		}},
		"customers": {schema: customers, rows: [][]Value{
			{Str("Alice"), Str("Seattle")},
			{Str("Bob"), Str("Portland")},
		}},
	}
}

func execSQL(t *testing.T, cat memCatalog, sql string) [][]Value {
	t.Helper()
	stmt, err := parser.Parse(sql)
	require.NoError(t, err)
	plan, err := logical.Build(stmt.(*ast.SelectStatement), cat)
	require.NoError(t, err)
	op, err := Build(plan, cat)
	require.NoError(t, err)
	require.NotEmpty(t, op.Schema().Fields) // exercise Schema()
	return collectRows(t, op)
}

func TestTranslate_FilterProject(t *testing.T) {
	rows := execSQL(t, ordersCatalog(), "SELECT name, amount FROM orders WHERE amount > 100")
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Greater(t, r[1].asFloat(), 100.0)
	}
}

func TestTranslate_GroupBy(t *testing.T) {
	rows := execSQL(t, ordersCatalog(), "SELECT name, COUNT(*) FROM orders GROUP BY name")
	got := map[string]int64{}
	for _, r := range rows {
		got[r[0].S] = r[1].I
	}
	assert.Equal(t, map[string]int64{"Alice": 2, "Bob": 1}, got)
}

func TestTranslate_AllAggregates(t *testing.T) {
	rows := execSQL(t, ordersCatalog(),
		"SELECT name, SUM(amount) AS s, AVG(amount) AS a, MIN(amount) AS mn, MAX(amount) AS mx FROM orders GROUP BY name")
	got := map[string][]Value{}
	for _, r := range rows {
		got[r[0].S] = r[1:]
	}
	// Alice: {150, 220} -> sum 370, avg 185, min 150, max 220.
	assert.Equal(t, Float(370), got["Alice"][0])
	assert.Equal(t, Float(185), got["Alice"][1])
	assert.Equal(t, Float(150), got["Alice"][2])
	assert.Equal(t, Float(220), got["Alice"][3])
	// Bob: single value 75.
	assert.Equal(t, Float(75), got["Bob"][0])
	assert.Equal(t, Float(75), got["Bob"][3])
}

func TestTranslate_OrderByLimit(t *testing.T) {
	rows := execSQL(t, ordersCatalog(), "SELECT amount FROM orders ORDER BY amount DESC LIMIT 2")
	require.Len(t, rows, 2)
	assert.Equal(t, Float(220), rows[0][0])
	assert.Equal(t, Float(150), rows[1][0])
}

func TestTranslate_Distinct(t *testing.T) {
	rows := execSQL(t, ordersCatalog(), "SELECT DISTINCT name FROM orders")
	names := map[string]bool{}
	for _, r := range rows {
		names[r[0].S] = true
	}
	assert.Equal(t, map[string]bool{"Alice": true, "Bob": true}, names)
}

func TestTranslate_Join(t *testing.T) {
	rows := execSQL(t, ordersCatalog(),
		"SELECT o.name, c.city FROM orders o JOIN customers c ON o.name = c.name")
	require.Len(t, rows, 3) // all three orders have a matching customer
}

func TestTranslate_RightJoinUnsupported(t *testing.T) {
	stmt, err := parser.Parse("SELECT o.id FROM orders o RIGHT JOIN customers c ON o.name = c.name")
	require.NoError(t, err)
	plan, err := logical.Build(stmt.(*ast.SelectStatement), ordersCatalog())
	require.NoError(t, err)
	_, err = Build(plan, ordersCatalog())
	assert.ErrorContains(t, err, "RIGHT")
}

func TestTranslate_NonEquiJoinRejected(t *testing.T) {
	stmt, err := parser.Parse("SELECT o.id FROM orders o JOIN customers c ON o.amount > 100")
	require.NoError(t, err)
	plan, err := logical.Build(stmt.(*ast.SelectStatement), ordersCatalog())
	require.NoError(t, err)
	_, err = Build(plan, ordersCatalog())
	assert.ErrorContains(t, err, "=")
}
