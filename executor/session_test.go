package executor

import (
	"testing"

	"aurasql/core"
	"aurasql/memstore"
)

func TestSession_Autocommit(t *testing.T) {
	eng := memstore.New()
	sess := NewSession(eng)

	// Autocommit: INSERT should be immediately visible
	createStmt := &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}
	_, err := sess.Exec(createStmt)
	if err != nil {
		t.Fatalf("CREATE failed: %v", err)
	}

	insertStmt := &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("alice")},
		},
	}
	_, err = sess.Exec(insertStmt)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	// Verify row exists immediately (autocommit)
	selectStmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	}
	res, err := sess.Exec(selectStmt)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
}

func TestSession_BeginCommitRollback(t *testing.T) {
	eng := memstore.New()
	sess := NewSession(eng)

	// Setup
	createStmt := &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}
	sess.Exec(createStmt)

	// Test BEGIN + ROLLBACK
	beginStmt := &core.BeginStmt{}
	_, err := sess.Exec(beginStmt)
	if err != nil {
		t.Fatalf("BEGIN failed: %v", err)
	}
	if !sess.InTxn() {
		t.Error("expected to be in transaction after BEGIN")
	}

	insertStmt := &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("alice")},
		},
	}
	_, err = sess.Exec(insertStmt)
	if err != nil {
		t.Fatalf("INSERT in txn failed: %v", err)
	}

	rollbackStmt := &core.RollbackStmt{}
	_, err = sess.Exec(rollbackStmt)
	if err != nil {
		t.Fatalf("ROLLBACK failed: %v", err)
	}
	if sess.InTxn() {
		t.Error("expected to not be in transaction after ROLLBACK")
	}

	// Verify row is gone
	selectStmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	}
	res, err := sess.Exec(selectStmt)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("expected 0 rows after ROLLBACK, got %d", len(res.Rows))
	}

	// Test BEGIN + COMMIT
	_, err = sess.Exec(&core.BeginStmt{})
	if err != nil {
		t.Fatalf("BEGIN failed: %v", err)
	}
	_, err = sess.Exec(&core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(2)},
			&core.Literal{Value: core.NewText("bob")},
		},
	})
	if err != nil {
		t.Fatalf("INSERT in txn failed: %v", err)
	}
	_, err = sess.Exec(&core.CommitStmt{})
	if err != nil {
		t.Fatalf("COMMIT failed: %v", err)
	}
	if sess.InTxn() {
		t.Error("expected to not be in transaction after COMMIT")
	}

	// Verify row persists
	res, err = sess.Exec(selectStmt)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row after COMMIT, got %d", len(res.Rows))
	}
}

func TestSession_ErrorCases(t *testing.T) {
	eng := memstore.New()
	sess := NewSession(eng)

	// COMMIT without BEGIN
	_, err := sess.Exec(&core.CommitStmt{})
	if err != ErrNoTxn {
		t.Errorf("expected ErrNoTxn, got %v", err)
	}

	// ROLLBACK without BEGIN
	_, err = sess.Exec(&core.RollbackStmt{})
	if err != ErrNoTxn {
		t.Errorf("expected ErrNoTxn, got %v", err)
	}

	// BEGIN twice
	_, err = sess.Exec(&core.BeginStmt{})
	if err != nil {
		t.Fatalf("BEGIN failed: %v", err)
	}
	_, err = sess.Exec(&core.BeginStmt{})
	if err != ErrAlreadyInTxn {
		t.Errorf("expected ErrAlreadyInTxn, got %v", err)
	}
}

func TestSession_WriteConflict(t *testing.T) {
	eng := memstore.New()
	sess1 := NewSession(eng)
	sess2 := NewSession(eng)

	// Setup
	createStmt := &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}
	sess1.Exec(createStmt)
	sess1.Exec(&core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("alice")},
		},
	})

	// Session 1: BEGIN, SELECT, UPDATE (but not commit)
	sess1.Exec(&core.BeginStmt{})
	sess1.Exec(&core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	})
	sess1.Exec(&core.UpdateStmt{
		Table: "users",
		Set: []core.Assignment{
			{Column: "name", Value: &core.Literal{Value: core.NewText("alice-updated")}},
		},
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	})

	// Session 2: BEGIN, try to update same row (should conflict)
	sess2.Exec(&core.BeginStmt{})
	_, err := sess2.Exec(&core.UpdateStmt{
		Table: "users",
		Set: []core.Assignment{
			{Column: "name", Value: &core.Literal{Value: core.NewText("bob")}},
		},
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	})

	// Should get a write conflict error
	if err == nil {
		t.Error("expected write conflict error, got nil")
	}
	if !isWriteConflict(err) {
		t.Errorf("expected isWriteConflict to return true, got false for error: %v", err)
	}
}
