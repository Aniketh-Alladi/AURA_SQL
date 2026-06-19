package executor

import (
	"fmt"

	"aurasql/core"
)

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
		schema := core.Schema{Columns: s.Columns}
		if err := eng.CreateTable(txn, s.Table, schema); err != nil {
			return core.Result{}, fmt.Errorf("create table %q: %w", s.Table, err)
		}
		return core.Result{}, nil
	case *core.InsertStmt:
		schema, ok := eng.GetSchema(s.Table)
		if !ok {
			return core.Result{}, fmt.Errorf("table %q does not exist", s.Table)
		}
		if len(s.Values) != len(schema.Columns) {
			return core.Result{}, fmt.Errorf("insert into %q has %d values for %d columns", s.Table, len(s.Values), len(schema.Columns))
		}

		values := make([]core.Value, len(s.Values))
		for i, expr := range s.Values {
			value, err := eval(expr, core.Row{}, core.Schema{})
			if err != nil {
				return core.Result{}, fmt.Errorf("insert into %q value for column %q: %w", s.Table, schema.Columns[i].Name, err)
			}
			if value.Type != schema.Columns[i].Type {
				return core.Result{}, fmt.Errorf("insert into %q column %q expects %s, got %s", s.Table, schema.Columns[i].Name, schema.Columns[i].Type, value.Type)
			}
			values[i] = value
		}

		row := core.Row{
			Values: values,
		}
		if _, err := eng.Insert(txn, s.Table, row); err != nil {
			return core.Result{}, fmt.Errorf("insert into %q: %w", s.Table, err)
		}
		return core.Result{RowsAffected: 1}, nil
	case *core.SelectStmt:
		schema, ok := eng.GetSchema(s.From)
		if !ok {
			return core.Result{}, fmt.Errorf("table %q does not exist", s.From)
		}
		if s.Join != nil {
			return core.Result{}, fmt.Errorf("select from %q: joins are not supported", s.From)
		}

		selectStar := isStarProjection(s.Projection)
		resultSchema := schema
		if !selectStar {
			projectedSchema, err := buildProjectionSchema(s.Projection, schema)
			if err != nil {
				return core.Result{}, fmt.Errorf("select from %q: %w", s.From, err)
			}
			resultSchema = projectedSchema
		}

		it, err := eng.Scan(txn, s.From)
		if err != nil {
			return core.Result{}, fmt.Errorf("select from %q: %w", s.From, err)
		}

		rows := []core.Row{}
		for {
			_, row, ok, err := it.Next()
			if err != nil {
				closeErr := it.Close()
				if closeErr != nil {
					return core.Result{}, fmt.Errorf("select from %q: scan error: %v; close error: %w", s.From, err, closeErr)
				}
				return core.Result{}, fmt.Errorf("select from %q: %w", s.From, err)
			}
			if !ok {
				break
			}
			if s.Where != nil {
				matches, err := rowMatchesWhere(s.Where, row, schema)
				if err != nil {
					closeErr := it.Close()
					if closeErr != nil {
						return core.Result{}, fmt.Errorf("select from %q: where error: %v; close error: %w", s.From, err, closeErr)
					}
					return core.Result{}, fmt.Errorf("select from %q: where: %w", s.From, err)
				}
				if !matches {
					continue
				}
			}
			if selectStar {
				rows = append(rows, row)
				continue
			}

			projectedRow, err := projectRow(s.Projection, row, schema)
			if err != nil {
				closeErr := it.Close()
				if closeErr != nil {
					return core.Result{}, fmt.Errorf("select from %q: projection error: %v; close error: %w", s.From, err, closeErr)
				}
				return core.Result{}, fmt.Errorf("select from %q: projection: %w", s.From, err)
			}
			rows = append(rows, projectedRow)
		}
		if err := it.Close(); err != nil {
			return core.Result{}, fmt.Errorf("select from %q: close iterator: %w", s.From, err)
		}

		return core.Result{
			Schema: resultSchema,
			Rows:   rows,
		}, nil
	default:
		return core.Result{}, fmt.Errorf("unsupported statement type %T", stmt)
	}
}

func isStarProjection(projection []core.Expr) bool {
	if len(projection) != 1 {
		return false
	}
	_, ok := projection[0].(*core.Star)
	return ok
}

func rowMatchesWhere(where core.Expr, row core.Row, schema core.Schema) (bool, error) {
	value, err := eval(where, row, schema)
	if err != nil {
		return false, err
	}
	if value.Null || value.Type != core.TypeBool {
		return false, fmt.Errorf("where must evaluate to a non-null boolean")
	}
	return value.Bool, nil
}

func projectRow(projection []core.Expr, row core.Row, schema core.Schema) (core.Row, error) {
	values := make([]core.Value, len(projection))
	for i, expr := range projection {
		value, err := eval(expr, row, schema)
		if err != nil {
			return core.Row{}, err
		}
		values[i] = value
	}
	return core.Row{Values: values}, nil
}

func buildProjectionSchema(projection []core.Expr, schema core.Schema) (core.Schema, error) {
	columns := make([]core.Column, len(projection))
	for i, expr := range projection {
		column, err := projectionColumn(expr, i, schema)
		if err != nil {
			return core.Schema{}, err
		}
		columns[i] = column
	}
	return core.Schema{Columns: columns}, nil
}

func projectionColumn(expr core.Expr, index int, schema core.Schema) (core.Column, error) {
	switch e := expr.(type) {
	case *core.ColumnRef:
		columnIndex := schema.ColumnIndex(e.Name)
		if columnIndex < 0 {
			return core.Column{}, fmt.Errorf("column %q does not exist", e.Name)
		}
		return core.Column{Name: e.Name, Type: schema.Columns[columnIndex].Type}, nil
	case *core.Literal:
		return core.Column{Name: fmt.Sprintf("expr%d", index+1), Type: e.Value.Type}, nil
	case *core.BinaryExpr:
		return core.Column{Name: fmt.Sprintf("expr%d", index+1), Type: core.TypeBool}, nil
	default:
		return core.Column{}, fmt.Errorf("unsupported projection expression type %T", expr)
	}
}
