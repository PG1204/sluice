// Package lexer tokenizes raw SQL text into a stream of tokens for the parser.
// Hand-written (no generator) so we own error positions and keyword handling.
//
// Call New(input) then NextToken() repeatedly until an EOF token. Each token
// carries its source Position (line/column) for error reporting.
package lexer
