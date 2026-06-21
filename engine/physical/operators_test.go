package physical

import (
	"context"
	"io"
	"testing"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceOp is a leaf Operator that replays pre-built batches, for testing
// operators without the filesystem.
type sourceOp struct {
	schema  storage.Schema
	batches []*storage.Batch
	i       int
}

func (s *sourceOp) Schema() storage.Schema     { return s.schema }
func (s *sourceOp) Open(context.Context) error { s.i = 0; return nil }
func (s *sourceOp) Close() error               { return nil }
func (s *sourceOp) Next(context.Context) (*storage.Batch, error) {
	if s.i >= len(s.batches) {
		return nil, io.EOF
	}
	b := s.batches[s.i]
	s.i++
	return b, nil
}

// makeBatch builds a batch from a schema and a row-major matrix of Values.
func makeBatch(schema storage.Schema, rows [][]Value) *storage.Batch {
	return buildBatch(schema, len(rows), func(col, row int) Value { return rows[row][col] })
}

func leaf(schema storage.Schema, rows [][]Value) *sourceOp {
	b := makeBatch(schema, rows)
	return &sourceOp{schema: schema, batches: []*storage.Batch{b}}
}

// collectRows runs an operator and returns all output rows as Values.
func collectRows(t *testing.T, op Operator) [][]Value {
	t.Helper()
	var out [][]Value
	err := Run(context.Background(), op, func(b *storage.Batch) error {
		for r := 0; r < b.NumRows(); r++ {
			row := make([]Value, b.NumCols())
			for c := 0; c < b.NumCols(); c++ {
				row[c] = columnValue(b.Columns[c], r)
			}
			out = append(out, row)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

func TestGlobalAggregate_EmptyInputEmitsOneRow(t *testing.T) {
	schema := storage.Schema{Fields: []storage.Field{{Name: "x", Type: storage.TypeInt64}}}
	child := &sourceOp{schema: schema} // no batches => empty input

	outSchema := storage.Schema{Fields: []storage.Field{{Name: "COUNT(*)", Type: storage.TypeInt64}}}
	agg := NewHashAggregate(child, nil, []aggSpec{{kind: aggCountStar, resultType: storage.TypeInt64}}, outSchema)

	rows := collectRows(t, agg)
	require.Len(t, rows, 1, "COUNT(*) over empty input must still produce one row")
	assert.Equal(t, Int(0), rows[0][0])
}

func TestSumOverAllNullIsNull(t *testing.T) {
	schema := storage.Schema{Fields: []storage.Field{{Name: "x", Type: storage.TypeInt64}}}
	child := leaf(schema, [][]Value{{Null()}, {Null()}})

	outSchema := storage.Schema{Fields: []storage.Field{{Name: "SUM(x)", Type: storage.TypeInt64}}}
	agg := NewHashAggregate(child, nil, []aggSpec{{kind: aggSum, arg: colRef{idx: 0, t: storage.TypeInt64}, resultType: storage.TypeInt64}}, outSchema)

	rows := collectRows(t, agg)
	require.Len(t, rows, 1)
	assert.True(t, rows[0][0].IsNull(), "SUM over all-NULL is NULL")
}

func TestLeftJoin_KeepsUnmatchedLeftRowWithNulls(t *testing.T) {
	leftSchema := storage.Schema{Fields: []storage.Field{
		{Name: "id", Type: storage.TypeInt64},
		{Name: "name", Type: storage.TypeString},
	}}
	rightSchema := storage.Schema{Fields: []storage.Field{
		{Name: "uid", Type: storage.TypeInt64},
		{Name: "city", Type: storage.TypeString},
	}}
	left := leaf(leftSchema, [][]Value{{Int(1), Str("Alice")}, {Int(2), Str("Bob")}})
	right := leaf(rightSchema, [][]Value{{Int(1), Str("Seattle")}}) // no row for id 2

	outSchema := storage.Schema{Fields: []storage.Field{
		{Name: "id", Type: storage.TypeInt64}, {Name: "name", Type: storage.TypeString},
		{Name: "uid", Type: storage.TypeInt64}, {Name: "city", Type: storage.TypeString},
	}}
	join := NewHashJoin(left, right,
		[]evalExpr{colRef{idx: 0, t: storage.TypeInt64}},
		[]evalExpr{colRef{idx: 0, t: storage.TypeInt64}},
		ast.LeftJoin, outSchema, 2)

	rows := collectRows(t, join)
	require.Len(t, rows, 2)
	// Bob (id 2) has no match: right columns are NULL.
	var bob []Value
	for _, r := range rows {
		if r[0] == Int(2) {
			bob = r
		}
	}
	require.NotNil(t, bob)
	assert.Equal(t, Str("Bob"), bob[1])
	assert.True(t, bob[2].IsNull())
	assert.True(t, bob[3].IsNull())
}

func TestInnerJoin_DropsUnmatched(t *testing.T) {
	leftSchema := storage.Schema{Fields: []storage.Field{{Name: "id", Type: storage.TypeInt64}}}
	rightSchema := storage.Schema{Fields: []storage.Field{{Name: "uid", Type: storage.TypeInt64}}}
	left := leaf(leftSchema, [][]Value{{Int(1)}, {Int(2)}})
	right := leaf(rightSchema, [][]Value{{Int(1)}})

	outSchema := storage.Schema{Fields: []storage.Field{
		{Name: "id", Type: storage.TypeInt64}, {Name: "uid", Type: storage.TypeInt64},
	}}
	join := NewHashJoin(left, right,
		[]evalExpr{colRef{idx: 0, t: storage.TypeInt64}},
		[]evalExpr{colRef{idx: 0, t: storage.TypeInt64}},
		ast.InnerJoin, outSchema, 1)

	rows := collectRows(t, join)
	require.Len(t, rows, 1, "only id 1 matches")
	assert.Equal(t, Int(1), rows[0][0])
}

func TestLimit_TruncatesAcrossBatchBoundary(t *testing.T) {
	schema := storage.Schema{Fields: []storage.Field{{Name: "x", Type: storage.TypeInt64}}}
	b1 := makeBatch(schema, [][]Value{{Int(1)}, {Int(2)}})
	b2 := makeBatch(schema, [][]Value{{Int(3)}, {Int(4)}})
	child := &sourceOp{schema: schema, batches: []*storage.Batch{b1, b2}}

	rows := collectRows(t, NewLimit(child, 3))
	require.Len(t, rows, 3)
	assert.Equal(t, []Value{Int(1)}, rows[0])
	assert.Equal(t, []Value{Int(3)}, rows[2])
}

func TestSort_NullsLast(t *testing.T) {
	schema := storage.Schema{Fields: []storage.Field{{Name: "x", Type: storage.TypeInt64}}}
	child := leaf(schema, [][]Value{{Int(3)}, {Null()}, {Int(1)}, {Int(2)}})

	sorted := collectRows(t, NewSort(child, []sortKey{{expr: colRef{idx: 0, t: storage.TypeInt64}}}))
	require.Len(t, sorted, 4)
	assert.Equal(t, Int(1), sorted[0][0])
	assert.Equal(t, Int(3), sorted[2][0])
	assert.True(t, sorted[3][0].IsNull(), "NULL sorts last ascending")
}
