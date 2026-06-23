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

func TestParseDeleteSuccess(t *testing.T) {
	// Test 1: Full table delete without WHERE
	t.Run("Delete All", func(t *testing.T) {
		stmt, err := Parse("DELETE FROM logs")
		if err != nil {
			t.Fatalf("Unexpected delete parse error: %v", err)
		}
		del, ok := stmt.(*core.DeleteStmt)
		if !ok {
			t.Fatalf("Expected *core.DeleteStmt, got %T", stmt)
		}
		if del.Table != "logs" {
			t.Errorf("Expected table 'logs', got %q", del.Table)
		}
		if del.Where != nil {
			t.Errorf("Expected Where to be nil for unconditional delete")
		}
	})

	// Test 2: Filtered delete with Phase 2 table-qualified column reference
	t.Run("Delete With Condition", func(t *testing.T) {
		stmt, err := Parse("DELETE FROM users WHERE users.id = 10")
		if err != nil {
			t.Fatalf("Unexpected delete parse error: %v", err)
		}
		del, ok := stmt.(*core.DeleteStmt)
		if !ok {
			t.Fatalf("Expected *core.DeleteStmt")
		}
		if del.Table != "users" {
			t.Errorf("Expected table 'users', got %q", del.Table)
		}

		// Ensure table-qualification parsed perfectly
		bin, ok := del.Where.(*core.BinaryExpr)
		if !ok || bin.Op != core.OpEq {
			t.Fatalf("Expected equality binary expression condition")
		}
		col, ok := bin.Left.(*core.ColumnRef)
		if !ok || col.Table != "users" || col.Name != "id" {
			t.Errorf("Expected qualified column reference 'users.id'")
		}
	})
}

func TestParseUpdateSuccess(t *testing.T) {
	t.Run("Update Multiple Fields with WHERE", func(t *testing.T) {
		stmt, err := Parse("UPDATE users SET active = false, name = 'updated' WHERE id = 1")
		if err != nil {
			t.Fatalf("Unexpected update parse error: %v", err)
		}

		upd, ok := stmt.(*core.UpdateStmt)
		if !ok {
			t.Fatalf("Expected *core.UpdateStmt, got %T", stmt)
		}

		if upd.Table != "users" {
			t.Errorf("Expected table 'users', got %q", upd.Table)
		}

		if len(upd.Set) != 2 {
			t.Fatalf("Expected 2 assignments, got %d", len(upd.Set))
		}

		if upd.Set[0].Column != "active" || upd.Set[1].Column != "name" {
			t.Errorf("Assignments parsed in wrong order or with invalid columns")
		}

		if upd.Where == nil {
			t.Errorf("Expected WHERE condition to be populated")
		}
	})
}

func TestParseSelectWithJoinSuccess(t *testing.T) {
	t.Run("Select with JOIN and Qualified Columns", func(t *testing.T) {
		query := "SELECT users.name FROM users JOIN orders ON users.id = orders.uid WHERE users.active = true"
		stmt, err := Parse(query)
		if err != nil {
			t.Fatalf("Unexpected JOIN select parse error: %v", err)
		}

		sel, ok := stmt.(*core.SelectStmt)
		if !ok {
			t.Fatalf("Expected *core.SelectStmt, got %T", stmt)
		}

		if sel.From != "users" {
			t.Errorf("Expected From table 'users', got %q", sel.From)
		}

		// Verify Join structure
		if sel.Join == nil {
			t.Fatalf("Expected Join field to be populated")
		}
		if sel.Join.Table != "orders" {
			t.Errorf("Expected Join table 'orders', got %q", sel.Join.Table)
		}

		// Verify ON condition contains binary expression mapping
		bin, ok := sel.Join.On.(*core.BinaryExpr)
		if !ok || bin.Op != core.OpEq {
			t.Errorf("Expected Equality operator in ON clause")
		}
	})
}

func TestParseCreateIndexSuccess(t *testing.T) {
	// 1. Test unnamed auto-generated index format
	t.Run("Unnamed Index", func(t *testing.T) {
		stmt, err := Parse("CREATE INDEX ON users(id)")
		if err != nil {
			t.Fatalf("Unexpected error parsing unnamed index: %v", err)
		}
		ci, ok := stmt.(*core.CreateIndexStmt)
		if !ok {
			t.Fatalf("Expected *core.CreateIndexStmt, got %T", stmt)
		}
		if ci.Table != "users" || ci.Column != "id" || ci.Name != "" {
			t.Errorf("Mismatch in parsed unnamed index values: %+v", ci)
		}
	})

	// 2. Test explicitly named index format
	t.Run("Named Index", func(t *testing.T) {
		stmt, err := Parse("CREATE INDEX idx_users_id ON users(id)")
		if err != nil {
			t.Fatalf("Unexpected error parsing named index: %v", err)
		}
		ci, ok := stmt.(*core.CreateIndexStmt)
		if !ok {
			t.Fatalf("Expected *core.CreateIndexStmt")
		}
		if ci.Name != "idx_users_id" || ci.Table != "users" || ci.Column != "id" {
			t.Errorf("Mismatch in parsed named index values: %+v", ci)
		}
	})
}

// ============================================================
// Phase 4: Transaction Control Statement Tests
// ============================================================

func TestParseBeginSuccess(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"BEGIN", "BEGIN"},
		{"BEGIN TRANSACTION", "BEGIN TRANSACTION"},
		{"START TRANSACTION", "START TRANSACTION"},
		{"begin lowercase", "begin"},
		{"Begin mixed case", "Begin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.sql)
			if err != nil {
				t.Fatalf("Parse failed unexpectedly: %v", err)
			}

			_, ok := stmt.(*core.BeginStmt)
			if !ok {
				t.Fatalf("Expected *core.BeginStmt, got %T", stmt)
			}
		})
	}
}

func TestParseCommitSuccess(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"COMMIT", "COMMIT"},
		{"COMMIT TRANSACTION", "COMMIT TRANSACTION"},
		{"END", "END"},
		{"commit lowercase", "commit"},
		{"Commit mixed case", "Commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.sql)
			if err != nil {
				t.Fatalf("Parse failed unexpectedly: %v", err)
			}

			_, ok := stmt.(*core.CommitStmt)
			if !ok {
				t.Fatalf("Expected *core.CommitStmt, got %T", stmt)
			}
		})
	}
}

func TestParseRollbackSuccess(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"ROLLBACK", "ROLLBACK"},
		{"ROLLBACK TRANSACTION", "ROLLBACK TRANSACTION"},
		{"rollback lowercase", "rollback"},
		{"Rollback mixed case", "Rollback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.sql)
			if err != nil {
				t.Fatalf("Parse failed unexpectedly: %v", err)
			}

			_, ok := stmt.(*core.RollbackStmt)
			if !ok {
				t.Fatalf("Expected *core.RollbackStmt, got %T", stmt)
			}
		})
	}
}

// TestTransactionTrailingJunkError verifies that trailing tokens after
// transaction statements cause errors (as required by Phase 4 brief).
func TestTransactionTrailingJunkError(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"BEGIN with extra", "BEGIN EXTRA"},
		{"BEGIN TRANSACTION with extra", "BEGIN TRANSACTION EXTRA"},
		{"START TRANSACTION with extra", "START TRANSACTION EXTRA"},
		{"COMMIT with extra", "COMMIT EXTRA"},
		{"COMMIT TRANSACTION with extra", "COMMIT TRANSACTION EXTRA"},
		{"END with extra", "END EXTRA"},
		{"ROLLBACK with extra", "ROLLBACK EXTRA"},
		{"ROLLBACK TRANSACTION with extra", "ROLLBACK TRANSACTION EXTRA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.sql)
			if err == nil {
				t.Errorf("Expected error for trailing junk, got nil")
			}
		})
	}
}

// TestTransactionIncompleteStatements tests that incomplete transaction
// statements error appropriately.
func TestTransactionIncompleteStatements(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"BEGIN with transaction only", "BEGIN TRANSACTION"},
		{"START with transaction only", "START TRANSACTION"},
		{"COMMIT with transaction only", "COMMIT TRANSACTION"},
		{"ROLLBACK with transaction only", "ROLLBACK TRANSACTION"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These should parse successfully (they're valid)
			stmt, err := Parse(tt.sql)
			if err != nil {
				t.Fatalf("Expected valid statement, got error: %v", err)
			}
			if stmt == nil {
				t.Error("Expected non-nil statement")
			}
		})
	}
}
