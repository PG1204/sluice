package physical

import (
	"context"
	"io"
	"sort"

	"github.com/PG1204/sluice/engine/storage"
)

// sortKey is one compiled ORDER BY term.
type sortKey struct {
	expr evalExpr
	desc bool
}

// Sort orders all of its child's rows by the sort keys. It is a pipeline
// breaker: Open buffers every input batch (an in-memory sort, per the build
// plan) and sorts row references into them; Next then streams the sorted rows
// back out in batches without copying the underlying data until emission.
type Sort struct {
	child Operator
	keys  []sortKey

	batches []*storage.Batch
	order   []rowRef
	pos     int
}

// rowRef points at a row within one of the buffered batches.
type rowRef struct {
	batch int
	row   int
}

// NewSort creates a Sort over child with the given compiled keys.
func NewSort(child Operator, keys []sortKey) *Sort {
	return &Sort{child: child, keys: keys}
}

// Schema implements Operator.
func (s *Sort) Schema() storage.Schema { return s.child.Schema() }

// Open buffers and sorts the child's rows.
func (s *Sort) Open(ctx context.Context) error {
	if err := s.child.Open(ctx); err != nil {
		return err
	}
	batches, err := drainOpen(ctx, s.child)
	if err != nil {
		return err
	}
	s.batches = batches

	for bi, b := range batches {
		for r := 0; r < b.NumRows(); r++ {
			s.order = append(s.order, rowRef{batch: bi, row: r})
		}
	}

	sort.SliceStable(s.order, func(i, j int) bool {
		return s.less(s.order[i], s.order[j])
	})
	return nil
}

// less reports whether row a should sort before row b under the keys. NULLs
// sort last in ascending order (and therefore first under DESC).
func (s *Sort) less(a, b rowRef) bool {
	for _, k := range s.keys {
		av := k.expr.eval(s.batches[a.batch], a.row)
		bv := k.expr.eval(s.batches[b.batch], b.row)
		c := compareNullable(av, bv)
		if k.desc {
			c = -c
		}
		if c != 0 {
			return c < 0
		}
	}
	return false
}

// compareNullable orders two values with NULLs treated as greater than any
// non-NULL value (so they sort last under ascending order).
func compareNullable(a, b Value) int {
	switch {
	case a.IsNull() && b.IsNull():
		return 0
	case a.IsNull():
		return 1
	case b.IsNull():
		return -1
	default:
		return compareValues(a, b)
	}
}

// Next emits the sorted rows in batches of DefaultBatchSize.
func (s *Sort) Next(_ context.Context) (*storage.Batch, error) {
	if s.pos >= len(s.order) {
		return nil, io.EOF
	}
	end := s.pos + storage.DefaultBatchSize
	if end > len(s.order) {
		end = len(s.order)
	}
	refs := s.order[s.pos:end]
	s.pos = end

	return buildBatch(s.child.Schema(), len(refs), func(col, i int) Value {
		ref := refs[i]
		return columnValue(s.batches[ref.batch].Columns[col], ref.row)
	}), nil
}

// Close implements Operator.
func (s *Sort) Close() error { return s.child.Close() }
