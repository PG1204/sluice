package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/logical"
	"github.com/PG1204/sluice/engine/optimizer"
	"github.com/PG1204/sluice/engine/parser"
	"github.com/PG1204/sluice/engine/physical"
	"github.com/PG1204/sluice/engine/storage"
)

// Engine ties the pipeline together — parser, logical planner, optimizer,
// physical executor, and storage registry — behind a small API. It is the
// single entry point the CLI and (later) the HTTP service use to run queries.
type Engine struct {
	registry *storage.Registry
	stats    *optimizer.Provider
}

// New creates an Engine that reads tables from dataDir.
func New(dataDir string) *Engine {
	registry := storage.NewRegistry(dataDir)
	return &Engine{registry: registry, stats: optimizer.NewProvider(registry)}
}

// Tables lists the available table names.
func (e *Engine) Tables() ([]string, error) {
	return e.registry.Tables()
}

// TableSchema returns the schema of a named table.
func (e *Engine) TableSchema(ctx context.Context, name string) (storage.Schema, error) {
	return e.registry.Schema(ctx, name)
}

// Plan parses the SQL and builds a validated (unoptimized) logical plan.
func (e *Engine) Plan(sql string) (logical.Plan, error) {
	stmt, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	sel, ok := stmt.(*ast.SelectStatement)
	if !ok {
		return nil, fmt.Errorf("only SELECT statements are supported")
	}
	return logical.Build(sel, logical.NewRegistryCatalog(e.registry))
}

// OptimizedPlan builds the logical plan and applies the optimizer rules.
func (e *Engine) OptimizedPlan(ctx context.Context, sql string) (logical.Plan, error) {
	plan, err := e.Plan(sql)
	if err != nil {
		return nil, err
	}
	return optimizer.Optimize(ctx, plan, e.stats)
}

// Explain returns the EXPLAIN tree for a query's unoptimized logical plan.
func (e *Engine) Explain(sql string) (string, error) {
	plan, err := e.Plan(sql)
	if err != nil {
		return "", err
	}
	return logical.Explain(plan), nil
}

// ExplainCost returns the optimized plan annotated with estimated rows and cost
// per operator, plus the total query cost.
func (e *Engine) ExplainCost(ctx context.Context, sql string) (string, error) {
	plan, err := e.OptimizedPlan(ctx, sql)
	if err != nil {
		return "", err
	}
	return optimizer.ExplainCost(ctx, plan, e.stats)
}

// Cost returns the optimizer's total estimated cost for a query — the single
// number the rate limiter charges against a tenant's quota.
func (e *Engine) Cost(ctx context.Context, sql string) (float64, error) {
	plan, err := e.OptimizedPlan(ctx, sql)
	if err != nil {
		return 0, err
	}
	analysis, err := optimizer.Analyze(ctx, plan, e.stats)
	if err != nil {
		return 0, err
	}
	return analysis.TotalCost(plan), nil
}

// PlanNode is a node of the optimized plan as a serializable tree, for the
// dashboard's plan visualizer: a label plus the optimizer's row/cost estimates
// and the node's children.
type PlanNode struct {
	Label    string     `json:"label"`
	Rows     int64      `json:"rows"`
	Cost     float64    `json:"cost"`
	Children []PlanNode `json:"children,omitempty"`
}

// PlanTree returns the optimized plan for a query as an annotated tree.
func (e *Engine) PlanTree(ctx context.Context, sql string) (*PlanNode, error) {
	plan, err := e.OptimizedPlan(ctx, sql)
	if err != nil {
		return nil, err
	}
	analysis, err := optimizer.Analyze(ctx, plan, e.stats)
	if err != nil {
		return nil, err
	}
	node := buildPlanNode(plan, analysis)
	return &node, nil
}

func buildPlanNode(p logical.Plan, a *optimizer.Analysis) PlanNode {
	est := a.Of(p)
	node := PlanNode{Label: p.Describe(), Rows: est.Rows, Cost: est.Cost}
	for _, child := range p.Children() {
		node.Children = append(node.Children, buildPlanNode(child, a))
	}
	return node
}

// Result is the materialized output of a query.
type Result struct {
	Schema  storage.Schema
	Batches []*storage.Batch
}

// RowCount returns the total number of result rows across all batches.
func (r *Result) RowCount() int {
	n := 0
	for _, b := range r.Batches {
		n += b.NumRows()
	}
	return n
}

// Prepared is a query that has been parsed, planned, optimized, cost-estimated,
// and lowered to an executable operator — everything except running it. It lets
// a caller learn a query's cost (e.g. to make a rate-limiting decision) and
// then execute the *same* plan, without re-doing the planning work or risking
// the estimate diverging from what runs.
type Prepared struct {
	op     physical.Operator
	cost   float64
	schema storage.Schema
}

// Cost is the optimizer's total estimated cost for the prepared query.
func (p *Prepared) Cost() float64 { return p.cost }

// Schema is the output schema of the prepared query.
func (p *Prepared) Schema() storage.Schema { return p.schema }

// Prepare parses, plans, optimizes, estimates the cost of, and builds the
// physical operator for a query. All input errors (syntax, validation,
// unsupported features surfaced during lowering) happen here — before any work
// is charged or executed.
func (e *Engine) Prepare(ctx context.Context, sql string) (*Prepared, error) {
	plan, err := e.OptimizedPlan(ctx, sql)
	if err != nil {
		return nil, err
	}
	analysis, err := optimizer.Analyze(ctx, plan, e.stats)
	if err != nil {
		return nil, err
	}
	op, err := physical.Build(plan, e.registry)
	if err != nil {
		return nil, err
	}
	return &Prepared{op: op, cost: analysis.TotalCost(plan), schema: plan.Schema()}, nil
}

// Execute runs a prepared query and returns the materialized result. Results
// are collected in memory, which suits a CLI/demo; streaming output is a later
// concern.
func (e *Engine) Execute(ctx context.Context, p *Prepared) (*Result, error) {
	result := &Result{Schema: p.schema}
	err := physical.Run(ctx, p.op, func(b *storage.Batch) error {
		result.Batches = append(result.Batches, b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Query prepares and executes a query end to end.
func (e *Engine) Query(ctx context.Context, sql string) (*Result, error) {
	p, err := e.Prepare(ctx, sql)
	if err != nil {
		return nil, err
	}
	return e.Execute(ctx, p)
}

// String renders the result as an aligned text table, with a trailing row
// count. NULLs print as "NULL".
func (r *Result) String() string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)

	fmt.Fprintln(tw, strings.Join(r.Schema.Names(), "\t"))

	for _, batch := range r.Batches {
		for row := 0; row < batch.NumRows(); row++ {
			cells := make([]string, batch.NumCols())
			for col := 0; col < batch.NumCols(); col++ {
				cells[col] = formatCell(batch.Columns[col], row)
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
	}
	tw.Flush()

	n := r.RowCount()
	plural := "rows"
	if n == 1 {
		plural = "row"
	}
	fmt.Fprintf(&sb, "(%d %s)\n", n, plural)
	return sb.String()
}

// formatCell renders one cell for display.
func formatCell(col storage.Column, row int) string {
	v := col.Value(row)
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}
