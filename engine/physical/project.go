package physical

import (
	"context"

	"github.com/PG1204/sluice/engine/storage"
)

// Projection computes the SELECT list: each output column is a compiled
// expression evaluated over the child's rows. With Distinct set, it drops
// duplicate output rows, tracking seen rows across all batches.
type Projection struct {
	child    Operator
	exprs    []evalExpr
	schema   storage.Schema
	distinct bool

	seen map[string]bool // populated only when distinct
}

// NewProjection creates a projection. len(exprs) must equal len(schema.Fields).
func NewProjection(child Operator, exprs []evalExpr, schema storage.Schema, distinct bool) *Projection {
	p := &Projection{child: child, exprs: exprs, schema: schema, distinct: distinct}
	if distinct {
		p.seen = make(map[string]bool)
	}
	return p
}

// Schema implements Operator.
func (p *Projection) Schema() storage.Schema { return p.schema }

// Open implements Operator.
func (p *Projection) Open(ctx context.Context) error { return p.child.Open(ctx) }

// Next projects the child's next batch. For DISTINCT it filters to rows whose
// projected tuple hasn't been seen; if that leaves nothing, it pulls again.
func (p *Projection) Next(ctx context.Context) (*storage.Batch, error) {
	for {
		batch, err := p.child.Next(ctx)
		if err != nil {
			return nil, err
		}

		if !p.distinct {
			return p.projectRows(batch, identityRows(batch.NumRows())), nil
		}

		kept := make([]int, 0, batch.NumRows())
		for row := 0; row < batch.NumRows(); row++ {
			key := tupleKey(p.evalRow(batch, row))
			if p.seen[key] {
				continue
			}
			p.seen[key] = true
			kept = append(kept, row)
		}
		if len(kept) == 0 {
			continue
		}
		return p.projectRows(batch, kept), nil
	}
}

// Close implements Operator.
func (p *Projection) Close() error { return p.child.Close() }

// projectRows builds the output batch by evaluating each projection expression
// over the given child rows.
func (p *Projection) projectRows(batch *storage.Batch, rows []int) *storage.Batch {
	return buildBatch(p.schema, len(rows), func(col, i int) Value {
		return p.exprs[col].eval(batch, rows[i])
	})
}

func (p *Projection) evalRow(batch *storage.Batch, row int) []Value {
	vals := make([]Value, len(p.exprs))
	for i, e := range p.exprs {
		vals[i] = e.eval(batch, row)
	}
	return vals
}

// identityRows returns [0, 1, ..., n-1].
func identityRows(n int) []int {
	rows := make([]int, n)
	for i := range rows {
		rows[i] = i
	}
	return rows
}
