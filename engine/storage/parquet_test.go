package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parquetEmp mirrors the employees fixture. A pointer field is "optional" in
// Parquet terms, letting us exercise NULL handling; nil writes a NULL.
type parquetEmp struct {
	ID      int64   `parquet:"id"`
	Name    string  `parquet:"name"`
	Salary  float64 `parquet:"salary"`
	Active  bool    `parquet:"active"`
	Manager *string `parquet:"manager,optional"`
}

// writeParquetFixture writes rows to a temp .parquet file and returns its path.
func writeParquetFixture(t *testing.T, rows []parquetEmp) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "employees.parquet")
	require.NoError(t, parquet.WriteFile(path, rows))
	return path
}

func strptr(s string) *string { return &s }

func TestParquet_SchemaAndValues(t *testing.T) {
	path := writeParquetFixture(t, []parquetEmp{
		{ID: 1, Name: "Alice", Salary: 95000.50, Active: true, Manager: nil},
		{ID: 2, Name: "Bob", Salary: 72000, Active: false, Manager: strptr("Alice")},
		{ID: 3, Name: "Carol", Salary: 88000.25, Active: true, Manager: strptr("Alice")},
	})

	src, err := OpenParquet(path)
	require.NoError(t, err)
	defer src.Close()

	schema := src.Schema()
	assert.Equal(t, []string{"id", "name", "salary", "active", "manager"}, schema.Names())
	assert.Equal(t, TypeInt64, schema.Fields[0].Type)
	assert.Equal(t, TypeString, schema.Fields[1].Type)
	assert.Equal(t, TypeFloat64, schema.Fields[2].Type)
	assert.Equal(t, TypeBool, schema.Fields[3].Type)
	assert.True(t, schema.Fields[4].Nullable, "optional column must be nullable")

	batches, err := ReadAll(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	b := batches[0]
	require.Equal(t, 3, b.NumRows())

	assert.Equal(t, int64(2), b.Columns[0].(*Int64Column).Value(1))
	assert.Equal(t, "Carol", b.Columns[1].(*StringColumn).Value(2))
	assert.Equal(t, 95000.50, b.Columns[2].(*Float64Column).Value(0))
	assert.Equal(t, true, b.Columns[3].(*BoolColumn).Value(0))

	manager := b.Columns[4].(*StringColumn)
	assert.True(t, manager.IsNull(0), "Alice's manager is NULL")
	assert.Equal(t, "Alice", manager.Value(1))
}

func TestParquet_Batching(t *testing.T) {
	rows := make([]parquetEmp, 5)
	for i := range rows {
		rows[i] = parquetEmp{ID: int64(i), Name: "x"}
	}
	path := writeParquetFixture(t, rows)

	src, err := OpenParquet(path, WithParquetBatchSize(2))
	require.NoError(t, err)
	defer src.Close()

	batches, err := ReadAll(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, batches, 3)
	assert.Equal(t, 2, batches[0].NumRows())
	assert.Equal(t, 1, batches[2].NumRows())
}

func TestParquet_MissingFile(t *testing.T) {
	_, err := OpenParquet(filepath.Join(t.TempDir(), "nope.parquet"))
	require.Error(t, err)
}
