// Package lexer tokenizes raw SQL text into a stream of tokens for the parser.
// Hand-written (no generator) so we own error positions and keyword handling.
//
// Implemented in Phase 1.
package lexer
