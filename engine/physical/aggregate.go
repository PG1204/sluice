package physical

import (
	"context"
	"io"

	"github.com/PG1204/sluice/engine/storage"
)

// aggKind identifies which aggregate an accumulator computes.
type aggKind int

const (
	aggCountStar aggKind = iota // COUNT(*)
	aggCount                    // COUNT(expr): non-NULL count
	aggSum
	aggAvg
	aggMin
	aggMax
)

// aggSpec is a compiled aggregate: its kind, its (compiled) argument expression
// — nil for COUNT(*) — and the result type the output column expects.
type aggSpec struct {
	kind       aggKind
	arg        evalExpr
	resultType storage.Type
}

// HashAggregate groups rows by the group-by expressions and computes the
// aggregates per group. It is a pipeline breaker: Open consumes all input,
// hashing each row into its group and updating that group's accumulators; Next
// then emits one row per group. Output columns are the group keys followed by
// the aggregate results, matching the logical Aggregate's schema.
//
// With no group-by expressions the whole input is one group, and an empty input
// still yields one row (COUNT(*) = 0), as SQL requires.
type HashAggregate struct {
	child     Operator
	groupBy   []evalExpr
	specs     []aggSpec
	schema    storage.Schema
	numGroups int // count of group-by columns, = leading output columns

	groups []*groupState
	pos    int
}

// groupState holds one group's key values and live accumulators.
type groupState struct {
	keys []Value
	accs []accumulator
}

// NewHashAggregate builds a HashAggregate. schema must be the group-by columns
// followed by the aggregate columns.
func NewHashAggregate(child Operator, groupBy []evalExpr, specs []aggSpec, schema storage.Schema) *HashAggregate {
	return &HashAggregate{
		child:     child,
		groupBy:   groupBy,
		specs:     specs,
		schema:    schema,
		numGroups: len(groupBy),
	}
}

// Schema implements Operator.
func (a *HashAggregate) Schema() storage.Schema { return a.schema }

// Open consumes the child and builds all groups.
func (a *HashAggregate) Open(ctx context.Context) error {
	if err := a.child.Open(ctx); err != nil {
		return err
	}
	batches, err := drainOpen(ctx, a.child)
	if err != nil {
		return err
	}

	index := make(map[string]*groupState)
	for _, batch := range batches {
		for row := 0; row < batch.NumRows(); row++ {
			a.accumulateRow(index, batch, row)
		}
	}

	// A global aggregate over an empty input still emits one row.
	if a.numGroups == 0 && len(a.groups) == 0 {
		a.groups = append(a.groups, &groupState{accs: a.newAccumulators()})
	}
	return nil
}

// accumulateRow routes one input row into its group, creating the group on
// first sight and updating its accumulators.
func (a *HashAggregate) accumulateRow(index map[string]*groupState, batch *storage.Batch, row int) {
	keys := make([]Value, len(a.groupBy))
	for i, g := range a.groupBy {
		keys[i] = g.eval(batch, row)
	}
	key := tupleKey(keys)

	group, ok := index[key]
	if !ok {
		group = &groupState{keys: keys, accs: a.newAccumulators()}
		index[key] = group
		a.groups = append(a.groups, group) // preserve first-seen order
	}

	for i, spec := range a.specs {
		var arg Value
		if spec.arg != nil {
			arg = spec.arg.eval(batch, row)
		}
		group.accs[i].update(arg)
	}
}

func (a *HashAggregate) newAccumulators() []accumulator {
	accs := make([]accumulator, len(a.specs))
	for i, spec := range a.specs {
		accs[i] = newAccumulator(spec)
	}
	return accs
}

// Next emits the computed groups in batches.
func (a *HashAggregate) Next(_ context.Context) (*storage.Batch, error) {
	if a.pos >= len(a.groups) {
		return nil, io.EOF
	}
	end := a.pos + storage.DefaultBatchSize
	if end > len(a.groups) {
		end = len(a.groups)
	}
	groups := a.groups[a.pos:end]
	a.pos = end

	return buildBatch(a.schema, len(groups), func(col, i int) Value {
		g := groups[i]
		if col < a.numGroups {
			return g.keys[col]
		}
		return g.accs[col-a.numGroups].result()
	}), nil
}

// Close implements Operator.
func (a *HashAggregate) Close() error { return a.child.Close() }

// --- accumulators ---

// accumulator folds a sequence of argument values into a single aggregate
// result. update receives the (possibly NULL) argument for each input row.
type accumulator interface {
	update(v Value)
	result() Value
}

func newAccumulator(spec aggSpec) accumulator {
	switch spec.kind {
	case aggCountStar:
		return &countStarAcc{}
	case aggCount:
		return &countAcc{}
	case aggSum:
		return &sumAcc{float: spec.resultType == storage.TypeFloat64}
	case aggAvg:
		return &avgAcc{}
	case aggMin:
		return &minMaxAcc{wantMax: false}
	default: // aggMax
		return &minMaxAcc{wantMax: true}
	}
}

// countStarAcc counts every row regardless of value (COUNT(*)).
type countStarAcc struct{ n int64 }

func (a *countStarAcc) update(Value)  { a.n++ }
func (a *countStarAcc) result() Value { return Int(a.n) }

// countAcc counts non-NULL values (COUNT(expr)).
type countAcc struct{ n int64 }

func (a *countAcc) update(v Value) {
	if !v.IsNull() {
		a.n++
	}
}
func (a *countAcc) result() Value { return Int(a.n) }

// sumAcc sums non-NULL numeric values; SUM over no values is NULL.
type sumAcc struct {
	float bool
	i     int64
	f     float64
	any   bool
}

func (a *sumAcc) update(v Value) {
	if v.IsNull() {
		return
	}
	a.any = true
	if a.float {
		a.f += v.asFloat()
	} else {
		a.i += v.I
	}
}

func (a *sumAcc) result() Value {
	if !a.any {
		return Null()
	}
	if a.float {
		return Float(a.f)
	}
	return Int(a.i)
}

// avgAcc averages non-NULL values; AVG over no values is NULL.
type avgAcc struct {
	sum   float64
	count int64
}

func (a *avgAcc) update(v Value) {
	if v.IsNull() {
		return
	}
	a.sum += v.asFloat()
	a.count++
}

func (a *avgAcc) result() Value {
	if a.count == 0 {
		return Null()
	}
	return Float(a.sum / float64(a.count))
}

// minMaxAcc tracks the smallest or largest non-NULL value seen.
type minMaxAcc struct {
	wantMax bool
	best    Value
	set     bool
}

func (a *minMaxAcc) update(v Value) {
	if v.IsNull() {
		return
	}
	if !a.set {
		a.best = v
		a.set = true
		return
	}
	cmp := compareValues(v, a.best)
	if (a.wantMax && cmp > 0) || (!a.wantMax && cmp < 0) {
		a.best = v
	}
}

func (a *minMaxAcc) result() Value {
	if !a.set {
		return Null()
	}
	return a.best
}
