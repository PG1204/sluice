package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDataDir builds a temp data directory containing an orders.csv and a
// people.parquet, and returns its path.
func newDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	csv := "order_id,total\n1,9.99\n2,19.50\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.csv"), []byte(csv), 0o644))

	require.NoError(t, parquet.WriteFile(
		filepath.Join(dir, "people.parquet"),
		[]parquetEmp{{ID: 1, Name: "Alice"}},
	))

	// A non-data file that must be ignored by the registry.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.txt"), []byte("ignore me"), 0o644))
	return dir
}

func TestRegistry_SchemaIsCached(t *testing.T) {
	dir := newDataDir(t)
	r := NewRegistry(dir)
	ctx := context.Background()

	first, err := r.Schema(ctx, "orders")
	require.NoError(t, err)
	require.Equal(t, []string{"order_id", "total"}, first.Names())

	// Delete the backing file: a cached schema must still resolve, proving the
	// second call did not re-open (and re-scan) the file.
	require.NoError(t, os.Remove(filepath.Join(dir, "orders.csv")))

	second, err := r.Schema(ctx, "orders")
	require.NoError(t, err, "schema should be served from cache after the file is gone")
	assert.Equal(t, first, second)
}

func TestRegistry_Tables(t *testing.T) {
	r := NewRegistry(newDataDir(t))
	tables, err := r.Tables()
	require.NoError(t, err)
	assert.Equal(t, []string{"orders", "people"}, tables, "sorted, extension-stripped, non-data ignored")
}

func TestRegistry_OpensByName(t *testing.T) {
	r := NewRegistry(newDataDir(t))
	ctx := context.Background()

	csvSrc, err := r.Open(ctx, "orders")
	require.NoError(t, err)
	defer csvSrc.Close()
	assert.IsType(t, &CSVSource{}, csvSrc)
	assert.Equal(t, []string{"order_id", "total"}, csvSrc.Schema().Names())

	pqSrc, err := r.Open(ctx, "people")
	require.NoError(t, err)
	defer pqSrc.Close()
	assert.IsType(t, &ParquetSource{}, pqSrc)
}

func TestRegistry_SchemaWithoutReadingData(t *testing.T) {
	r := NewRegistry(newDataDir(t))
	schema, err := r.Schema(context.Background(), "orders")
	require.NoError(t, err)
	assert.Equal(t, []string{"order_id", "total"}, schema.Names())
	assert.Equal(t, TypeFloat64, schema.Fields[1].Type)
}

func TestRegistry_NotFound(t *testing.T) {
	r := NewRegistry(newDataDir(t))
	_, err := r.Open(context.Background(), "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_RejectsPathTraversal(t *testing.T) {
	r := NewRegistry(newDataDir(t))
	for _, bad := range []string{"", "../secrets", "sub/dir", `a\b`} {
		_, err := r.Open(context.Background(), bad)
		assert.Errorf(t, err, "should reject %q", bad)
	}
}

func TestRegistry_PrefersCSVWhenBothExist(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dup.csv"), []byte("a\n1\n"), 0o644))
	require.NoError(t, parquet.WriteFile(filepath.Join(dir, "dup.parquet"), []parquetEmp{{ID: 1}}))

	r := NewRegistry(dir)
	tables, err := r.Tables()
	require.NoError(t, err)
	assert.Equal(t, []string{"dup"}, tables, "a table backed by two files lists once")

	src, err := r.Open(context.Background(), "dup")
	require.NoError(t, err)
	defer src.Close()
	assert.IsType(t, &CSVSource{}, src, "CSV wins the tie")
}
