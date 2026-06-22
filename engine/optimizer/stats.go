// Package optimizer estimates the cost of a query before it runs and rewrites
// the plan to lower that cost. The total cost it produces is a single number
// that the rate limiter charges against a tenant's quota — the feedback loop
// that is the point of Sluice.
//
// The pieces:
//   - statistics (this file): table/column stats, computed by scanning once and
//     cached, the raw input to estimation.
//   - cardinality + cost (estimate.go): per-operator row-count and cost
//     estimates derived from the stats and the plan shape.
//   - rules (rules.go) + driver (optimize.go): cost-reducing plan rewrites.
//   - EXPLAIN COST (explain.go): the annotated plan tree.
package optimizer

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"

	"github.com/PG1204/sluice/engine/storage"
)

// ColumnStats summarizes one column, enough to drive selectivity estimates.
type ColumnStats struct {
	// DistinctCount is the number of distinct non-NULL values. Used to estimate
	// equality selectivity (1/DistinctCount) and group counts.
	DistinctCount int64
	// NullCount is the number of NULL values.
	NullCount int64
	// Min and Max bound numeric columns, for range selectivity. Valid only when
	// Numeric is true.
	Min, Max float64
	Numeric  bool
}

// TableStats summarizes one table.
type TableStats struct {
	RowCount int64
	Columns  map[string]ColumnStats
}

// TableOpener opens a named table for scanning. *storage.Registry satisfies it.
type TableOpener interface {
	Open(ctx context.Context, name string) (storage.DataSource, error)
}

// Provider computes and caches table statistics. Stats are gathered on first
// request by scanning the table once ("computed and cached on first scan"), so
// repeated planning of queries over the same table pays the scan only once.
type Provider struct {
	opener TableOpener

	mu    sync.Mutex
	cache map[string]*TableStats
}

// NewProvider creates a statistics provider backed by the given table opener.
func NewProvider(opener TableOpener) *Provider {
	return &Provider{opener: opener, cache: make(map[string]*TableStats)}
}

// TableStats returns statistics for the named table, computing and caching them
// on first use.
func (p *Provider) TableStats(ctx context.Context, name string) (*TableStats, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if s, ok := p.cache[name]; ok {
		return s, nil
	}
	stats, err := p.computeTableStats(ctx, name)
	if err != nil {
		return nil, err
	}
	p.cache[name] = stats
	return stats, nil
}

// computeTableStats scans the whole table once, accumulating per-column stats.
func (p *Provider) computeTableStats(ctx context.Context, name string) (*TableStats, error) {
	src, err := p.opener.Open(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("stats: open %q: %w", name, err)
	}
	defer src.Close()

	schema := src.Schema()
	accs := make([]*columnAccumulator, len(schema.Fields))
	for i, f := range schema.Fields {
		accs[i] = newColumnAccumulator(f.Type)
	}

	var rowCount int64
	for {
		batch, err := src.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("stats: scan %q: %w", name, err)
		}
		rowCount += int64(batch.NumRows())
		for ci, col := range batch.Columns {
			accs[ci].observe(col)
		}
	}

	cols := make(map[string]ColumnStats, len(schema.Fields))
	for i, f := range schema.Fields {
		cols[f.Name] = accs[i].finish()
	}
	return &TableStats{RowCount: rowCount, Columns: cols}, nil
}

// columnAccumulator gathers stats for one column across batches. Distinct
// counting is exact (a set of stringified values), which is fine at the data
// sizes in scope; a sketch like HyperLogLog would replace it at scale.
type columnAccumulator struct {
	numeric  bool
	distinct map[string]struct{}
	nulls    int64
	min, max float64
	seenNum  bool
}

func newColumnAccumulator(t storage.Type) *columnAccumulator {
	return &columnAccumulator{
		numeric:  t == storage.TypeInt64 || t == storage.TypeFloat64,
		distinct: make(map[string]struct{}),
	}
}

func (a *columnAccumulator) observe(col storage.Column) {
	for row := 0; row < col.Len(); row++ {
		if col.IsNull(row) {
			a.nulls++
			continue
		}
		v := col.Value(row)
		a.distinct[distinctKey(v)] = struct{}{}
		if a.numeric {
			f := numericValue(v)
			if !a.seenNum || f < a.min {
				a.min = f
			}
			if !a.seenNum || f > a.max {
				a.max = f
			}
			a.seenNum = true
		}
	}
}

func (a *columnAccumulator) finish() ColumnStats {
	return ColumnStats{
		DistinctCount: int64(len(a.distinct)),
		NullCount:     a.nulls,
		Min:           a.min,
		Max:           a.max,
		Numeric:       a.numeric && a.seenNum,
	}
}

// distinctKey turns a cell value into a string key for distinct counting.
func distinctKey(v any) string {
	switch x := v.(type) {
	case int64:
		return "i" + strconv.FormatInt(x, 10)
	case float64:
		return "f" + strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return "b" + strconv.FormatBool(x)
	case string:
		return "s" + x
	default:
		return fmt.Sprintf("?%v", x)
	}
}

// numericValue returns a numeric column value as a float64.
func numericValue(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	default:
		return math.NaN()
	}
}
