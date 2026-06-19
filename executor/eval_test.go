package executor

import (
	"strings"
	"testing"

	"aurasql/core"
)

func TestEvalLiteral(t *testing.T) {
	got, err := eval(&core.Literal{Value: core.NewText("hello")}, core.Row{}, core.Schema{})
	if err != nil {
		t.Fatalf("eval literal returned error: %v", err)
	}
	if got != core.NewText("hello") {
		t.Fatalf("eval literal = %v, want %v", got, core.NewText("hello"))
	}
}

func TestEvalColumnRef(t *testing.T) {
	schema := core.Schema{Columns: []core.Column{
		{Name: "id", Type: core.TypeInt},
		{Name: "name", Type: core.TypeText},
	}}
	row := core.Row{Values: []core.Value{
		core.NewInt(1),
		core.NewText("tejus"),
	}}

	got, err := eval(&core.ColumnRef{Name: "name"}, row, schema)
	if err != nil {
		t.Fatalf("eval column ref returned error: %v", err)
	}
	if got != core.NewText("tejus") {
		t.Fatalf("eval column ref = %v, want %v", got, core.NewText("tejus"))
	}
}

func TestEvalComparisons(t *testing.T) {
	tests := []struct {
		name  string
		op    core.BinOp
		left  core.Value
		right core.Value
		want  core.Value
	}{
		{name: "OpEq true", op: core.OpEq, left: core.NewInt(2), right: core.NewInt(2), want: core.NewBool(true)},
		{name: "OpNe true", op: core.OpNe, left: core.NewText("a"), right: core.NewText("b"), want: core.NewBool(true)},
		{name: "OpLt true", op: core.OpLt, left: core.NewInt(1), right: core.NewInt(2), want: core.NewBool(true)},
		{name: "OpLe equal true", op: core.OpLe, left: core.NewInt(2), right: core.NewInt(2), want: core.NewBool(true)},
		{name: "OpGt true", op: core.OpGt, left: core.NewInt(3), right: core.NewInt(2), want: core.NewBool(true)},
		{name: "OpGe equal true", op: core.OpGe, left: core.NewInt(2), right: core.NewInt(2), want: core.NewBool(true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval(&core.BinaryExpr{
				Op:    tt.op,
				Left:  &core.Literal{Value: tt.left},
				Right: &core.Literal{Value: tt.right},
			}, core.Row{}, core.Schema{})
			if err != nil {
				t.Fatalf("eval comparison returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("eval comparison = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvalBooleanOperators(t *testing.T) {
	tests := []struct {
		name  string
		op    core.BinOp
		left  bool
		right bool
		want  core.Value
	}{
		{name: "OpAnd", op: core.OpAnd, left: true, right: false, want: core.NewBool(false)},
		{name: "OpOr", op: core.OpOr, left: true, right: false, want: core.NewBool(true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval(&core.BinaryExpr{
				Op:    tt.op,
				Left:  &core.Literal{Value: core.NewBool(tt.left)},
				Right: &core.Literal{Value: core.NewBool(tt.right)},
			}, core.Row{}, core.Schema{})
			if err != nil {
				t.Fatalf("eval boolean operator returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("eval boolean operator = %v, want %v", got, tt.want)
			}
		})
	}
}

// NEW TEST: Arithmetic operators (Phase 2)
func TestEvalArithmeticOperators(t *testing.T) {
	tests := []struct {
		name  string
		op    core.BinOp
		left  int64
		right int64
		want  core.Value
	}{
		{name: "OpAdd", op: core.OpAdd, left: 5, right: 3, want: core.NewInt(8)},
		{name: "OpSub", op: core.OpSub, left: 10, right: 4, want: core.NewInt(6)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval(&core.BinaryExpr{
				Op:    tt.op,
				Left:  &core.Literal{Value: core.NewInt(tt.left)},
				Right: &core.Literal{Value: core.NewInt(tt.right)},
			}, core.Row{}, core.Schema{})
			if err != nil {
				t.Fatalf("eval arithmetic operator returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("eval arithmetic operator = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvalUnknownColumnError(t *testing.T) {
	schema := core.Schema{Columns: []core.Column{{Name: "id", Type: core.TypeInt}}}
	row := core.Row{Values: []core.Value{core.NewInt(1)}}

	_, err := eval(&core.ColumnRef{Name: "missing"}, row, schema)
	if err == nil {
		t.Fatal("eval unknown column returned nil error")
	}
	if !strings.Contains(err.Error(), "column \"missing\" does not exist") {
		t.Fatalf("eval unknown column error = %q", err.Error())
	}
}

// FIXED: Use an invalid operator that's truly unsupported
func TestEvalUnsupportedOperatorError(t *testing.T) {
	// Use an invalid operator value (999 is not defined in core.BinOp)
	_, err := eval(&core.BinaryExpr{
		Op:    999, // Not a valid BinOp
		Left:  &core.Literal{Value: core.NewInt(1)},
		Right: &core.Literal{Value: core.NewInt(2)},
	}, core.Row{}, core.Schema{})
	if err == nil {
		t.Fatal("eval unsupported operator returned nil error")
	}
	if !strings.Contains(err.Error(), "unsupported binary operator") {
		t.Fatalf("eval unsupported operator error = %q", err.Error())
	}
}

func TestEvalUnsupportedExpressionTypeError(t *testing.T) {
	var expr core.Expr

	_, err := eval(expr, core.Row{}, core.Schema{})
	if err == nil {
		t.Fatal("eval unsupported expression type returned nil error")
	}
	if !strings.Contains(err.Error(), "unsupported expression type") {
		t.Fatalf("eval unsupported expression type error = %q", err.Error())
	}
}
