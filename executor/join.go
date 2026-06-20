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

// IndexedNestedLoopJoinOp probes an index on the right table for each left row.
type IndexedNestedLoopJoinOp struct {
	left        Operator
	eng         core.StorageEngine
	txn         core.Txn
	rightTable  string
	rightColumn string
	leftColumn  string
	on          core.Expr
	leftSchema  core.Schema
	rightSchema core.Schema
	schema      core.Schema
	leftRow     core.Row
	rightIter   core.RowIterator
}

func NewIndexedNestedLoopJoinOp(
	left Operator,
	eng core.StorageEngine,
	txn core.Txn,
	rightTable string,
	rightColumn string,
	leftColumn string,
	on core.Expr,
) (*IndexedNestedLoopJoinOp, error) {
	rightSchema, ok := eng.GetSchema(rightTable)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", rightTable)
	}

	leftSchema := left.Schema()
	cols := make([]core.Column, 0, len(leftSchema.Columns)+len(rightSchema.Columns))
	cols = append(cols, leftSchema.Columns...)
	cols = append(cols, rightSchema.Columns...)

	return &IndexedNestedLoopJoinOp{
		left:        left,
		eng:         eng,
		txn:         txn,
		rightTable:  rightTable,
		rightColumn: rightColumn,
		leftColumn:  leftColumn,
		on:          on,
		leftSchema:  leftSchema,
		rightSchema: rightSchema,
		schema:      core.Schema{Columns: cols},
	}, nil
}

func (j *IndexedNestedLoopJoinOp) Next() (core.Row, bool, error) {
	for {
		if j.rightIter == nil {
			leftRow, ok, err := j.left.Next()
			if err != nil {
				return core.Row{}, false, fmt.Errorf("left next: %w", err)
			}
			if !ok {
				return core.Row{}, false, nil
			}
			key, err := eval(&core.ColumnRef{Name: j.leftColumn}, leftRow, j.leftSchema)
			if err != nil {
				return core.Row{}, false, fmt.Errorf("left key: %w", err)
			}
			iter, err := j.eng.SeekIndex(j.txn, j.rightTable, j.rightColumn, key)
			if err != nil {
				return core.Row{}, false, fmt.Errorf("seek right index %q.%q: %w", j.rightTable, j.rightColumn, err)
			}
			j.leftRow = leftRow
			j.rightIter = iter
		}

		_, rightRow, ok, err := j.rightIter.Next()
		if err != nil {
			return core.Row{}, false, fmt.Errorf("right index next: %w", err)
		}
		if !ok {
			if err := j.rightIter.Close(); err != nil {
				return core.Row{}, false, fmt.Errorf("close right index: %w", err)
			}
			j.rightIter = nil
			continue
		}

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

func (j *IndexedNestedLoopJoinOp) Schema() core.Schema {
	return j.schema
}

func (j *IndexedNestedLoopJoinOp) Close() error {
	if j.rightIter != nil {
		if err := j.rightIter.Close(); err != nil {
			return fmt.Errorf("close right index: %w", err)
		}
		j.rightIter = nil
	}
	return j.left.Close()
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
