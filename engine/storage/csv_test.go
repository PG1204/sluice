package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCSV_InfersSchema(t *testing.T) {
	src, err := OpenCSV(filepath.Join("testdata", "employees.csv"))
	require.NoError(t, err)
	defer src.Close()

	want := Schema{Fields: []Field{
		{Name: "id", Type: TypeInt64, Nullable: false},
		{Name: "name", Type: TypeString, Nullable: false},
		{Name: "salary", Type: TypeFloat64, Nullable: true}, // mix of int- and float-looking + a blank
		{Name: "active", Type: TypeBool, Nullable: false},
		{Name: "manager", Type: TypeString, Nullable: true}, // first row is blank
	}}
	assert.Equal(t, want, src.Schema())
}

func TestCSV_ReadsValuesAndNulls(t *testing.T) {
	src, err := OpenCSV(filepath.Join("testdata", "employees.csv"))
	require.NoError(t, err)
	defer src.Close()

	batches, err := ReadAll(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, batches, 1)

	b := batches[0]
	require.Equal(t, 5, b.NumRows())
	require.Equal(t, 5, b.NumCols())

	id := b.Columns[0].(*Int64Column)
	first, ok := id.At(0)
	assert.True(t, ok)
	assert.Equal(t, int64(1), first)

	salary := b.Columns[2].(*Float64Column)
	assert.Equal(t, 95000.50, salary.Value(0))
	assert.True(t, salary.IsNull(3), "Dave's salary is blank -> NULL")

	active := b.Columns[3].(*BoolColumn)
	assert.Equal(t, true, active.Value(0))
	assert.Equal(t, false, active.Value(1))

	manager := b.Columns[4].(*StringColumn)
	assert.True(t, manager.IsNull(0), "Alice has no manager")
	assert.Equal(t, "Alice", manager.Value(1))
}

func TestCSV_BatchingSplitsRows(t *testing.T) {
	src, err := OpenCSV(filepath.Join("testdata", "employees.csv"), WithBatchSize(2))
	require.NoError(t, err)
	defer src.Close()

	batches, err := ReadAll(context.Background(), src)
	require.NoError(t, err)

	// 5 rows at batch size 2 => 2 + 2 + 1.
	require.Len(t, batches, 3)
	assert.Equal(t, 2, batches[0].NumRows())
	assert.Equal(t, 2, batches[1].NumRows())
	assert.Equal(t, 1, batches[2].NumRows())
}

func TestCSV_HeaderOnly(t *testing.T) {
	src, err := OpenCSV(filepath.Join("testdata", "header_only.csv"))
	require.NoError(t, err)
	defer src.Close()

	// No data rows: columns with no values default to STRING and are nullable.
	for _, f := range src.Schema().Fields {
		assert.Equal(t, TypeString, f.Type)
		assert.True(t, f.Nullable)
	}

	batches, err := ReadAll(context.Background(), src)
	require.NoError(t, err)
	assert.Empty(t, batches)
}

func TestCSV_MissingFile(t *testing.T) {
	_, err := OpenCSV(filepath.Join("testdata", "does_not_exist.csv"))
	require.Error(t, err)
}

func TestCSV_ContextCancellation(t *testing.T) {
	src, err := OpenCSV(filepath.Join("testdata", "employees.csv"))
	require.NoError(t, err)
	defer src.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = src.Next(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}
