// Package parser is a hand-written recursive-descent parser that turns the
// lexer's token stream into an AST. Errors carry source positions so callers
// can point at exactly where a query went wrong.
//
// Implemented in Phase 1.
package parser
