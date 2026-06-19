// Package parser is a hand-written recursive-descent parser that turns the
// lexer's token stream into an AST.
//
// Statement structure (SELECT ... FROM ... WHERE ...) is parsed by recursive
// descent: one method per grammar production. Expressions are parsed by
// precedence climbing (a Pratt parser), which handles operator precedence and
// associativity without a separate method per precedence level — far less code
// than the textbook expr/term/factor cascade, and easy to extend.
//
// Errors are values: every production returns (node, error) and the parser
// stops at the first error, reporting the offending token's line and column.
// Fail-fast (rather than error recovery) keeps the parser simple and the
// messages unambiguous, which matters more than multi-error reporting for a
// query engine where queries are short.
package parser

import (
	"fmt"
	"strconv"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/lexer"
)

// ParseError reports a syntax error at a specific source position.
type ParseError struct {
	Pos lexer.Position
	Msg string
}

// Error renders the error as "line:column: message".
func (e *ParseError) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Pos.Line, e.Pos.Column, e.Msg)
}

// Parser holds the lexer and a two-token window (current + lookahead), which
// is all the lookahead this grammar needs.
type Parser struct {
	lex  *lexer.Lexer
	cur  lexer.Token
	peek lexer.Token
}

// New creates a Parser over the given SQL input.
func New(input string) *Parser {
	p := &Parser{lex: lexer.New(input)}
	// Prime cur and peek.
	p.nextToken()
	p.nextToken()
	return p
}

// Parse is the one-shot convenience: parse a single statement from input.
func Parse(input string) (ast.Statement, error) {
	return New(input).ParseStatement()
}

func (p *Parser) nextToken() {
	p.cur = p.peek
	p.peek = p.lex.NextToken()
}

func (p *Parser) curIs(t lexer.TokenType) bool  { return p.cur.Type == t }
func (p *Parser) peekIs(t lexer.TokenType) bool { return p.peek.Type == t }

// errorf builds a ParseError at the current token's position.
func (p *Parser) errorf(format string, args ...any) error {
	return &ParseError{Pos: p.cur.Pos, Msg: fmt.Sprintf(format, args...)}
}

// describe renders the current token for an error message, preferring its
// literal text when it has one ("orders") over its type label (EOF).
func (p *Parser) describe() string {
	if p.cur.Type == lexer.ILLEGAL {
		return p.cur.Literal
	}
	if p.cur.Literal != "" {
		return fmt.Sprintf("%q", p.cur.Literal)
	}
	return p.cur.Type.String()
}

// expect consumes the current token if it has type t, otherwise errors.
func (p *Parser) expect(t lexer.TokenType) (lexer.Token, error) {
	if p.cur.Type != t {
		return lexer.Token{}, p.errorf("expected %s, got %s", t, p.describe())
	}
	tok := p.cur
	p.nextToken()
	return tok, nil
}

// ParseStatement parses exactly one statement and requires the input to end
// (after an optional trailing semicolon) once it is consumed.
func (p *Parser) ParseStatement() (ast.Statement, error) {
	if !p.curIs(lexer.SELECT) {
		return nil, p.errorf("expected SELECT, got %s", p.describe())
	}
	stmt, err := p.parseSelectStatement()
	if err != nil {
		return nil, err
	}
	if p.curIs(lexer.SEMICOLON) {
		p.nextToken()
	}
	if !p.curIs(lexer.EOF) {
		return nil, p.errorf("unexpected %s after statement", p.describe())
	}
	return stmt, nil
}

func (p *Parser) parseSelectStatement() (*ast.SelectStatement, error) {
	if _, err := p.expect(lexer.SELECT); err != nil {
		return nil, err
	}
	stmt := &ast.SelectStatement{}

	if p.curIs(lexer.DISTINCT) {
		stmt.Distinct = true
		p.nextToken()
	}

	cols, err := p.parseSelectItems()
	if err != nil {
		return nil, err
	}
	stmt.Columns = cols

	if _, err := p.expect(lexer.FROM); err != nil {
		return nil, err
	}
	if stmt.From, err = p.parseTableRef(); err != nil {
		return nil, err
	}

	if stmt.Joins, err = p.parseJoins(); err != nil {
		return nil, err
	}

	if p.curIs(lexer.WHERE) {
		p.nextToken()
		if stmt.Where, err = p.parseExpression(precLowest); err != nil {
			return nil, err
		}
	}

	if p.curIs(lexer.GROUP) {
		if stmt.GroupBy, err = p.parseGroupBy(); err != nil {
			return nil, err
		}
	}

	if p.curIs(lexer.ORDER) {
		if stmt.OrderBy, err = p.parseOrderBy(); err != nil {
			return nil, err
		}
	}

	if p.curIs(lexer.LIMIT) {
		if stmt.Limit, err = p.parseLimit(); err != nil {
			return nil, err
		}
	}

	return stmt, nil
}

// parseSelectItems parses the comma-separated projection list.
func (p *Parser) parseSelectItems() ([]ast.SelectItem, error) {
	var items []ast.SelectItem
	for {
		expr, err := p.parseExpression(precLowest)
		if err != nil {
			return nil, err
		}
		item := ast.SelectItem{Expr: expr}

		// Optional alias: "expr AS name" or the implicit "expr name".
		if p.curIs(lexer.AS) {
			p.nextToken()
			alias, err := p.expect(lexer.IDENT)
			if err != nil {
				return nil, err
			}
			item.Alias = alias.Literal
		} else if p.curIs(lexer.IDENT) {
			item.Alias = p.cur.Literal
			p.nextToken()
		}

		items = append(items, item)
		if !p.curIs(lexer.COMMA) {
			return items, nil
		}
		p.nextToken()
	}
}

// parseTableRef parses "name [AS alias]" or "name alias".
func (p *Parser) parseTableRef() (*ast.TableRef, error) {
	name, err := p.expect(lexer.IDENT)
	if err != nil {
		return nil, err
	}
	ref := &ast.TableRef{Name: name.Literal}

	if p.curIs(lexer.AS) {
		p.nextToken()
		alias, err := p.expect(lexer.IDENT)
		if err != nil {
			return nil, err
		}
		ref.Alias = alias.Literal
	} else if p.curIs(lexer.IDENT) {
		ref.Alias = p.cur.Literal
		p.nextToken()
	}
	return ref, nil
}

// parseJoins parses zero or more JOIN clauses. The build plan scopes Phase 1
// to a single join; we accept a sequence because the loop is no more code and
// the AST already carries a slice, so nothing here needs to change later.
func (p *Parser) parseJoins() ([]ast.JoinClause, error) {
	var joins []ast.JoinClause
	for {
		jt, ok, err := p.parseJoinType()
		if err != nil {
			return nil, err
		}
		if !ok {
			return joins, nil
		}

		table, err := p.parseTableRef()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(lexer.ON); err != nil {
			return nil, err
		}
		on, err := p.parseExpression(precLowest)
		if err != nil {
			return nil, err
		}
		joins = append(joins, ast.JoinClause{Type: jt, Table: table, On: on})
	}
}

// parseJoinType consumes a join keyword sequence if one starts here, returning
// the join type and ok=true. If the current token doesn't begin a join, it
// returns ok=false without consuming anything.
func (p *Parser) parseJoinType() (ast.JoinType, bool, error) {
	switch p.cur.Type {
	case lexer.JOIN:
		p.nextToken()
		return ast.InnerJoin, true, nil
	case lexer.INNER:
		p.nextToken()
		_, err := p.expect(lexer.JOIN)
		return ast.InnerJoin, true, err
	case lexer.LEFT, lexer.RIGHT, lexer.FULL:
		kind := p.cur.Type
		p.nextToken()
		if p.curIs(lexer.OUTER) { // OUTER is noise; LEFT JOIN == LEFT OUTER JOIN
			p.nextToken()
		}
		if _, err := p.expect(lexer.JOIN); err != nil {
			return 0, true, err
		}
		switch kind {
		case lexer.LEFT:
			return ast.LeftJoin, true, nil
		case lexer.RIGHT:
			return ast.RightJoin, true, nil
		default:
			return ast.FullJoin, true, nil
		}
	default:
		return 0, false, nil
	}
}

func (p *Parser) parseGroupBy() ([]ast.Expression, error) {
	p.nextToken() // GROUP
	if _, err := p.expect(lexer.BY); err != nil {
		return nil, err
	}
	return p.parseExpressionList()
}

func (p *Parser) parseOrderBy() ([]ast.OrderByItem, error) {
	p.nextToken() // ORDER
	if _, err := p.expect(lexer.BY); err != nil {
		return nil, err
	}
	var items []ast.OrderByItem
	for {
		expr, err := p.parseExpression(precLowest)
		if err != nil {
			return nil, err
		}
		item := ast.OrderByItem{Expr: expr}
		switch p.cur.Type {
		case lexer.ASC:
			p.nextToken()
		case lexer.DESC:
			item.Desc = true
			p.nextToken()
		}
		items = append(items, item)
		if !p.curIs(lexer.COMMA) {
			return items, nil
		}
		p.nextToken()
	}
}

func (p *Parser) parseLimit() (*int64, error) {
	p.nextToken() // LIMIT
	tok, err := p.expect(lexer.INT)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(tok.Literal, 10, 64)
	if err != nil {
		return nil, &ParseError{Pos: tok.Pos, Msg: fmt.Sprintf("invalid LIMIT value %q", tok.Literal)}
	}
	return &n, nil
}

// parseExpressionList parses a comma-separated list of expressions (used by
// GROUP BY and, internally, function arguments).
func (p *Parser) parseExpressionList() ([]ast.Expression, error) {
	var exprs []ast.Expression
	for {
		expr, err := p.parseExpression(precLowest)
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, expr)
		if !p.curIs(lexer.COMMA) {
			return exprs, nil
		}
		p.nextToken()
	}
}
