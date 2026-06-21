package physical

import (
	"context"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/storage"
)

// HashJoin implements an equi-join by building a hash table on the right
// (build) input and probing it with the left (probe) input. Build the smaller
// side for efficiency — choosing which side is "smaller" is the optimizer's job
// in Phase 5; here the translator puts the right input on the build side.
//
// Only equi-joins are supported (the ON condition reduces to equalities of the
// form leftExpr = rightExpr, optionally ANDed); the translator rejects anything
// else. INNER and LEFT joins are implemented; RIGHT/FULL are not yet.
type HashJoin struct {
	left, right Operator
	leftKeys    []evalExpr // join-key expressions over the left (probe) row
	rightKeys   []evalExpr // join-key expressions over the right (build) row
	joinType    ast.JoinType
	schema      storage.Schema
	numLeft     int // number of columns from the left input

	rightBatches []*storage.Batch
	rightIndex   map[string][]rowRef
}

// NewHashJoin builds a HashJoin. schema must be the left columns followed by the
// right columns; leftKeys and rightKeys must be the same length and aligned.
func NewHashJoin(left, right Operator, leftKeys, rightKeys []evalExpr, joinType ast.JoinType, schema storage.Schema, numLeft int) *HashJoin {
	return &HashJoin{
		left:      left,
		right:     right,
		leftKeys:  leftKeys,
		rightKeys: rightKeys,
		joinType:  joinType,
		schema:    schema,
		numLeft:   numLeft,
	}
}

// Schema implements Operator.
func (j *HashJoin) Schema() storage.Schema { return j.schema }

// Open opens both inputs and builds the hash table from the right input.
func (j *HashJoin) Open(ctx context.Context) error {
	if err := j.left.Open(ctx); err != nil {
		return err
	}
	if err := j.right.Open(ctx); err != nil {
		return err
	}

	batches, err := drainOpen(ctx, j.right)
	if err != nil {
		return err
	}
	j.rightBatches = batches
	j.rightIndex = make(map[string][]rowRef)

	for bi, b := range batches {
		for row := 0; row < b.NumRows(); row++ {
			key, ok := joinKey(evalAll(j.rightKeys, b, row))
			if !ok {
				continue // a NULL key can't match anything
			}
			j.rightIndex[key] = append(j.rightIndex[key], rowRef{batch: bi, row: row})
		}
	}
	return nil
}

// joinPair records one output row: the left row index within the current left
// batch, and either a matching right row or a NULL right side (LEFT join miss).
type joinPair struct {
	leftRow int
	right   rowRef
	rNull   bool
}

// Next probes the hash table with the left input's next batch and returns the
// resulting joined rows. Left batches that produce no output are skipped.
func (j *HashJoin) Next(ctx context.Context) (*storage.Batch, error) {
	for {
		lb, err := j.left.Next(ctx)
		if err != nil {
			return nil, err
		}

		var pairs []joinPair
		for lrow := 0; lrow < lb.NumRows(); lrow++ {
			key, ok := joinKey(evalAll(j.leftKeys, lb, lrow))
			var matches []rowRef
			if ok {
				matches = j.rightIndex[key]
			}
			if len(matches) == 0 {
				if j.joinType == ast.LeftJoin {
					pairs = append(pairs, joinPair{leftRow: lrow, rNull: true})
				}
				continue
			}
			for _, m := range matches {
				pairs = append(pairs, joinPair{leftRow: lrow, right: m})
			}
		}

		if len(pairs) == 0 {
			continue
		}
		return j.buildJoined(lb, pairs), nil
	}
}

// buildJoined materializes the joined output batch from the current left batch
// and the matched pairs.
func (j *HashJoin) buildJoined(lb *storage.Batch, pairs []joinPair) *storage.Batch {
	return buildBatch(j.schema, len(pairs), func(col, i int) Value {
		p := pairs[i]
		if col < j.numLeft {
			return columnValue(lb.Columns[col], p.leftRow)
		}
		if p.rNull {
			return Null()
		}
		rcol := col - j.numLeft
		rb := j.rightBatches[p.right.batch]
		return columnValue(rb.Columns[rcol], p.right.row)
	})
}

// Close closes both inputs.
func (j *HashJoin) Close() error {
	err := j.left.Close()
	if cerr := j.right.Close(); err == nil {
		err = cerr
	}
	return err
}

// evalAll evaluates a list of expressions against one row.
func evalAll(exprs []evalExpr, b *storage.Batch, row int) []Value {
	vals := make([]Value, len(exprs))
	for i, e := range exprs {
		vals[i] = e.eval(b, row)
	}
	return vals
}
