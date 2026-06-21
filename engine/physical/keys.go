package physical

import (
	"strconv"
	"strings"

	"github.com/PG1204/sluice/engine/storage"
)

// This file builds string keys from tuples of values, used for grouping
// (HashAggregate), DISTINCT (Projection), and equi-join probing (HashJoin).
//
// Each value is tagged by type so values of different types never collide (the
// int 5 and the string "5" produce different keys), and strings are
// length-prefixed so the field separator can't be forged by the data. Two key
// builders differ only in how they treat NULL:
//
//   - tupleKey treats NULL as a distinct, groupable value (NULLs group
//     together, and DISTINCT keeps one). This matches GROUP BY / DISTINCT.
//   - joinKey reports ok=false if any value is NULL, since NULL never satisfies
//     an equi-join predicate.

const fieldSep = 0x1f // ASCII unit separator

func encodeValue(b *strings.Builder, v Value) {
	switch v.Kind {
	case storage.TypeInt64:
		b.WriteByte('i')
		b.WriteString(strconv.FormatInt(v.I, 10))
	case storage.TypeFloat64:
		b.WriteByte('f')
		b.WriteString(strconv.FormatFloat(v.F, 'g', -1, 64))
	case storage.TypeBool:
		if v.B {
			b.WriteString("b1")
		} else {
			b.WriteString("b0")
		}
	case storage.TypeString:
		b.WriteByte('s')
		b.WriteString(strconv.Itoa(len(v.S)))
		b.WriteByte(':')
		b.WriteString(v.S)
	default: // NULL
		b.WriteByte('n')
	}
}

// tupleKey builds a key in which NULL is a value (NULLs are equal to each
// other). Used by GROUP BY and DISTINCT.
func tupleKey(vals []Value) string {
	var b strings.Builder
	for _, v := range vals {
		encodeValue(&b, v)
		b.WriteByte(fieldSep)
	}
	return b.String()
}

// joinKey builds a key for equi-join matching; ok is false if any value is
// NULL, so NULL keys never match.
func joinKey(vals []Value) (key string, ok bool) {
	for _, v := range vals {
		if v.IsNull() {
			return "", false
		}
	}
	return tupleKey(vals), true
}
