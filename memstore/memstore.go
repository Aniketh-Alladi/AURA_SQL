// Package memstore is a throwaway, in-memory implementation of
// core.StorageEngine. Its only job is to let the parser and executor tracks run
// end to end on day one, before the real disk-backed engine exists.
//
// It does NOT implement real MVCC: every transaction sees the latest committed
// state, Commit and Rollback are no-ops, and nothing is persisted. When the real
// engine (heap files, buffer pool, B-tree, MVCC) is ready, swap it in wherever a
// core.StorageEngine is expected — no other code has to change, because both
// satisfy the same interface. Keep this around afterwards anyway: it makes a
// fast, deterministic backend for the executor's unit tests.
package memstore

import (
	"fmt"
	"sort"
	"sync"

	"aurasql/core"
)

type table struct {
	schema core.Schema
	rows   map[core.RowID]core.Row
}

// Engine is an in-memory StorageEngine. Create one with New.
type Engine struct {
	mu      sync.Mutex
	tables  map[string]*table
	indexes map[string]bool // Added for Phase 3
	nextID  core.RowID
	nextTx  uint64
}

// New returns an empty in-memory engine.
func New() *Engine {
	return &Engine{
		tables:  make(map[string]*table),
		indexes: make(map[string]bool),
	}
}

type txn struct{ id uint64 }

func (t *txn) ID() uint64      { return t.id }
func (t *txn) Commit() error   { return nil }
func (t *txn) Rollback() error { return nil }

func (e *Engine) Begin() (core.Txn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextTx++
	return &txn{id: e.nextTx}, nil
}

func (e *Engine) CreateTable(_ core.Txn, name string, schema core.Schema) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tables[name]; ok {
		return fmt.Errorf("table %q already exists", name)
	}
	e.tables[name] = &table{schema: schema, rows: make(map[core.RowID]core.Row)}
	return nil
}

func (e *Engine) DropTable(_ core.Txn, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tables[name]; !ok {
		return fmt.Errorf("table %q does not exist", name)
	}
	delete(e.tables, name)
	return nil
}

func (e *Engine) GetSchema(name string) (core.Schema, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tables[name]
	if !ok {
		return core.Schema{}, false
	}
	return t.schema, true
}

func (e *Engine) Insert(_ core.Txn, name string, row core.Row) (core.RowID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tables[name]
	if !ok {
		return 0, fmt.Errorf("table %q does not exist", name)
	}
	e.nextID++
	id := e.nextID
	t.rows[id] = row
	return id, nil
}

func (e *Engine) Update(_ core.Txn, name string, id core.RowID, row core.Row) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tables[name]
	if !ok {
		return fmt.Errorf("table %q does not exist", name)
	}
	if _, ok := t.rows[id]; !ok {
		return fmt.Errorf("row %d not found in %q", id, name)
	}
	t.rows[id] = row
	return nil
}

func (e *Engine) Delete(_ core.Txn, name string, id core.RowID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tables[name]
	if !ok {
		return fmt.Errorf("table %q does not exist", name)
	}
	delete(t.rows, id)
	return nil
}

type iterator struct {
	ids  []core.RowID
	rows map[core.RowID]core.Row
	pos  int
}

func (it *iterator) Next() (core.RowID, core.Row, bool, error) {
	if it.pos >= len(it.ids) {
		return 0, core.Row{}, false, nil
	}
	id := it.ids[it.pos]
	it.pos++
	return id, it.rows[id], true, nil
}

func (it *iterator) Close() error { return nil }

func (e *Engine) Scan(_ core.Txn, name string) (core.RowIterator, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tables[name]
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", name)
	}
	// Snapshot under the lock so the iterator is stable, and sort the IDs so
	// scans are deterministic (handy for tests).
	ids := make([]core.RowID, 0, len(t.rows))
	rowsCopy := make(map[core.RowID]core.Row, len(t.rows))
	for id, r := range t.rows {
		ids = append(ids, id)
		rowsCopy[id] = r
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return &iterator{ids: ids, rows: rowsCopy}, nil
}

// ============================================================
// Phase 3: Indexing Stand-ins
// ============================================================

func (e *Engine) CreateIndex(_ core.Txn, table, column string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	indexKey := table + ":" + column
	e.indexes[indexKey] = true
	return nil
}

func (e *Engine) HasIndex(table, column string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	indexKey := table + ":" + column
	return e.indexes[indexKey]
}

func (e *Engine) SeekIndex(txn core.Txn, table, column string, key core.Value) (core.RowIterator, error) {
	schema, ok := e.GetSchema(table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", table)
	}

	colIdx := schema.ColumnIndex(column)
	if colIdx < 0 {
		return nil, fmt.Errorf("column %q not found in table %q", column, table)
	}

	// Rely on the existing scan to get all rows
	iter, err := e.Scan(txn, table)
	if err != nil {
		return nil, err
	}

	return &memstoreIndexIterator{
		base:      iter,
		colIdx:    colIdx,
		targetKey: key,
	}, nil
}

// memstoreIndexIterator filters a full table scan down to matching keys.
type memstoreIndexIterator struct {
	base      core.RowIterator
	colIdx    int
	targetKey core.Value
}

func (it *memstoreIndexIterator) Next() (core.RowID, core.Row, bool, error) {
	for {
		id, row, ok, err := it.base.Next()
		if !ok || err != nil {
			return 0, core.Row{}, false, err
		}

		val := row.Values[it.colIdx]
		cmp, err := val.Compare(it.targetKey)

		// If the values match perfectly, yield the row
		if err == nil && cmp == 0 {
			return id, row, true, nil
		}
	}
}

func (it *memstoreIndexIterator) Close() error {
	return it.base.Close()
}
