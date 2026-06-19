// Package ast defines the abstract syntax tree produced by the parser.
//
// The hierarchy has three layers: Node (anything printable), Statement (a
// top-level command — currently only SELECT), and Expression (anything that
// evaluates to a value: literals, column references, operators, calls).
//
// Nodes are plain data. Each implements String() to render itself back to SQL,
// which doubles as a debugging aid and a round-trip check in tests.
package ast

// Node is any AST node. String renders the node back into SQL text.
type Node interface {
	String() string
}

// Statement is a top-level SQL command.
type Statement interface {
	Node
	statementNode()
}

// Expression is anything that produces a value.
type Expression interface {
	Node
	expressionNode()
}

// SelectStatement is a SELECT query. Joins is a slice for forward
// compatibility, though Phase 1 parses at most one join.
type SelectStatement struct {
	Distinct bool
	Columns  []SelectItem
	From     *TableRef
	Joins    []JoinClause
	Where    Expression    // nil if no WHERE
	GroupBy  []Expression  // empty if no GROUP BY
	OrderBy  []OrderByItem // empty if no ORDER BY
	Limit    *int64        // nil if no LIMIT
}

func (*SelectStatement) statementNode() {}

// SelectItem is one entry in the projection list: an expression with an
// optional alias (SELECT amount AS total).
type SelectItem struct {
	Expr  Expression
	Alias string // empty if no alias
}

// TableRef names a table in FROM or JOIN, with an optional alias (orders o).
type TableRef struct {
	Name  string
	Alias string // empty if no alias
}

// JoinType distinguishes the join variants. Phase 1 parses the syntax; the
// planner gives them meaning later.
type JoinType int

const (
	// InnerJoin is a plain JOIN / INNER JOIN.
	InnerJoin JoinType = iota
	// LeftJoin is LEFT [OUTER] JOIN.
	LeftJoin
	// RightJoin is RIGHT [OUTER] JOIN.
	RightJoin
	// FullJoin is FULL [OUTER] JOIN.
	FullJoin
)

// String renders the join keyword(s).
func (j JoinType) String() string {
	switch j {
	case InnerJoin:
		return "JOIN"
	case LeftJoin:
		return "LEFT JOIN"
	case RightJoin:
		return "RIGHT JOIN"
	case FullJoin:
		return "FULL JOIN"
	default:
		return "JOIN"
	}
}

// JoinClause is "<type> JOIN <table> ON <predicate>".
type JoinClause struct {
	Type  JoinType
	Table *TableRef
	On    Expression
}

// OrderByItem is one ORDER BY term with its sort direction.
type OrderByItem struct {
	Expr Expression
	Desc bool // false = ASC (default)
}

// --- Expressions ---

// Identifier is a column reference, optionally table-qualified (o.amount).
type Identifier struct {
	Table string // empty if unqualified
	Name  string
}

// Star is the "*" wildcard, optionally table-qualified (t.*). It is valid only
// in a projection list or as the sole argument to COUNT.
type Star struct {
	Table string // empty for a bare *
}

// IntegerLiteral is a whole-number constant.
type IntegerLiteral struct {
	Value int64
}

// FloatLiteral is a floating-point constant.
type FloatLiteral struct {
	Value float64
}

// StringLiteral is a text constant; Value is the decoded (unquoted) string.
type StringLiteral struct {
	Value string
}

// BooleanLiteral is TRUE or FALSE.
type BooleanLiteral struct {
	Value bool
}

// NullLiteral is the NULL constant.
type NullLiteral struct{}

// BinaryExpr is a two-operand operation: comparison, AND/OR, or arithmetic.
type BinaryExpr struct {
	Op    Operator
	Left  Expression
	Right Expression
}

// UnaryExpr is a one-operand operation: NOT or unary minus.
type UnaryExpr struct {
	Op      Operator
	Operand Expression
}

// FunctionCall is a function invocation, e.g. COUNT(*), SUM(amount),
// COUNT(DISTINCT customer_id).
type FunctionCall struct {
	Name     string
	Args     []Expression
	Distinct bool
}

func (*Identifier) expressionNode()     {}
func (*Star) expressionNode()           {}
func (*IntegerLiteral) expressionNode() {}
func (*FloatLiteral) expressionNode()   {}
func (*StringLiteral) expressionNode()  {}
func (*BooleanLiteral) expressionNode() {}
func (*NullLiteral) expressionNode()    {}
func (*BinaryExpr) expressionNode()     {}
func (*UnaryExpr) expressionNode()      {}
func (*FunctionCall) expressionNode()   {}

// Operator identifies a binary or unary operator. Keeping it as an enum (rather
// than a raw string) lets the pretty-printer reason about precedence and keeps
// the parser's operator handling exhaustive.
type Operator int

const (
	_ Operator = iota
	// Logical.
	OpOr
	OpAnd
	OpNot // unary
	// Comparison.
	OpEq
	OpNeq
	OpLt
	OpLte
	OpGt
	OpGte
	// Arithmetic.
	OpAdd
	OpSub
	OpMul
	OpDiv
	OpNeg // unary minus
)

var operatorSymbols = map[Operator]string{
	OpOr:  "OR",
	OpAnd: "AND",
	OpNot: "NOT",
	OpEq:  "=",
	OpNeq: "!=",
	OpLt:  "<",
	OpLte: "<=",
	OpGt:  ">",
	OpGte: ">=",
	OpAdd: "+",
	OpSub: "-",
	OpMul: "*",
	OpDiv: "/",
	OpNeg: "-",
}

// String returns the SQL spelling of the operator.
func (o Operator) String() string {
	if s, ok := operatorSymbols[o]; ok {
		return s
	}
	return "?"
}

// precedence returns the binding strength of an operator for pretty-printing.
// Higher binds tighter. Used to decide when a subexpression needs parentheses.
func (o Operator) precedence() int {
	switch o {
	case OpOr:
		return 1
	case OpAnd:
		return 2
	case OpNot:
		return 3
	case OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte:
		return 4
	case OpAdd, OpSub:
		return 5
	case OpMul, OpDiv:
		return 6
	case OpNeg:
		return 7
	default:
		return 0
	}
}
