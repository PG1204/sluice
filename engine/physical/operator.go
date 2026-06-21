package physical

import (
	"context"
	"io"

	"github.com/PG1204/sluice/engine/storage"
)

// Operator is a node in the physical plan, following the Volcano iterator
// model. The lifecycle is Open once, Next until io.EOF, then Close. Next
// returns a batch of rows; an empty batch is allowed (callers skip it) and
// io.EOF (with a nil batch) signals exhaustion.
type Operator interface {
	// Schema is the layout of rows this operator produces, known after Open.
	Schema() storage.Schema
	// Open prepares the operator and its inputs (e.g. building hash tables).
	Open(ctx context.Context) error
	// Next returns the next batch, or io.EOF when there are no more rows.
	Next(ctx context.Context) (*storage.Batch, error)
	// Close releases resources held by this operator and its inputs.
	Close() error
}

// Run drives an operator to completion, invoking fn for each non-empty batch.
// It handles Open/Close and stops at io.EOF.
func Run(ctx context.Context, op Operator, fn func(*storage.Batch) error) error {
	if err := op.Open(ctx); err != nil {
		return err
	}
	defer op.Close()
	for {
		batch, err := op.Next(ctx)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if batch.NumRows() == 0 {
			continue
		}
		if err := fn(batch); err != nil {
			return err
		}
	}
}

// drainOpen reads every remaining batch from an already-opened operator. It is
// used by buffering operators (Sort, the build side of HashJoin) that must see
// all input before producing output.
func drainOpen(ctx context.Context, op Operator) ([]*storage.Batch, error) {
	var batches []*storage.Batch
	for {
		batch, err := op.Next(ctx)
		if err == io.EOF {
			return batches, nil
		}
		if err != nil {
			return nil, err
		}
		if batch.NumRows() > 0 {
			batches = append(batches, batch)
		}
	}
}
