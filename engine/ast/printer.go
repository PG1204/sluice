package ast

import (
	"strconv"
	"strings"
)

// This file holds the String() implementations that render an AST back to SQL.
//
// Expression printing is precedence-aware: a subexpression is wrapped in
// parentheses only when its operator binds more loosely than its parent's, so
// the output is both correct and minimally parenthesized. That keeps the
// pretty-printer useful as a debugging tool and lets tests assert exact,
// readable strings.

func (s *SelectStatement) String() string {
	var b strings.Builder
	b.WriteString("SELECT ")
	if s.Distinct {
		b.WriteString("DISTINCT ")
	}

	items := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		items[i] = c.String()
	}
	b.WriteString(strings.Join(items, ", "))

	if s.From != nil {
		b.WriteString(" FROM ")
		b.WriteString(s.From.String())
	}
	for _, j := range s.Joins {
		b.WriteString(j.String())
	}
	if s.Where != nil {
		b.WriteString(" WHERE ")
		b.WriteString(s.Where.String())
	}
	if len(s.GroupBy) > 0 {
		b.WriteString(" GROUP BY ")
		b.WriteString(joinExprs(s.GroupBy, ", "))
	}
	if len(s.OrderBy) > 0 {
		b.WriteString(" ORDER BY ")
		terms := make([]string, len(s.OrderBy))
		for i, o := range s.OrderBy {
			terms[i] = o.String()
		}
		b.WriteString(strings.Join(terms, ", "))
	}
	if s.Limit != nil {
		b.WriteString(" LIMIT ")
		b.WriteString(strconv.FormatInt(*s.Limit, 10))
	}
	return b.String()
}

func (c SelectItem) String() string {
	if c.Alias != "" {
		return c.Expr.String() + " AS " + c.Alias
	}
	return c.Expr.String()
}

func (t *TableRef) String() string {
	if t.Alias != "" {
		return t.Name + " AS " + t.Alias
	}
	return t.Name
}

func (j JoinClause) String() string {
	return " " + j.Type.String() + " " + j.Table.String() + " ON " + j.On.String()
}

func (o OrderByItem) String() string {
	if o.Desc {
		return o.Expr.String() + " DESC"
	}
	return o.Expr.String()
}

func (i *Identifier) String() string {
	if i.Table != "" {
		return i.Table + "." + i.Name
	}
	return i.Name
}

func (s *Star) String() string {
	if s.Table != "" {
		return s.Table + ".*"
	}
	return "*"
}

func (i *IntegerLiteral) String() string { return strconv.FormatInt(i.Value, 10) }

func (f *FloatLiteral) String() string { return strconv.FormatFloat(f.Value, 'g', -1, 64) }

func (s *StringLiteral) String() string {
	// Re-escape embedded single quotes by doubling them, matching the lexer.
	return "'" + strings.ReplaceAll(s.Value, "'", "''") + "'"
}

func (b *BooleanLiteral) String() string {
	if b.Value {
		return "TRUE"
	}
	return "FALSE"
}

func (*NullLiteral) String() string { return "NULL" }

func (b *BinaryExpr) String() string {
	parent := b.Op.precedence()
	// Left and right are wrapped only when they bind more loosely than the
	// parent. The right operand is also wrapped on a precedence tie because
	// our binary operators are left-associative (a - (b - c) must keep parens).
	left := parenIf(b.Left, exprPrecedence(b.Left) < parent)
	right := parenIf(b.Right, exprPrecedence(b.Right) <= parent)
	return left + " " + b.Op.String() + " " + right
}

func (u *UnaryExpr) String() string {
	operand := parenIf(u.Operand, exprPrecedence(u.Operand) < u.Op.precedence())
	if u.Op == OpNot {
		return "NOT " + operand
	}
	return u.Op.String() + operand // unary minus, no space
}

func (f *FunctionCall) String() string {
	var b strings.Builder
	b.WriteString(f.Name)
	b.WriteByte('(')
	if f.Distinct {
		b.WriteString("DISTINCT ")
	}
	b.WriteString(joinExprs(f.Args, ", "))
	b.WriteByte(')')
	return b.String()
}

// exprPrecedence reports the binding strength of an expression for printing.
// Primary expressions (literals, identifiers, calls) never need parentheses,
// so they report a value above every operator.
func exprPrecedence(e Expression) int {
	switch n := e.(type) {
	case *BinaryExpr:
		return n.Op.precedence()
	case *UnaryExpr:
		return n.Op.precedence()
	default:
		return 100
	}
}

// parenIf renders e, wrapping it in parentheses when need is true.
func parenIf(e Expression, need bool) string {
	if need {
		return "(" + e.String() + ")"
	}
	return e.String()
}

// joinExprs renders a list of expressions joined by sep.
func joinExprs(exprs []Expression, sep string) string {
	parts := make([]string, len(exprs))
	for i, e := range exprs {
		parts[i] = e.String()
	}
	return strings.Join(parts, sep)
}
