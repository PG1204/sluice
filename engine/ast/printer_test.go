package ast

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func col(name string) *Identifier    { return &Identifier{Name: name} }
func qcol(t, n string) *Identifier   { return &Identifier{Table: t, Name: n} }
func intLit(v int64) *IntegerLiteral { return &IntegerLiteral{Value: v} }
func bin(op Operator, l, r Expression) *BinaryExpr {
	return &BinaryExpr{Op: op, Left: l, Right: r}
}

func TestExpression_String(t *testing.T) {
	tests := []struct {
		name string
		expr Expression
		want string
	}{
		{"qualified column", qcol("o", "amount"), "o.amount"},
		{"string literal escapes quotes", &StringLiteral{Value: "it's"}, "'it''s'"},
		{"float", &FloatLiteral{Value: 100.5}, "100.5"},
		{"null", &NullLiteral{}, "NULL"},
		{"bool", &BooleanLiteral{Value: true}, "TRUE"},
		{
			"comparison binds tighter than AND, no parens",
			bin(OpAnd, bin(OpGt, col("a"), intLit(1)), bin(OpLt, col("b"), intLit(2))),
			"a > 1 AND b < 2",
		},
		{
			"OR under AND needs parens",
			bin(OpAnd, bin(OpOr, col("a"), col("b")), col("c")),
			"(a OR b) AND c",
		},
		{
			"left-associative subtraction keeps right parens",
			bin(OpSub, col("a"), bin(OpSub, col("b"), col("c"))),
			"a - (b - c)",
		},
		{
			"multiplication binds tighter than addition",
			bin(OpAdd, col("a"), bin(OpMul, col("b"), col("c"))),
			"a + b * c",
		},
		{
			"NOT wraps a looser OR",
			&UnaryExpr{Op: OpNot, Operand: bin(OpOr, col("a"), col("b"))},
			"NOT (a OR b)",
		},
		{
			"count star",
			&FunctionCall{Name: "COUNT", Args: []Expression{&Star{}}},
			"COUNT(*)",
		},
		{
			"count distinct",
			&FunctionCall{Name: "COUNT", Args: []Expression{col("id")}, Distinct: true},
			"COUNT(DISTINCT id)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.expr.String())
		})
	}
}

func TestSelectStatement_String(t *testing.T) {
	limit := int64(10)
	s := &SelectStatement{
		Distinct: true,
		Columns: []SelectItem{
			{Expr: col("name")},
			{Expr: &FunctionCall{Name: "COUNT", Args: []Expression{&Star{}}}, Alias: "n"},
		},
		From:    &TableRef{Name: "orders", Alias: "o"},
		Where:   bin(OpGt, qcol("o", "amount"), intLit(100)),
		GroupBy: []Expression{col("name")},
		OrderBy: []OrderByItem{{Expr: col("n"), Desc: true}},
		Limit:   &limit,
	}
	want := "SELECT DISTINCT name, COUNT(*) AS n FROM orders AS o " +
		"WHERE o.amount > 100 GROUP BY name ORDER BY n DESC LIMIT 10"
	assert.Equal(t, want, s.String())
}
