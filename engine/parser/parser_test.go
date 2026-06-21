package parser

import (
	"errors"
	"testing"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse_ValidQueries feeds 30+ representative queries through the parser
// and checks two things at once:
//  1. the parsed AST pretty-prints to the expected canonical SQL, and
//  2. re-parsing that canonical SQL yields an identical AST (round-trip),
//
// which exercises the parser and the printer against each other.
func TestParse_ValidQueries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // canonical pretty-printed form
	}{
		{"select star", "SELECT * FROM users", "SELECT * FROM users"},
		{"column list", "SELECT id, name FROM users", "SELECT id, name FROM users"},
		{"qualified columns with AS alias", "SELECT u.id, u.name FROM users AS u", "SELECT u.id, u.name FROM users AS u"},
		{"implicit table alias", "SELECT u.id FROM users u", "SELECT u.id FROM users AS u"},
		{"count star", "SELECT COUNT(*) FROM orders", "SELECT COUNT(*) FROM orders"},
		{"count star aliased", "SELECT COUNT(*) AS n FROM orders", "SELECT COUNT(*) AS n FROM orders"},
		{"multiple aggregates", "SELECT SUM(amount), AVG(amount) FROM orders", "SELECT SUM(amount), AVG(amount) FROM orders"},
		{"min max", "SELECT MIN(x), MAX(x) FROM t", "SELECT MIN(x), MAX(x) FROM t"},
		{"count distinct", "SELECT COUNT(DISTINCT customer_id) FROM orders", "SELECT COUNT(DISTINCT customer_id) FROM orders"},
		{"select distinct", "SELECT DISTINCT country FROM users", "SELECT DISTINCT country FROM users"},
		{"where gt", "SELECT * FROM orders WHERE amount > 100", "SELECT * FROM orders WHERE amount > 100"},
		{"where and", "SELECT * FROM orders WHERE amount >= 100 AND status = 'paid'", "SELECT * FROM orders WHERE amount >= 100 AND status = 'paid'"},
		{"and binds tighter than or", "SELECT * FROM t WHERE a = 1 OR b = 2 AND c = 3", "SELECT * FROM t WHERE a = 1 OR b = 2 AND c = 3"},
		{"parens override precedence", "SELECT * FROM t WHERE (a = 1 OR b = 2) AND c = 3", "SELECT * FROM t WHERE (a = 1 OR b = 2) AND c = 3"},
		{"not", "SELECT * FROM t WHERE NOT active", "SELECT * FROM t WHERE NOT active"},
		{"not with comparison", "SELECT * FROM t WHERE NOT a = b", "SELECT * FROM t WHERE NOT a = b"},
		{"angle-bracket not-equal normalizes", "SELECT * FROM t WHERE x <> 0", "SELECT * FROM t WHERE x != 0"},
		{"arithmetic precedence", "SELECT * FROM t WHERE price * qty > 1000", "SELECT * FROM t WHERE price * qty > 1000"},
		{"arithmetic mixed", "SELECT * FROM t WHERE a + b * c = d", "SELECT * FROM t WHERE a + b * c = d"},
		{"group by", "SELECT name, COUNT(*) FROM orders GROUP BY name", "SELECT name, COUNT(*) FROM orders GROUP BY name"},
		{"group by order by", "SELECT name, COUNT(*) FROM orders GROUP BY name ORDER BY COUNT(*) DESC", "SELECT name, COUNT(*) FROM orders GROUP BY name ORDER BY COUNT(*) DESC"},
		{"order by drops explicit asc", "SELECT * FROM t ORDER BY a ASC, b DESC", "SELECT * FROM t ORDER BY a, b DESC"},
		{"limit", "SELECT * FROM t LIMIT 10", "SELECT * FROM t LIMIT 10"},
		{"join", "SELECT * FROM a JOIN b ON a.id = b.a_id", "SELECT * FROM a JOIN b ON a.id = b.a_id"},
		{"inner join normalizes", "SELECT * FROM a INNER JOIN b ON a.id = b.a_id", "SELECT * FROM a JOIN b ON a.id = b.a_id"},
		{"left join", "SELECT * FROM a LEFT JOIN b ON a.id = b.id", "SELECT * FROM a LEFT JOIN b ON a.id = b.id"},
		{"left outer join normalizes", "SELECT * FROM a LEFT OUTER JOIN b ON a.id = b.id", "SELECT * FROM a LEFT JOIN b ON a.id = b.id"},
		{"qualified star", "SELECT t.* FROM t", "SELECT t.* FROM t"},
		{"case insensitive keywords", "select * from t", "SELECT * FROM t"},
		{"trailing semicolon", "SELECT * FROM t;", "SELECT * FROM t"},
		{"negative literal", "SELECT * FROM t WHERE x = -5", "SELECT * FROM t WHERE x = -5"},
		{"boolean literal", "SELECT * FROM t WHERE active = TRUE", "SELECT * FROM t WHERE active = TRUE"},
		{"null literal", "SELECT * FROM t WHERE deleted_at = NULL", "SELECT * FROM t WHERE deleted_at = NULL"},
		{
			"full kitchen-sink query",
			"SELECT u.name, COUNT(*) AS orders FROM users u JOIN orders o ON u.id = o.user_id WHERE o.amount > 100 AND u.active = TRUE GROUP BY u.name ORDER BY orders DESC LIMIT 5",
			"SELECT u.name, COUNT(*) AS orders FROM users AS u JOIN orders AS o ON u.id = o.user_id WHERE o.amount > 100 AND u.active = TRUE GROUP BY u.name ORDER BY orders DESC LIMIT 5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.in)
			require.NoError(t, err)
			require.NotNil(t, stmt)
			assert.Equal(t, tt.want, stmt.String(), "pretty-print mismatch")

			// Round-trip: the canonical form must re-parse to an equal AST.
			reparsed, err := Parse(tt.want)
			require.NoError(t, err, "canonical form must re-parse")
			assert.Equal(t, stmt, reparsed, "AST not stable under round-trip")
		})
	}
}

// TestParse_ASTShape pins the concrete tree for one query, so a refactor that
// silently changes structure (not just the printed string) is caught.
func TestParse_ASTShape(t *testing.T) {
	stmt, err := Parse("SELECT amount FROM orders WHERE amount > 100 LIMIT 5")
	require.NoError(t, err)

	limit := int64(5)
	want := &ast.SelectStatement{
		Columns: []ast.SelectItem{{Expr: &ast.Identifier{Name: "amount"}}},
		From:    &ast.TableRef{Name: "orders"},
		Where: &ast.BinaryExpr{
			Op:    ast.OpGt,
			Left:  &ast.Identifier{Name: "amount"},
			Right: &ast.IntegerLiteral{Value: 100},
		},
		Limit: &limit,
	}
	assert.Equal(t, want, stmt)
}

func TestParse_Errors(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantLine  int
		wantCol   int
		msgSubstr string
	}{
		{"empty input", "", 1, 1, "expected SELECT"},
		{"not a select", "DELETE FROM t", 1, 1, "expected SELECT"},
		{"select then eof", "SELECT", 1, 7, "unexpected EOF in expression"},
		{"keyword where column expected", "SELECT FROM t", 1, 8, `unexpected "FROM"`},
		{"missing from table", "SELECT * FROM", 1, 14, "expected IDENT"},
		{"dangling where", "SELECT a FROM t WHERE", 1, 22, "unexpected EOF in expression"},
		{"dangling operator", "SELECT a FROM t WHERE a >", 1, 26, "unexpected EOF in expression"},
		{"join without on", "SELECT * FROM a JOIN b", 1, 23, "expected ON"},
		{"group without by", "SELECT a FROM t GROUP name", 1, 23, "expected BY"},
		{"non-integer limit", "SELECT * FROM t LIMIT abc", 1, 23, `expected INT, got "abc"`},
		{"unclosed paren", "SELECT * FROM t WHERE (a = 1", 1, 29, "expected )"},
		{"trailing tokens", "SELECT a FROM t bogus extra", 1, 23, "unexpected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.in)
			require.Error(t, err)

			var pe *ParseError
			require.True(t, errors.As(err, &pe), "error should be a *ParseError, got %T", err)
			assert.Equal(t, tt.wantLine, pe.Pos.Line, "line")
			assert.Equal(t, tt.wantCol, pe.Pos.Column, "column")
			assert.Contains(t, pe.Msg, tt.msgSubstr)
		})
	}
}

func TestParse_UnterminatedStringReportsPosition(t *testing.T) {
	_, err := Parse("SELECT * FROM t WHERE name = 'oops")
	require.Error(t, err)
	var pe *ParseError
	require.True(t, errors.As(err, &pe))
	assert.Contains(t, pe.Msg, "unterminated string")
	// The opening quote is at column 30.
	assert.Equal(t, 30, pe.Pos.Column)
}

func TestParse_IllegalCharReportsPosition(t *testing.T) {
	_, err := Parse("SELECT @ FROM t")
	require.Error(t, err)
	var pe *ParseError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 8, pe.Pos.Column)
}

func TestParseError_Format(t *testing.T) {
	pe := &ParseError{Msg: "boom"}
	pe.Pos.Line = 3
	pe.Pos.Column = 12
	assert.Equal(t, "3:12: boom", pe.Error())
}
