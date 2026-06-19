package executor

import (
	"fmt"

	"aurasql/core"
)

func eval(e core.Expr, row core.Row, schema core.Schema) (core.Value, error) {
	switch expr := e.(type) {
	case *core.Literal:
		return expr.Value, nil
	case *core.ColumnRef:
		idx := schema.ColumnIndex(expr.Name)
		if idx < 0 {
			return core.Value{}, fmt.Errorf("column %q does not exist", expr.Name)
		}
		if idx >= len(row.Values) {
			return core.Value{}, fmt.Errorf("column %q is not present in row", expr.Name)
		}
		return row.Values[idx], nil
	case *core.BinaryExpr:
		return evalBinary(expr, row, schema)
	default:
		return core.Value{}, fmt.Errorf("unsupported expression type %T", e)
	}
}

func evalBinary(expr *core.BinaryExpr, row core.Row, schema core.Schema) (core.Value, error) {
	left, err := eval(expr.Left, row, schema)
	if err != nil {
		return core.Value{}, err
	}
	right, err := eval(expr.Right, row, schema)
	if err != nil {
		return core.Value{}, err
	}

	switch expr.Op {
	case core.OpEq, core.OpNe, core.OpLt, core.OpLe, core.OpGt, core.OpGe:
		return evalComparison(expr.Op, left, right)
	case core.OpAnd:
		l, r, err := boolOperands(expr.Op, left, right)
		if err != nil {
			return core.Value{}, err
		}
		return core.NewBool(l && r), nil
	case core.OpOr:
		l, r, err := boolOperands(expr.Op, left, right)
		if err != nil {
			return core.Value{}, err
		}
		return core.NewBool(l || r), nil
	default:
		return core.Value{}, fmt.Errorf("unsupported binary operator %d", expr.Op)
	}
}

func evalComparison(op core.BinOp, left, right core.Value) (core.Value, error) {
	cmp, err := left.Compare(right)
	if err != nil {
		return core.Value{}, err
	}

	switch op {
	case core.OpEq:
		return core.NewBool(cmp == 0), nil
	case core.OpNe:
		return core.NewBool(cmp != 0), nil
	case core.OpLt:
		return core.NewBool(cmp < 0), nil
	case core.OpLe:
		return core.NewBool(cmp <= 0), nil
	case core.OpGt:
		return core.NewBool(cmp > 0), nil
	case core.OpGe:
		return core.NewBool(cmp >= 0), nil
	default:
		return core.Value{}, fmt.Errorf("unsupported comparison operator %d", op)
	}
}

func boolOperands(op core.BinOp, left, right core.Value) (bool, bool, error) {
	if left.Null || right.Null {
		return false, false, fmt.Errorf("operator %d requires non-null boolean operands", op)
	}
	if left.Type != core.TypeBool || right.Type != core.TypeBool {
		return false, false, fmt.Errorf("operator %d requires boolean operands, got %s and %s", op, left.Type, right.Type)
	}
	return left.Bool, right.Bool, nil
}
