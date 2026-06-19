package parser

import (
	"testing"
)

func TestLexerSuccess(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []Token
	}{
		{
			name:  "Create Table Statement",
			input: "CREATE TABLE users (id INT, name TEXT, active BOOL)",
			expected: []Token{
				{TokenCreate, "CREATE"}, {TokenTable, "TABLE"}, {TokenIdentifier, "users"},
				{TokenOpenParen, "("}, {TokenIdentifier, "id"}, {TokenIntType, "INT"}, {TokenComma, ","},
				{TokenIdentifier, "name"}, {TokenTextType, "TEXT"}, {TokenComma, ","},
				{TokenIdentifier, "active"}, {TokenBoolType, "BOOL"}, {TokenCloseParen, ")"},
				{TokenEOF, ""},
			},
		},
		{
			name:  "Insert Statement",
			input: "INSERT INTO users VALUES (1, 'varun', true)",
			expected: []Token{
				{TokenInsert, "INSERT"}, {TokenInto, "INTO"}, {TokenIdentifier, "users"},
				{TokenValues, "VALUES"}, {TokenOpenParen, "("}, {TokenIntLiteral, "1"}, {TokenComma, ","},
				{TokenTextLiteral, "varun"}, {TokenComma, ","}, {TokenTrue, "true"}, {TokenCloseParen, ")"},
				{TokenEOF, ""},
			},
		},
		{
			name:  "Select With Predicate Expressions",
			input: "SELECT name, active FROM users WHERE id != 5 AND active = false",
			expected: []Token{
				{TokenSelect, "SELECT"}, {TokenIdentifier, "name"}, {TokenComma, ","}, {TokenIdentifier, "active"},
				{TokenFrom, "FROM"}, {TokenIdentifier, "users"}, {TokenWhere, "WHERE"},
				{TokenIdentifier, "id"}, {TokenNe, "!="}, {TokenIntLiteral, "5"},
				{TokenAnd, "AND"}, {TokenIdentifier, "active"}, {TokenEq, "="}, {TokenFalse, "false"},
				{TokenEOF, ""},
			},
		},
		{
			name:  "Alternative Inequality Operator",
			input: "id <> 10",
			expected: []Token{
				{TokenIdentifier, "id"}, {TokenNe, "<>"}, {TokenIntLiteral, "10"}, {TokenEOF, ""},
			},
		},
		{
			name:  "All operators and bounds",
			input: "* <= >= < >",
			expected: []Token{
				{TokenStar, "*"}, {TokenLe, "<="}, {TokenGe, ">="}, {TokenLt, "<"}, {TokenGt, ">"}, {TokenEOF, ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := LexAll(tt.input)
			if len(tokens) != len(tt.expected) {
				t.Fatalf("Token count mismatch. Expected %d, got %d. Result: %v", len(tt.expected), len(tokens), tokens)
			}
			for i, tok := range tokens {
				if tok.Type != tt.expected[i].Type || tok.Value != tt.expected[i].Value {
					t.Errorf("At token index %d: expected {%s, %q}, got {%s, %q}",
						i, tt.expected[i].Type, tt.expected[i].Value, tok.Type, tok.Value)
				}
			}
		})
	}
}

func TestLexerErrors(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		expectType TokenType
	}{
		{
			name:       "Unclosed string literal",
			input:      "INSERT INTO users VALUES ('unclosed string)",
			expectType: TokenError,
		},
		{
			name:       "Invalid standalone bang operator",
			input:      "WHERE active ! true",
			expectType: TokenError,
		},
		{
			name:       "Unexpected special character",
			input:      "SELECT # FROM users",
			expectType: TokenError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := LexAll(tt.input)
			lastToken := tokens[len(tokens)-1]
			if lastToken.Type != tt.expectType {
				t.Errorf("Expected an error terminal token context, but got: %s (%q)", lastToken.Type, lastToken.Value)
			}
		})
	}
}
