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
	case *core.CreateIndexStmt:
		return executeCreateIndex(eng, txn, s)
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

func executeCreateIndex(eng core.StorageEngine, txn core.Txn, stmt *core.CreateIndexStmt) (core.Result, error) {
	if err := eng.CreateIndex(txn, stmt.Table, stmt.Column); err != nil {
		return core.Result{}, fmt.Errorf("create index on %q.%q: %w", stmt.Table, stmt.Column, err)
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
	root, remainingWhere, err := buildBaseAccessPath(eng, txn, stmt.From, stmt.Where)
	if err != nil {
		return nil, err
	}

	// Add JOIN if present
	for _, join := range stmt.Joins {
		joinOp, err := buildJoinPlan(eng, txn, root, &join)
		if err != nil {
			return nil, err
		}
		root = joinOp
	}

	// Add WHERE filter
	if remainingWhere != nil {
		root = NewFilterOp(root, remainingWhere)
	}

	// Add projection
	project, err := NewProjectOp(root, stmt.Projection)
	if err != nil {
		return nil, fmt.Errorf("create projection: %w", err)
	}
	root = project

	return root, nil
}

func buildBaseAccessPath(eng core.StorageEngine, txn core.Txn, table string, where core.Expr) (Operator, core.Expr, error) {
	indexed, remaining := findIndexedEquality(eng, table, where)
	if indexed != nil {
		scan, err := NewIndexScanOp(eng, txn, table, indexed.column, indexed.value)
		if err != nil {
			return nil, nil, err
		}
		return scan, remaining, nil
	}

	scan, err := NewScanOp(eng, txn, table)
	if err != nil {
		return nil, nil, err
	}
	return scan, where, nil
}

func buildJoinPlan(eng core.StorageEngine, txn core.Txn, left Operator, join *core.JoinClause) (Operator, error) {
	if indexed := indexedJoinEquality(eng, left.Schema(), join.Table, join.On); indexed != nil {
		joinOp, err := NewIndexedNestedLoopJoinOp(left, eng, txn, join.Table, indexed.rightColumn, indexed.leftColumn, join.On)
		if err != nil {
			return nil, fmt.Errorf("create indexed join: %w", err)
		}
		return joinOp, nil
	}

	rightScan, err := NewScanOp(eng, txn, join.Table)
	if err != nil {
		return nil, fmt.Errorf("join table %q: %w", join.Table, err)
	}
	joinOp, err := NewNestedLoopJoinOp(left, rightScan, join.On)
	if err != nil {
		return nil, fmt.Errorf("create join: %w", err)
	}
	return joinOp, nil
}

type indexedEquality struct {
	column string
	value  core.Value
}

func findIndexedEquality(eng core.StorageEngine, table string, where core.Expr) (*indexedEquality, core.Expr) {
	if where == nil {
		return nil, nil
	}

	terms := splitAndTerms(where)
	for i, term := range terms {
		equality := equalityColumnLiteral(term, table)
		if equality == nil || !eng.HasIndex(table, equality.column) {
			continue
		}

		remaining := append([]core.Expr{}, terms[:i]...)
		remaining = append(remaining, terms[i+1:]...)
		return equality, combineAndTerms(remaining)
	}
	return nil, where
}

func splitAndTerms(expr core.Expr) []core.Expr {
	binary, ok := expr.(*core.BinaryExpr)
	if !ok || binary.Op != core.OpAnd {
		return []core.Expr{expr}
	}

	terms := splitAndTerms(binary.Left)
	terms = append(terms, splitAndTerms(binary.Right)...)
	return terms
}

func combineAndTerms(terms []core.Expr) core.Expr {
	if len(terms) == 0 {
		return nil
	}
	combined := terms[0]
	for _, term := range terms[1:] {
		combined = &core.BinaryExpr{
			Op:    core.OpAnd,
			Left:  combined,
			Right: term,
		}
	}
	return combined
}

func equalityColumnLiteral(expr core.Expr, table string) *indexedEquality {
	binary, ok := expr.(*core.BinaryExpr)
	if !ok || binary.Op != core.OpEq {
		return nil
	}

	if equality := columnLiteral(binary.Left, binary.Right, table); equality != nil {
		return equality
	}
	return columnLiteral(binary.Right, binary.Left, table)
}

func columnLiteral(left core.Expr, right core.Expr, table string) *indexedEquality {
	column, ok := left.(*core.ColumnRef)
	if !ok {
		return nil
	}
	if column.Table != "" && column.Table != table {
		return nil
	}
	literal, ok := right.(*core.Literal)
	if !ok {
		return nil
	}
	return &indexedEquality{
		column: column.Name,
		value:  literal.Value,
	}
}

type indexedJoin struct {
	leftColumn  string
	rightColumn string
}

func indexedJoinEquality(eng core.StorageEngine, leftSchema core.Schema, rightTable string, on core.Expr) *indexedJoin {
	binary, ok := on.(*core.BinaryExpr)
	if !ok || binary.Op != core.OpEq {
		return nil
	}

	rightSchema, ok := eng.GetSchema(rightTable)
	if !ok {
		return nil
	}

	if indexed := joinColumnsForIndex(eng, leftSchema, rightSchema, rightTable, binary.Left, binary.Right); indexed != nil {
		return indexed
	}
	return joinColumnsForIndex(eng, leftSchema, rightSchema, rightTable, binary.Right, binary.Left)
}

func joinColumnsForIndex(
	eng core.StorageEngine,
	leftSchema core.Schema,
	rightSchema core.Schema,
	rightTable string,
	leftExpr core.Expr,
	rightExpr core.Expr,
) *indexedJoin {
	leftColumn, ok := leftExpr.(*core.ColumnRef)
	if !ok {
		return nil
	}
	rightColumn, ok := rightExpr.(*core.ColumnRef)
	if !ok {
		return nil
	}

	if !columnBelongsToSchema(leftColumn, leftSchema, "") {
		return nil
	}
	if !columnBelongsToSchema(rightColumn, rightSchema, rightTable) {
		return nil
	}
	if !eng.HasIndex(rightTable, rightColumn.Name) {
		return nil
	}
	return &indexedJoin{
		leftColumn:  leftColumn.Name,
		rightColumn: rightColumn.Name,
	}
}

func columnBelongsToSchema(column *core.ColumnRef, schema core.Schema, table string) bool {
	if table != "" && column.Table != "" && column.Table != table {
		return false
	}
	return schema.ColumnIndex(column.Name) >= 0
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
