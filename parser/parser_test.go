package parser

import (
	"testing"

	"aurasql/core"
)

func TestParseCreateTableSuccess(t *testing.T) {
	sql := "CREATE TABLE users (id INT, name TEXT, active BOOL)"
	stmt, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse failed unexpectedly: %v", err)
	}

	createStmt, ok := stmt.(*core.CreateTableStmt)
	if !ok {
		t.Fatalf("Expected *core.CreateTableStmt, got %T", stmt)
	}

	if createStmt.Table != "users" {
		t.Errorf("Expected table name 'users', got %q", createStmt.Table)
	}

	expectedCols := []core.Column{
		{Name: "id", Type: core.TypeInt},
		{Name: "name", Type: core.TypeText},
		{Name: "active", Type: core.TypeBool},
	}

	if len(createStmt.Columns) != len(expectedCols) {
		t.Fatalf("Expected %d columns, got %d", len(expectedCols), len(createStmt.Columns))
	}

	for i, col := range createStmt.Columns {
		if col.Name != expectedCols[i].Name || col.Type != expectedCols[i].Type {
			t.Errorf("Column %d mismatch. Expected {%s, %s}, got {%s, %s}",
				i, expectedCols[i].Name, expectedCols[i].Type, col.Name, col.Type)
		}
	}
}

func TestParseCreateTableErrors(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"Missing TABLE keyword", "CREATE users (id INT)"},
		{"Missing Table Name", "CREATE TABLE (id INT)"},
		{"Missing Open Paren", "CREATE TABLE users id INT)"},
		{"Invalid Type", "CREATE TABLE users (id BLOB)"},
		{"Unclosed Paren", "CREATE TABLE users (id INT"},
		{"Trailing Garbage", "CREATE TABLE users (id INT) SELECT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.sql)
			if err == nil {
				t.Error("Expected parsing error for malformed query, but got nil")
			}
		})
	}
}

func TestParseInsertSuccess(t *testing.T) {
	sql := "INSERT INTO users VALUES (1, 'varun', true)"
	stmt, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse failed unexpectedly: %v", err)
	}

	insStmt, ok := stmt.(*core.InsertStmt)
	if !ok {
		t.Fatalf("Expected *core.InsertStmt, got %T", stmt)
	}

	if insStmt.Table != "users" {
		t.Errorf("Expected table name 'users', got %q", insStmt.Table)
	}

	if len(insStmt.Values) != 3 {
		t.Fatalf("Expected 3 values, got %d", len(insStmt.Values))
	}

	// Verify Int Literal
	lit0, ok := insStmt.Values[0].(*core.Literal)
	if !ok || lit0.Value.Int != 1 {
		t.Errorf("Value 0 mismatch: expected Int 1, got %v", insStmt.Values[0])
	}

	// Verify Text Literal
	lit1, ok := insStmt.Values[1].(*core.Literal)
	if !ok || lit1.Value.Str != "varun" {
		t.Errorf("Value 1 mismatch: expected Text 'varun', got %v", insStmt.Values[1])
	}

	// Verify Bool Literal
	lit2, ok := insStmt.Values[2].(*core.Literal)
	if !ok || lit2.Value.Bool != true {
		t.Errorf("Value 2 mismatch: expected Bool true, got %v", insStmt.Values[2])
	}
}

func TestParseInsertErrors(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"Missing INTO", "INSERT users VALUES (1)"},
		{"Missing VALUES", "INSERT INTO users (1)"},
		{"Empty items list", "INSERT INTO users VALUES ()"},
		{"Missing closing paren", "INSERT INTO users VALUES (1, 'varun'"},
		{"Invalid literal item", "INSERT INTO users VALUES (1, identifier_here)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.sql)
			if err == nil {
				t.Error("Expected failure for malformed INSERT syntax, but got success")
			}
		})
	}
}

func TestParseSelectSuccess(t *testing.T) {
	// Test 1: SELECT * FROM table
	t.Run("Select Star", func(t *testing.T) {
		stmt, err := Parse("SELECT * FROM users")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		sel, ok := stmt.(*core.SelectStmt)
		if !ok {
			t.Fatalf("Expected *core.SelectStmt")
		}
		if sel.From != "users" {
			t.Errorf("Expected from table 'users', got %q", sel.From)
		}
		if len(sel.Projection) != 1 {
			t.Fatalf("Expected projection length 1")
		}
		if _, isStar := sel.Projection[0].(*core.Star); !isStar {
			t.Errorf("Expected projection item to be Star")
		}
		if sel.Where != nil {
			t.Errorf("Expected where clause to be nil")
		}
	})

	// Test 2: SELECT explicit columns WITH complex WHERE expression clause
	t.Run("Select With Columns and Where Precedence", func(t *testing.T) {
		stmt, err := Parse("SELECT name, active FROM users WHERE active = true AND id != 5")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		sel, ok := stmt.(*core.SelectStmt)
		if !ok {
			t.Fatalf("Expected *core.SelectStmt")
		}
		if len(sel.Projection) != 2 {
			t.Fatalf("Expected projection length 2, got %d", len(sel.Projection))
		}

		proj0, ok0 := sel.Projection[0].(*core.ColumnRef)
		if !ok0 || proj0.Name != "name" {
			t.Errorf("Expected column ref 'name'")
		}

		// Validate top-level WHERE expression is AND (since AND binds tighter than OR, but lower than comparisons)
		binWhere, ok := sel.Where.(*core.BinaryExpr)
		if !ok || binWhere.Op != core.OpAnd {
			t.Fatalf("Expected top level WHERE op to be AND, got %v", sel.Where)
		}

		// Left condition: active = true
		leftOp, ok := binWhere.Left.(*core.BinaryExpr)
		if !ok || leftOp.Op != core.OpEq {
			t.Errorf("Expected left condition op to be Eq (=)")
		}

		// Right condition: id != 5
		rightOp, ok := binWhere.Right.(*core.BinaryExpr)
		if !ok || rightOp.Op != core.OpNe {
			t.Errorf("Expected right condition op to be Ne (!=)")
		}
	})
}

func TestParseSelectErrors(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"Missing FROM", "SELECT * users"},
		{"Trailing comma projection", "SELECT id, name, FROM users"},
		{"Missing table target", "SELECT * FROM"},
		{"Missing expression after where", "SELECT * FROM users WHERE"},
		{"Incomplete trailing expression operator", "SELECT * FROM users WHERE id = "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.sql)
			if err == nil {
				t.Error("Expected parsing failure for malformed SELECT query syntax")
			}
		})
	}
}
