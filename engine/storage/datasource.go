package storage

import (
	"context"
	"io"
)

// DataSource is anything that produces rows in columnar batches. It is the
// single abstraction the rest of the engine reads through, so CSV files,
// Parquet files, and (later) in-memory or cached sources are interchangeable.
//
// The interface is pull-based: the consumer repeatedly calls Next until it
// returns io.EOF. This is the Volcano model the physical operators in Phase 4
// build on, and it bounds memory to one batch at a time rather than
// materializing the whole table.
type DataSource interface {
	// Schema returns the column layout of the rows this source produces. It is
	// known before the first Next call.
	Schema() Schema

	// Next returns the next batch of rows. It returns io.EOF (with a nil batch)
	// once the source is exhausted. The returned batch is owned by the caller
	// until the next call to Next. Next must honor ctx cancellation.
	Next(ctx context.Context) (*Batch, error)

	// Close releases any underlying resources (open files, etc.). It is safe to
	// call more than once.
	Close() error
}

// ReadAll drains a DataSource into a single slice of batches. It is a
// convenience for tests and small inputs; production paths should stream via
// Next to keep memory bounded.
func ReadAll(ctx context.Context, src DataSource) ([]*Batch, error) {
	var batches []*Batch
	for {
		batch, err := src.Next(ctx)
		if err == io.EOF {
			return batches, nil
		}
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
}
