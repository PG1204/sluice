package storage

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
)

// DefaultBatchSize is the number of rows a reader emits per batch unless
// overridden. 1024 is a common sweet spot: big enough to amortize per-batch
// overhead, small enough to stay cache-resident.
const DefaultBatchSize = 1024

// CSVSource reads a CSV file as a DataSource.
//
// CSV carries no schema, so OpenCSV makes one inference pass over the whole
// file to determine column types, then Next makes a second, streaming pass
// that parses rows into batches. Two passes (rather than inferring from a
// sample) means we never misclassify a column because a late row disagreed
// with the first few — correctness over a marginal open-time saving. Streaming
// the second pass keeps memory bounded to one batch regardless of file size.
type CSVSource struct {
	path      string
	schema    Schema
	batchSize int

	// Streaming state, initialized lazily on the first Next call.
	file   *os.File
	reader *csv.Reader
	opened bool
	done   bool
}

// CSVOption configures a CSVSource.
type CSVOption func(*CSVSource)

// WithBatchSize sets the number of rows emitted per batch.
func WithBatchSize(n int) CSVOption {
	return func(c *CSVSource) {
		if n > 0 {
			c.batchSize = n
		}
	}
}

// OpenCSV opens a CSV file and infers its schema. The first line is treated as
// a header of column names. The returned source must be Closed.
func OpenCSV(path string, opts ...CSVOption) (*CSVSource, error) {
	src := &CSVSource{path: path, batchSize: DefaultBatchSize}
	for _, opt := range opts {
		opt(src)
	}

	schema, err := inferCSVSchema(path)
	if err != nil {
		return nil, err
	}
	src.schema = schema
	return src, nil
}

// inferCSVSchema reads the entire file once to determine column names (from the
// header) and the narrowest type that fits every value in each column.
func inferCSVSchema(path string) (Schema, error) {
	f, err := os.Open(path)
	if err != nil {
		return Schema{}, fmt.Errorf("open csv %q: %w", path, err)
	}
	defer f.Close()

	r := newCSVReader(f)
	header, err := r.Read()
	if err == io.EOF {
		return Schema{}, fmt.Errorf("csv %q is empty", path)
	}
	if err != nil {
		return Schema{}, fmt.Errorf("read csv header %q: %w", path, err)
	}

	types := make([]Type, len(header))    // accumulated per-column type (starts NULL)
	nullable := make([]bool, len(header)) // any empty cell seen?

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Schema{}, fmt.Errorf("read csv %q: %w", path, err)
		}
		for i, cell := range rec {
			if cell == "" {
				nullable[i] = true
				continue
			}
			types[i] = mergeTypes(types[i], inferCellType(cell))
		}
	}

	fields := make([]Field, len(header))
	for i, name := range header {
		t := types[i]
		if t == TypeNull {
			// A column with no non-empty values: default to STRING so it is
			// still queryable, and mark it nullable.
			t = TypeString
			nullable[i] = true
		}
		fields[i] = Field{Name: name, Type: t, Nullable: nullable[i]}
	}
	return Schema{Fields: fields}, nil
}

// Schema implements DataSource.
func (c *CSVSource) Schema() Schema { return c.schema }

// Next implements DataSource, streaming the file in batchSize-row chunks.
func (c *CSVSource) Next(ctx context.Context) (*Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.done {
		return nil, io.EOF
	}
	if !c.opened {
		if err := c.openForStreaming(); err != nil {
			return nil, err
		}
	}

	records := make([][]string, 0, c.batchSize)
	for len(records) < c.batchSize {
		rec, err := c.reader.Read()
		if err == io.EOF {
			c.done = true
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv %q: %w", c.path, err)
		}
		records = append(records, rec)
	}

	if len(records) == 0 {
		return nil, io.EOF
	}
	return buildCSVBatch(c.schema, records)
}

// openForStreaming opens the file for the second pass and skips the header.
func (c *CSVSource) openForStreaming() error {
	f, err := os.Open(c.path)
	if err != nil {
		return fmt.Errorf("open csv %q: %w", c.path, err)
	}
	c.file = f
	c.reader = newCSVReader(f)
	if _, err := c.reader.Read(); err != nil { // discard header
		c.file.Close()
		return fmt.Errorf("read csv header %q: %w", c.path, err)
	}
	c.opened = true
	return nil
}

// Close implements DataSource. It is safe to call multiple times.
func (c *CSVSource) Close() error {
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

// newCSVReader builds a csv.Reader with the conventions we want: a leading
// space after a delimiter is insignificant, and every row must have the same
// field count as the header (ragged rows are an error, not silent NULLs).
func newCSVReader(r io.Reader) *csv.Reader {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = 0 // 0 => set from the first record (the header)
	return cr
}

// buildCSVBatch parses a slice of raw records into a columnar batch according
// to the inferred schema. An empty cell becomes NULL; a cell that fails to
// parse as its column's type is an error (inference guarantees this can't
// happen for well-formed input, so it signals a malformed file).
func buildCSVBatch(schema Schema, records [][]string) (*Batch, error) {
	cols := make([]Column, len(schema.Fields))
	for ci, f := range schema.Fields {
		col, err := buildCSVColumn(f, ci, records)
		if err != nil {
			return nil, err
		}
		cols[ci] = col
	}
	batch := &Batch{Schema: schema, Columns: cols}
	if err := batch.validate(); err != nil {
		return nil, err
	}
	return batch, nil
}

// buildCSVColumn builds one typed column from column index ci of the records.
func buildCSVColumn(f Field, ci int, records [][]string) (Column, error) {
	switch f.Type {
	case TypeInt64:
		b := NewColumnBuilder[int64](TypeInt64)
		for _, rec := range records {
			cell := rec[ci]
			if cell == "" {
				b.AppendNull()
				continue
			}
			v, err := strconv.ParseInt(cell, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("column %q: %q is not an integer: %w", f.Name, cell, err)
			}
			b.Append(v)
		}
		return b.Build(), nil
	case TypeFloat64:
		b := NewColumnBuilder[float64](TypeFloat64)
		for _, rec := range records {
			cell := rec[ci]
			if cell == "" {
				b.AppendNull()
				continue
			}
			v, err := strconv.ParseFloat(cell, 64)
			if err != nil {
				return nil, fmt.Errorf("column %q: %q is not a float: %w", f.Name, cell, err)
			}
			b.Append(v)
		}
		return b.Build(), nil
	case TypeBool:
		b := NewColumnBuilder[bool](TypeBool)
		for _, rec := range records {
			cell := rec[ci]
			if cell == "" {
				b.AppendNull()
				continue
			}
			if !isBoolText(cell) {
				return nil, fmt.Errorf("column %q: %q is not a boolean", f.Name, cell)
			}
			b.Append(cell == "true" || cell == "TRUE" || cell == "True")
		}
		return b.Build(), nil
	default: // TypeString
		b := NewColumnBuilder[string](TypeString)
		for _, rec := range records {
			cell := rec[ci]
			if cell == "" {
				b.AppendNull()
				continue
			}
			b.Append(cell)
		}
		return b.Build(), nil
	}
}
