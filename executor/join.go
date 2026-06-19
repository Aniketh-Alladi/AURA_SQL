package executor

import (
	"fmt"

	"aurasql/core"
)

// NestedLoopJoinOp performs a nested loop join between left and right children.
type NestedLoopJoinOp struct {
	left        Operator
	right       Operator
	on          core.Expr
	schema      core.Schema
	leftRow     core.Row
	leftOK      bool
	rightRows   []core.Row
	rightIndex  int
	initialized bool
}

// NewNestedLoopJoinOp creates a nested loop join operator.
func NewNestedLoopJoinOp(left, right Operator, on core.Expr) (*NestedLoopJoinOp, error) {
	leftSchema := left.Schema()
	rightSchema := right.Schema()

	cols := make([]core.Column, 0, len(leftSchema.Columns)+len(rightSchema.Columns))
	cols = append(cols, leftSchema.Columns...)
	cols = append(cols, rightSchema.Columns...)

	return &NestedLoopJoinOp{
		left:        left,
		right:       right,
		on:          on,
		schema:      core.Schema{Columns: cols},
		rightRows:   nil,
		rightIndex:  0,
		leftOK:      false,
		initialized: false,
	}, nil
}

func (j *NestedLoopJoinOp) Next() (core.Row, bool, error) {
	if !j.initialized {
		if err := j.materializeRight(); err != nil {
			return core.Row{}, false, fmt.Errorf("materialize right: %w", err)
		}
		j.initialized = true
	}

	for {
		if !j.leftOK || j.rightIndex >= len(j.rightRows) {
			leftRow, ok, err := j.left.Next()
			if err != nil {
				return core.Row{}, false, fmt.Errorf("left next: %w", err)
			}
			if !ok {
				return core.Row{}, false, nil
			}
			j.leftRow = leftRow
			j.leftOK = true
			j.rightIndex = 0
		}

		for j.rightIndex < len(j.rightRows) {
			rightRow := j.rightRows[j.rightIndex]
			j.rightIndex++

			combined := core.Row{
				Values: append(append([]core.Value{}, j.leftRow.Values...), rightRow.Values...),
			}

			matches, err := evalWhere(j.on, combined, j.schema)
			if err != nil {
				return core.Row{}, false, fmt.Errorf("join condition: %w", err)
			}
			if matches {
				return combined, true, nil
			}
		}
	}
}

func (j *NestedLoopJoinOp) materializeRight() error {
	rows := []core.Row{}
	for {
		row, ok, err := j.right.Next()
		if err != nil {
			return fmt.Errorf("right scan: %w", err)
		}
		if !ok {
			break
		}
		rows = append(rows, row)
	}
	j.rightRows = rows
	return nil
}

func (j *NestedLoopJoinOp) Schema() core.Schema {
	return j.schema
}

func (j *NestedLoopJoinOp) Close() error {
	if err := j.left.Close(); err != nil {
		return fmt.Errorf("close left: %w", err)
	}
	if err := j.right.Close(); err != nil {
		return fmt.Errorf("close right: %w", err)
	}
	return nil
}
