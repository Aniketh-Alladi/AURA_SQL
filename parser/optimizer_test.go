package parser

import (
	"testing"

	"aurasql/core"
)

// TestOptimizerComplexSelect verifies multiple chained joins with complex base/join aliases
func TestOptimizerComplexSelect(t *testing.T) {
	sql := `SELECT u.id, o.total, p.name 
	        FROM users u 
	        JOIN orders o ON u.id = o.user_id 
	        JOIN products p ON o.prod_id = p.id 
	        WHERE u.active = true`

	stmt, err := Parse(sql)
	if err != nil {
		t.Fatalf("Failed to parse complex multi-join statement: %v", err)
	}

	selectStmt, ok := stmt.(*core.SelectStmt)
	if !ok {
		t.Fatalf("Expected *core.SelectStmt, got %T", stmt)
	}

	// Verify Base Table and Base Alias
	if selectStmt.From != "users" {
		t.Errorf("Expected base table 'users', got %q", selectStmt.From)
	}
	if selectStmt.FromAlias != "u" {
		t.Errorf("Expected base table alias 'u', got %q", selectStmt.FromAlias)
	}

	// Verify Chained Joins
	if len(selectStmt.Joins) != 2 {
		t.Fatalf("Expected 2 joins, got %d", len(selectStmt.Joins))
	}

	// First Join: JOIN orders o
	if selectStmt.Joins[0].Table != "orders" {
		t.Errorf("Expected first join table 'orders', got %q", selectStmt.Joins[0].Table)
	}
	if selectStmt.Joins[0].Alias != "o" {
		t.Errorf("Expected first join alias 'o', got %q", selectStmt.Joins[0].Alias)
	}

	// Second Join: JOIN products p
	if selectStmt.Joins[1].Table != "products" {
		t.Errorf("Expected second join table 'products', got %q", selectStmt.Joins[1].Table)
	}
	if selectStmt.Joins[1].Alias != "p" {
		t.Errorf("Expected second join alias 'p', got %q", selectStmt.Joins[1].Alias)
	}
}

// TestOptimizerExplainPlan ensures EXPLAIN beautifully wraps deep multi-join constructs
func TestOptimizerExplainPlan(t *testing.T) {
	sql := `EXPLAIN SELECT * FROM customer c JOIN regional_sales r ON c.id = r.cust_id`

	stmt, err := Parse(sql)
	if err != nil {
		t.Fatalf("Failed to parse EXPLAIN statement: %v", err)
	}

	explainStmt, ok := stmt.(*core.ExplainStmt)
	if !ok {
		t.Fatalf("Expected *core.ExplainStmt, got %T", stmt)
	}

	// Ensure the embedded sub-statement parses down to the correct select structure
	subSelect, ok := explainStmt.Stmt.(*core.SelectStmt)
	if !ok {
		t.Fatalf("Expected wrapped statement to be *core.SelectStmt, got %T", explainStmt.Stmt)
	}

	if subSelect.From != "customer" || subSelect.FromAlias != "c" {
		t.Errorf("Sub-select extraction failed. Got table %q with alias %q", subSelect.From, subSelect.FromAlias)
	}

	if len(subSelect.Joins) != 1 || subSelect.Joins[0].Table != "regional_sales" {
		t.Errorf("Sub-select join extraction failed structure validation")
	}
}

// TestOptimizerAnalyzeSyntax tests variations of statistics sampling commands
func TestOptimizerAnalyzeSyntax(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectedTable string
	}{
		{"Direct clean identifier", "ANALYZE items", "items"},
		{"Standard SQL syntax variation", "ANALYZE TABLE items", "items"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := Parse(tt.sql)
			if err != nil {
				t.Fatalf("Failed parsing during optimizer checks: %v", err)
			}

			analyzeStmt, ok := stmt.(*core.AnalyzeStmt)
			if !ok {
				t.Fatalf("Expected *core.AnalyzeStmt, got %T", stmt)
			}

			if analyzeStmt.Table != tt.expectedTable {
				t.Errorf("Expected table target %q, got %q", tt.expectedTable, analyzeStmt.Table)
			}
		})
	}
}
