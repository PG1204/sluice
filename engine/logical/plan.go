// Package logical defines the logical query plan: a tree of relational-algebra
// operators translated from the AST and validated against the table catalog.
//
// A logical plan says *what* the query computes — scan this table, filter by
// this predicate, group and aggregate, project these columns — without saying
// *how* (which join algorithm, which order to read). The physical planner
// (Phase 4) makes those choices; the optimizer (Phase 5) rewrites this tree.
//
// Every node exposes its output Schema, resolved during planning, so the rest
// of the engine can type-check and lay out columns without re-deriving types.
package logical

import (
	"strings"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/storage"
)

// Plan is a node in the logical plan tree. Each node knows its output schema
// and its children, which is all the EXPLAIN printer and the physical planner
// need to walk the tree.
type Plan interface {
	// Schema returns the columns this node outputs.
	Schema() storage.Schema
	// Children returns the input plans, in order (leaf operators return nil).
	Children() []Plan
	// Describe renders this node as a single EXPLAIN line (without children).
	Describe() string
	// isPlan is an unexported marker so only this package defines plan nodes.
	isPlan()
}

// Scan reads rows of a base table. It is always a leaf.
//
// Projection is the set of column names the scan needs to output, in table
// order; it is set by the projection-pushdown optimizer rule. nil means "all
// columns" (the unoptimized default).
type Scan struct {
	Table       string
	Alias       string // empty if the table was referenced without an alias
	TableSchema storage.Schema
	Projection  []string
}

// Filter keeps only rows for which Predicate evaluates to true.
type Filter struct {
	Input     Plan
	Predicate ast.Expression
}

// ProjectItem is one output column of a Project: an expression, an optional
// alias, and the resolved output name/type recorded during planning.
type ProjectItem struct {
	Expr  ast.Expression
	Alias string // explicit AS alias, or ""
	Name  string // resolved output column name
	Type  storage.Type
}

// Project computes the SELECT list: a set of output columns derived from the
// input. Distinct records whether SELECT DISTINCT was used.
type Project struct {
	Input     Plan
	Items     []ProjectItem
	Distinct  bool
	OutSchema storage.Schema
}

// Join combines two inputs on a predicate. Phase 3 records the join type and
// condition; the physical planner picks the algorithm (hash join).
type Join struct {
	Left      Plan
	Right     Plan
	JoinType  ast.JoinType
	On        ast.Expression
	OutSchema storage.Schema
}

// AggregateExpr is one aggregate computed by an Aggregate node, e.g. COUNT(*)
// or SUM(amount), with its resolved output name and type.
type AggregateExpr struct {
	Call *ast.FunctionCall
	Name string
	Type storage.Type
}

// Aggregate groups its input by GroupBy expressions and computes Aggregates per
// group. With no GroupBy it produces a single global aggregate row. Its output
// is the group-by columns followed by the aggregate results.
type Aggregate struct {
	Input      Plan
	GroupBy    []ast.Expression
	Aggregates []AggregateExpr
	OutSchema  storage.Schema
}

// SortKey is one ORDER BY term and its direction.
type SortKey struct {
	Expr ast.Expression
	Desc bool
}

// Sort orders its input by the given keys.
type Sort struct {
	Input Plan
	Keys  []SortKey
}

// Limit caps the number of output rows.
type Limit struct {
	Input Plan
	Count int64
}

// --- Schema() ---

// Schema returns the scan's output schema: the projected subset of the table's
// columns when a projection has been pushed down, otherwise the full table.
func (s *Scan) Schema() storage.Schema {
	if s.Projection == nil {
		return s.TableSchema
	}
	fields := make([]storage.Field, 0, len(s.Projection))
	for _, f := range s.TableSchema.Fields {
		if containsString(s.Projection, f.Name) {
			fields = append(fields, f)
		}
	}
	return storage.Schema{Fields: fields}
}

// containsString reports whether name is in names (case-insensitive).
func containsString(names []string, name string) bool {
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// Schema is the input schema: a filter never changes columns.
func (f *Filter) Schema() storage.Schema { return f.Input.Schema() }

// Schema returns the projected output columns.
func (p *Project) Schema() storage.Schema { return p.OutSchema }

// Schema returns the concatenation of the two inputs' columns.
func (j *Join) Schema() storage.Schema { return j.OutSchema }

// Schema returns the group-by columns followed by the aggregate results.
func (a *Aggregate) Schema() storage.Schema { return a.OutSchema }

// Schema is the input schema: sorting never changes columns.
func (s *Sort) Schema() storage.Schema { return s.Input.Schema() }

// Schema is the input schema: a limit never changes columns.
func (l *Limit) Schema() storage.Schema { return l.Input.Schema() }

// --- Children() ---

func (s *Scan) Children() []Plan      { return nil }
func (f *Filter) Children() []Plan    { return []Plan{f.Input} }
func (p *Project) Children() []Plan   { return []Plan{p.Input} }
func (j *Join) Children() []Plan      { return []Plan{j.Left, j.Right} }
func (a *Aggregate) Children() []Plan { return []Plan{a.Input} }
func (s *Sort) Children() []Plan      { return []Plan{s.Input} }
func (l *Limit) Children() []Plan     { return []Plan{l.Input} }

// --- isPlan marker ---

func (*Scan) isPlan()      {}
func (*Filter) isPlan()    {}
func (*Project) isPlan()   {}
func (*Join) isPlan()      {}
func (*Aggregate) isPlan() {}
func (*Sort) isPlan()      {}
func (*Limit) isPlan()     {}

// relationName is the name a Scan exposes for qualified column references: the
// alias if present, otherwise the table name.
func (s *Scan) relationName() string {
	if s.Alias != "" {
		return s.Alias
	}
	return s.Table
}
