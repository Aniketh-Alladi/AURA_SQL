package executor

import (
	"fmt"

	"aurasql/core"
)

// Execute processes a parsed SQL statement against the storage engine.
// Now builds operator trees instead of direct execution.
func Execute(eng core.StorageEngine, txn core.Txn, stmt core.Statement) (core.Result, error) {
	if eng == nil {
		return core.Result{}, fmt.Errorf("storage engine is nil")
	}
	if txn == nil {
		return core.Result{}, fmt.Errorf("transaction is nil")
	}
	if stmt == nil {
		return core.Result{}, fmt.Errorf("statement is nil")
	}

	switch s := stmt.(type) {
	case *core.CreateTableStmt:
		return executeCreateTable(eng, txn, s)
	case *core.InsertStmt:
		return executeInsert(eng, txn, s)
	case *core.SelectStmt:
		return executeSelect(eng, txn, s)
	case *core.UpdateStmt:
		return executeUpdate(eng, txn, s)
	case *core.DeleteStmt:
		return executeDelete(eng, txn, s)
	default:
		return core.Result{}, fmt.Errorf("unsupported statement type %T", stmt)
	}
}

// ============================================================
// DDL
// ============================================================

func executeCreateTable(eng core.StorageEngine, txn core.Txn, stmt *core.CreateTableStmt) (core.Result, error) {
	schema := core.Schema{Columns: stmt.Columns}
	if err := eng.CreateTable(txn, stmt.Table, schema); err != nil {
		return core.Result{}, fmt.Errorf("create table %q: %w", stmt.Table, err)
	}
	return core.Result{}, nil
}

// ============================================================
// DML: INSERT
// ============================================================

func executeInsert(eng core.StorageEngine, txn core.Txn, stmt *core.InsertStmt) (core.Result, error) {
	schema, ok := eng.GetSchema(stmt.Table)
	if !ok {
		return core.Result{}, fmt.Errorf("table %q does not exist", stmt.Table)
	}
	if len(stmt.Values) != len(schema.Columns) {
		return core.Result{}, fmt.Errorf("insert into %q has %d values for %d columns",
			stmt.Table, len(stmt.Values), len(schema.Columns))
	}

	values := make([]core.Value, len(stmt.Values))
	for i, expr := range stmt.Values {
		value, err := eval(expr, core.Row{}, core.Schema{})
		if err != nil {
			return core.Result{}, fmt.Errorf("insert into %q value for column %q: %w",
				stmt.Table, schema.Columns[i].Name, err)
		}
		// FIX: Check value.Null instead of core.TypeNull
		if !value.Null && value.Type != schema.Columns[i].Type {
			return core.Result{}, fmt.Errorf("insert into %q column %q expects %s, got %s",
				stmt.Table, schema.Columns[i].Name, schema.Columns[i].Type, value.Type)
		}
		values[i] = value
	}

	row := core.Row{Values: values}
	if _, err := eng.Insert(txn, stmt.Table, row); err != nil {
		return core.Result{}, fmt.Errorf("insert into %q: %w", stmt.Table, err)
	}
	return core.Result{RowsAffected: 1}, nil
}

// ============================================================
// DML: SELECT (using operator model)
// ============================================================

func executeSelect(eng core.StorageEngine, txn core.Txn, stmt *core.SelectStmt) (core.Result, error) {
	// Build the operator tree
	root, err := buildSelectPlan(eng, txn, stmt)
	if err != nil {
		return core.Result{}, fmt.Errorf("build plan for %q: %w", stmt.From, err)
	}
	defer root.Close()

	// Drain the operator
	rows := []core.Row{}
	for {
		row, ok, err := root.Next()
		if err != nil {
			return core.Result{}, fmt.Errorf("execute plan: %w", err)
		}
		if !ok {
			break
		}
		rows = append(rows, row)
	}

	return core.Result{
		Schema: root.Schema(),
		Rows:   rows,
	}, nil
}

func buildSelectPlan(eng core.StorageEngine, txn core.Txn, stmt *core.SelectStmt) (Operator, error) {
	// Start with ScanOp for the FROM table
	scan, err := NewScanOp(eng, txn, stmt.From)
	if err != nil {
		return nil, err
	}
	var root Operator = scan

	// Add JOIN if present
	if stmt.Join != nil {
		rightScan, err := NewScanOp(eng, txn, stmt.Join.Table)
		if err != nil {
			return nil, fmt.Errorf("join table %q: %w", stmt.Join.Table, err)
		}
		join, err := NewNestedLoopJoinOp(root, rightScan, stmt.Join.On)
		if err != nil {
			return nil, fmt.Errorf("create join: %w", err)
		}
		root = join
	}

	// Add WHERE filter
	if stmt.Where != nil {
		root = NewFilterOp(root, stmt.Where)
	}

	// Add projection
	project, err := NewProjectOp(root, stmt.Projection)
	if err != nil {
		return nil, fmt.Errorf("create projection: %w", err)
	}
	root = project

	return root, nil
}

// ============================================================
// DML: UPDATE (materialize-then-mutate)
// ============================================================

func executeUpdate(eng core.StorageEngine, txn core.Txn, stmt *core.UpdateStmt) (core.Result, error) {
	schema, ok := eng.GetSchema(stmt.Table)
	if !ok {
		return core.Result{}, fmt.Errorf("table %q does not exist", stmt.Table)
	}

	// Phase 1: Materialize matching RowIDs
	iter, err := eng.Scan(txn, stmt.Table)
	if err != nil {
		return core.Result{}, fmt.Errorf("scan table %q: %w", stmt.Table, err)
	}
	defer iter.Close()

	var rowIDs []core.RowID
	var rows []core.Row

	for {
		rowID, row, ok, err := iter.Next()
		if err != nil {
			return core.Result{}, fmt.Errorf("scan next: %w", err)
		}
		if !ok {
			break
		}

		// Apply WHERE clause (nil WHERE = all rows)
		if stmt.Where != nil {
			matches, err := evalWhere(stmt.Where, row, schema)
			if err != nil {
				return core.Result{}, fmt.Errorf("where clause: %w", err)
			}
			if !matches {
				continue
			}
		}
		rowIDs = append(rowIDs, rowID)
		rows = append(rows, row)
	}

	// Phase 2: Apply updates to materialized rows
	if err := iter.Close(); err != nil {
		return core.Result{}, fmt.Errorf("close scan: %w", err)
	}

	for i, rowID := range rowIDs {
		row := rows[i]
		newRow, err := applyUpdateAssignments(stmt.Set, row, schema)
		if err != nil {
			return core.Result{}, fmt.Errorf("apply assignments for row %d: %w", i, err)
		}
		if err := eng.Update(txn, stmt.Table, rowID, newRow); err != nil {
			return core.Result{}, fmt.Errorf("update row %d: %w", i, err)
		}
	}

	return core.Result{RowsAffected: len(rowIDs)}, nil
}

func applyUpdateAssignments(assignments []core.Assignment, row core.Row, schema core.Schema) (core.Row, error) {
	newRow := row
	for _, assign := range assignments {
		idx := schema.ColumnIndex(assign.Column)
		if idx < 0 {
			return core.Row{}, fmt.Errorf("column %q does not exist", assign.Column)
		}
		val, err := eval(assign.Value, row, schema)
		if err != nil {
			return core.Row{}, fmt.Errorf("evaluate assignment for %q: %w", assign.Column, err)
		}
		// FIX: Check value.Null instead of core.TypeNull
		if !val.Null && val.Type != schema.Columns[idx].Type {
			return core.Row{}, fmt.Errorf("type mismatch for column %q: expected %s, got %s",
				assign.Column, schema.Columns[idx].Type, val.Type)
		}
		newRow.Values[idx] = val
	}
	return newRow, nil
}

// ============================================================
// DML: DELETE (materialize-then-mutate)
// ============================================================

func executeDelete(eng core.StorageEngine, txn core.Txn, stmt *core.DeleteStmt) (core.Result, error) {
	schema, ok := eng.GetSchema(stmt.Table)
	if !ok {
		return core.Result{}, fmt.Errorf("table %q does not exist", stmt.Table)
	}

	// Phase 1: Materialize matching RowIDs
	iter, err := eng.Scan(txn, stmt.Table)
	if err != nil {
		return core.Result{}, fmt.Errorf("scan table %q: %w", stmt.Table, err)
	}
	defer iter.Close()

	var rowIDs []core.RowID

	for {
		rowID, row, ok, err := iter.Next()
		if err != nil {
			return core.Result{}, fmt.Errorf("scan next: %w", err)
		}
		if !ok {
			break
		}

		// Apply WHERE clause (nil WHERE = all rows)
		if stmt.Where != nil {
			matches, err := evalWhere(stmt.Where, row, schema)
			if err != nil {
				return core.Result{}, fmt.Errorf("where clause: %w", err)
			}
			if !matches {
				continue
			}
		}
		rowIDs = append(rowIDs, rowID)
	}

	// Phase 2: Delete materialized rows
	if err := iter.Close(); err != nil {
		return core.Result{}, fmt.Errorf("close scan: %w", err)
	}

	for _, rowID := range rowIDs {
		if err := eng.Delete(txn, stmt.Table, rowID); err != nil {
			return core.Result{}, fmt.Errorf("delete row %d: %w", rowID, err)
		}
	}

	return core.Result{RowsAffected: len(rowIDs)}, nil
}
