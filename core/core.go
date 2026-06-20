// Package core defines the shared contracts for the database engine.
//
// Everything that the three tracks must agree on lives here, and ONLY here:
//   - the value / row / schema vocabulary (used by everyone),
//   - the StorageEngine interface (you implement it, the executor calls it),
//   - the SQL AST (the parser produces it, the executor consumes it).
//
// Because all three tracks import only this package, each one can be built and
// tested in isolation. The parser can be tested by checking the AST it returns.
// The executor can be tested against the in-memory memstore. The real storage
// engine can be tested directly through the StorageEngine interface. Nobody has
// to wait for anybody else's real code — they just need these types to be fixed.
//
// Scope is deliberately small (core only): three column types, a single optional
// join, no subqueries, no aggregates. Add those later as stretch goals; do not
// widen this file mid-project without all three of you agreeing.
package core

import (
	"fmt"
	"strings"
)

// ============================================================
// 1. Values, columns, schemas, rows  (shared by all three tracks)
// ============================================================

// ColumnType is the set of supported column types in core scope.
type ColumnType int

const (
	TypeInt ColumnType = iota
	TypeText
	TypeBool
)

func (t ColumnType) String() string {
	switch t {
	case TypeInt:
		return "INT"
	case TypeText:
		return "TEXT"
	case TypeBool:
		return "BOOL"
	default:
		return "UNKNOWN"
	}
}

// Value is a single typed cell. NULL is tracked explicitly via the Null flag;
// when Null is true the other fields are ignored.
type Value struct {
	Type ColumnType
	Int  int64
	Str  string
	Bool bool
	Null bool
}

func NewInt(v int64) Value         { return Value{Type: TypeInt, Int: v} }
func NewText(v string) Value       { return Value{Type: TypeText, Str: v} }
func NewBool(v bool) Value         { return Value{Type: TypeBool, Bool: v} }
func NullValue(t ColumnType) Value { return Value{Type: t, Null: true} }

func (v Value) String() string {
	if v.Null {
		return "NULL"
	}
	switch v.Type {
	case TypeInt:
		return fmt.Sprintf("%d", v.Int)
	case TypeText:
		return v.Str
	case TypeBool:
		return fmt.Sprintf("%t", v.Bool)
	default:
		return "?"
	}
}

// Compare returns -1, 0, or +1 for v relative to o. It is only meaningful for
// two non-null values of the same type; otherwise it returns an error. The
// executor uses this to evaluate WHERE predicates and join conditions.
func (v Value) Compare(o Value) (int, error) {
	if v.Null || o.Null {
		return 0, fmt.Errorf("cannot compare NULL values")
	}
	if v.Type != o.Type {
		return 0, fmt.Errorf("cannot compare %s with %s", v.Type, o.Type)
	}
	switch v.Type {
	case TypeInt:
		switch {
		case v.Int < o.Int:
			return -1, nil
		case v.Int > o.Int:
			return 1, nil
		default:
			return 0, nil
		}
	case TypeText:
		return strings.Compare(v.Str, o.Str), nil
	case TypeBool:
		switch {
		case v.Bool == o.Bool:
			return 0, nil
		case !v.Bool:
			return -1, nil
		default:
			return 1, nil
		}
	default:
		return 0, fmt.Errorf("unsupported type %s", v.Type)
	}
}

// Column is a named, typed column.
type Column struct {
	Name string
	Type ColumnType
}

// Schema is the ordered list of columns for a table or a result set.
type Schema struct {
	Columns []Column
}

// ColumnIndex returns the position of a named column, or -1 if absent.
func (s Schema) ColumnIndex(name string) int {
	for i, c := range s.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// RowID identifies a row's physical location. The storage engine chooses the
// encoding (for example page number + slot). Everyone else treats it as opaque.
type RowID uint64

// Row holds one Value per column, in the same order as the table's Schema.
type Row struct {
	Values []Value
}

// ============================================================
// 2. Storage contract  (YOU implement; the executor consumes)
// ============================================================
//
// MVCC visibility is handled INSIDE the engine. Scan only ever returns the rows
// that are visible to the transaction it is given, so the executor never sees
// row versions or has to reason about isolation — that complexity stays behind
// this interface. The in-memory memstore satisfies the same interface without
// real MVCC, which is exactly why the executor can be built before MVCC exists.

// Txn is a handle to an in-progress transaction.
type Txn interface {
	ID() uint64
	Commit() error
	Rollback() error
}

// RowIterator streams rows from a scan.
type RowIterator interface {
	// Next returns the next visible row. The bool is false when iteration is
	// finished (with a nil error). Always Close when done.
	Next() (RowID, Row, bool, error)
	Close() error
}

// StorageEngine is the only surface the executor uses to touch data. The real
// disk-backed engine (heap files, buffer pool, B-tree index, MVCC) and the
// throwaway memstore both implement it.
type StorageEngine interface {
	Begin() (Txn, error)

	// Schema / DDL.
	CreateTable(txn Txn, name string, schema Schema) error
	DropTable(txn Txn, name string) error
	GetSchema(name string) (Schema, bool)

	// Rows / DML.
	Insert(txn Txn, table string, row Row) (RowID, error)
	Update(txn Txn, table string, id RowID, row Row) error
	Delete(txn Txn, table string, id RowID) error

	// Scan iterates the rows of a table that are visible to txn.
	Scan(txn Txn, table string) (RowIterator, error)

	// Phase 3: Indexing
	CreateIndex(txn Txn, table, column string) error
	HasIndex(table, column string) bool
	SeekIndex(txn Txn, table, column string, key Value) (RowIterator, error)
}

// ============================================================
// 3. SQL AST  (the PARSER produces this; the EXECUTOR consumes it)
// ============================================================

// Statement is any top-level parsed SQL statement. The unexported marker method
// keeps the set of statement types closed to this package.
type Statement interface{ isStatement() }

// CreateTableStmt: CREATE TABLE <Table> (<Columns...>).
type CreateTableStmt struct {
	Table   string
	Columns []Column
}

// CreateIndexStmt: CREATE INDEX [Name] ON <Table> (<Column>).
type CreateIndexStmt struct {
	Name   string // optional index name; "" means auto (e.g. idx_table_col)
	Table  string
	Column string
}

// InsertStmt: INSERT INTO <Table> VALUES (<Values...>).
// Core scope requires one value expression per column, in schema order.
type InsertStmt struct {
	Table  string
	Values []Expr
}

// SelectStmt: SELECT <Projection> FROM <From> [JOIN ...] [WHERE ...].
// A Projection of exactly one *Star means SELECT *.
type SelectStmt struct {
	Projection []Expr
	From       string
	Join       *JoinClause // nil if absent; core scope allows at most one
	Where      Expr        // nil if absent
}

// JoinClause: JOIN <Table> ON <On>.
type JoinClause struct {
	Table string
	On    Expr
}

// UpdateStmt: UPDATE <Table> SET <Set...> [WHERE ...].
type UpdateStmt struct {
	Table string
	Set   []Assignment
	Where Expr // nil means every row
}

// Assignment is one "column = value" pair inside SET.
type Assignment struct {
	Column string
	Value  Expr
}

// DeleteStmt: DELETE FROM <Table> [WHERE ...].
type DeleteStmt struct {
	Table string
	Where Expr // nil means every row
}

func (*CreateTableStmt) isStatement() {}
func (*CreateIndexStmt) isStatement() {}
func (*InsertStmt) isStatement()      {}
func (*SelectStmt) isStatement()      {}
func (*UpdateStmt) isStatement()      {}
func (*DeleteStmt) isStatement()      {}

// CreateIndexStmt represents a 'CREATE INDEX' statement.
type CreateIndexStmt struct {
	Name   string // Optional index name; "" means auto-generated (e.g., idx_table_col)
	Table  string
	Column string
}

// Marker method ensuring CreateIndexStmt implements the core.Statement interface
func (*CreateIndexStmt) isStatement() {}

// ---- Expressions ----

// Expr is any expression appearing in a projection, value list, or predicate.
type Expr interface{ isExpr() }

// Star represents "*" in a projection.
type Star struct{}

// ColumnRef references a column, optionally qualified by table ("" if not).
type ColumnRef struct {
	Table string
	Name  string
}

// Literal is a constant value.
type Literal struct {
	Value Value
}

// BinOp is the set of binary operators in core scope.
type BinOp int

const (
	OpEq  BinOp = iota // =
	OpNe               // != / <>
	OpLt               // <
	OpLe               // <=
	OpGt               // >
	OpGe               // >=
	OpAnd              // AND
	OpOr               // OR
	OpAdd              // +
	OpSub              // -
)

// BinaryExpr is "Left Op Right".
type BinaryExpr struct {
	Op    BinOp
	Left  Expr
	Right Expr
}

func (*Star) isExpr()       {}
func (*ColumnRef) isExpr()  {}
func (*Literal) isExpr()    {}
func (*BinaryExpr) isExpr() {}

// ============================================================
// 4. The two functions each track must provide
// ============================================================
//
// These live in their own packages (so they are written as comments here, not
// declared), but their signatures are part of the contract and must not drift:
//
//   Parser   (Varun), package parser:
//       func Parse(sql string) (core.Statement, error)
//
//   Executor (Tejus), package executor:
//       func Execute(eng core.StorageEngine, txn core.Txn, stmt core.Statement) (core.Result, error)

// Result is what Execute returns. For SELECT, Schema describes the columns of
// Rows. For INSERT / UPDATE / DELETE, RowsAffected is set and Rows is empty.
type Result struct {
	Schema       Schema
	Rows         []Row
	RowsAffected int
}
