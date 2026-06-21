package storage

import "strconv"

// Type is the logical data type of a column. The set is intentionally small —
// just what the SQL subset needs — so the executor's type switches stay
// exhaustive and the cost model has few cases to reason about.
type Type int

const (
	// TypeNull is the type of a column whose values are all NULL (or unknown).
	// It widens to any other type when merged.
	TypeNull Type = iota
	// TypeInt64 is a 64-bit signed integer.
	TypeInt64
	// TypeFloat64 is a 64-bit IEEE-754 float.
	TypeFloat64
	// TypeBool is a boolean.
	TypeBool
	// TypeString is a UTF-8 string.
	TypeString
)

// String returns the type's name, used in schemas and EXPLAIN output.
func (t Type) String() string {
	switch t {
	case TypeNull:
		return "NULL"
	case TypeInt64:
		return "INT64"
	case TypeFloat64:
		return "FLOAT64"
	case TypeBool:
		return "BOOL"
	case TypeString:
		return "STRING"
	default:
		return "UNKNOWN"
	}
}

// inferCellType returns the narrowest type that can represent a single
// non-empty cell of text. The caller is responsible for treating empty cells
// as NULL before calling this.
//
// The order matters: an integer also parses as a float, so we try the narrower
// integer first and only widen when necessary.
func inferCellType(s string) Type {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return TypeInt64
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return TypeFloat64
	}
	if isBoolText(s) {
		return TypeBool
	}
	return TypeString
}

// mergeTypes combines two per-cell type observations into the narrowest type
// that can hold both, forming a small widening lattice:
//
//	NULL widens to anything; INT64 widens to FLOAT64; any incompatible mix
//	(e.g. BOOL with a number, or a number with arbitrary text) falls back to
//	STRING, since every value has a faithful text form.
func mergeTypes(a, b Type) Type {
	if a == b {
		return a
	}
	if a == TypeNull {
		return b
	}
	if b == TypeNull {
		return a
	}
	if isNumeric(a) && isNumeric(b) {
		return TypeFloat64 // INT64 + FLOAT64
	}
	return TypeString
}

func isNumeric(t Type) bool { return t == TypeInt64 || t == TypeFloat64 }

// isBoolText reports whether s is a recognized boolean literal. We accept only
// true/false (case-insensitive); "0"/"1" are deliberately left as integers so
// numeric columns aren't misread as booleans.
func isBoolText(s string) bool {
	switch s {
	case "true", "TRUE", "True", "false", "FALSE", "False":
		return true
	default:
		return false
	}
}
