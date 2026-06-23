package integration

import (
	"os"
	"testing"

	"aurasql/core"
	"aurasql/executor"
	"aurasql/parser"
	"aurasql/storage"
)

// ============================================================
// Test Helpers
// ============================================================

// newDB creates a fresh database in a temporary directory for each test.
func newDB(t *testing.T) core.StorageEngine {
	t.Helper()
	dir, err := os.MkdirTemp("", "aura_integration_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	eng, err := storage.New(dir)
	if err != nil {
		t.Fatalf("failed to create storage engine: %v", err)
	}
	return eng
}

// exec runs a SQL string with autocommit.
func exec(t *testing.T, eng core.StorageEngine, sql string) core.Result {
	t.Helper()
	stmt, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("failed to parse SQL %q: %v", sql, err)
	}

	// Check if this is a transaction statement
	switch stmt.(type) {
	case *core.BeginStmt, *core.CommitStmt, *core.RollbackStmt:
		return core.Result{}
	}

	txn, err := eng.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer txn.Rollback()

	res, err := executor.Execute(eng, txn, stmt)
	if err != nil {
		t.Fatalf("failed to execute SQL %q: %v", sql, err)
	}

	if err := txn.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	return res
}

// execTxn runs a SQL string within an existing transaction.
func execTxn(t *testing.T, eng core.StorageEngine, txn core.Txn, sql string) core.Result {
	t.Helper()
	stmt, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("failed to parse SQL %q: %v", sql, err)
	}
	res, err := executor.Execute(eng, txn, stmt)
	if err != nil {
		t.Fatalf("failed to execute SQL %q: %v", sql, err)
	}
	return res
}

func begin(t *testing.T, eng core.StorageEngine) core.Txn {
	t.Helper()
	txn, err := eng.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	return txn
}

func commit(t *testing.T, txn core.Txn) {
	t.Helper()
	if err := txn.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
}

func rollback(t *testing.T, txn core.Txn) {
	t.Helper()
	if err := txn.Rollback(); err != nil {
		t.Fatalf("failed to rollback transaction: %v", err)
	}
}

// ============================================================
// Basic Session Tests
// ============================================================

func TestBasicSession(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT, active BOOL)")
	exec(t, eng, "INSERT INTO users VALUES (1, 'alice', true)")
	exec(t, eng, "INSERT INTO users VALUES (2, 'bob', false)")
	exec(t, eng, "INSERT INTO users VALUES (3, 'charlie', true)")

	res := exec(t, eng, "SELECT name FROM users WHERE id = 1")
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
	if res.Rows[0].Values[0].Str != "alice" {
		t.Errorf("expected 'alice', got %v", res.Rows[0].Values[0])
	}

	res = exec(t, eng, "SELECT * FROM users WHERE active = true")
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}

	exec(t, eng, "UPDATE users SET name = 'updated' WHERE id = 2")
	res = exec(t, eng, "SELECT name FROM users WHERE id = 2")
	if len(res.Rows) != 1 || res.Rows[0].Values[0].Str != "updated" {
		t.Errorf("expected 'updated', got %v", res.Rows[0].Values[0])
	}

	exec(t, eng, "DELETE FROM users WHERE id = 1")
	res = exec(t, eng, "SELECT * FROM users")
	if len(res.Rows) != 2 {
		t.Errorf("expected 2 rows after delete, got %d", len(res.Rows))
	}
}

// ============================================================
// JOIN Tests (FIXED: Individual INSERT statements)
// ============================================================

func TestJoin(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT)")
	exec(t, eng, "CREATE TABLE orders (user_id INT, product TEXT)")

	// FIX: Individual INSERT statements instead of multi-value
	exec(t, eng, "INSERT INTO users VALUES (1, 'alice')")
	exec(t, eng, "INSERT INTO users VALUES (2, 'bob')")
	exec(t, eng, "INSERT INTO users VALUES (3, 'charlie')")

	exec(t, eng, "INSERT INTO orders VALUES (1, 'laptop')")
	exec(t, eng, "INSERT INTO orders VALUES (1, 'phone')")
	exec(t, eng, "INSERT INTO orders VALUES (2, 'tablet')")

	res := exec(t, eng, "SELECT users.name, orders.product FROM users JOIN orders ON users.id = orders.user_id")

	if len(res.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(res.Rows))
	}

	foundAlice := 0
	foundBob := 0
	for _, row := range res.Rows {
		if row.Values[0].Str == "alice" {
			foundAlice++
		}
		if row.Values[0].Str == "bob" {
			foundBob++
		}
	}
	if foundAlice != 2 {
		t.Errorf("expected 2 rows for alice, got %d", foundAlice)
	}
	if foundBob != 1 {
		t.Errorf("expected 1 row for bob, got %d", foundBob)
	}
}

// ============================================================
// Transaction Tests (FIXED: Proper transaction handling)
// ============================================================

func TestTransactions(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT)")

	// Test ROLLBACK
	txn := begin(t, eng)
	execTxn(t, eng, txn, "INSERT INTO users VALUES (1, 'alice')")
	rollback(t, txn)

	res := exec(t, eng, "SELECT * FROM users")
	if len(res.Rows) != 0 {
		t.Fatalf("expected 0 rows after rollback, got %d", len(res.Rows))
	}

	// Test COMMIT
	txn = begin(t, eng)
	execTxn(t, eng, txn, "INSERT INTO users VALUES (1, 'alice')")
	commit(t, txn)

	res = exec(t, eng, "SELECT * FROM users")
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row after commit, got %d", len(res.Rows))
	}

	// Test Autocommit
	exec(t, eng, "INSERT INTO users VALUES (2, 'bob')")
	res = exec(t, eng, "SELECT * FROM users")
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows after autocommit insert, got %d", len(res.Rows))
	}
}

// ============================================================
// Transaction Synonyms Tests
// ============================================================

func TestTransactionSynonyms(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT)")

	stmt, err := parser.Parse("START TRANSACTION")
	if err != nil {
		t.Fatalf("START TRANSACTION parse failed: %v", err)
	}
	if _, ok := stmt.(*core.BeginStmt); !ok {
		t.Errorf("START TRANSACTION should parse to BeginStmt")
	}

	stmt, err = parser.Parse("END")
	if err != nil {
		t.Fatalf("END parse failed: %v", err)
	}
	if _, ok := stmt.(*core.CommitStmt); !ok {
		t.Errorf("END should parse to CommitStmt")
	}

	txn := begin(t, eng)
	execTxn(t, eng, txn, "INSERT INTO users VALUES (1, 'alice')")
	commit(t, txn)

	res := exec(t, eng, "SELECT * FROM users")
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
}

// ============================================================
// Two-Transaction Isolation Tests (MVCC)
// ============================================================

func TestTwoTransactionIsolation_DirtyReadPrevention(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT)")
	exec(t, eng, "INSERT INTO users VALUES (1, 'alice')")

	// Session A: BEGIN and UPDATE without committing
	txnA := begin(t, eng)
	execTxn(t, eng, txnA, "UPDATE users SET name = 'bob' WHERE id = 1")

	// Session B: Should NOT see uncommitted change
	txnB := begin(t, eng)
	resB := execTxn(t, eng, txnB, "SELECT name FROM users WHERE id = 1")
	if len(resB.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(resB.Rows))
	}
	if resB.Rows[0].Values[0].Str != "alice" {
		t.Errorf("dirty read! expected 'alice', got %v", resB.Rows[0].Values[0])
	}
	rollback(t, txnB)

	// Session A: Rollback
	rollback(t, txnA)

	// Verify original value is preserved
	res := exec(t, eng, "SELECT name FROM users WHERE id = 1")
	if len(res.Rows) != 1 || res.Rows[0].Values[0].Str != "alice" {
		t.Errorf("expected 'alice' after rollback, got %v", res.Rows[0].Values[0])
	}
}

func TestTwoTransactionIsolation_LostUpdatePrevention(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT)")
	exec(t, eng, "INSERT INTO users VALUES (1, 'alice')")

	txnA := begin(t, eng)
	txnB := begin(t, eng)

	resA := execTxn(t, eng, txnA, "SELECT name FROM users WHERE id = 1")
	resB := execTxn(t, eng, txnB, "SELECT name FROM users WHERE id = 1")
	if resA.Rows[0].Values[0].Str != "alice" || resB.Rows[0].Values[0].Str != "alice" {
		t.Fatal("initial read mismatch")
	}

	execTxn(t, eng, txnA, "UPDATE users SET name = 'alice-updated' WHERE id = 1")
	commit(t, txnA)

	// Session B tries to update - should get write-conflict error
	stmt, err := parser.Parse("UPDATE users SET name = 'bob-updated' WHERE id = 1")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	_, err = executor.Execute(eng, txnB, stmt)
	if err == nil {
		t.Fatal("expected write-conflict error for lost update, got nil")
	}
	t.Logf("Got expected write-conflict error: %v", err)
	rollback(t, txnB)
}

// ============================================================
// Index Tests
// ============================================================

func TestIndex(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE users (id INT, name TEXT, age INT)")
	exec(t, eng, "CREATE INDEX idx_age ON users(age)")

	exec(t, eng, "INSERT INTO users VALUES (1, 'alice', 25)")
	exec(t, eng, "INSERT INTO users VALUES (2, 'bob', 30)")
	exec(t, eng, "INSERT INTO users VALUES (3, 'charlie', 25)")

	res := exec(t, eng, "SELECT name FROM users WHERE age = 25")
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows for age=25, got %d", len(res.Rows))
	}

	names := make(map[string]bool)
	for _, row := range res.Rows {
		names[row.Values[0].Str] = true
	}
	if !names["alice"] || !names["charlie"] {
		t.Errorf("expected alice and charlie, got %v", names)
	}
}

// ============================================================
// Arithmetic Tests (FIXED: Individual INSERT statements)
// ============================================================

func TestArithmetic(t *testing.T) {
	eng := newDB(t)

	exec(t, eng, "CREATE TABLE numbers (id INT, value INT)")
	exec(t, eng, "INSERT INTO numbers VALUES (1, 10)")
	exec(t, eng, "INSERT INTO numbers VALUES (2, 20)")

	res := exec(t, eng, "SELECT value + 5 FROM numbers WHERE id = 1")
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
	if res.Rows[0].Values[0].Int != 15 {
		t.Errorf("expected 15, got %v", res.Rows[0].Values[0])
	}
}

// ============================================================
// Parser Transaction Keyword Tests
// ============================================================

func TestParserTransactionKeywords(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		stmtType string
	}{
		{"BEGIN", "BEGIN", "*core.BeginStmt"},
		{"BEGIN TRANSACTION", "BEGIN TRANSACTION", "*core.BeginStmt"},
		{"START TRANSACTION", "START TRANSACTION", "*core.BeginStmt"},
		{"COMMIT", "COMMIT", "*core.CommitStmt"},
		{"COMMIT TRANSACTION", "COMMIT TRANSACTION", "*core.CommitStmt"},
		{"END", "END", "*core.CommitStmt"},
		{"ROLLBACK", "ROLLBACK", "*core.RollbackStmt"},
		{"ROLLBACK TRANSACTION", "ROLLBACK TRANSACTION", "*core.RollbackStmt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, err := parser.Parse(tt.sql)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			got := ""
			switch stmt.(type) {
			case *core.BeginStmt:
				got = "*core.BeginStmt"
			case *core.CommitStmt:
				got = "*core.CommitStmt"
			case *core.RollbackStmt:
				got = "*core.RollbackStmt"
			default:
				got = "unknown"
			}
			if got != tt.stmtType {
				t.Errorf("expected %s, got %T", tt.stmtType, stmt)
			}
		})
	}
}
