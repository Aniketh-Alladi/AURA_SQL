package executor_test

import (
	"reflect"
	"testing"

	"aurasql/core"
	"aurasql/executor"
	"aurasql/memstore"
	"aurasql/storage" // <-- The new power unit
)

// setupEngine creates a fresh disk-backed engine for every test
func setupEngine(t *testing.T) core.StorageEngine {
	t.Helper()
	dir := t.TempDir()

	eng, err := storage.New(dir)
	if err != nil {
		t.Fatalf("failed to create storage engine: %v", err)
	}

	t.Cleanup(func() {
		eng.Close()
	})

	return eng
}

// ============================================================
// Phase 1 tests (should still pass with refactored code)
// ============================================================

func TestCreateTable(t *testing.T) {
	eng := setupEngine(t)
	tx, err := eng.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	stmt := &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}

	_, err = executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	// Verify
	schema, ok := eng.GetSchema("users")
	if !ok {
		t.Fatal("table not created")
	}
	if len(schema.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(schema.Columns))
	}
}

func TestInsert(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	// Create table
	executor.Execute(eng, tx, &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	})

	// Insert
	result, err := executor.Execute(eng, tx, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("alice")},
		},
	})
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if result.RowsAffected != 1 {
		t.Errorf("expected 1 row affected, got %d", result.RowsAffected)
	}
}

func TestSelectWithWhere(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	// Setup
	setupUsersTable(t, eng, tx)

	// SELECT * FROM users WHERE id = 2
	stmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(2)},
		},
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0].Values[1].Str != "bob" {
		t.Errorf("expected 'bob', got %v", result.Rows[0].Values[1])
	}
}

func TestSelectWithProjection(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	// SELECT name FROM users WHERE id = 1
	stmt := &core.SelectStmt{
		Projection: []core.Expr{&core.ColumnRef{Name: "name"}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
	if len(result.Rows[0].Values) != 1 {
		t.Errorf("expected 1 value, got %d", len(result.Rows[0].Values))
	}
	if result.Rows[0].Values[0].Str != "alice" {
		t.Errorf("expected 'alice', got %v", result.Rows[0].Values[0])
	}
}

// ============================================================
// Phase 2: UPDATE tests
// ============================================================

func TestUpdate(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	// UPDATE users SET name = 'updated' WHERE id = 1
	stmt := &core.UpdateStmt{
		Table: "users",
		Set: []core.Assignment{
			{Column: "name", Value: &core.Literal{Value: core.NewText("updated")}},
		},
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.RowsAffected != 1 {
		t.Errorf("expected 1 row affected, got %d", result.RowsAffected)
	}

	// Verify
	selectStmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	}
	selectResult, _ := executor.Execute(eng, tx, selectStmt)
	if len(selectResult.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(selectResult.Rows))
	}

	// Because of Gear-2 updates, the physical order might change.
	// We must check values by their ID, not their array index!
	for _, row := range selectResult.Rows {
		id := row.Values[0].Int
		name := row.Values[1].Str

		if id == 1 && name != "updated" {
			t.Errorf("expected id 1 to be 'updated', got %v", name)
		}
		if id == 2 && name != "bob" {
			t.Errorf("expected id 2 to be 'bob', got %v", name)
		}
	}
}

func TestUpdateAllRows(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	// UPDATE users SET name = 'all' (no WHERE)
	stmt := &core.UpdateStmt{
		Table: "users",
		Set: []core.Assignment{
			{Column: "name", Value: &core.Literal{Value: core.NewText("all")}},
		},
		Where: nil,
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.RowsAffected != 3 {
		t.Errorf("expected 3 rows affected, got %d", result.RowsAffected)
	}

	// Verify all rows updated
	selectStmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	}
	selectResult, _ := executor.Execute(eng, tx, selectStmt)
	for i, row := range selectResult.Rows {
		if row.Values[1].Str != "all" {
			t.Errorf("row %d: expected 'all', got %v", i, row.Values[1])
		}
	}
}

// ============================================================
// Phase 2: DELETE tests
// ============================================================

func TestDelete(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	// DELETE FROM users WHERE id = 1
	stmt := &core.DeleteStmt{
		Table: "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if result.RowsAffected != 1 {
		t.Errorf("expected 1 row affected, got %d", result.RowsAffected)
	}

	// Verify
	selectStmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	}
	selectResult, _ := executor.Execute(eng, tx, selectStmt)
	if len(selectResult.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(selectResult.Rows))
	}
}

func TestDeleteAllRows(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	// DELETE FROM users (no WHERE)
	stmt := &core.DeleteStmt{
		Table: "users",
		Where: nil,
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if result.RowsAffected != 3 {
		t.Errorf("expected 3 rows affected, got %d", result.RowsAffected)
	}

	// Verify all rows deleted
	selectStmt := &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where:      nil,
	}
	selectResult, _ := executor.Execute(eng, tx, selectStmt)
	if len(selectResult.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(selectResult.Rows))
	}
}

// ============================================================
// Phase 2: JOIN tests
// ============================================================

func TestJoin(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	// Create users table
	setupUsersTable(t, eng, tx)

	// Create orders table
	executor.Execute(eng, tx, &core.CreateTableStmt{
		Table: "orders",
		Columns: []core.Column{
			{Name: "order_id", Type: core.TypeInt},
			{Name: "user_id", Type: core.TypeInt},
			{Name: "product", Type: core.TypeText},
		},
	})
	executor.Execute(eng, tx, &core.InsertStmt{
		Table: "orders",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(101)},
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("laptop")},
		},
	})
	executor.Execute(eng, tx, &core.InsertStmt{
		Table: "orders",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(102)},
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("phone")},
		},
	})
	executor.Execute(eng, tx, &core.InsertStmt{
		Table: "orders",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(103)},
			&core.Literal{Value: core.NewInt(2)},
			&core.Literal{Value: core.NewText("tablet")},
		},
	})

	// SELECT users.name, orders.product FROM users JOIN orders ON users.id = orders.user_id
	stmt := &core.SelectStmt{
		Projection: []core.Expr{
			&core.ColumnRef{Name: "name"},
			&core.ColumnRef{Name: "product"},
		},
		From: "users",
		Join: &core.JoinClause{
			Table: "orders",
			On: &core.BinaryExpr{
				Op:    core.OpEq,
				Left:  &core.ColumnRef{Name: "id"},
				Right: &core.ColumnRef{Name: "user_id"},
			},
		},
		Where: nil,
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(result.Rows))
	}
	// Check first row: alice, laptop
	if result.Rows[0].Values[0].Str != "alice" {
		t.Errorf("expected 'alice', got %v", result.Rows[0].Values[0])
	}
	if result.Rows[0].Values[1].Str != "laptop" {
		t.Errorf("expected 'laptop', got %v", result.Rows[0].Values[1])
	}
}

func TestJoinWithWhere(t *testing.T) {
	eng := setupEngine(t)
	tx, _ := eng.Begin()
	defer tx.Rollback()

	// Setup (same as above but with more data)
	setupUsersTable(t, eng, tx)

	executor.Execute(eng, tx, &core.CreateTableStmt{
		Table: "orders",
		Columns: []core.Column{
			{Name: "order_id", Type: core.TypeInt},
			{Name: "user_id", Type: core.TypeInt},
			{Name: "product", Type: core.TypeText},
			{Name: "price", Type: core.TypeInt},
		},
	})
	executor.Execute(eng, tx, &core.InsertStmt{
		Table: "orders",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(101)},
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("laptop")},
			&core.Literal{Value: core.NewInt(1000)},
		},
	})
	executor.Execute(eng, tx, &core.InsertStmt{
		Table: "orders",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(102)},
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("phone")},
			&core.Literal{Value: core.NewInt(500)},
		},
	})
	executor.Execute(eng, tx, &core.InsertStmt{
		Table: "orders",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(103)},
			&core.Literal{Value: core.NewInt(3)},
			&core.Literal{Value: core.NewText("keyboard")},
			&core.Literal{Value: core.NewInt(50)},
		},
	})

	// SELECT users.name, orders.product, orders.price
	// FROM users JOIN orders ON users.id = orders.user_id
	// WHERE orders.price > 100
	stmt := &core.SelectStmt{
		Projection: []core.Expr{
			&core.ColumnRef{Name: "name"},
			&core.ColumnRef{Name: "product"},
			&core.ColumnRef{Name: "price"},
		},
		From: "users",
		Join: &core.JoinClause{
			Table: "orders",
			On: &core.BinaryExpr{
				Op:    core.OpEq,
				Left:  &core.ColumnRef{Name: "id"},
				Right: &core.ColumnRef{Name: "user_id"},
			},
		},
		Where: &core.BinaryExpr{
			Op:    core.OpGt,
			Left:  &core.ColumnRef{Name: "price"},
			Right: &core.Literal{Value: core.NewInt(100)},
		},
	}
	result, err := executor.Execute(eng, tx, stmt)
	if err != nil {
		t.Fatalf("Join with where failed: %v", err)
	}
	// Should get: alice-laptop-1000, alice-phone-500 (3 rows total, but only 2 with price > 100)
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}
}

// ============================================================
// Phase 3: INDEX tests
// ============================================================

func TestCreateIndex(t *testing.T) {
	eng := newCountingEngine()
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	_, err := executor.Execute(eng, tx, &core.CreateIndexStmt{
		Table:  "users",
		Column: "id",
	})
	if err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}
	if !eng.HasIndex("users", "id") {
		t.Fatal("expected users.id index to exist")
	}
}

func TestIndexedSelectUsesSeekIndex(t *testing.T) {
	eng := newCountingEngine()
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)
	_, err := executor.Execute(eng, tx, &core.CreateIndexStmt{Table: "users", Column: "id"})
	if err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	result, err := executor.Execute(eng, tx, &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(2)},
		},
	})
	if err != nil {
		t.Fatalf("indexed select failed: %v", err)
	}
	if eng.seekCount == 0 {
		t.Fatal("expected indexed select to call SeekIndex")
	}
	if len(result.Rows) != 1 || result.Rows[0].Values[1].Str != "bob" {
		t.Fatalf("unexpected indexed select result: %+v", result.Rows)
	}
}

func TestFallbackSelectDoesNotUseSeekIndex(t *testing.T) {
	eng := newCountingEngine()
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)

	result, err := executor.Execute(eng, tx, &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(2)},
		},
	})
	if err != nil {
		t.Fatalf("fallback select failed: %v", err)
	}
	if eng.seekCount != 0 {
		t.Fatalf("expected fallback select not to call SeekIndex, got %d calls", eng.seekCount)
	}
	if len(result.Rows) != 1 || result.Rows[0].Values[1].Str != "bob" {
		t.Fatalf("unexpected fallback select result: %+v", result.Rows)
	}
}

func TestIndexedJoinUsesSeekIndex(t *testing.T) {
	eng := newCountingEngine()
	tx, _ := eng.Begin()
	defer tx.Rollback()

	setupUsersTable(t, eng, tx)
	setupOrdersTable(t, eng, tx)
	_, err := executor.Execute(eng, tx, &core.CreateIndexStmt{Table: "orders", Column: "user_id"})
	if err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	result, err := executor.Execute(eng, tx, usersOrdersJoinStmt())
	if err != nil {
		t.Fatalf("indexed join failed: %v", err)
	}
	if eng.seekCount == 0 {
		t.Fatal("expected indexed join to call SeekIndex")
	}
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 joined rows, got %d", len(result.Rows))
	}
}

func TestIndexedAndNonIndexedResultEquivalence(t *testing.T) {
	indexed := newCountingEngine()
	indexedTx, _ := indexed.Begin()
	defer indexedTx.Rollback()
	setupUsersTable(t, indexed, indexedTx)
	_, err := executor.Execute(indexed, indexedTx, &core.CreateIndexStmt{Table: "users", Column: "id"})
	if err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	plain := newCountingEngine()
	plainTx, _ := plain.Begin()
	defer plainTx.Rollback()
	setupUsersTable(t, plain, plainTx)

	stmt := &core.SelectStmt{
		Projection: []core.Expr{
			&core.ColumnRef{Name: "id"},
			&core.ColumnRef{Name: "name"},
		},
		From: "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(2)},
		},
	}

	indexedResult, err := executor.Execute(indexed, indexedTx, stmt)
	if err != nil {
		t.Fatalf("indexed select failed: %v", err)
	}
	plainResult, err := executor.Execute(plain, plainTx, stmt)
	if err != nil {
		t.Fatalf("plain select failed: %v", err)
	}

	if !reflect.DeepEqual(indexedResult, plainResult) {
		t.Fatalf("indexed result = %+v, plain result = %+v", indexedResult, plainResult)
	}
	if indexed.seekCount == 0 {
		t.Fatal("expected indexed path to use SeekIndex")
	}
	if plain.seekCount != 0 {
		t.Fatalf("expected non-indexed path not to use SeekIndex, got %d calls", plain.seekCount)
	}
}

// ============================================================
// Test helpers
// ============================================================

type countingEngine struct {
	core.StorageEngine
	seekCount int
}

func newCountingEngine() *countingEngine {
	return &countingEngine{StorageEngine: memstore.New()}
}

func (e *countingEngine) SeekIndex(txn core.Txn, table, column string, key core.Value) (core.RowIterator, error) {
	e.seekCount++
	return e.StorageEngine.SeekIndex(txn, table, column, key)
}

func setupUsersTable(t *testing.T, eng core.StorageEngine, txn core.Txn) {
	t.Helper()
	executor.Execute(eng, txn, &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
			{Name: "active", Type: core.TypeBool},
		},
	})
	executor.Execute(eng, txn, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("alice")},
			&core.Literal{Value: core.NewBool(true)},
		},
	})
	executor.Execute(eng, txn, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(2)},
			&core.Literal{Value: core.NewText("bob")},
			&core.Literal{Value: core.NewBool(false)},
		},
	})
	executor.Execute(eng, txn, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(3)},
			&core.Literal{Value: core.NewText("charlie")},
			&core.Literal{Value: core.NewBool(true)},
		},
	})
}

func setupOrdersTable(t *testing.T, eng core.StorageEngine, txn core.Txn) {
	t.Helper()
	executor.Execute(eng, txn, &core.CreateTableStmt{
		Table: "orders",
		Columns: []core.Column{
			{Name: "order_id", Type: core.TypeInt},
			{Name: "user_id", Type: core.TypeInt},
			{Name: "product", Type: core.TypeText},
		},
	})
	orders := []struct {
		id      int64
		userID  int64
		product string
	}{
		{id: 101, userID: 1, product: "laptop"},
		{id: 102, userID: 1, product: "phone"},
		{id: 103, userID: 2, product: "tablet"},
	}
	for _, order := range orders {
		executor.Execute(eng, txn, &core.InsertStmt{
			Table: "orders",
			Values: []core.Expr{
				&core.Literal{Value: core.NewInt(order.id)},
				&core.Literal{Value: core.NewInt(order.userID)},
				&core.Literal{Value: core.NewText(order.product)},
			},
		})
	}
}

func usersOrdersJoinStmt() *core.SelectStmt {
	return &core.SelectStmt{
		Projection: []core.Expr{
			&core.ColumnRef{Name: "name"},
			&core.ColumnRef{Name: "product"},
		},
		From: "users",
		Join: &core.JoinClause{
			Table: "orders",
			On: &core.BinaryExpr{
				Op:    core.OpEq,
				Left:  &core.ColumnRef{Name: "id"},
				Right: &core.ColumnRef{Name: "user_id"},
			},
		},
	}
}
