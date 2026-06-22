package physical

import (
	"context"

	"github.com/PG1204/sluice/engine/storage"
)

// TableOpener opens a named table as a streaming DataSource. *storage.Registry
// satisfies it; the executor depends only on this interface so it isn't tied to
// the filesystem registry.
type TableOpener interface {
	Open(ctx context.Context, name string) (storage.DataSource, error)
}

// SeqScan reads every row of a base table, one batch at a time. It is the leaf
// of the operator tree. The underlying DataSource is opened lazily in Open so a
// plan can be built without touching the filesystem.
//
// When projection pushdown has narrowed the scan, indices holds the positions
// of the wanted columns within each source batch and schema is the narrowed
// output; nil indices means "pass the full batch through".
type SeqScan struct {
	opener  TableOpener
	table   string
	schema  storage.Schema
	indices []int

	source storage.DataSource
}

// NewSeqScan creates a scan of the named table. schema is the scan's output
// schema and indices selects the output columns from each source batch (nil =
// all columns, in source order).
func NewSeqScan(opener TableOpener, table string, schema storage.Schema, indices []int) *SeqScan {
	return &SeqScan{opener: opener, table: table, schema: schema, indices: indices}
}

// Schema implements Operator.
func (s *SeqScan) Schema() storage.Schema { return s.schema }

// Open opens the underlying DataSource.
func (s *SeqScan) Open(ctx context.Context) error {
	src, err := s.opener.Open(ctx, s.table)
	if err != nil {
		return err
	}
	s.source = src
	return nil
}

// Next returns the next batch from the source, narrowed to the projected
// columns when a projection was pushed down.
func (s *SeqScan) Next(ctx context.Context) (*storage.Batch, error) {
	batch, err := s.source.Next(ctx)
	if err != nil {
		return nil, err
	}
	if s.indices == nil {
		return batch, nil
	}
	cols := make([]storage.Column, len(s.indices))
	for i, idx := range s.indices {
		cols[i] = batch.Columns[idx]
	}
	return &storage.Batch{Schema: s.schema, Columns: cols}, nil
}

// Close closes the underlying source.
func (s *SeqScan) Close() error {
	if s.source == nil {
		return nil
	}
	err := s.source.Close()
	s.source = nil
	return err
}
