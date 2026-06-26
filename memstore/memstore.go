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
	statsCache map[string]core.TableStats            // cached statistics for tables
}

// New returns an empty in-memory engine.
func New() *Engine {
	return &Engine{
		tables:     make(map[string]*table),
		indexes:    make(map[string]bool),
		pending:    make(map[uint64]map[string][]pendingChange),
		activeTxns: make(map[uint64]bool),
		statsCache: make(map[string]core.TableStats),
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
	// Release any rows this transaction had claimed via pending changes
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

	if _, exists := t.rows[id]; !exists {
		return fmt.Errorf("row %d not found in %q", id, name)
	}

	if txn != nil {
		txnID := txn.ID()
		if existingTxnID, exists := t.rowTxns[id]; exists && existingTxnID != 0 {
			if existingTxnID != txnID {
				if _, active := e.activeTxns[existingTxnID]; active {
					return core.ErrWriteConflict
				}
			}
		}
	}

	if len(row.Values) != len(t.schema.Columns) {
		return fmt.Errorf("update has %d values, table has %d columns",
			len(row.Values), len(t.schema.Columns))
	}

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
		t.rowTxns[id] = txnID
		return nil
	}

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

	if _, exists := t.rows[id]; !exists {
		return fmt.Errorf("row %d not found in %q", id, name)
	}

	if txn != nil {
		txnID := txn.ID()
		if existingTxnID, exists := t.rowTxns[id]; exists && existingTxnID != 0 {
			if existingTxnID != txnID {
				if _, active := e.activeTxns[existingTxnID]; active {
					return core.ErrWriteConflict
				}
			}
		}
	}

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
		t.rowTxns[id] = txnID
		return nil
	}

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
	rowsCopy := make(map[core.RowID]core.Row, len(t.rows))
	for id, r := range t.rows {
		rowsCopy[id] = r
	}
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

// ============================================================
// Phase 6: Statistics Support
// ============================================================

// Analyze computes and caches statistics for a table.
func (e *Engine) Analyze(txn core.Txn, table string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	t, ok := e.tables[table]
	if !ok {
		return fmt.Errorf("table %q does not exist", table)
	}

	colStats := make(map[string]core.ColumnStats)
	schema := t.schema

	// Initialize stats for each column
	for _, col := range schema.Columns {
		colStats[col.Name] = core.ColumnStats{
			DistinctCount: 0,
			NullCount:     0,
			Min:           core.NullValue(col.Type),
			Max:           core.NullValue(col.Type),
		}
	}

	if len(t.rows) == 0 {
		e.statsCache[table] = core.TableStats{
			RowCount: 0,
			Columns:  colStats,
		}
		return nil
	}

	colValues := make(map[string][]core.Value)
	for _, col := range schema.Columns {
		colValues[col.Name] = make([]core.Value, 0, len(t.rows))
	}

	for _, row := range t.rows {
		for i, col := range schema.Columns {
			val := row.Values[i]
			colValues[col.Name] = append(colValues[col.Name], val)
		}
	}

	for _, col := range schema.Columns {
		values := colValues[col.Name]
		stats := core.ColumnStats{
			DistinctCount: 0,
			NullCount:     0,
			Min:           core.NullValue(col.Type),
			Max:           core.NullValue(col.Type),
		}

		distinct := make(map[string]bool)
		hasNonNull := false

		for _, val := range values {
			if val.Null {
				stats.NullCount++
				continue
			}
			hasNonNull = true
			key := val.String()
			distinct[key] = true

			if stats.Min.Null {
				stats.Min = val
			} else if cmp, err := val.Compare(stats.Min); err == nil && cmp < 0 {
				stats.Min = val
			}

			if stats.Max.Null {
				stats.Max = val
			} else if cmp, err := val.Compare(stats.Max); err == nil && cmp > 0 {
				stats.Max = val
			}
		}

		if hasNonNull {
			stats.DistinctCount = int64(len(distinct))
		}

		colStats[col.Name] = stats
	}

	e.statsCache[table] = core.TableStats{
		RowCount: int64(len(t.rows)),
		Columns:  colStats,
	}

	return nil
}

// Stats returns cached statistics for a table.
func (e *Engine) Stats(table string) (core.TableStats, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check cache first
	if stats, ok := e.statsCache[table]; ok {
		return stats, true
	}

	// If not cached, compute on the fly
	t, ok := e.tables[table]
	if !ok {
		return core.TableStats{}, false
	}

	colStats := make(map[string]core.ColumnStats)
	schema := t.schema

	for _, col := range schema.Columns {
		colStats[col.Name] = core.ColumnStats{
			DistinctCount: 0,
			NullCount:     0,
			Min:           core.NullValue(col.Type),
			Max:           core.NullValue(col.Type),
		}
	}

	if len(t.rows) == 0 {
		stats := core.TableStats{
			RowCount: 0,
			Columns:  colStats,
		}
		e.statsCache[table] = stats
		return stats, true
	}

	colValues := make(map[string][]core.Value)
	for _, col := range schema.Columns {
		colValues[col.Name] = make([]core.Value, 0, len(t.rows))
	}

	for _, row := range t.rows {
		for i, col := range schema.Columns {
			val := row.Values[i]
			colValues[col.Name] = append(colValues[col.Name], val)
		}
	}

	for _, col := range schema.Columns {
		values := colValues[col.Name]
		stats := core.ColumnStats{
			DistinctCount: 0,
			NullCount:     0,
			Min:           core.NullValue(col.Type),
			Max:           core.NullValue(col.Type),
		}

		distinct := make(map[string]bool)
		hasNonNull := false

		for _, val := range values {
			if val.Null {
				stats.NullCount++
				continue
			}
			hasNonNull = true
			key := val.String()
			distinct[key] = true

			if stats.Min.Null {
				stats.Min = val
			} else if cmp, err := val.Compare(stats.Min); err == nil && cmp < 0 {
				stats.Min = val
			}

			if stats.Max.Null {
				stats.Max = val
			} else if cmp, err := val.Compare(stats.Max); err == nil && cmp > 0 {
				stats.Max = val
			}
		}

		if hasNonNull {
			stats.DistinctCount = int64(len(distinct))
		}

		colStats[col.Name] = stats
	}

	tableStats := core.TableStats{
		RowCount: int64(len(t.rows)),
		Columns:  colStats,
	}
	e.statsCache[table] = tableStats

	return tableStats, true
}
