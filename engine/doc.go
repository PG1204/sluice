// Package engine is the root of Sluice's cost-aware query engine.
//
// The engine is organized as a pipeline of subpackages, each a stage in
// turning a SQL string into results:
//
//	lexer     — tokenize raw SQL
//	parser    — recursive-descent parse into an AST
//	ast       — abstract syntax tree node types
//	logical   — relational-algebra logical plan
//	physical  — executable physical plan (Volcano iterators)
//	optimizer — cost estimation + plan rewrites (the novel piece)
//	storage   — CSV/Parquet data sources and the schema registry
//
// The optimizer's total cost estimate is the value the rate limiter charges
// against a tenant's quota — that feedback loop is the heart of Sluice.
package engine
