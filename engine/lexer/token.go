package lexer

import "strings"

// TokenType enumerates every kind of token the lexer can emit. We use a small
// int enum rather than strings so token comparisons in the parser are cheap
// and a switch is exhaustive at a glance.
type TokenType int

const (
	// ILLEGAL marks a character the lexer could not recognize. It carries the
	// offending text in Token.Literal so the parser can report it.
	ILLEGAL TokenType = iota
	// EOF signals the end of input. The parser stops when it sees this.
	EOF

	// IDENT is an unquoted identifier: a table name, column name, or function
	// name (e.g. orders, amount, count).
	IDENT
	// INT is an integer literal (e.g. 42).
	INT
	// FLOAT is a floating-point literal (e.g. 3.14).
	FLOAT
	// STRING is a single-quoted string literal (e.g. 'hello'). Literal holds
	// the unquoted, unescaped value.
	STRING

	// Operators.
	EQ    // =
	NEQ   // != or <>
	LT    // <
	GT    // >
	LTE   // <=
	GTE   // >=
	PLUS  // +
	MINUS // -
	STAR  // *  (multiplication, or the "all columns" wildcard)
	SLASH // /

	// Delimiters.
	COMMA     // ,
	SEMICOLON // ;
	LPAREN    // (
	RPAREN    // )
	DOT       // .  (qualifies a column: table.column)

	// Keywords. Kept contiguous so isKeyword/range checks stay simple.
	SELECT
	DISTINCT
	FROM
	WHERE
	AS
	JOIN
	INNER
	LEFT
	RIGHT
	FULL
	OUTER
	ON
	GROUP
	ORDER
	BY
	ASC
	DESC
	LIMIT
	AND
	OR
	NOT
	NULL
	TRUE
	FALSE
)

// tokenNames maps each TokenType to a human-readable label, used by String()
// for debugging and error messages ("expected FROM, got IDENT").
var tokenNames = map[TokenType]string{
	ILLEGAL:   "ILLEGAL",
	EOF:       "EOF",
	IDENT:     "IDENT",
	INT:       "INT",
	FLOAT:     "FLOAT",
	STRING:    "STRING",
	EQ:        "=",
	NEQ:       "!=",
	LT:        "<",
	GT:        ">",
	LTE:       "<=",
	GTE:       ">=",
	PLUS:      "+",
	MINUS:     "-",
	STAR:      "*",
	SLASH:     "/",
	COMMA:     ",",
	SEMICOLON: ";",
	LPAREN:    "(",
	RPAREN:    ")",
	DOT:       ".",
	SELECT:    "SELECT",
	DISTINCT:  "DISTINCT",
	FROM:      "FROM",
	WHERE:     "WHERE",
	AS:        "AS",
	JOIN:      "JOIN",
	INNER:     "INNER",
	LEFT:      "LEFT",
	RIGHT:     "RIGHT",
	FULL:      "FULL",
	OUTER:     "OUTER",
	ON:        "ON",
	GROUP:     "GROUP",
	ORDER:     "ORDER",
	BY:        "BY",
	ASC:       "ASC",
	DESC:      "DESC",
	LIMIT:     "LIMIT",
	AND:       "AND",
	OR:        "OR",
	NOT:       "NOT",
	NULL:      "NULL",
	TRUE:      "TRUE",
	FALSE:     "FALSE",
}

// String returns the label for a token type, suitable for error messages.
func (t TokenType) String() string {
	if name, ok := tokenNames[t]; ok {
		return name
	}
	return "UNKNOWN"
}

// keywords maps the uppercase spelling of each reserved word to its token
// type. SQL keywords are case-insensitive, so the lexer uppercases an
// identifier before looking it up here.
var keywords = map[string]TokenType{
	"SELECT":   SELECT,
	"DISTINCT": DISTINCT,
	"FROM":     FROM,
	"WHERE":    WHERE,
	"AS":       AS,
	"JOIN":     JOIN,
	"INNER":    INNER,
	"LEFT":     LEFT,
	"RIGHT":    RIGHT,
	"FULL":     FULL,
	"OUTER":    OUTER,
	"ON":       ON,
	"GROUP":    GROUP,
	"ORDER":    ORDER,
	"BY":       BY,
	"ASC":      ASC,
	"DESC":     DESC,
	"LIMIT":    LIMIT,
	"AND":      AND,
	"OR":       OR,
	"NOT":      NOT,
	"NULL":     NULL,
	"TRUE":     TRUE,
	"FALSE":    FALSE,
}

// LookupIdent classifies a word coming out of the lexer: if it matches a
// reserved keyword (case-insensitively) it returns that keyword's type,
// otherwise it is a plain identifier.
func LookupIdent(word string) TokenType {
	if tt, ok := keywords[strings.ToUpper(word)]; ok {
		return tt
	}
	return IDENT
}

// Position records where a token starts in the source, for error reporting.
// Line and Column are 1-based; Offset is a 0-based byte index into the input.
type Position struct {
	Line   int
	Column int
	Offset int
}

// Token is a single lexical unit: its type, the source text it was lexed from,
// and where it began.
type Token struct {
	Type    TokenType
	Literal string
	Pos     Position
}
