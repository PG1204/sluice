package lexer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect drains a lexer into a slice of tokens, stopping after EOF.
func collect(input string) []Token {
	l := New(input)
	var toks []Token
	for {
		tok := l.NextToken()
		toks = append(toks, tok)
		if tok.Type == EOF {
			return toks
		}
	}
}

func TestNextToken_TypesAndLiterals(t *testing.T) {
	input := `SELECT name, amount FROM orders WHERE amount >= 100.5 AND name != 'bob';`

	want := []struct {
		typ TokenType
		lit string
	}{
		{SELECT, "SELECT"},
		{IDENT, "name"},
		{COMMA, ","},
		{IDENT, "amount"},
		{FROM, "FROM"},
		{IDENT, "orders"},
		{WHERE, "WHERE"},
		{IDENT, "amount"},
		{GTE, ">="},
		{FLOAT, "100.5"},
		{AND, "AND"},
		{IDENT, "name"},
		{NEQ, "!="},
		{STRING, "bob"},
		{SEMICOLON, ";"},
		{EOF, ""},
	}

	toks := collect(input)
	require.Len(t, toks, len(want))
	for i, w := range want {
		assert.Equalf(t, w.typ, toks[i].Type, "token %d type", i)
		assert.Equalf(t, w.lit, toks[i].Literal, "token %d literal", i)
	}
}

func TestNextToken_OperatorsAndPunctuation(t *testing.T) {
	input := `= != <> < <= > >= + - * / , ; ( ) .`
	want := []TokenType{
		EQ, NEQ, NEQ, LT, LTE, GT, GTE,
		PLUS, MINUS, STAR, SLASH,
		COMMA, SEMICOLON, LPAREN, RPAREN, DOT, EOF,
	}
	toks := collect(input)
	require.Len(t, toks, len(want))
	for i, w := range want {
		assert.Equalf(t, w, toks[i].Type, "token %d", i)
	}
}

func TestNextToken_KeywordsAreCaseInsensitive(t *testing.T) {
	toks := collect(`select Select sElEcT from JOIN inner left right group order by asc desc limit and or not null true false distinct as on`)
	want := []TokenType{
		SELECT, SELECT, SELECT, FROM, JOIN, INNER, LEFT, RIGHT,
		GROUP, ORDER, BY, ASC, DESC, LIMIT, AND, OR, NOT,
		NULL, TRUE, FALSE, DISTINCT, AS, ON, EOF,
	}
	require.Len(t, toks, len(want))
	for i, w := range want {
		assert.Equalf(t, w, toks[i].Type, "token %d (%q)", i, toks[i].Literal)
	}
}

func TestNextToken_QualifiedIdentifier(t *testing.T) {
	toks := collect(`orders.amount`)
	require.Equal(t, []TokenType{IDENT, DOT, IDENT, EOF}, typesOf(toks))
	assert.Equal(t, "orders", toks[0].Literal)
	assert.Equal(t, "amount", toks[2].Literal)
}

func TestNextToken_Numbers(t *testing.T) {
	tests := []struct {
		in  string
		typ TokenType
		lit string
	}{
		{"0", INT, "0"},
		{"42", INT, "42"},
		{"3.14", FLOAT, "3.14"},
		{".5", FLOAT, ".5"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			toks := collect(tt.in)
			require.Equal(t, tt.typ, toks[0].Type)
			assert.Equal(t, tt.lit, toks[0].Literal)
		})
	}
}

func TestNextToken_IntThenDotIsNotFloat(t *testing.T) {
	// "5." (no digit after the dot) is an INT followed by a DOT, not a float.
	toks := collect(`5.x`)
	assert.Equal(t, []TokenType{INT, DOT, IDENT, EOF}, typesOf(toks))
	assert.Equal(t, "5", toks[0].Literal)
}

func TestNextToken_StringEscaping(t *testing.T) {
	toks := collect(`'it''s fine'`)
	require.Equal(t, STRING, toks[0].Type)
	assert.Equal(t, "it's fine", toks[0].Literal)
}

func TestNextToken_UnterminatedString(t *testing.T) {
	toks := collect(`'oops`)
	require.Equal(t, ILLEGAL, toks[0].Type)
	assert.Equal(t, "unterminated string", toks[0].Literal)
}

func TestNextToken_Comments(t *testing.T) {
	input := "SELECT a -- a line comment\nFROM /* block */ t"
	want := []TokenType{SELECT, IDENT, FROM, IDENT, EOF}
	assert.Equal(t, want, typesOf(collect(input)))
}

func TestNextToken_IllegalChar(t *testing.T) {
	toks := collect(`@`)
	require.Equal(t, ILLEGAL, toks[0].Type)
	assert.Equal(t, "@", toks[0].Literal)
}

func TestNextToken_TracksPositions(t *testing.T) {
	// Line 1: "SELECT a"   Line 2: "FROM t"
	input := "SELECT a\nFROM t"
	toks := collect(input)

	// SELECT at line 1, col 1.
	assert.Equal(t, Position{Line: 1, Column: 1, Offset: 0}, toks[0].Pos)
	// "a" at line 1, col 8.
	assert.Equal(t, Position{Line: 1, Column: 8, Offset: 7}, toks[1].Pos)
	// FROM at line 2, col 1.
	assert.Equal(t, 2, toks[2].Pos.Line)
	assert.Equal(t, 1, toks[2].Pos.Column)
	// "t" at line 2, col 6.
	assert.Equal(t, 2, toks[3].Pos.Line)
	assert.Equal(t, 6, toks[3].Pos.Column)
}

func TestTokenType_String(t *testing.T) {
	assert.Equal(t, "SELECT", SELECT.String())
	assert.Equal(t, "!=", NEQ.String())
	assert.Equal(t, "IDENT", IDENT.String())
	assert.Equal(t, "UNKNOWN", TokenType(9999).String())
}

// typesOf projects a token slice down to its types for terse assertions.
func typesOf(toks []Token) []TokenType {
	out := make([]TokenType, len(toks))
	for i, t := range toks {
		out[i] = t.Type
	}
	return out
}
