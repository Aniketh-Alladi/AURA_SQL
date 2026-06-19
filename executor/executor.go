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

		row := core.Row{Values: make([]core.Value, len(s.Values))}
		for i, expr := range s.Values {
			value, err := eval(expr, core.Row{}, core.Schema{})
			if err != nil {
				return core.Result{}, fmt.Errorf("insert into %q value for column %q: %w", s.Table, schema.Columns[i].Name, err)
			}
			if value.Type != schema.Columns[i].Type {
				return core.Result{}, fmt.Errorf("insert into %q column %q expects %s, got %s", s.Table, schema.Columns[i].Name, schema.Columns[i].Type, value.Type)
			}
			row.Values[i] = value
		}

		if _, err := eng.Insert(txn, s.Table, row); err != nil {
			return core.Result{}, fmt.Errorf("insert into %q: %w", s.Table, err)
		}
		return core.Result{RowsAffected: 1}, nil
	default:
		return core.Result{}, fmt.Errorf("unsupported statement type %T", stmt)
	}
}
