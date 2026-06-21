package physical

import (
	"context"
	"io"

	"github.com/PG1204/sluice/engine/storage"
)

// Limit emits at most Count rows from its child, then stops. It truncates the
// batch that crosses the boundary and returns io.EOF on subsequent calls.
type Limit struct {
	child   Operator
	limit   int64
	emitted int64
}

// NewLimit creates a Limit of count rows over child.
func NewLimit(child Operator, count int64) *Limit {
	return &Limit{child: child, limit: count}
}

// Schema implements Operator.
func (l *Limit) Schema() storage.Schema { return l.child.Schema() }

// Open implements Operator.
func (l *Limit) Open(ctx context.Context) error { return l.child.Open(ctx) }

// Next returns the child's batches until the row limit is reached, truncating
// the final batch as needed.
func (l *Limit) Next(ctx context.Context) (*storage.Batch, error) {
	if l.emitted >= l.limit {
		return nil, io.EOF
	}
	batch, err := l.child.Next(ctx)
	if err != nil {
		return nil, err
	}

	remaining := l.limit - l.emitted
	n := int64(batch.NumRows())
	if n <= remaining {
		l.emitted += n
		return batch, nil
	}

	// This batch crosses the limit: take only the first `remaining` rows.
	take := int(remaining)
	l.emitted = l.limit
	return buildBatch(batch.Schema, take, func(col, row int) Value {
		return columnValue(batch.Columns[col], row)
	}), nil
}

// Close implements Operator.
func (l *Limit) Close() error { return l.child.Close() }
