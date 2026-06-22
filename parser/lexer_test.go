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
		// NEW TRANSACTION STATEMENT TESTS
		{
			name:  "BEGIN Statement",
			input: "BEGIN",
			expected: []Token{
				{TokenBegin, "BEGIN"}, {TokenEOF, ""},
			},
		},
		{
			name:  "BEGIN TRANSACTION",
			input: "BEGIN TRANSACTION",
			expected: []Token{
				{TokenBegin, "BEGIN"}, {TokenTransaction, "TRANSACTION"}, {TokenEOF, ""},
			},
		},
		{
			name:  "START TRANSACTION",
			input: "START TRANSACTION",
			expected: []Token{
				{TokenStart, "START"}, {TokenTransaction, "TRANSACTION"}, {TokenEOF, ""},
			},
		},
		{
			name:  "COMMIT Statement",
			input: "COMMIT",
			expected: []Token{
				{TokenCommit, "COMMIT"}, {TokenEOF, ""},
			},
		},
		{
			name:  "COMMIT TRANSACTION",
			input: "COMMIT TRANSACTION",
			expected: []Token{
				{TokenCommit, "COMMIT"}, {TokenTransaction, "TRANSACTION"}, {TokenEOF, ""},
			},
		},
		{
			name:  "END Statement (synonym for COMMIT)",
			input: "END",
			expected: []Token{
				{TokenEnd, "END"}, {TokenEOF, ""},
			},
		},
		{
			name:  "ROLLBACK Statement",
			input: "ROLLBACK",
			expected: []Token{
				{TokenRollback, "ROLLBACK"}, {TokenEOF, ""},
			},
		},
		{
			name:  "ROLLBACK TRANSACTION",
			input: "ROLLBACK TRANSACTION",
			expected: []Token{
				{TokenRollback, "ROLLBACK"}, {TokenTransaction, "TRANSACTION"}, {TokenEOF, ""},
			},
		},
		{
			name:  "Case Insensitive BEGIN",
			input: "begin",
			expected: []Token{
				{TokenBegin, "begin"}, {TokenEOF, ""},
			},
		},
		{
			name:  "Case Insensitive COMMIT",
			input: "Commit",
			expected: []Token{
				{TokenCommit, "Commit"}, {TokenEOF, ""},
			},
		},
		{
			name:  "Case Insensitive ROLLBACK",
			input: "rollback",
			expected: []Token{
				{TokenRollback, "rollback"}, {TokenEOF, ""},
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

// ============================================================
// NEW: Focused tests for transaction keywords
// ============================================================

// TestTransactionKeywords tests that all transaction keywords are recognized
func TestTransactionKeywords(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected TokenType
	}{
		{"BEGIN", "BEGIN", TokenBegin},
		{"BEGIN lowercase", "begin", TokenBegin},
		{"BEGIN mixed case", "BeGiN", TokenBegin},
		{"START", "START", TokenStart},
		{"START lowercase", "start", TokenStart},
		{"COMMIT", "COMMIT", TokenCommit},
		{"COMMIT lowercase", "commit", TokenCommit},
		{"END", "END", TokenEnd},
		{"END lowercase", "end", TokenEnd},
		{"ROLLBACK", "ROLLBACK", TokenRollback},
		{"ROLLBACK lowercase", "rollback", TokenRollback},
		{"TRANSACTION", "TRANSACTION", TokenTransaction},
		{"TRANSACTION lowercase", "transaction", TokenTransaction},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			tok := lexer.NextToken()
			if tok.Type != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, tok.Type)
			}
			// Ensure we get EOF after the keyword
			eof := lexer.NextToken()
			if eof.Type != TokenEOF {
				t.Errorf("expected EOF after keyword, got %v", eof.Type)
			}
		})
	}
}

// TestTransactionStatementsFullTokens tests full transaction statements
func TestTransactionStatementsFullTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []TokenType
	}{
		{
			name:  "BEGIN",
			input: "BEGIN",
			want:  []TokenType{TokenBegin, TokenEOF},
		},
		{
			name:  "BEGIN TRANSACTION",
			input: "BEGIN TRANSACTION",
			want:  []TokenType{TokenBegin, TokenTransaction, TokenEOF},
		},
		{
			name:  "START TRANSACTION",
			input: "START TRANSACTION",
			want:  []TokenType{TokenStart, TokenTransaction, TokenEOF},
		},
		{
			name:  "COMMIT",
			input: "COMMIT",
			want:  []TokenType{TokenCommit, TokenEOF},
		},
		{
			name:  "COMMIT TRANSACTION",
			input: "COMMIT TRANSACTION",
			want:  []TokenType{TokenCommit, TokenTransaction, TokenEOF},
		},
		{
			name:  "END",
			input: "END",
			want:  []TokenType{TokenEnd, TokenEOF},
		},
		{
			name:  "ROLLBACK",
			input: "ROLLBACK",
			want:  []TokenType{TokenRollback, TokenEOF},
		},
		{
			name:  "ROLLBACK TRANSACTION",
			input: "ROLLBACK TRANSACTION",
			want:  []TokenType{TokenRollback, TokenTransaction, TokenEOF},
		},
		{
			name:  "Case insensitive BEGIN TRANSACTION",
			input: "begin transaction",
			want:  []TokenType{TokenBegin, TokenTransaction, TokenEOF},
		},
		{
			name:  "Case insensitive COMMIT TRANSACTION",
			input: "Commit Transaction",
			want:  []TokenType{TokenCommit, TokenTransaction, TokenEOF},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			var got []TokenType
			for {
				tok := lexer.NextToken()
				got = append(got, tok.Type)
				if tok.Type == TokenEOF || tok.Type == TokenError {
					break
				}
			}

			if len(got) != len(tt.want) {
				t.Errorf("token count mismatch: got %d, want %d", len(got), len(tt.want))
				t.Logf("got: %v", got)
				t.Logf("want: %v", tt.want)
				return
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("token %d: got %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
