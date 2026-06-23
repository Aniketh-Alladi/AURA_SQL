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
	schema  core.Schema
	rows    map[core.RowID]core.Row
	rowTxns map[core.RowID]uint64 // Track which transaction created/modified each row
}

// pendingChange tracks a change made by a transaction before commit.
type pendingChange struct {
	rowID    core.RowID
	row      core.Row
	isDelete bool
}

// Engine is an in-memory StorageEngine. Create one with New.
type Engine struct {
	mu         sync.Mutex
	tables     map[string]*table
	indexes    map[string]bool
	nextID     core.RowID
	nextTx     uint64
	pending    map[uint64]map[string][]pendingChange // txnID -> table -> changes
	activeTxns map[uint64]bool                       // tracks active (uncommitted) transactions
}

// New returns an empty in-memory engine.
func New() *Engine {
	return &Engine{
		tables:     make(map[string]*table),
		indexes:    make(map[string]bool),
		pending:    make(map[uint64]map[string][]pendingChange),
		activeTxns: make(map[uint64]bool),
	}
}

type txn struct {
	id uint64
	e  *Engine
}

func (t *txn) ID() uint64 { return t.id }

func (t *txn) Commit() error {
	t.e.mu.Lock()
	defer t.e.mu.Unlock()

	// Apply all pending changes
	if pending, ok := t.e.pending[t.id]; ok {
		for tableName, changes := range pending {
			tbl, exists := t.e.tables[tableName]
			if !exists {
				continue
			}
			for _, change := range changes {
				if change.isDelete {
					delete(tbl.rows, change.rowID)
					delete(tbl.rowTxns, change.rowID)
				} else {
					tbl.rows[change.rowID] = change.row
					tbl.rowTxns[change.rowID] = 0 // committed rows have txn ID 0
				}
			}
		}
		delete(t.e.pending, t.id)
	}
	delete(t.e.activeTxns, t.id)
	return nil
}

func (t *txn) Rollback() error {
	t.e.mu.Lock()
	defer t.e.mu.Unlock()
	// Release any rows this transaction had claimed via pending changes, so a
	// later transaction doesn't see a stale owner. (The activeTxns guard in the
	// conflict check already tolerates stale entries, but clearing them keeps
	// rowTxns honest.)
	if pending, ok := t.e.pending[t.id]; ok {
		for tableName, changes := range pending {
			if tbl, exists := t.e.tables[tableName]; exists {
				for _, change := range changes {
					if owner, ok := tbl.rowTxns[change.rowID]; ok && owner == t.id {
						tbl.rowTxns[change.rowID] = 0
					}
				}
			}
		}
	}
	// Discard all pending changes
	delete(t.e.pending, t.id)
	delete(t.e.activeTxns, t.id)
	return nil
}

func (e *Engine) Begin() (core.Txn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextTx++
	e.activeTxns[e.nextTx] = true
	return &txn{id: e.nextTx, e: e}, nil
}

func (e *Engine) CreateTable(_ core.Txn, name string, schema core.Schema) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tables[name]; ok {
		return fmt.Errorf("table %q already exists", name)
	}
	e.tables[name] = &table{
		schema:  schema,
		rows:    make(map[core.RowID]core.Row),
		rowTxns: make(map[core.RowID]uint64),
	}
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

func (e *Engine) ListTables() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.tables))
	for name := range e.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (e *Engine) Insert(txn core.Txn, name string, row core.Row) (core.RowID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	t, ok := e.tables[name]
	if !ok {
		return 0, fmt.Errorf("table %q does not exist", name)
	}

	if len(row.Values) != len(t.schema.Columns) {
		return 0, fmt.Errorf("insert has %d values, table has %d columns",
			len(row.Values), len(t.schema.Columns))
	}

	e.nextID++
	id := e.nextID

	// Store as pending change if in a transaction
	if txn != nil {
		txnID := txn.ID()
		if _, exists := e.pending[txnID]; !exists {
			e.pending[txnID] = make(map[string][]pendingChange)
		}
		e.pending[txnID][name] = append(e.pending[txnID][name], pendingChange{
			rowID:    id,
			row:      row,
			isDelete: false,
		})
		return id, nil
	}

	// Autocommit: apply immediately
	t.rows[id] = row
	t.rowTxns[id] = 0
	return id, nil
}

func (e *Engine) Update(txn core.Txn, name string, id core.RowID, row core.Row) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	t, ok := e.tables[name]
	if !ok {
		return fmt.Errorf("table %q does not exist", name)
	}

	// Check if row exists
	if _, exists := t.rows[id]; !exists {
		return fmt.Errorf("row %d not found in %q", id, name)
	}

	// Check for write conflict
	if txn != nil {
		txnID := txn.ID()
		if existingTxnID, exists := t.rowTxns[id]; exists && existingTxnID != 0 {
			// Row is claimed by some transaction.
			if existingTxnID != txnID {
				// Claimed by a *different* transaction — conflict only if that
				// transaction is still active (uncommitted).
				if _, active := e.activeTxns[existingTxnID]; active {
					return core.ErrWriteConflict
				}
				// Otherwise the previous owner is done; safe to take the row.
			}
		}
	}

	if len(row.Values) != len(t.schema.Columns) {
		return fmt.Errorf("update has %d values, table has %d columns",
			len(row.Values), len(t.schema.Columns))
	}

	// Store as pending change if in a transaction
	if txn != nil {
		txnID := txn.ID()
		if _, exists := e.pending[txnID]; !exists {
			e.pending[txnID] = make(map[string][]pendingChange)
		}
		e.pending[txnID][name] = append(e.pending[txnID][name], pendingChange{
			rowID:    id,
			row:      row,
			isDelete: false,
		})
		// Claim the row for this transaction so a concurrent writer sees the
		// conflict. Committed rows are reset to 0 in Commit.
		t.rowTxns[id] = txnID
		return nil
	}

	// Autocommit: apply immediately
	t.rows[id] = row
	t.rowTxns[id] = 0
	return nil
}

func (e *Engine) Delete(txn core.Txn, name string, id core.RowID) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	t, ok := e.tables[name]
	if !ok {
		return fmt.Errorf("table %q does not exist", name)
	}

	// Check if row exists
	if _, exists := t.rows[id]; !exists {
		return fmt.Errorf("row %d not found in %q", id, name)
	}

	// Check for write conflict
	if txn != nil {
		txnID := txn.ID()
		if existingTxnID, exists := t.rowTxns[id]; exists && existingTxnID != 0 {
			// Row is claimed by some transaction.
			if existingTxnID != txnID {
				// Claimed by a *different* transaction — conflict only if that
				// transaction is still active (uncommitted).
				if _, active := e.activeTxns[existingTxnID]; active {
					return core.ErrWriteConflict
				}
				// Otherwise the previous owner is done; safe to take the row.
			}
		}
	}

	// Store as pending change if in a transaction
	if txn != nil {
		txnID := txn.ID()
		if _, exists := e.pending[txnID]; !exists {
			e.pending[txnID] = make(map[string][]pendingChange)
		}
		e.pending[txnID][name] = append(e.pending[txnID][name], pendingChange{
			rowID:    id,
			row:      core.Row{},
			isDelete: true,
		})
		// Claim the row for this transaction so a concurrent writer sees the
		// conflict. The row's rowTxns entry is removed in Commit.
		t.rowTxns[id] = txnID
		return nil
	}

	// Autocommit: apply immediately
	delete(t.rows, id)
	delete(t.rowTxns, id)
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

func (e *Engine) Scan(txn core.Txn, name string) (core.RowIterator, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tables[name]
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", name)
	}
	// Snapshot committed rows under the lock so the iterator is stable.
	rowsCopy := make(map[core.RowID]core.Row, len(t.rows))
	for id, r := range t.rows {
		rowsCopy[id] = r
	}
	// Overlay this transaction's own uncommitted changes so it can read its own
	// writes (insert/update/delete) before commit. Other transactions' pending
	// changes are intentionally NOT visible — that's the isolation boundary.
	if txn != nil {
		if pend, ok := e.pending[txn.ID()]; ok {
			for _, change := range pend[name] {
				if change.isDelete {
					delete(rowsCopy, change.rowID)
				} else {
					rowsCopy[change.rowID] = change.row
				}
			}
		}
	}
	ids := make([]core.RowID, 0, len(rowsCopy))
	for id := range rowsCopy {
		ids = append(ids, id)
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

		if err == nil && cmp == 0 {
			return id, row, true, nil
		}
	}
}

func (it *memstoreIndexIterator) Close() error {
	return it.base.Close()
}
