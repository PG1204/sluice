package storage

// This file defines the columnar in-memory representation. Data is stored one
// column at a time (a vector of values per column) rather than one row at a
// time. Columnar layout is cache-friendly for the scan/filter/aggregate
// patterns the executor runs — it touches one column's contiguous values
// instead of striding over whole rows — and it is the natural shape for the
// vectorized execution that's a later stretch goal.

// Column is one column's worth of values within a Batch. It is a small,
// non-generic interface so a Batch can hold a heterogeneous slice of columns;
// callers that need typed, allocation-free access type-assert to the concrete
// *Int64Column / *Float64Column / *BoolColumn / *StringColumn.
type Column interface {
	// Type reports the column's logical type.
	Type() Type
	// Len reports the number of values (rows) in the column.
	Len() int
	// IsNull reports whether the value at row i is NULL.
	IsNull(i int) bool
	// Value returns the value at row i boxed in an any, or nil if it is NULL.
	// Convenient for printing and tests; hot paths should use typed access.
	Value(i int) any
}

// TypedColumn is the generic backing type for every column. Values holds the
// data; valid is a parallel validity bitmap where valid[i]==false means the
// value at i is NULL. As a space optimization, valid may be nil, which means
// "no NULLs" — every value is present.
type TypedColumn[T any] struct {
	typ    Type
	values []T
	valid  []bool // nil => all values valid (non-NULL)
}

// Concrete column types are aliases of the generic, so callers get readable
// names (*Int64Column) while the implementation lives in one place.
type (
	// Int64Column holds INT64 values.
	Int64Column = TypedColumn[int64]
	// Float64Column holds FLOAT64 values.
	Float64Column = TypedColumn[float64]
	// BoolColumn holds BOOL values.
	BoolColumn = TypedColumn[bool]
	// StringColumn holds STRING values.
	StringColumn = TypedColumn[string]
)

// Type implements Column.
func (c *TypedColumn[T]) Type() Type { return c.typ }

// Len implements Column.
func (c *TypedColumn[T]) Len() int { return len(c.values) }

// IsNull implements Column.
func (c *TypedColumn[T]) IsNull(i int) bool {
	return c.valid != nil && !c.valid[i]
}

// Value implements Column, returning nil for NULLs.
func (c *TypedColumn[T]) Value(i int) any {
	if c.IsNull(i) {
		return nil
	}
	return c.values[i]
}

// At returns the typed value at row i and whether it is present (non-NULL).
// When ok is false the returned value is the type's zero value.
func (c *TypedColumn[T]) At(i int) (T, bool) {
	if c.IsNull(i) {
		var zero T
		return zero, false
	}
	return c.values[i], true
}

// Values exposes the underlying value slice for bulk processing. Entries at
// NULL positions hold the zero value; consult IsNull before using them.
func (c *TypedColumn[T]) Values() []T { return c.values }

// ColumnBuilder accumulates values (and NULLs) for one column and produces a
// TypedColumn. The CSV and Parquet readers use it so the null-tracking logic
// lives in exactly one place.
type ColumnBuilder[T any] struct {
	typ     Type
	values  []T
	valid   []bool
	hasNull bool
}

// NewColumnBuilder creates a builder for a column of the given type.
func NewColumnBuilder[T any](typ Type) *ColumnBuilder[T] {
	return &ColumnBuilder[T]{typ: typ}
}

// Append adds a present (non-NULL) value.
func (b *ColumnBuilder[T]) Append(v T) {
	b.values = append(b.values, v)
	b.valid = append(b.valid, true)
}

// AppendNull adds a NULL, recording the type's zero value as a placeholder.
func (b *ColumnBuilder[T]) AppendNull() {
	var zero T
	b.values = append(b.values, zero)
	b.valid = append(b.valid, false)
	b.hasNull = true
}

// Len reports how many values have been appended so far.
func (b *ColumnBuilder[T]) Len() int { return len(b.values) }

// Build finalizes the column. If no NULLs were appended, the validity bitmap
// is dropped to save space (nil means all-valid).
func (b *ColumnBuilder[T]) Build() *TypedColumn[T] {
	valid := b.valid
	if !b.hasNull {
		valid = nil
	}
	return &TypedColumn[T]{typ: b.typ, values: b.values, valid: valid}
}
