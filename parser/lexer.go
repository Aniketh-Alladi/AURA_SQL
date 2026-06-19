package parser

import (
	"fmt"
	"strings"
	"unicode"
)

// TokenType represents the classification of a lexical token.
type TokenType int

const (
	TokenError TokenType = iota
	TokenEOF

	// Keywords
	TokenSelect
	TokenFrom
	TokenWhere
	TokenCreate
	TokenTable
	TokenInsert
	TokenInto
	TokenValues
	TokenAnd
	TokenOr
	TokenIntType
	TokenTextType
	TokenBoolType
	TokenTrue
	TokenFalse
	TokenDelete // DELETE
	TokenUpdate
	TokenSet
	TokenJoin
	TokenOn

	// Literals & Identifiers
	TokenIdentifier
	TokenIntLiteral
	TokenTextLiteral

	// Operators & Punctuation
	TokenOpenParen  // (
	TokenCloseParen // )
	TokenComma      // ,
	TokenStar       // *
	TokenEq         // =
	TokenNe         // != or <>
	TokenLt         // <
	TokenLe         // <=
	TokenGt         // >
	TokenGe         // >=
	TokenDot        // .
	TokenPlus       // +
	TokenMinus      // -
)

func (t TokenType) String() string {
	switch t {
	case TokenError:
		return "ERROR"
	case TokenEOF:
		return "EOF"
	case TokenSelect:
		return "SELECT"
	case TokenFrom:
		return "FROM"
	case TokenWhere:
		return "WHERE"
	case TokenCreate:
		return "CREATE"
	case TokenTable:
		return "TABLE"
	case TokenInsert:
		return "INSERT"
	case TokenInto:
		return "INTO"
	case TokenValues:
		return "VALUES"
	case TokenDelete:
		return "DELETE"
	case TokenUpdate:
		return "UPDATE"
	case TokenSet:
		return "SET"
	case TokenJoin:
		return "JOIN"
	case TokenOn:
		return "ON"
	case TokenAnd:
		return "AND"
	case TokenOr:
		return "OR"
	case TokenIntType:
		return "INT"
	case TokenTextType:
		return "TEXT"
	case TokenBoolType:
		return "BOOL"
	case TokenTrue:
		return "true"
	case TokenFalse:
		return "false"
	case TokenIdentifier:
		return "IDENTIFIER"
	case TokenIntLiteral:
		return "INT_LITERAL"
	case TokenTextLiteral:
		return "TEXT_LITERAL"
	case TokenOpenParen:
		return "("
	case TokenCloseParen:
		return ")"
	case TokenComma:
		return ","
	case TokenStar:
		return "*"
	case TokenEq:
		return "="
	case TokenNe:
		return "!="
	case TokenLt:
		return "<"
	case TokenLe:
		return "<="
	case TokenGt:
		return ">"
	case TokenGe:
		return ">="
	case TokenDot:
		return "."
	case TokenPlus:
		return "+"
	case TokenMinus:
		return "-"
	default:
		return "UNKNOWN"
	}
}

// Token represents a single scanned unit of SQL text.
type Token struct {
	Type  TokenType
	Value string
}

// Lexer scans a SQL string and emits tokens.
type Lexer struct {
	input []rune
	pos   int
}

// NewLexer initializes a lexer with the given input string.
func NewLexer(input string) *Lexer {
	return &Lexer{input: []rune(input)}
}

// NextToken scans and returns the next available Token.
// NextToken scans and returns the next available Token.
// NextToken scans and returns the next available Token.
func (l *Lexer) NextToken() Token {
	l.skipWhitespace()

	if l.pos >= len(l.input) {
		return Token{Type: TokenEOF, Value: ""}
	}

	// Switch completely on the expression directly to satisfy QF1003
	switch l.input[l.pos] {
	case '(':
		l.pos++
		return Token{Type: TokenOpenParen, Value: "("}
	case ')':
		l.pos++
		return Token{Type: TokenCloseParen, Value: ")"}
	case ',':
		l.pos++
		return Token{Type: TokenComma, Value: ","}
	case '*':
		l.pos++
		return Token{Type: TokenStar, Value: "*"}
	case '.':
		l.pos++
		return Token{Type: TokenDot, Value: "."}
	case '+':
		l.pos++
		return Token{Type: TokenPlus, Value: "+"}
	case '-':
		l.pos++
		return Token{Type: TokenMinus, Value: "-"}
	case '=':
		l.pos++
		return Token{Type: TokenEq, Value: "="}
	case '<':
		l.pos++
		if l.pos < len(l.input) {
			switch l.input[l.pos] {
			case '=':
				l.pos++
				return Token{Type: TokenLe, Value: "<="}
			case '>':
				l.pos++
				return Token{Type: TokenNe, Value: "<>"}
			}
		}
		return Token{Type: TokenLt, Value: "<"}
	case '>':
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Type: TokenGe, Value: ">="}
		}
		return Token{Type: TokenGt, Value: ">"}
	case '!':
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Type: TokenNe, Value: "!="}
		}
		return Token{Type: TokenError, Value: "unexpected character '!'"}
	case '\'':
		return l.readTextLiteral()
	default:
		ch := l.input[l.pos]
		// Numbers
		if unicode.IsDigit(ch) {
			return l.readIntLiteral()
		}

		// Identifiers & Keywords
		if isIdentifierStart(ch) {
			return l.readIdentifierOrKeyword()
		}

		l.pos++
		return Token{Type: TokenError, Value: fmt.Sprintf("unexpected character %q", string(ch))}
	}
}

func (l *Lexer) skipWhitespace() {
	for l.pos < len(l.input) && unicode.IsSpace(l.input[l.pos]) {
		l.pos++
	}
}

func (l *Lexer) readTextLiteral() Token {
	l.pos++ // consume opening single quote
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != '\'' {
		l.pos++
	}

	if l.pos >= len(l.input) {
		return Token{Type: TokenError, Value: "unclosed string literal"}
	}

	val := string(l.input[start:l.pos])
	l.pos++ // consume closing single quote
	return Token{Type: TokenTextLiteral, Value: val}
}

func (l *Lexer) readIntLiteral() Token {
	start := l.pos
	for l.pos < len(l.input) && unicode.IsDigit(l.input[l.pos]) {
		l.pos++
	}
	return Token{Type: TokenIntLiteral, Value: string(l.input[start:l.pos])}
}

func (l *Lexer) readIdentifierOrKeyword() Token {
	start := l.pos
	for l.pos < len(l.input) && (isIdentifierStart(l.input[l.pos]) || unicode.IsDigit(l.input[l.pos])) {
		l.pos++
	}
	val := string(l.input[start:l.pos])

	// Check case-insensitive keywords
	switch strings.ToUpper(val) {
	case "SELECT":
		return Token{Type: TokenSelect, Value: val}
	case "FROM":
		return Token{Type: TokenFrom, Value: val}
	case "WHERE":
		return Token{Type: TokenWhere, Value: val}
	case "CREATE":
		return Token{Type: TokenCreate, Value: val}
	case "TABLE":
		return Token{Type: TokenTable, Value: val}
	case "INSERT":
		return Token{Type: TokenInsert, Value: val}
	case "INTO":
		return Token{Type: TokenInto, Value: val}
	case "VALUES":
		return Token{Type: TokenValues, Value: val}
	case "DELETE": // <--- ADD THIS CASE HERE
		return Token{Type: TokenDelete, Value: val}
	case "UPDATE":
		return Token{Type: TokenUpdate, Value: val}
	case "SET":
		return Token{Type: TokenSet, Value: val}
	case "JOIN":
		return Token{Type: TokenJoin, Value: val}
	case "ON":
		return Token{Type: TokenOn, Value: val}
	case "AND":
		return Token{Type: TokenAnd, Value: val}
	case "OR":
		return Token{Type: TokenOr, Value: val}
	case "INT":
		return Token{Type: TokenIntType, Value: val}
	case "TEXT":
		return Token{Type: TokenTextType, Value: val}
	case "BOOL":
		return Token{Type: TokenBoolType, Value: val}
	case "TRUE":
		return Token{Type: TokenTrue, Value: val}
	case "FALSE":
		return Token{Type: TokenFalse, Value: val}
	}

	return Token{Type: TokenIdentifier, Value: val}
}

func isIdentifierStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_'
}

// LexAll collects all tokens until EOF or an error token is reached.
// Useful for batch testing.
func LexAll(sql string) []Token {
	lexer := NewLexer(sql)
	var tokens []Token
	for {
		tok := lexer.NextToken()
		tokens = append(tokens, tok)
		if tok.Type == TokenEOF || tok.Type == TokenError {
			break
		}
	}
	return tokens
}
