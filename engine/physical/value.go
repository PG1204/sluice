// Package physical executes a logical plan. It implements the Volcano iterator
// model: every operator exposes Open/Next/Close and pulls columnar batches from
// its children, so memory stays bounded to a batch per operator rather than
// materializing whole intermediate tables (except where an operator must
// buffer, like Sort and the build side of HashJoin).
//
// Execution is row-at-a-time within a batch — the naive model from the build
// plan. Vectorized evaluation is a later stretch goal; the columnar batch
// layout is already in place for it.
package physical

import (
	"strings"

	"github.com/PG1204/sluice/engine/storage"
)

// Value is a single evaluated scalar during execution. It is a tagged union
// over the engine's types; Kind == storage.TypeNull represents SQL NULL. Using
// a small struct (rather than `any`) keeps evaluation allocation-free and makes
// the type handling in comparisons and arithmetic explicit.
type Value struct {
	Kind storage.Type
	I    int64
	F    float64
	B    bool
	S    string
}

// Null is the NULL value.
func Null() Value { return Value{Kind: storage.TypeNull} }

// Int, Float, Bool, Str construct non-null values.
func Int(v int64) Value     { return Value{Kind: storage.TypeInt64, I: v} }
func Float(v float64) Value { return Value{Kind: storage.TypeFloat64, F: v} }
func Bool(v bool) Value     { return Value{Kind: storage.TypeBool, B: v} }
func Str(v string) Value    { return Value{Kind: storage.TypeString, S: v} }

// IsNull reports whether the value is SQL NULL.
func (v Value) IsNull() bool { return v.Kind == storage.TypeNull }

// asFloat returns the value as a float64, promoting integers. Only valid for
// numeric (or already-checked) values.
func (v Value) asFloat() float64 {
	if v.Kind == storage.TypeInt64 {
		return float64(v.I)
	}
	return v.F
}

// isNumeric reports whether a type participates in arithmetic/numeric compares.
func isNumeric(t storage.Type) bool {
	return t == storage.TypeInt64 || t == storage.TypeFloat64
}

// columnValue reads the value at row from a column into a Value.
func columnValue(col storage.Column, row int) Value {
	if col.IsNull(row) {
		return Null()
	}
	switch col.Type() {
	case storage.TypeInt64:
		return Int(col.Value(row).(int64))
	case storage.TypeFloat64:
		return Float(col.Value(row).(float64))
	case storage.TypeBool:
		return Bool(col.Value(row).(bool))
	case storage.TypeString:
		return Str(col.Value(row).(string))
	default:
		return Null()
	}
}

// compareValues orders two non-null values of compatible types: negative if
// a < b, zero if equal, positive if a > b. Numeric values compare by magnitude
// across int/float; strings lexicographically; booleans false < true.
func compareValues(a, b Value) int {
	if isNumeric(a.Kind) && isNumeric(b.Kind) {
		if a.Kind == storage.TypeInt64 && b.Kind == storage.TypeInt64 {
			switch {
			case a.I < b.I:
				return -1
			case a.I > b.I:
				return 1
			default:
				return 0
			}
		}
		af, bf := a.asFloat(), b.asFloat()
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	switch a.Kind {
	case storage.TypeString:
		return strings.Compare(a.S, b.S)
	case storage.TypeBool:
		switch {
		case a.B == b.B:
			return 0
		case !a.B:
			return -1
		default:
			return 1
		}
	default:
		return 0
	}
}

// buildBatch materializes a batch of n rows for the given schema, calling fill
// for each (column, row) to obtain the value. It is the single place that maps
// runtime Values back into typed, nullable columns, so operators that produce
// output (filter, project, join, aggregate, sort) share one implementation.
func buildBatch(schema storage.Schema, n int, fill func(col, row int) Value) *storage.Batch {
	cols := make([]storage.Column, len(schema.Fields))
	for ci, f := range schema.Fields {
		cols[ci] = buildColumn(f.Type, n, func(row int) Value { return fill(ci, row) })
	}
	return &storage.Batch{Schema: schema, Columns: cols}
}

// buildColumn builds one typed column of n rows from a per-row value function.
func buildColumn(t storage.Type, n int, value func(row int) Value) storage.Column {
	switch t {
	case storage.TypeInt64:
		b := storage.NewColumnBuilder[int64](storage.TypeInt64)
		for r := 0; r < n; r++ {
			if v := value(r); v.IsNull() {
				b.AppendNull()
			} else {
				b.Append(v.I)
			}
		}
		return b.Build()
	case storage.TypeFloat64:
		b := storage.NewColumnBuilder[float64](storage.TypeFloat64)
		for r := 0; r < n; r++ {
			if v := value(r); v.IsNull() {
				b.AppendNull()
			} else {
				b.Append(v.asFloat())
			}
		}
		return b.Build()
	case storage.TypeBool:
		b := storage.NewColumnBuilder[bool](storage.TypeBool)
		for r := 0; r < n; r++ {
			if v := value(r); v.IsNull() {
				b.AppendNull()
			} else {
				b.Append(v.B)
			}
		}
		return b.Build()
	default: // TypeString and TypeNull (the latter yields an all-NULL string column)
		b := storage.NewColumnBuilder[string](storage.TypeString)
		for r := 0; r < n; r++ {
			if v := value(r); v.IsNull() {
				b.AppendNull()
			} else {
				b.Append(v.S)
			}
		}
		return b.Build()
	}
}
