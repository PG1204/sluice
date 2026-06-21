package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestColumnBuilder_NoNulls_DropsValidityBitmap(t *testing.T) {
	b := NewColumnBuilder[int64](TypeInt64)
	b.Append(10)
	b.Append(20)
	col := b.Build()

	assert.Equal(t, TypeInt64, col.Type())
	assert.Equal(t, 2, col.Len())
	assert.Nil(t, col.valid, "all-valid column should drop its validity bitmap")
	assert.Equal(t, []int64{10, 20}, col.Values())

	for i := 0; i < col.Len(); i++ {
		assert.False(t, col.IsNull(i))
	}
	v, ok := col.At(1)
	assert.True(t, ok)
	assert.Equal(t, int64(20), v)
}

func TestColumnBuilder_WithNulls(t *testing.T) {
	b := NewColumnBuilder[string](TypeString)
	b.Append("a")
	b.AppendNull()
	b.Append("c")
	col := b.Build()

	require.Equal(t, 3, col.Len())
	assert.False(t, col.IsNull(0))
	assert.True(t, col.IsNull(1))
	assert.False(t, col.IsNull(2))

	assert.Equal(t, "a", col.Value(0))
	assert.Nil(t, col.Value(1), "NULL must box to nil")
	assert.Equal(t, "c", col.Value(2))

	_, ok := col.At(1)
	assert.False(t, ok, "At reports NULL via ok=false")
}

func TestColumn_SatisfiesInterface(t *testing.T) {
	// Each concrete alias must satisfy the Column interface.
	var _ Column = NewColumnBuilder[int64](TypeInt64).Build()
	var _ Column = NewColumnBuilder[float64](TypeFloat64).Build()
	var _ Column = NewColumnBuilder[bool](TypeBool).Build()
	var _ Column = NewColumnBuilder[string](TypeString).Build()
}
