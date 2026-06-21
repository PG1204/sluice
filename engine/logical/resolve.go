package logical

import (
	"fmt"
	"strings"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/storage"
)

// This file is the semantic core of planning: name resolution and type
// checking. A scope tracks which relations (tables/aliases) and columns are
// visible; a checker walks an expression against a scope, verifying every
// column exists and is unambiguous and that operators get compatible types,
// returning the expression's result type.

// relation is one named input visible in a scope: a table or its alias, plus
// the columns it exposes.
type relation struct {
	name   string
	schema storage.Schema
}

// scope is the set of relations visible at a point in the plan. A bare column
// reference must match exactly one column across all relations; a qualified
// reference (t.col) is restricted to the named relation.
type scope struct {
	relations []relation
}

// resolveColumn finds the column referenced by (table, name). table may be ""
// for an unqualified reference. It errors if the column is unknown or, for an
// unqualified reference, ambiguous across relations.
func (s *scope) resolveColumn(table, name string) (storage.Field, error) {
	var matches []storage.Field
	for _, r := range s.relations {
		if table != "" && !strings.EqualFold(table, r.name) {
			continue
		}
		if i, ok := r.schema.ColumnIndex(name); ok {
			matches = append(matches, r.schema.Fields[i])
		}
	}
	switch len(matches) {
	case 0:
		return storage.Field{}, fmt.Errorf("unknown column %s", qualify(table, name))
	case 1:
		return matches[0], nil
	default:
		return storage.Field{}, fmt.Errorf("ambiguous column %q (qualify it with a table name)", name)
	}
}

func qualify(table, name string) string {
	if table != "" {
		return fmt.Sprintf("%q", table+"."+name)
	}
	return fmt.Sprintf("%q", name)
}

// checker type-checks expressions against a scope. The aggregate flags control
// context-sensitive rules:
//   - allowAgg: whether aggregate function calls are permitted here (true in
//     SELECT/ORDER BY, false in WHERE/JOIN ON).
//   - aggregating: whether the query groups/aggregates, in which case a bare
//     column reference outside an aggregate must be a GROUP BY key.
type checker struct {
	scope       *scope
	allowAgg    bool
	aggregating bool
	groupKeys   []ast.Expression
}

// resolve returns the result type of expr, validating it along the way.
func (c *checker) resolve(expr ast.Expression) (storage.Type, error) {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return storage.TypeInt64, nil
	case *ast.FloatLiteral:
		return storage.TypeFloat64, nil
	case *ast.StringLiteral:
		return storage.TypeString, nil
	case *ast.BooleanLiteral:
		return storage.TypeBool, nil
	case *ast.NullLiteral:
		return storage.TypeNull, nil
	case *ast.Star:
		return storage.TypeNull, fmt.Errorf("'*' is only valid in SELECT or COUNT(*)")
	case *ast.Identifier:
		return c.resolveIdentifier(e)
	case *ast.UnaryExpr:
		return c.resolveUnary(e)
	case *ast.BinaryExpr:
		return c.resolveBinary(e)
	case *ast.FunctionCall:
		return c.resolveCall(e)
	default:
		return storage.TypeNull, fmt.Errorf("unsupported expression %T", expr)
	}
}

func (c *checker) resolveIdentifier(e *ast.Identifier) (storage.Type, error) {
	if c.aggregating && !c.isGroupKey(e) {
		return storage.TypeNull, fmt.Errorf(
			"column %s must appear in GROUP BY or be used in an aggregate", qualify(e.Table, e.Name))
	}
	f, err := c.scope.resolveColumn(e.Table, e.Name)
	if err != nil {
		return storage.TypeNull, err
	}
	return f.Type, nil
}

func (c *checker) resolveUnary(e *ast.UnaryExpr) (storage.Type, error) {
	t, err := c.resolve(e.Operand)
	if err != nil {
		return storage.TypeNull, err
	}
	switch e.Op {
	case ast.OpNot:
		if !isBoolish(t) {
			return storage.TypeNull, fmt.Errorf("NOT requires a boolean, got %s", t)
		}
		return storage.TypeBool, nil
	case ast.OpNeg:
		if !isNumericOrNull(t) {
			return storage.TypeNull, fmt.Errorf("unary minus requires a number, got %s", t)
		}
		return t, nil
	default:
		return storage.TypeNull, fmt.Errorf("unsupported unary operator %s", e.Op)
	}
}

func (c *checker) resolveBinary(e *ast.BinaryExpr) (storage.Type, error) {
	lt, err := c.resolve(e.Left)
	if err != nil {
		return storage.TypeNull, err
	}
	rt, err := c.resolve(e.Right)
	if err != nil {
		return storage.TypeNull, err
	}

	switch e.Op {
	case ast.OpAnd, ast.OpOr:
		if !isBoolish(lt) || !isBoolish(rt) {
			return storage.TypeNull, fmt.Errorf("%s requires booleans, got %s and %s", e.Op, lt, rt)
		}
		return storage.TypeBool, nil
	case ast.OpEq, ast.OpNeq, ast.OpLt, ast.OpLte, ast.OpGt, ast.OpGte:
		if !comparable(lt, rt) {
			return storage.TypeNull, fmt.Errorf("cannot compare %s with %s", lt, rt)
		}
		return storage.TypeBool, nil
	case ast.OpAdd, ast.OpSub, ast.OpMul, ast.OpDiv:
		if !isNumericOrNull(lt) || !isNumericOrNull(rt) {
			return storage.TypeNull, fmt.Errorf("%s requires numbers, got %s and %s", e.Op, lt, rt)
		}
		return numericResult(lt, rt), nil
	default:
		return storage.TypeNull, fmt.Errorf("unsupported operator %s", e.Op)
	}
}

// resolveCall type-checks an aggregate function call. Sluice has no scalar
// functions, so any call must be a known aggregate.
func (c *checker) resolveCall(e *ast.FunctionCall) (storage.Type, error) {
	if !isAggregateFunc(e.Name) {
		return storage.TypeNull, fmt.Errorf("unknown function %q", e.Name)
	}
	if !c.allowAgg {
		return storage.TypeNull, fmt.Errorf("aggregate %s is not allowed here", strings.ToUpper(e.Name))
	}
	return aggregateResultType(e, c.scope)
}

// isGroupKey reports whether identifier e refers to the same column as one of
// the GROUP BY keys. Group keys that are plain identifiers are compared by the
// column they resolve to; non-identifier keys are compared structurally.
func (c *checker) isGroupKey(e *ast.Identifier) bool {
	for _, gk := range c.groupKeys {
		gid, ok := gk.(*ast.Identifier)
		if !ok {
			continue
		}
		if sameColumn(c.scope, e, gid) {
			return true
		}
	}
	return false
}

// sameColumn reports whether two identifiers resolve to the same column. It
// compares the relation each belongs to and the column name, so "amount" and
// "o.amount" match when there is a single relation o exposing amount.
func sameColumn(s *scope, a, b *ast.Identifier) bool {
	ra, na, oka := locate(s, a)
	rb, nb, okb := locate(s, b)
	return oka && okb && ra == rb && strings.EqualFold(na, nb)
}

// locate returns the index of the relation owning the identifier and the
// canonical column name, or ok=false if it cannot be resolved unambiguously.
func locate(s *scope, id *ast.Identifier) (relIdx int, colName string, ok bool) {
	found := -1
	var name string
	for i, r := range s.relations {
		if id.Table != "" && !strings.EqualFold(id.Table, r.name) {
			continue
		}
		if ci, exists := r.schema.ColumnIndex(id.Name); exists {
			if found != -1 {
				return 0, "", false // ambiguous
			}
			found = i
			name = r.schema.Fields[ci].Name
		}
	}
	if found == -1 {
		return 0, "", false
	}
	return found, name, true
}

// --- aggregate helpers ---

// isAggregateFunc reports whether name is one of the supported aggregates.
func isAggregateFunc(name string) bool {
	switch strings.ToUpper(name) {
	case "COUNT", "SUM", "AVG", "MIN", "MAX":
		return true
	default:
		return false
	}
}

// aggregateResultType validates an aggregate call's arguments and returns its
// result type. Argument expressions are checked in a non-aggregating context
// (no nested aggregates).
func aggregateResultType(call *ast.FunctionCall, s *scope) (storage.Type, error) {
	name := strings.ToUpper(call.Name)

	// COUNT(*) is the only call that takes a Star, and it always yields INT64.
	if name == "COUNT" && len(call.Args) == 1 {
		if _, isStar := call.Args[0].(*ast.Star); isStar {
			return storage.TypeInt64, nil
		}
	}

	if len(call.Args) != 1 {
		return storage.TypeNull, fmt.Errorf("%s expects exactly one argument", name)
	}

	argChecker := &checker{scope: s, allowAgg: false}
	argType, err := argChecker.resolve(call.Args[0])
	if err != nil {
		return storage.TypeNull, err
	}

	switch name {
	case "COUNT":
		return storage.TypeInt64, nil
	case "SUM":
		if !isNumericOrNull(argType) {
			return storage.TypeNull, fmt.Errorf("SUM requires a number, got %s", argType)
		}
		if argType == storage.TypeFloat64 {
			return storage.TypeFloat64, nil
		}
		return storage.TypeInt64, nil
	case "AVG":
		if !isNumericOrNull(argType) {
			return storage.TypeNull, fmt.Errorf("AVG requires a number, got %s", argType)
		}
		return storage.TypeFloat64, nil
	case "MIN", "MAX":
		if !isOrderable(argType) {
			return storage.TypeNull, fmt.Errorf("%s requires an orderable value, got %s", name, argType)
		}
		return argType, nil
	default:
		return storage.TypeNull, fmt.Errorf("unknown aggregate %q", call.Name)
	}
}

// --- type compatibility helpers ---

func isBoolish(t storage.Type) bool { return t == storage.TypeBool || t == storage.TypeNull }

func isNumericOrNull(t storage.Type) bool {
	return t == storage.TypeInt64 || t == storage.TypeFloat64 || t == storage.TypeNull
}

func isOrderable(t storage.Type) bool {
	return t == storage.TypeInt64 || t == storage.TypeFloat64 || t == storage.TypeString
}

// comparable reports whether two types may be compared. NULL compares with
// anything; otherwise both must be numeric, both string, or both boolean.
func comparable(a, b storage.Type) bool {
	if a == storage.TypeNull || b == storage.TypeNull {
		return true
	}
	if isNumericOrNull(a) && isNumericOrNull(b) {
		return true
	}
	return a == b
}

// numericResult is the type of an arithmetic expression: FLOAT64 if either
// side is float, INT64 if both are integers, NULL only if both are NULL.
func numericResult(a, b storage.Type) storage.Type {
	if a == storage.TypeFloat64 || b == storage.TypeFloat64 {
		return storage.TypeFloat64
	}
	if a == storage.TypeNull && b == storage.TypeNull {
		return storage.TypeNull
	}
	return storage.TypeInt64
}
