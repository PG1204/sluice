package logical

import (
	"context"
	"fmt"
	"strings"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/storage"
)

// Catalog resolves a table name to its schema. The planner depends only on
// this narrow interface, not on the concrete storage registry, so plans can be
// built and tested without touching the filesystem.
type Catalog interface {
	TableSchema(name string) (storage.Schema, error)
}

// registryCatalog adapts a *storage.Registry to the Catalog interface.
type registryCatalog struct {
	r *storage.Registry
}

// NewRegistryCatalog wraps a storage Registry as a Catalog.
func NewRegistryCatalog(r *storage.Registry) Catalog {
	return registryCatalog{r: r}
}

func (c registryCatalog) TableSchema(name string) (storage.Schema, error) {
	return c.r.Schema(context.Background(), name)
}

// Build translates a parsed SELECT statement into a validated logical plan.
//
// It builds the tree in SQL's logical evaluation order — FROM/JOIN, WHERE,
// GROUP BY/aggregate, SELECT, ORDER BY, LIMIT — resolving and type-checking
// each clause against the schema in scope at that point.
func Build(stmt *ast.SelectStatement, cat Catalog) (Plan, error) {
	plan, sc, err := planFrom(stmt, cat)
	if err != nil {
		return nil, err
	}

	if stmt.Where != nil {
		c := &checker{scope: sc}
		t, err := c.resolve(stmt.Where)
		if err != nil {
			return nil, fmt.Errorf("WHERE: %w", err)
		}
		if !isBoolish(t) {
			return nil, fmt.Errorf("WHERE clause must be boolean, got %s", t)
		}
		plan = &Filter{Input: plan, Predicate: stmt.Where}
	}

	selectExprs := selectExpressions(stmt)
	orderExprs := orderByExpressions(stmt)
	aggregating := len(stmt.GroupBy) > 0 || containsAggregate(selectExprs...) || containsAggregate(orderExprs...)

	if aggregating {
		agg, err := buildAggregate(stmt, plan, sc, selectExprs, orderExprs)
		if err != nil {
			return nil, err
		}
		plan = agg
	}

	proj, err := buildProject(stmt, plan, sc, aggregating)
	if err != nil {
		return nil, err
	}
	plan = proj

	if len(stmt.OrderBy) > 0 {
		sort, err := buildSort(stmt, plan, sc, proj, aggregating)
		if err != nil {
			return nil, err
		}
		plan = sort
	}

	if stmt.Limit != nil {
		plan = &Limit{Input: plan, Count: *stmt.Limit}
	}
	return plan, nil
}

// planFrom builds the Scan and any Join nodes, returning the plan and the scope
// of columns they expose.
func planFrom(stmt *ast.SelectStatement, cat Catalog) (Plan, *scope, error) {
	if stmt.From == nil {
		return nil, nil, fmt.Errorf("missing FROM clause")
	}

	plan, rel, err := scanTable(stmt.From, cat)
	if err != nil {
		return nil, nil, err
	}
	sc := &scope{relations: []relation{rel}}

	for _, j := range stmt.Joins {
		rightPlan, rightRel, err := scanTable(j.Table, cat)
		if err != nil {
			return nil, nil, err
		}
		sc.relations = append(sc.relations, rightRel)

		c := &checker{scope: sc}
		t, err := c.resolve(j.On)
		if err != nil {
			return nil, nil, fmt.Errorf("JOIN ON: %w", err)
		}
		if !isBoolish(t) {
			return nil, nil, fmt.Errorf("JOIN ON condition must be boolean, got %s", t)
		}

		plan = &Join{
			Left:      plan,
			Right:     rightPlan,
			JoinType:  j.Type,
			On:        j.On,
			OutSchema: concatSchemas(plan.Schema(), rightPlan.Schema()),
		}
	}
	return plan, sc, nil
}

// scanTable resolves a table reference to a Scan and the relation it exposes.
func scanTable(ref *ast.TableRef, cat Catalog) (Plan, relation, error) {
	schema, err := cat.TableSchema(ref.Name)
	if err != nil {
		return nil, relation{}, fmt.Errorf("table %q: %w", ref.Name, err)
	}
	scan := &Scan{Table: ref.Name, Alias: ref.Alias, TableSchema: schema}
	return scan, relation{name: scan.relationName(), schema: schema}, nil
}

// buildAggregate constructs the Aggregate node: it validates the GROUP BY keys
// and collects every distinct aggregate call from SELECT and ORDER BY, deriving
// the node's output schema (group keys followed by aggregate results).
func buildAggregate(stmt *ast.SelectStatement, input Plan, sc *scope, selectExprs, orderExprs []ast.Expression) (*Aggregate, error) {
	agg := &Aggregate{Input: input, GroupBy: stmt.GroupBy}
	var fields []storage.Field

	for _, gb := range stmt.GroupBy {
		gc := &checker{scope: sc}
		t, err := gc.resolve(gb)
		if err != nil {
			return nil, fmt.Errorf("GROUP BY: %w", err)
		}
		fields = append(fields, storage.Field{Name: exprName(gb), Type: t, Nullable: itemNullable(gb, sc)})
	}

	seen := make(map[string]bool)
	addAggsFrom := func(exprs []ast.Expression) error {
		for _, e := range exprs {
			for _, call := range aggregateCalls(e) {
				key := call.String()
				if seen[key] {
					continue
				}
				seen[key] = true
				t, err := aggregateResultType(call, sc)
				if err != nil {
					return err
				}
				agg.Aggregates = append(agg.Aggregates, AggregateExpr{Call: call, Name: key, Type: t})
				fields = append(fields, storage.Field{Name: key, Type: t, Nullable: aggregateNullable(call)})
			}
		}
		return nil
	}
	if err := addAggsFrom(selectExprs); err != nil {
		return nil, err
	}
	if err := addAggsFrom(orderExprs); err != nil {
		return nil, err
	}

	agg.OutSchema = storage.Schema{Fields: fields}
	return agg, nil
}

// buildProject constructs the Project node, expanding any "*" and resolving
// every output column's name and type.
func buildProject(stmt *ast.SelectStatement, input Plan, sc *scope, aggregating bool) (*Project, error) {
	c := &checker{scope: sc, allowAgg: aggregating, aggregating: aggregating, groupKeys: stmt.GroupBy}

	var items []ProjectItem
	for _, col := range stmt.Columns {
		if star, ok := col.Expr.(*ast.Star); ok {
			if aggregating {
				return nil, fmt.Errorf("'*' cannot be used with GROUP BY or aggregates")
			}
			expanded, err := expandStar(star, sc)
			if err != nil {
				return nil, err
			}
			items = append(items, expanded...)
			continue
		}

		t, err := c.resolve(col.Expr)
		if err != nil {
			return nil, fmt.Errorf("SELECT: %w", err)
		}
		items = append(items, ProjectItem{
			Expr:  col.Expr,
			Alias: col.Alias,
			Name:  outputName(col),
			Type:  t,
		})
	}

	return &Project{
		Input:     input,
		Items:     items,
		Distinct:  stmt.Distinct,
		OutSchema: projectSchema(items, sc),
	}, nil
}

// buildSort resolves each ORDER BY key. A key may reference a projected output
// column (including an alias) or any column in scope; aggregating queries may
// order by aggregates.
func buildSort(stmt *ast.SelectStatement, input Plan, sc *scope, proj *Project, aggregating bool) (*Sort, error) {
	keys := make([]SortKey, 0, len(stmt.OrderBy))
	c := &checker{scope: sc, allowAgg: aggregating, aggregating: aggregating, groupKeys: stmt.GroupBy}

	for _, o := range stmt.OrderBy {
		// An unqualified name matching a SELECT output (e.g. an alias) resolves
		// against the projection; otherwise type-check against the scope.
		if id, ok := o.Expr.(*ast.Identifier); ok && id.Table == "" {
			if _, found := proj.OutSchema.ColumnIndex(id.Name); found {
				keys = append(keys, SortKey{Expr: o.Expr, Desc: o.Desc})
				continue
			}
		}
		if _, err := c.resolve(o.Expr); err != nil {
			return nil, fmt.Errorf("ORDER BY: %w", err)
		}
		keys = append(keys, SortKey{Expr: o.Expr, Desc: o.Desc})
	}
	return &Sort{Input: input, Keys: keys}, nil
}

// expandStar turns "*" or "t.*" into one ProjectItem per matching column.
func expandStar(star *ast.Star, sc *scope) ([]ProjectItem, error) {
	var items []ProjectItem
	matched := false
	for _, r := range sc.relations {
		if star.Table != "" && !strings.EqualFold(star.Table, r.name) {
			continue
		}
		matched = true
		for _, f := range r.schema.Fields {
			items = append(items, ProjectItem{
				Expr: &ast.Identifier{Table: r.name, Name: f.Name},
				Name: f.Name,
				Type: f.Type,
			})
		}
	}
	if !matched {
		return nil, fmt.Errorf("unknown table %q in %s.*", star.Table, star.Table)
	}
	return items, nil
}

// --- schema/name helpers ---

func concatSchemas(a, b storage.Schema) storage.Schema {
	fields := make([]storage.Field, 0, len(a.Fields)+len(b.Fields))
	fields = append(fields, a.Fields...)
	fields = append(fields, b.Fields...)
	return storage.Schema{Fields: fields}
}

func projectSchema(items []ProjectItem, sc *scope) storage.Schema {
	fields := make([]storage.Field, len(items))
	for i, it := range items {
		fields[i] = storage.Field{Name: it.Name, Type: it.Type, Nullable: itemNullable(it.Expr, sc)}
	}
	return storage.Schema{Fields: fields}
}

// outputName is the name a SELECT item exposes: its alias, else the column name
// for a bare identifier, else the printed expression.
func outputName(col ast.SelectItem) string {
	if col.Alias != "" {
		return col.Alias
	}
	return exprName(col.Expr)
}

// exprName is the default output name for an expression used as a column.
func exprName(e ast.Expression) string {
	if id, ok := e.(*ast.Identifier); ok {
		return id.Name
	}
	return e.String()
}

// itemNullable estimates whether an output column can contain NULLs.
func itemNullable(expr ast.Expression, sc *scope) bool {
	switch e := expr.(type) {
	case *ast.Identifier:
		if f, err := sc.resolveColumn(e.Table, e.Name); err == nil {
			return f.Nullable
		}
		return true
	case *ast.FunctionCall:
		return aggregateNullable(e)
	default:
		return true
	}
}

// aggregateNullable reports whether an aggregate can produce NULL. COUNT never
// does (it returns 0 over an empty group); the others can.
func aggregateNullable(call *ast.FunctionCall) bool {
	return strings.ToUpper(call.Name) != "COUNT"
}

// --- expression walking ---

// walkExpr visits e and all of its sub-expressions.
func walkExpr(e ast.Expression, visit func(ast.Expression)) {
	if e == nil {
		return
	}
	visit(e)
	switch n := e.(type) {
	case *ast.UnaryExpr:
		walkExpr(n.Operand, visit)
	case *ast.BinaryExpr:
		walkExpr(n.Left, visit)
		walkExpr(n.Right, visit)
	case *ast.FunctionCall:
		for _, a := range n.Args {
			walkExpr(a, visit)
		}
	}
}

// containsAggregate reports whether any expression contains an aggregate call.
func containsAggregate(exprs ...ast.Expression) bool {
	for _, e := range exprs {
		if len(aggregateCalls(e)) > 0 {
			return true
		}
	}
	return false
}

// aggregateCalls returns the aggregate function calls within an expression.
func aggregateCalls(e ast.Expression) []*ast.FunctionCall {
	var calls []*ast.FunctionCall
	walkExpr(e, func(x ast.Expression) {
		if fc, ok := x.(*ast.FunctionCall); ok && isAggregateFunc(fc.Name) {
			calls = append(calls, fc)
		}
	})
	return calls
}

func selectExpressions(stmt *ast.SelectStatement) []ast.Expression {
	exprs := make([]ast.Expression, 0, len(stmt.Columns))
	for _, c := range stmt.Columns {
		exprs = append(exprs, c.Expr)
	}
	return exprs
}

func orderByExpressions(stmt *ast.SelectStatement) []ast.Expression {
	exprs := make([]ast.Expression, 0, len(stmt.OrderBy))
	for _, o := range stmt.OrderBy {
		exprs = append(exprs, o.Expr)
	}
	return exprs
}
