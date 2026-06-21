package parser

import (
	"strconv"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/lexer"
)

// Binding powers for the Pratt parser. Higher binds tighter. These mirror SQL
// precedence: OR < AND < NOT < comparison < additive < multiplicative < unary.
const (
	precLowest  = 0
	precOr      = 1
	precAnd     = 2
	precNot     = 3 // NOT is prefix; this is the precedence it parses its operand at
	precCompare = 4 // = != < <= > >=
	precSum     = 5 // + -
	precProduct = 6 // * /
	precPrefix  = 7 // unary minus
)

// infixPrec returns the binding power of an infix operator token, or
// precLowest for tokens that don't continue an expression (which stops the
// precedence-climbing loop).
func infixPrec(t lexer.TokenType) int {
	switch t {
	case lexer.OR:
		return precOr
	case lexer.AND:
		return precAnd
	case lexer.EQ, lexer.NEQ, lexer.LT, lexer.LTE, lexer.GT, lexer.GTE:
		return precCompare
	case lexer.PLUS, lexer.MINUS:
		return precSum
	case lexer.STAR, lexer.SLASH:
		return precProduct
	default:
		return precLowest
	}
}

// infixOperator maps an operator token to its AST operator.
var infixOperator = map[lexer.TokenType]ast.Operator{
	lexer.OR:    ast.OpOr,
	lexer.AND:   ast.OpAnd,
	lexer.EQ:    ast.OpEq,
	lexer.NEQ:   ast.OpNeq,
	lexer.LT:    ast.OpLt,
	lexer.LTE:   ast.OpLte,
	lexer.GT:    ast.OpGt,
	lexer.GTE:   ast.OpGte,
	lexer.PLUS:  ast.OpAdd,
	lexer.MINUS: ast.OpSub,
	lexer.STAR:  ast.OpMul,
	lexer.SLASH: ast.OpDiv,
}

// parseExpression is the precedence-climbing core: parse a prefix expression,
// then keep folding in infix operators whose precedence exceeds minPrec. Each
// operator recurses with its own precedence, which gives left-associativity.
func (p *Parser) parseExpression(minPrec int) (ast.Expression, error) {
	left, err := p.parsePrefix()
	if err != nil {
		return nil, err
	}

	for minPrec < infixPrec(p.cur.Type) {
		op := infixOperator[p.cur.Type]
		opPrec := infixPrec(p.cur.Type)
		p.nextToken()
		right, err := p.parseExpression(opPrec)
		if err != nil {
			return nil, err
		}
		left = &ast.BinaryExpr{Op: op, Left: left, Right: right}
	}
	return left, nil
}

// parsePrefix parses a primary expression or a prefix-operator expression.
func (p *Parser) parsePrefix() (ast.Expression, error) {
	switch p.cur.Type {
	case lexer.INT:
		return p.parseIntegerLiteral()
	case lexer.FLOAT:
		return p.parseFloatLiteral()
	case lexer.STRING:
		lit := &ast.StringLiteral{Value: p.cur.Literal}
		p.nextToken()
		return lit, nil
	case lexer.TRUE, lexer.FALSE:
		lit := &ast.BooleanLiteral{Value: p.curIs(lexer.TRUE)}
		p.nextToken()
		return lit, nil
	case lexer.NULL:
		p.nextToken()
		return &ast.NullLiteral{}, nil
	case lexer.STAR:
		// In prefix position, '*' is the wildcard (SELECT *, COUNT(*)); in
		// infix position it is multiplication, handled by parseExpression.
		p.nextToken()
		return &ast.Star{}, nil
	case lexer.IDENT:
		return p.parseIdentifierOrCall()
	case lexer.MINUS:
		p.nextToken()
		operand, err := p.parseExpression(precPrefix)
		if err != nil {
			return nil, err
		}
		return &ast.UnaryExpr{Op: ast.OpNeg, Operand: operand}, nil
	case lexer.NOT:
		p.nextToken()
		operand, err := p.parseExpression(precNot)
		if err != nil {
			return nil, err
		}
		return &ast.UnaryExpr{Op: ast.OpNot, Operand: operand}, nil
	case lexer.LPAREN:
		p.nextToken()
		inner, err := p.parseExpression(precLowest)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(lexer.RPAREN); err != nil {
			return nil, err
		}
		return inner, nil
	default:
		return nil, p.errorf("unexpected %s in expression", p.describe())
	}
}

func (p *Parser) parseIntegerLiteral() (ast.Expression, error) {
	v, err := strconv.ParseInt(p.cur.Literal, 10, 64)
	if err != nil {
		return nil, p.errorf("invalid integer %q", p.cur.Literal)
	}
	p.nextToken()
	return &ast.IntegerLiteral{Value: v}, nil
}

func (p *Parser) parseFloatLiteral() (ast.Expression, error) {
	v, err := strconv.ParseFloat(p.cur.Literal, 64)
	if err != nil {
		return nil, p.errorf("invalid float %q", p.cur.Literal)
	}
	p.nextToken()
	return &ast.FloatLiteral{Value: v}, nil
}

// parseIdentifierOrCall handles three forms that all begin with an identifier:
// a function call (name "("), a qualified name or wildcard (name "." x), or a
// bare column reference.
func (p *Parser) parseIdentifierOrCall() (ast.Expression, error) {
	name := p.cur.Literal

	if p.peekIs(lexer.LPAREN) {
		return p.parseFunctionCall(name)
	}

	p.nextToken() // consume the identifier
	if !p.curIs(lexer.DOT) {
		return &ast.Identifier{Name: name}, nil
	}

	// Qualified: name "." (ident | "*").
	p.nextToken() // consume '.'
	switch p.cur.Type {
	case lexer.STAR:
		p.nextToken()
		return &ast.Star{Table: name}, nil
	case lexer.IDENT:
		col := p.cur.Literal
		p.nextToken()
		return &ast.Identifier{Table: name, Name: col}, nil
	default:
		return nil, p.errorf("expected column name or * after %q., got %s", name, p.describe())
	}
}

// parseFunctionCall parses "name ( [DISTINCT] [arg, ...] )". The opening name
// token is still current on entry.
func (p *Parser) parseFunctionCall(name string) (ast.Expression, error) {
	p.nextToken() // consume name
	p.nextToken() // consume '('

	call := &ast.FunctionCall{Name: name}
	if p.curIs(lexer.DISTINCT) {
		call.Distinct = true
		p.nextToken()
	}

	if p.curIs(lexer.RPAREN) { // no-argument call, e.g. COUNT()
		p.nextToken()
		return call, nil
	}

	args, err := p.parseExpressionList()
	if err != nil {
		return nil, err
	}
	call.Args = args

	if _, err := p.expect(lexer.RPAREN); err != nil {
		return nil, err
	}
	return call, nil
}
