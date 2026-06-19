package executor

import (
	"strings"
	"testing"

	"aurasql/core"
	"aurasql/memstore"
)

func TestExecuteInsertSuccess(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)

	result, err := Execute(eng, txn, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewText("tejus")},
			&core.Literal{Value: core.NewBool(true)},
		},
	})
	if err != nil {
		t.Fatalf("Execute insert returned error: %v", err)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d, want 1", result.RowsAffected)
	}

	it, err := eng.Scan(txn, "users")
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	defer it.Close()

	_, row, ok, err := it.Next()
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if !ok {
		t.Fatal("inserted row was not found")
	}

	want := []core.Value{core.NewInt(1), core.NewText("tejus"), core.NewBool(true)}
	for i, value := range want {
		if row.Values[i] != value {
			t.Fatalf("row.Values[%d] = %v, want %v", i, row.Values[i], value)
		}
	}

	_, _, ok, err = it.Next()
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if ok {
		t.Fatal("scan returned more than one row")
	}
}

func TestExecuteInsertTableDoesNotExist(t *testing.T) {
	eng, txn := newTestEngine(t)

	_, err := Execute(eng, txn, &core.InsertStmt{
		Table: "missing",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
		},
	})
	if err == nil {
		t.Fatal("Execute insert into missing table returned nil error")
	}
	if !strings.Contains(err.Error(), `table "missing" does not exist`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExecuteInsertArityMismatch(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)

	_, err := Execute(eng, txn, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
		},
	})
	if err == nil {
		t.Fatal("Execute insert with arity mismatch returned nil error")
	}
	if !strings.Contains(err.Error(), `insert into "users" has 1 values for 3 columns`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExecuteInsertTypeMismatch(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)

	_, err := Execute(eng, txn, &core.InsertStmt{
		Table: "users",
		Values: []core.Expr{
			&core.Literal{Value: core.NewInt(1)},
			&core.Literal{Value: core.NewBool(false)},
			&core.Literal{Value: core.NewBool(true)},
		},
	})
	if err == nil {
		t.Fatal("Execute insert with type mismatch returned nil error")
	}
	if !strings.Contains(err.Error(), `column "name" expects TEXT, got BOOL`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExecuteSelectWhereActiveEqualsTrue(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "active"},
			Right: &core.Literal{Value: core.NewBool(true)},
		},
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultIDs(t, result, []int64{1, 3})
}

func TestExecuteSelectWhereIDGreaterThanOne(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpGt,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultIDs(t, result, []int64{2, 3})
}

func TestExecuteSelectWhereActiveEqualsTrueAndIDGreaterThanOne(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op: core.OpAnd,
			Left: &core.BinaryExpr{
				Op:    core.OpEq,
				Left:  &core.ColumnRef{Name: "active"},
				Right: &core.Literal{Value: core.NewBool(true)},
			},
			Right: &core.BinaryExpr{
				Op:    core.OpGt,
				Left:  &core.ColumnRef{Name: "id"},
				Right: &core.Literal{Value: core.NewInt(1)},
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultIDs(t, result, []int64{3})
}

func TestExecuteSelectWhereFiltersOutAllRows(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.Star{}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpGt,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(10)},
		},
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultIDs(t, result, nil)
}

func TestExecuteSelectNameProjection(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.ColumnRef{Name: "name"}},
		From:       "users",
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultColumns(t, result, []core.Column{{Name: "name", Type: core.TypeText}})
	assertResultValues(t, result, [][]core.Value{
		{core.NewText("varun")},
		{core.NewText("tejus")},
		{core.NewText("aniketh")},
	})
}

func TestExecuteSelectIDNameProjection(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{
			&core.ColumnRef{Name: "id"},
			&core.ColumnRef{Name: "name"},
		},
		From: "users",
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultColumns(t, result, []core.Column{
		{Name: "id", Type: core.TypeInt},
		{Name: "name", Type: core.TypeText},
	})
	assertResultValues(t, result, [][]core.Value{
		{core.NewInt(1), core.NewText("varun")},
		{core.NewInt(2), core.NewText("tejus")},
		{core.NewInt(3), core.NewText("aniketh")},
	})
}

func TestExecuteSelectNameProjectionWhereActiveEqualsTrue(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.ColumnRef{Name: "name"}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpEq,
			Left:  &core.ColumnRef{Name: "active"},
			Right: &core.Literal{Value: core.NewBool(true)},
		},
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultColumns(t, result, []core.Column{{Name: "name", Type: core.TypeText}})
	assertResultValues(t, result, [][]core.Value{
		{core.NewText("varun")},
		{core.NewText("aniketh")},
	})
}

func TestExecuteSelectProjectionOfMultipleMatchingRows(t *testing.T) {
	eng, txn := newTestEngine(t)
	createUsersTable(t, eng, txn)
	insertUsers(t, eng, txn)

	result, err := Execute(eng, txn, &core.SelectStmt{
		Projection: []core.Expr{&core.ColumnRef{Name: "active"}},
		From:       "users",
		Where: &core.BinaryExpr{
			Op:    core.OpGt,
			Left:  &core.ColumnRef{Name: "id"},
			Right: &core.Literal{Value: core.NewInt(1)},
		},
	})
	if err != nil {
		t.Fatalf("Execute select returned error: %v", err)
	}
	assertResultColumns(t, result, []core.Column{{Name: "active", Type: core.TypeBool}})
	assertResultValues(t, result, [][]core.Value{
		{core.NewBool(false)},
		{core.NewBool(true)},
	})
}

func newTestEngine(t *testing.T) (*memstore.Engine, core.Txn) {
	t.Helper()

	eng := memstore.New()
	txn, err := eng.Begin()
	if err != nil {
		t.Fatalf("Begin returned error: %v", err)
	}
	return eng, txn
}

func createUsersTable(t *testing.T, eng core.StorageEngine, txn core.Txn) {
	t.Helper()

	_, err := Execute(eng, txn, &core.CreateTableStmt{
		Table: "users",
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
			{Name: "active", Type: core.TypeBool},
		},
	})
	if err != nil {
		t.Fatalf("Execute create table returned error: %v", err)
	}
}

func assertResultColumns(t *testing.T, result core.Result, want []core.Column) {
	t.Helper()

	if len(result.Schema.Columns) != len(want) {
		t.Fatalf("result schema has %d columns, want %d", len(result.Schema.Columns), len(want))
	}
	for i, column := range want {
		if result.Schema.Columns[i] != column {
			t.Fatalf("result schema column %d = %+v, want %+v", i, result.Schema.Columns[i], column)
		}
	}
}

func assertResultValues(t *testing.T, result core.Result, want [][]core.Value) {
	t.Helper()

	if len(result.Rows) != len(want) {
		t.Fatalf("result has %d rows, want %d", len(result.Rows), len(want))
	}
	for rowIndex, row := range want {
		if len(result.Rows[rowIndex].Values) != len(row) {
			t.Fatalf("row %d has %d values, want %d", rowIndex, len(result.Rows[rowIndex].Values), len(row))
		}
		for valueIndex, value := range row {
			if result.Rows[rowIndex].Values[valueIndex] != value {
				t.Fatalf("row %d value %d = %v, want %v", rowIndex, valueIndex, result.Rows[rowIndex].Values[valueIndex], value)
			}
		}
	}
}

func insertUsers(t *testing.T, eng core.StorageEngine, txn core.Txn) {
	t.Helper()

	rows := []struct {
		id     int64
		name   string
		active bool
	}{
		{id: 1, name: "varun", active: true},
		{id: 2, name: "tejus", active: false},
		{id: 3, name: "aniketh", active: true},
	}

	for _, row := range rows {
		_, err := Execute(eng, txn, &core.InsertStmt{
			Table: "users",
			Values: []core.Expr{
				&core.Literal{Value: core.NewInt(row.id)},
				&core.Literal{Value: core.NewText(row.name)},
				&core.Literal{Value: core.NewBool(row.active)},
			},
		})
		if err != nil {
			t.Fatalf("Execute insert returned error: %v", err)
		}
	}
}

func assertResultIDs(t *testing.T, result core.Result, want []int64) {
	t.Helper()

	if len(result.Schema.Columns) != 3 {
		t.Fatalf("result schema has %d columns, want 3", len(result.Schema.Columns))
	}
	if len(result.Rows) != len(want) {
		t.Fatalf("result has %d rows, want %d", len(result.Rows), len(want))
	}
	for i, id := range want {
		if got := result.Rows[i].Values[0]; got != core.NewInt(id) {
			t.Fatalf("row %d id = %v, want %v", i, got, core.NewInt(id))
		}
	}
}
