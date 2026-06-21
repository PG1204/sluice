package physical

import (
	"fmt"
	"strings"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/storage"
)

// A layout is an operator's output columns with their source qualifier
// (table/alias), which storage.Schema alone doesn't carry. It is how a column
// reference in an expression — qualified (o.amount) or bare (amount) — is bound
// to a column index at translation time. The logical planner already validated
// these references; here we resolve them to positions for execution.
type colInfo struct {
	qualifier string // table or alias the column came from; "" if none
	name      string
	typ       storage.Type
}

type layout struct {
	cols []colInfo
}

// indexOf resolves a (table, name) reference to a column index and type. A
// qualified reference is restricted to columns from that relation; a bare
// reference must be unambiguous.
func (l layout) indexOf(table, name string) (int, storage.Type, error) {
	found := -1
	for i, c := range l.cols {
		if table != "" && !strings.EqualFold(table, c.qualifier) {
			continue
		}
		if strings.EqualFold(name, c.name) {
			if found != -1 {
				return 0, storage.TypeNull, fmt.Errorf("ambiguous column %q", name)
			}
			found = i
		}
	}
	if found == -1 {
		return 0, storage.TypeNull, fmt.Errorf("unknown column %q", name)
	}
	return found, l.cols[found].typ, nil
}

// schema projects the layout down to a storage.Schema (names + types).
func (l layout) schema() storage.Schema {
	fields := make([]storage.Field, len(l.cols))
	for i, c := range l.cols {
		fields[i] = storage.Field{Name: c.name, Type: c.typ}
	}
	return storage.Schema{Fields: fields}
}

// evalExpr is a compiled, column-bound expression evaluated per row.
type evalExpr interface {
	eval(b *storage.Batch, row int) Value
	typ() storage.Type
}

// compileExpr binds an AST expression to column positions in the given layout
// and returns an evaluable form. Aggregate calls are bound by name to the
// column the aggregate produced (the layout of an Aggregate operator names each
// aggregate by its printed form, e.g. "COUNT(*)").
func compileExpr(expr ast.Expression, l layout) (evalExpr, error) {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return litExpr{Int(e.Value)}, nil
	case *ast.FloatLiteral:
		return litExpr{Float(e.Value)}, nil
	case *ast.StringLiteral:
		return litExpr{Str(e.Value)}, nil
	case *ast.BooleanLiteral:
		return litExpr{Bool(e.Value)}, nil
	case *ast.NullLiteral:
		return litExpr{Null()}, nil
	case *ast.Identifier:
		idx, t, err := l.indexOf(e.Table, e.Name)
		if err != nil {
			return nil, err
		}
		return colRef{idx: idx, t: t}, nil
	case *ast.FunctionCall:
		// An aggregate appears here only after a HashAggregate has computed it;
		// bind it to that output column by its printed name.
		idx, t, err := l.indexOf("", e.String())
		if err != nil {
			return nil, fmt.Errorf("aggregate %s not available here: %w", e.String(), err)
		}
		return colRef{idx: idx, t: t}, nil
	case *ast.UnaryExpr:
		operand, err := compileExpr(e.Operand, l)
		if err != nil {
			return nil, err
		}
		return unaryExpr{op: e.Op, operand: operand, t: operand.typ()}, nil
	case *ast.BinaryExpr:
		left, err := compileExpr(e.Left, l)
		if err != nil {
			return nil, err
		}
		right, err := compileExpr(e.Right, l)
		if err != nil {
			return nil, err
		}
		return binExpr{op: e.Op, left: left, right: right, t: binaryResultType(e.Op, left.typ(), right.typ())}, nil
	default:
		return nil, fmt.Errorf("cannot evaluate expression %T", expr)
	}
}

// binaryResultType is the type a binary operation yields: BOOL for logical and
// comparison operators, otherwise FLOAT64 if either operand is float, else
// INT64 (matching the logical planner's rules).
func binaryResultType(op ast.Operator, lt, rt storage.Type) storage.Type {
	switch op {
	case ast.OpAnd, ast.OpOr, ast.OpEq, ast.OpNeq, ast.OpLt, ast.OpLte, ast.OpGt, ast.OpGte:
		return storage.TypeBool
	default:
		if lt == storage.TypeFloat64 || rt == storage.TypeFloat64 {
			return storage.TypeFloat64
		}
		return storage.TypeInt64
	}
}

// --- evalExpr implementations ---

type litExpr struct{ v Value }

func (e litExpr) eval(*storage.Batch, int) Value { return e.v }
func (e litExpr) typ() storage.Type              { return e.v.Kind }

type colRef struct {
	idx int
	t   storage.Type
}

func (e colRef) eval(b *storage.Batch, row int) Value { return columnValue(b.Columns[e.idx], row) }
func (e colRef) typ() storage.Type                    { return e.t }

type unaryExpr struct {
	op      ast.Operator
	operand evalExpr
	t       storage.Type
}

func (e unaryExpr) typ() storage.Type { return e.t }

func (e unaryExpr) eval(b *storage.Batch, row int) Value {
	v := e.operand.eval(b, row)
	if v.IsNull() {
		return Null()
	}
	switch e.op {
	case ast.OpNot:
		return Bool(!v.B)
	case ast.OpNeg:
		if v.Kind == storage.TypeInt64 {
			return Int(-v.I)
		}
		return Float(-v.F)
	default:
		return Null()
	}
}

type binExpr struct {
	op          ast.Operator
	left, right evalExpr
	t           storage.Type
}

func (e binExpr) typ() storage.Type { return e.t }

func (e binExpr) eval(b *storage.Batch, row int) Value {
	// AND/OR implement SQL three-valued logic, which can short-circuit to a
	// definite answer even when one side is NULL.
	switch e.op {
	case ast.OpAnd:
		return evalAnd(e.left.eval(b, row), e.right.eval(b, row))
	case ast.OpOr:
		return evalOr(e.left.eval(b, row), e.right.eval(b, row))
	}

	l := e.left.eval(b, row)
	r := e.right.eval(b, row)
	if l.IsNull() || r.IsNull() {
		return Null()
	}

	switch e.op {
	case ast.OpEq, ast.OpNeq, ast.OpLt, ast.OpLte, ast.OpGt, ast.OpGte:
		return Bool(compareOp(e.op, compareValues(l, r)))
	default:
		return evalArith(e.op, l, r, e.t)
	}
}

func evalAnd(l, r Value) Value {
	if (!l.IsNull() && l.Kind == storage.TypeBool && !l.B) ||
		(!r.IsNull() && r.Kind == storage.TypeBool && !r.B) {
		return Bool(false) // FALSE AND anything is FALSE
	}
	if l.IsNull() || r.IsNull() {
		return Null()
	}
	return Bool(l.B && r.B)
}

func evalOr(l, r Value) Value {
	if (!l.IsNull() && l.Kind == storage.TypeBool && l.B) ||
		(!r.IsNull() && r.Kind == storage.TypeBool && r.B) {
		return Bool(true) // TRUE OR anything is TRUE
	}
	if l.IsNull() || r.IsNull() {
		return Null()
	}
	return Bool(l.B || r.B)
}

// compareOp maps a comparison operator and a three-way compare result to a bool.
func compareOp(op ast.Operator, cmp int) bool {
	switch op {
	case ast.OpEq:
		return cmp == 0
	case ast.OpNeq:
		return cmp != 0
	case ast.OpLt:
		return cmp < 0
	case ast.OpLte:
		return cmp <= 0
	case ast.OpGt:
		return cmp > 0
	case ast.OpGte:
		return cmp >= 0
	default:
		return false
	}
}

// evalArith computes an arithmetic operation. Division by zero yields NULL
// rather than panicking or producing an infinity.
func evalArith(op ast.Operator, l, r Value, resultType storage.Type) Value {
	if resultType == storage.TypeInt64 {
		switch op {
		case ast.OpAdd:
			return Int(l.I + r.I)
		case ast.OpSub:
			return Int(l.I - r.I)
		case ast.OpMul:
			return Int(l.I * r.I)
		case ast.OpDiv:
			if r.I == 0 {
				return Null()
			}
			return Int(l.I / r.I)
		}
	}
	lf, rf := l.asFloat(), r.asFloat()
	switch op {
	case ast.OpAdd:
		return Float(lf + rf)
	case ast.OpSub:
		return Float(lf - rf)
	case ast.OpMul:
		return Float(lf * rf)
	case ast.OpDiv:
		if rf == 0 {
			return Null()
		}
		return Float(lf / rf)
	}
	return Null()
}

// isTrue reports whether a predicate value is definitely TRUE (NULL and FALSE
// are both treated as "not true" by filters and joins).
func isTrue(v Value) bool {
	return !v.IsNull() && v.Kind == storage.TypeBool && v.B
}
