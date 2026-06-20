package executor

import (
	"fmt"

	"aurasql/core"
)

// Operator is the volcano iterator interface.
type Operator interface {
	Next() (core.Row, bool, error)
	Schema() core.Schema
	Close() error
}

// QualifiedColumn tracks a column with its source table for join resolution.
type QualifiedColumn struct {
	Table string
	Name  string
	Type  core.ColumnType
}

// QualifiedSchema extends core.Schema with table qualification information.
type QualifiedSchema struct {
	Columns []QualifiedColumn
}

// ToCoreSchema converts to the public core.Schema.
func (qs QualifiedSchema) ToCoreSchema() core.Schema {
	cols := make([]core.Column, len(qs.Columns))
	for i, qc := range qs.Columns {
		cols[i] = core.Column{Name: qc.Name, Type: qc.Type}
	}
	return core.Schema{Columns: cols}
}

// ColumnIndex returns the index of a column by name.
func (qs QualifiedSchema) ColumnIndex(table, name string) (int, error) {
	var matches []int
	for i, col := range qs.Columns {
		if col.Name != name {
			continue
		}
		if table == "" || col.Table == table {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return -1, fmt.Errorf("column %q not found", name)
	}
	if len(matches) > 1 && table == "" {
		return -1, fmt.Errorf("column %q is ambiguous (found in multiple tables)", name)
	}
	return matches[0], nil
}

// ScanOp wraps a storage scan iterator.
type ScanOp struct {
	txn    core.Txn
	table  string
	iter   core.RowIterator
	schema QualifiedSchema
	closed bool
}

// NewScanOp creates a ScanOp from the storage engine.
func NewScanOp(eng core.StorageEngine, txn core.Txn, table string) (*ScanOp, error) {
	schema, ok := eng.GetSchema(table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", table)
	}
	iter, err := eng.Scan(txn, table)
	if err != nil {
		return nil, fmt.Errorf("scan table %q: %w", table, err)
	}

	qualified := QualifiedSchema{
		Columns: make([]QualifiedColumn, len(schema.Columns)),
	}
	for i, col := range schema.Columns {
		qualified.Columns[i] = QualifiedColumn{
			Table: table,
			Name:  col.Name,
			Type:  col.Type,
		}
	}

	return &ScanOp{
		txn:    txn,
		table:  table,
		iter:   iter,
		schema: qualified,
		closed: false,
	}, nil
}

// NewIndexScanOp creates a ScanOp backed by an index seek.
func NewIndexScanOp(eng core.StorageEngine, txn core.Txn, table, column string, key core.Value) (*ScanOp, error) {
	schema, ok := eng.GetSchema(table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", table)
	}
	iter, err := eng.SeekIndex(txn, table, column, key)
	if err != nil {
		return nil, fmt.Errorf("seek index %q.%q: %w", table, column, err)
	}

	qualified := QualifiedSchema{
		Columns: make([]QualifiedColumn, len(schema.Columns)),
	}
	for i, col := range schema.Columns {
		qualified.Columns[i] = QualifiedColumn{
			Table: table,
			Name:  col.Name,
			Type:  col.Type,
		}
	}

	return &ScanOp{
		txn:    txn,
		table:  table,
		iter:   iter,
		schema: qualified,
		closed: false,
	}, nil
}

func (s *ScanOp) Next() (core.Row, bool, error) {
	if s.closed {
		return core.Row{}, false, fmt.Errorf("scan is closed")
	}
	_, row, ok, err := s.iter.Next()
	if err != nil {
		return core.Row{}, false, fmt.Errorf("scan next: %w", err)
	}
	if !ok {
		return core.Row{}, false, nil
	}
	return row, true, nil
}

func (s *ScanOp) Schema() core.Schema {
	return s.schema.ToCoreSchema()
}

func (s *ScanOp) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.iter.Close()
}

// FilterOp applies a predicate to rows from a child operator.
type FilterOp struct {
	child     Operator
	predicate core.Expr
	schema    core.Schema
}

// NewFilterOp creates a FilterOp.
func NewFilterOp(child Operator, predicate core.Expr) *FilterOp {
	return &FilterOp{
		child:     child,
		predicate: predicate,
		schema:    child.Schema(),
	}
}

func (f *FilterOp) Next() (core.Row, bool, error) {
	for {
		row, ok, err := f.child.Next()
		if err != nil {
			return core.Row{}, false, fmt.Errorf("filter next: %w", err)
		}
		if !ok {
			return core.Row{}, false, nil
		}

		matches, err := evalWhere(f.predicate, row, f.schema)
		if err != nil {
			return core.Row{}, false, fmt.Errorf("filter predicate: %w", err)
		}
		if matches {
			return row, true, nil
		}
	}
}

func (f *FilterOp) Schema() core.Schema {
	return f.schema
}

func (f *FilterOp) Close() error {
	return f.child.Close()
}

// ProjectOp evaluates expressions to produce output rows.
type ProjectOp struct {
	child  Operator
	exprs  []core.Expr
	schema core.Schema
	isStar bool
}

// NewProjectOp creates a ProjectOp.
func NewProjectOp(child Operator, exprs []core.Expr) (*ProjectOp, error) {
	if len(exprs) == 1 {
		if _, ok := exprs[0].(*core.Star); ok {
			return &ProjectOp{
				child:  child,
				exprs:  exprs,
				schema: child.Schema(),
				isStar: true,
			}, nil
		}
	}

	childSchema := child.Schema()
	cols := make([]core.Column, len(exprs))
	for i, expr := range exprs {
		col, err := projectionColumn(expr, i, childSchema)
		if err != nil {
			return nil, fmt.Errorf("projection %d: %w", i, err)
		}
		cols[i] = col
	}

	return &ProjectOp{
		child:  child,
		exprs:  exprs,
		schema: core.Schema{Columns: cols},
		isStar: false,
	}, nil
}

func (p *ProjectOp) Next() (core.Row, bool, error) {
	row, ok, err := p.child.Next()
	if err != nil {
		return core.Row{}, false, fmt.Errorf("project next: %w", err)
	}
	if !ok {
		return core.Row{}, false, nil
	}

	if p.isStar {
		return row, true, nil
	}

	childSchema := p.child.Schema()
	values := make([]core.Value, len(p.exprs))
	for i, expr := range p.exprs {
		val, err := eval(expr, row, childSchema)
		if err != nil {
			return core.Row{}, false, fmt.Errorf("project eval %d: %w", i, err)
		}
		values[i] = val
	}
	return core.Row{Values: values}, true, nil
}

func (p *ProjectOp) Schema() core.Schema {
	return p.schema
}

func (p *ProjectOp) Close() error {
	return p.child.Close()
}

func projectionColumn(expr core.Expr, index int, schema core.Schema) (core.Column, error) {
	switch e := expr.(type) {
	case *core.ColumnRef:
		idx := schema.ColumnIndex(e.Name)
		if idx < 0 {
			return core.Column{}, fmt.Errorf("column %q does not exist", e.Name)
		}
		return core.Column{Name: e.Name, Type: schema.Columns[idx].Type}, nil
	case *core.Literal:
		return core.Column{Name: fmt.Sprintf("expr%d", index+1), Type: e.Value.Type}, nil
	case *core.BinaryExpr:
		colType := core.TypeBool
		if e.Op == core.OpAdd || e.Op == core.OpSub {
			colType = core.TypeInt
		}
		return core.Column{Name: fmt.Sprintf("expr%d", index+1), Type: colType}, nil
	default:
		return core.Column{}, fmt.Errorf("unsupported projection expression type %T", expr)
	}
}
