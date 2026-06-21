package physical

import (
	"context"

	"github.com/PG1204/sluice/engine/storage"
)

// Filter passes through only the rows of its child for which the predicate
// evaluates to TRUE (NULL and FALSE are dropped). It preserves the child's
// schema and column layout.
type Filter struct {
	child     Operator
	predicate evalExpr
}

// NewFilter creates a Filter over child using the compiled predicate.
func NewFilter(child Operator, predicate evalExpr) *Filter {
	return &Filter{child: child, predicate: predicate}
}

// Schema implements Operator.
func (f *Filter) Schema() storage.Schema { return f.child.Schema() }

// Open implements Operator.
func (f *Filter) Open(ctx context.Context) error { return f.child.Open(ctx) }

// Next pulls batches from the child until it finds one with surviving rows,
// then returns just those rows. Fully-filtered batches are skipped rather than
// returned empty, so consumers don't spin on empties.
func (f *Filter) Next(ctx context.Context) (*storage.Batch, error) {
	for {
		batch, err := f.child.Next(ctx)
		if err != nil {
			return nil, err
		}

		kept := make([]int, 0, batch.NumRows())
		for row := 0; row < batch.NumRows(); row++ {
			if isTrue(f.predicate.eval(batch, row)) {
				kept = append(kept, row)
			}
		}
		if len(kept) == 0 {
			continue
		}
		return buildBatch(batch.Schema, len(kept), func(col, i int) Value {
			return columnValue(batch.Columns[col], kept[i])
		}), nil
	}
}

// Close implements Operator.
func (f *Filter) Close() error { return f.child.Close() }
