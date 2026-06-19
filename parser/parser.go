package parser

import (
	"fmt"
	"strconv"

	"aurasql/core" // Use the precise import path from your brief
)

// Parser consumes tokens from a Lexer and constructs a core.Statement AST.
type Parser struct {
	lexer   *Lexer
	currTok Token
	peekTok Token
}

// NewParser initializes a parser with a given SQL string.
func NewParser(sql string) *Parser {
	p := &Parser{lexer: NewLexer(sql)}
	// Read two tokens to populate both currTok and peekTok
	p.nextToken()
	p.nextToken()
	return p
}

func (p *Parser) nextToken() {
	p.currTok = p.peekTok
	p.peekTok = p.lexer.NextToken()
}

// Parse is the entrypoint function required by your track contract.
func Parse(sql string) (core.Statement, error) {
	p := NewParser(sql)
	return p.parseStatement()
}

// parseStatement inspects the initial keyword and routes to the correct parsing logic.
func (p *Parser) parseStatement() (core.Statement, error) {
	if p.currTok.Type == TokenError {
		return nil, fmt.Errorf("lexer error: %s", p.currTok.Value)
	}

	switch p.currTok.Type {
	case TokenCreate:
		return p.parseCreateTable()
	case TokenInsert:
		return p.parseInsert()
	case TokenSelect:
		return p.parseSelect()
	// Future hooks for Phase 1:

	// case TokenSelect: return p.parseSelect()
	default:
		return nil, fmt.Errorf("unexpected statement starting with %q", p.currTok.Value)
	}
}

// parseCreateTable handles: CREATE TABLE <table_name> (col1 TYPE, col2 TYPE, ...)
func (p *Parser) parseCreateTable() (core.Statement, error) {
	p.nextToken() // consume CREATE

	if p.currTok.Type != TokenTable {
		return nil, fmt.Errorf("expected TABLE, got %q", p.currTok.Value)
	}
	p.nextToken() // consume TABLE

	if p.currTok.Type != TokenIdentifier {
		return nil, fmt.Errorf("expected table name, got %q", p.currTok.Value)
	}
	tableName := p.currTok.Value
	p.nextToken() // consume table name

	if p.currTok.Type != TokenOpenParen {
		return nil, fmt.Errorf("expected '(' after table name, got %q", p.currTok.Value)
	}
	p.nextToken() // consume '('

	var columns []core.Column

	// Parse column definitions comma-separated list
	for {
		if p.currTok.Type != TokenIdentifier {
			return nil, fmt.Errorf("expected column name, got %q", p.currTok.Value)
		}
		colName := p.currTok.Value
		p.nextToken() // consume column name

		var colType core.ColumnType
		switch p.currTok.Type {
		case TokenIntType:
			colType = core.TypeInt
		case TokenTextType:
			colType = core.TypeText
		case TokenBoolType:
			colType = core.TypeBool
		default:
			return nil, fmt.Errorf("unknown column type %q for column %q", p.currTok.Value, colName)
		}
		p.nextToken() // consume column type

		columns = append(columns, core.Column{Name: colName, Type: colType})

		if p.currTok.Type == TokenComma {
			p.nextToken() // consume ',' and keep looping
			continue
		}
		break
	}

	if p.currTok.Type != TokenCloseParen {
		return nil, fmt.Errorf("expected ')' at end of column definitions, got %q", p.currTok.Value)
	}
	p.nextToken() // consume ')'

	if p.currTok.Type != TokenEOF {
		return nil, fmt.Errorf("unexpected tokens after statement end: %q", p.currTok.Value)
	}

	return &core.CreateTableStmt{
		Table:   tableName,
		Columns: columns,
	}, nil
}

// parseInsert handles: INSERT INTO <table_name> VALUES (val1, val2, ...)
// parseInsert handles: INSERT INTO <table_name> VALUES (val1, val2, ...)
func (p *Parser) parseInsert() (core.Statement, error) {
	p.nextToken() // consume INSERT

	if p.currTok.Type != TokenInto {
		return nil, fmt.Errorf("expected INTO, got %q", p.currTok.Value)
	}
	p.nextToken() // consume INTO

	if p.currTok.Type != TokenIdentifier {
		return nil, fmt.Errorf("expected table name, got %q", p.currTok.Value)
	}
	tableName := p.currTok.Value
	p.nextToken() // consume table name

	if p.currTok.Type != TokenValues {
		return nil, fmt.Errorf("expected VALUES, got %q", p.currTok.Value)
	}
	p.nextToken() // consume VALUES

	if p.currTok.Type != TokenOpenParen {
		return nil, fmt.Errorf("expected '(' before values list, got %q", p.currTok.Value)
	}
	p.nextToken() // consume '('

	var values []core.Expr

	// Parse comma-separated value expressions
	for {
		expr, err := p.parsePrimaryExpr()
		if err != nil {
			return nil, err
		}

		// --- FIX: Ensure the expression is strictly a literal, not a column reference ---
		if _, ok := expr.(*core.Literal); !ok {
			return nil, fmt.Errorf("INSERT values must be constant literals, got identifier or expression")
		}

		values = append(values, expr)

		if p.currTok.Type == TokenComma {
			p.nextToken() // consume ',' and keep looping
			continue
		}
		break
	}

	if p.currTok.Type != TokenCloseParen {
		return nil, fmt.Errorf("expected ')' after values list, got %q", p.currTok.Value)
	}
	p.nextToken() // consume ')'

	if p.currTok.Type != TokenEOF {
		return nil, fmt.Errorf("unexpected tokens after statement end: %q", p.currTok.Value)
	}

	return &core.InsertStmt{
		Table:  tableName,
		Values: values,
	}, nil
}

// parseSelect handles: SELECT (* | col1, col2) FROM table [WHERE expr]
func (p *Parser) parseSelect() (core.Statement, error) {
	p.nextToken() // consume SELECT

	var projection []core.Expr

	// Check if it's a SELECT *
	if p.currTok.Type == TokenStar {
		projection = append(projection, &core.Star{})
		p.nextToken() // consume '*'
	} else {
		// Parse comma-separated projection column expressions
		for {
			if p.currTok.Type != TokenIdentifier {
				return nil, fmt.Errorf("expected column name in projection, got %q", p.currTok.Value)
			}
			projection = append(projection, &core.ColumnRef{Name: p.currTok.Value})
			p.nextToken() // consume column identifier

			if p.currTok.Type == TokenComma {
				p.nextToken() // consume ','
				continue
			}
			break
		}
	}

	if p.currTok.Type != TokenFrom {
		return nil, fmt.Errorf("expected FROM, got %q", p.currTok.Value)
	}
	p.nextToken() // consume FROM

	if p.currTok.Type != TokenIdentifier {
		return nil, fmt.Errorf("expected table name after FROM, got %q", p.currTok.Value)
	}
	tableName := p.currTok.Value
	p.nextToken() // consume table name

	var whereExpr core.Expr
	if p.currTok.Type == TokenWhere {
		p.nextToken() // consume WHERE
		var err error
		whereExpr, err = p.parseExpression()
		if err != nil {
			return nil, err
		}
	}

	if p.currTok.Type != TokenEOF {
		return nil, fmt.Errorf("unexpected tokens at end of SELECT statement: %q", p.currTok.Value)
	}

	return &core.SelectStmt{
		Projection: projection,
		From:       tableName,
		Join:       nil, // explicit Phase 1 requirement to leave nil
		Where:      whereExpr,
	}, nil
}

// --- Expression Parsing Engine (Precedence Order: OR -> AND -> Comparisons -> Primary Literals) ---

func (p *Parser) parseExpression() (core.Expr, error) {
	return p.parseOr()
}

func (p *Parser) parseOr() (core.Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for p.currTok.Type == TokenOr {
		p.nextToken() // consume OR
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &core.BinaryExpr{Op: core.OpOr, Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseAnd() (core.Expr, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}

	for p.currTok.Type == TokenAnd {
		p.nextToken() // consume AND
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		left = &core.BinaryExpr{Op: core.OpAnd, Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseComparison() (core.Expr, error) {
	left, err := p.parsePrimaryExpr()
	if err != nil {
		return nil, err
	}

	var op core.BinOp
	switch p.currTok.Type {
	case TokenEq:
		op = core.OpEq
	case TokenNe:
		op = core.OpNe
	case TokenLt:
		op = core.OpLt
	case TokenLe:
		op = core.OpLe
	case TokenGt:
		op = core.OpGt
	case TokenGe:
		op = core.OpGe
	default:
		return left, nil // No comparison operator found; return bare primary (e.g., bare booleans)
	}

	p.nextToken() // consume operator
	right, err := p.parsePrimaryExpr()
	if err != nil {
		return nil, err
	}

	return &core.BinaryExpr{Op: op, Left: left, Right: right}, nil
}

// parsePrimaryExpr parses basic units: integers, text, booleans, or column identifiers
func (p *Parser) parsePrimaryExpr() (core.Expr, error) {
	switch p.currTok.Type {
	case TokenIdentifier:
		name := p.currTok.Value
		p.nextToken() // consume identifier
		return &core.ColumnRef{Name: name}, nil

	case TokenIntLiteral:
		val, err := strconv.ParseInt(p.currTok.Value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer literal %q: %v", p.currTok.Value, err)
		}
		p.nextToken() // consume int literal
		return &core.Literal{Value: core.NewInt(val)}, nil

	case TokenTextLiteral:
		text := p.currTok.Value
		p.nextToken() // consume text literal
		return &core.Literal{Value: core.NewText(text)}, nil

	case TokenTrue:
		p.nextToken() // consume true
		return &core.Literal{Value: core.NewBool(true)}, nil

	case TokenFalse:
		p.nextToken() // consume false
		return &core.Literal{Value: core.NewBool(false)}, nil

	default:
		return nil, fmt.Errorf("expected expression primary element, got %q", p.currTok.Value)
	}
}
