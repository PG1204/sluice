package storage

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/parquet-go/parquet-go"
)

// ParquetSource reads a Parquet file as a DataSource.
//
// Unlike CSV, Parquet stores its schema (and types) in the file footer, so
// there is no inference pass — we map the file's physical types to our type
// system directly. We read through a single reader spanning all row groups
// (parquet.MultiRowGroup) and hand out batchSize-row batches, keeping memory
// bounded the same way the CSV reader does.
//
// Scope: we support flat schemas of the primitive types the engine knows about
// (bool, int32/64, float/double, byte arrays as strings). Nested groups,
// repeated fields, and INT96 timestamps are rejected with a clear error rather
// than silently mishandled — they're outside the SQL subset.
type ParquetSource struct {
	file      *os.File
	pf        *parquet.File
	rows      parquet.Rows
	schema    Schema
	kinds     []parquet.Kind // physical kind per column, for value decoding
	batchSize int
	done      bool
}

// ParquetOption configures a ParquetSource.
type ParquetOption func(*ParquetSource)

// WithParquetBatchSize sets the number of rows emitted per batch.
func WithParquetBatchSize(n int) ParquetOption {
	return func(p *ParquetSource) {
		if n > 0 {
			p.batchSize = n
		}
	}
}

// OpenParquet opens a Parquet file and reads its schema. The returned source
// must be Closed.
func OpenParquet(path string, opts ...ParquetOption) (*ParquetSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open parquet %q: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat parquet %q: %w", path, err)
	}

	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("parse parquet %q: %w", path, err)
	}

	schema, kinds, err := parquetSchema(pf)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("parquet %q: %w", path, err)
	}

	src := &ParquetSource{
		file:      f,
		pf:        pf,
		rows:      parquet.MultiRowGroup(pf.RowGroups()...).Rows(),
		schema:    schema,
		kinds:     kinds,
		batchSize: DefaultBatchSize,
	}
	for _, opt := range opts {
		opt(src)
	}
	return src, nil
}

// parquetSchema maps a Parquet file's leaf fields to our Schema, returning the
// physical kind of each column alongside so Next can decode values.
func parquetSchema(pf *parquet.File) (Schema, []parquet.Kind, error) {
	fields := pf.Schema().Fields()
	out := make([]Field, len(fields))
	kinds := make([]parquet.Kind, len(fields))

	for i, f := range fields {
		if !f.Leaf() {
			return Schema{}, nil, fmt.Errorf("column %q: nested schemas are not supported", f.Name())
		}
		if f.Repeated() {
			return Schema{}, nil, fmt.Errorf("column %q: repeated fields are not supported", f.Name())
		}
		kind := f.Type().Kind()
		t, err := parquetKindToType(kind)
		if err != nil {
			return Schema{}, nil, fmt.Errorf("column %q: %w", f.Name(), err)
		}
		kinds[i] = kind
		out[i] = Field{Name: f.Name(), Type: t, Nullable: f.Optional()}
	}
	return Schema{Fields: out}, kinds, nil
}

// parquetKindToType maps a Parquet physical kind to our logical type.
func parquetKindToType(k parquet.Kind) (Type, error) {
	switch k {
	case parquet.Boolean:
		return TypeBool, nil
	case parquet.Int32, parquet.Int64:
		return TypeInt64, nil
	case parquet.Float, parquet.Double:
		return TypeFloat64, nil
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return TypeString, nil
	default:
		return TypeNull, fmt.Errorf("unsupported parquet type %s", k)
	}
}

// Schema implements DataSource.
func (p *ParquetSource) Schema() Schema { return p.schema }

// Next implements DataSource, reading up to batchSize rows per call.
func (p *ParquetSource) Next(ctx context.Context) (*Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.done {
		return nil, io.EOF
	}

	buf := make([]parquet.Row, p.batchSize)
	n, err := p.rows.ReadRows(buf)
	if err == io.EOF {
		p.done = true
	} else if err != nil {
		return nil, fmt.Errorf("read parquet rows: %w", err)
	}

	if n == 0 {
		return nil, io.EOF
	}
	return p.buildBatch(buf[:n])
}

// buildBatch decodes n Parquet rows into a columnar batch. For a flat schema
// each row holds one value per column in column order, so row[ci] is the value
// for column ci.
func (p *ParquetSource) buildBatch(rows []parquet.Row) (*Batch, error) {
	cols := make([]Column, len(p.schema.Fields))
	for ci, f := range p.schema.Fields {
		cols[ci] = buildParquetColumn(f, p.kinds[ci], ci, rows)
	}
	batch := &Batch{Schema: p.schema, Columns: cols}
	if err := batch.validate(); err != nil {
		return nil, err
	}
	return batch, nil
}

// buildParquetColumn decodes column ci out of the given rows into a typed
// column, using the physical kind to pick the right value accessor.
func buildParquetColumn(f Field, kind parquet.Kind, ci int, rows []parquet.Row) Column {
	switch f.Type {
	case TypeBool:
		b := NewColumnBuilder[bool](TypeBool)
		for _, row := range rows {
			v := row[ci]
			if v.IsNull() {
				b.AppendNull()
			} else {
				b.Append(v.Boolean())
			}
		}
		return b.Build()
	case TypeInt64:
		b := NewColumnBuilder[int64](TypeInt64)
		for _, row := range rows {
			v := row[ci]
			switch {
			case v.IsNull():
				b.AppendNull()
			case kind == parquet.Int32:
				b.Append(int64(v.Int32()))
			default:
				b.Append(v.Int64())
			}
		}
		return b.Build()
	case TypeFloat64:
		b := NewColumnBuilder[float64](TypeFloat64)
		for _, row := range rows {
			v := row[ci]
			switch {
			case v.IsNull():
				b.AppendNull()
			case kind == parquet.Float:
				b.Append(float64(v.Float()))
			default:
				b.Append(v.Double())
			}
		}
		return b.Build()
	default: // TypeString
		b := NewColumnBuilder[string](TypeString)
		for _, row := range rows {
			v := row[ci]
			if v.IsNull() {
				b.AppendNull()
			} else {
				b.Append(string(v.ByteArray()))
			}
		}
		return b.Build()
	}
}

// Close implements DataSource. Safe to call multiple times.
func (p *ParquetSource) Close() error {
	if p.file == nil {
		return nil
	}
	if p.rows != nil {
		p.rows.Close()
		p.rows = nil
	}
	err := p.file.Close()
	p.file = nil
	return err
}
