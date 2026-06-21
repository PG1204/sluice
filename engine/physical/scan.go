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
type SeqScan struct {
	opener TableOpener
	table  string
	schema storage.Schema

	source storage.DataSource
}

// NewSeqScan creates a scan of the named table. schema is the table's schema
// (already known from the catalog), used so Schema works before Open.
func NewSeqScan(opener TableOpener, table string, schema storage.Schema) *SeqScan {
	return &SeqScan{opener: opener, table: table, schema: schema}
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

// Next returns the next batch from the source.
func (s *SeqScan) Next(ctx context.Context) (*storage.Batch, error) {
	return s.source.Next(ctx)
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
