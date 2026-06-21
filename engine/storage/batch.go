package storage

import "fmt"

// Field describes one column in a schema: its name, type, and whether it may
// contain NULLs.
type Field struct {
	Name     string
	Type     Type
	Nullable bool
}

// Schema is the ordered list of fields a DataSource produces. It is the
// contract between storage and the rest of the engine.
type Schema struct {
	Fields []Field
}

// Names returns the field names in order.
func (s Schema) Names() []string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
	}
	return names
}

// ColumnIndex returns the position of the named column and whether it exists.
// Lookup is case-insensitive to match SQL identifier semantics.
func (s Schema) ColumnIndex(name string) (int, bool) {
	for i, f := range s.Fields {
		if equalFoldASCII(f.Name, name) {
			return i, true
		}
	}
	return -1, false
}

// String renders the schema compactly, e.g. "(id INT64, name STRING)".
func (s Schema) String() string {
	out := "("
	for i, f := range s.Fields {
		if i > 0 {
			out += ", "
		}
		out += f.Name + " " + f.Type.String()
		if f.Nullable {
			out += " NULL"
		}
	}
	return out + ")"
}

// Batch is a chunk of rows in columnar form: one Column per schema field, all
// of equal length. The executor pulls batches from a DataSource, processes a
// whole batch at a time, and passes batches up the operator tree.
type Batch struct {
	Schema  Schema
	Columns []Column
}

// NumRows returns the number of rows in the batch (the length of any column).
func (b *Batch) NumRows() int {
	if len(b.Columns) == 0 {
		return 0
	}
	return b.Columns[0].Len()
}

// NumCols returns the number of columns in the batch.
func (b *Batch) NumCols() int { return len(b.Columns) }

// validate checks the batch is internally consistent: a column per field, and
// all columns the same length. It guards against reader bugs producing ragged
// batches that would corrupt downstream operators.
func (b *Batch) validate() error {
	if len(b.Columns) != len(b.Schema.Fields) {
		return fmt.Errorf("batch has %d columns but schema has %d fields", len(b.Columns), len(b.Schema.Fields))
	}
	for i := 1; i < len(b.Columns); i++ {
		if b.Columns[i].Len() != b.Columns[0].Len() {
			return fmt.Errorf("column %d has length %d, expected %d", i, b.Columns[i].Len(), b.Columns[0].Len())
		}
	}
	return nil
}

// equalFoldASCII reports whether two ASCII strings are equal ignoring case.
// Used for SQL-style case-insensitive column matching without the overhead of
// full Unicode folding (identifiers are ASCII).
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
