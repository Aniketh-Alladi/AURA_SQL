package storage

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"aurasql/core"
)

// ==========================================
// 1. Transaction & Engine Structures
// ==========================================

type txn struct {
	id     uint64
	engine *Engine
}

func (t *txn) ID() uint64      { return t.id }
func (t *txn) Commit() error   { return t.engine.commit(t.id) }
func (t *txn) Rollback() error { return t.engine.rollback(t.id) }

type Engine struct {
	mu           sync.Mutex
	catalog      *Catalog
	dataDir      string
	nextTx       uint64
	activeTxns   map[uint64]bool
	committed    map[uint64]bool
	txnSnapshots map[uint64]map[uint64]bool // txn ID -> committed-set frozen at Begin
	indexFile    *HeapFile
	indexPool    *BufferPool
}

// ==========================================
// 2. Lifecycle
// ==========================================

func New(dataDir string) (*Engine, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	cat := NewCatalog()
	if err := cat.Load(dataDir); err != nil {
		return nil, err
	}

	indexPath := filepath.Join(dataDir, "indexes.db")
	_, err := os.Stat(indexPath)
	isNew := os.IsNotExist(err)

	idxFile, err := NewHeapFile(indexPath)
	if err != nil {
		return nil, err
	}

	if isNew {
		emptyPage := NewPage()
		if _, err := idxFile.file.WriteAt(emptyPage.Data[:], 0); err != nil {
			return nil, err
		}
	}

	idxPool := NewBufferPool(idxFile, 100)
	idxFile.SetBufferPool(idxPool)

	if isNew {
		if _, err := idxPool.FetchPage(0); err != nil {
			return nil, err
		}
	}

	return &Engine{
		catalog:      cat,
		dataDir:      dataDir,
		indexFile:    idxFile,
		indexPool:    idxPool,
		activeTxns:   make(map[uint64]bool),
		committed:    make(map[uint64]bool),
		txnSnapshots: make(map[uint64]map[uint64]bool),
	}, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, meta := range e.catalog.tables {
		meta.HeapFile.pool.FlushAll()
		meta.HeapFile.Close()
	}

	if e.indexPool != nil {
		e.indexPool.FlushAll()
		e.indexFile.Close()
	}

	return nil
}

// ==========================================
// 3. Transaction Management (MVCC)
// ==========================================

// Begin initializes a new transaction, marks it active, and freezes its read
// snapshot: the set of transactions committed as of this moment. All visibility
// checks for this txn read against that frozen set, NOT the live committed map.
// This is what gives us snapshot isolation rather than read-committed.
func (e *Engine) Begin() (core.Txn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextTx++

	snap := make(map[uint64]bool, len(e.committed))
	for id := range e.committed {
		snap[id] = true
	}
	e.txnSnapshots[e.nextTx] = snap

	e.activeTxns[e.nextTx] = true
	return &txn{id: e.nextTx, engine: e}, nil
}

// commit moves the transaction from active to committed status.
func (e *Engine) commit(txID uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, active := e.activeTxns[txID]; !active {
		return fmt.Errorf("transaction %d not active", txID)
	}

	delete(e.activeTxns, txID)
	delete(e.txnSnapshots, txID)
	e.committed[txID] = true
	return nil
}

// rollback removes the transaction from active status; it becomes effectively aborted.
func (e *Engine) rollback(txID uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// It never enters 'committed', so any versions it created are ignored by visibility.
	delete(e.activeTxns, txID)
	delete(e.txnSnapshots, txID)
	return nil
}

// isVisible checks whether a row version is visible to a specific transaction
// under that transaction's frozen snapshot.
func (e *Engine) isVisible(txnID uint64, xmin uint64, xmax uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	snap := e.txnSnapshots[txnID]

	// committed reports whether `id` was committed AS OF this txn's snapshot.
	// Fallback to the live committed map if no snapshot exists (defensive: a
	// live txn always has one, so this only covers use-after-commit edge cases).
	committed := func(id uint64) bool {
		if snap != nil {
			return snap[id]
		}
		return e.committed[id]
	}

	// Created and visible: I created it, or its creator committed in my snapshot.
	createdVisible := xmin == txnID || committed(xmin)
	if !createdVisible {
		return false
	}

	// Deleted from my perspective: xmax set, and either I deleted it or its
	// deleter committed in my snapshot.
	deleted := xmax != 0 && (xmax == txnID || committed(xmax))
	return !deleted
}

// checkWriteConflict implements first-updater-wins. A conflict exists if the row
// we are about to write has already been touched by a transaction that committed
// outside our view.
func (e *Engine) checkWriteConflict(txnID uint64, row core.Row) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	snap := e.txnSnapshots[txnID]

	// Case A: someone already stamped this row's Xmax and committed -> conflict.
	if row.Xmax != 0 && row.Xmax != txnID && e.committed[row.Xmax] {
		return fmt.Errorf("write conflict: row modified by a concurrent transaction")
	}

	// Case B: the version we're about to overwrite was created by a txn that
	// committed AFTER we began (absent from our snapshot) -> conflict.
	if snap != nil && row.Xmin != txnID && e.committed[row.Xmin] && !snap[row.Xmin] {
		return fmt.Errorf("write conflict: row modified by a transaction that committed after this one began")
	}

	return nil
}

// ==========================================
// 4. DDL (Data Definition)
// ==========================================

func (e *Engine) CreateTable(_ core.Txn, name string, schema core.Schema) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	path := filepath.Join(e.dataDir, name+".db")
	hf, err := NewHeapFile(path)
	if err != nil {
		return err
	}

	pool := NewBufferPool(hf, 100)
	hf.SetBufferPool(pool)

	if err := e.catalog.AddTable(name, schema, hf); err != nil {
		return err
	}

	return e.catalog.Save(e.dataDir)
}

func (e *Engine) DropTable(_ core.Txn, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	meta, err := e.catalog.GetTable(name)
	if err != nil {
		return err
	}

	meta.HeapFile.pool.FlushAll()
	meta.HeapFile.Close()
	os.Remove(meta.HeapFile.file.Name())

	if err := e.catalog.DropTable(name); err != nil {
		return err
	}

	return e.catalog.Save(e.dataDir)
}

func (e *Engine) GetSchema(name string) (core.Schema, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	meta, err := e.catalog.GetTable(name)
	if err != nil {
		return core.Schema{}, false
	}
	return meta.Schema, true
}

// ListTables returns the names of all tables in the catalog, sorted.
func (e *Engine) ListTables() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.catalog.ListTables()
}

// ==========================================
// 5. DML Wrappers (MVCC)
// ==========================================

func (e *Engine) Insert(txn core.Txn, table string, row core.Row) (core.RowID, error) {
	row.Xmin = txn.ID()
	row.Xmax = 0

	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return 0, err
	}

	id, err := meta.HeapFile.Insert(Serialize(row))
	if err != nil {
		return 0, err
	}

	for i, col := range meta.Schema.Columns {
		if _, has := meta.IndexRoots[col.Name]; has && !row.Values[i].Null {
			if err := e.InsertIntoIndex(table, col.Name, row.Values[i], id); err != nil {
				return id, fmt.Errorf("failed to update index on %s.%s: %w", table, col.Name, err)
			}
		}
	}

	return id, nil
}

func (e *Engine) Update(txn core.Txn, table string, id core.RowID, row core.Row) error {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return err
	}

	data, err := meta.HeapFile.Get(id)
	if err != nil {
		return err
	}
	oldRow := Deserialize(data)

	if err := e.checkWriteConflict(txn.ID(), oldRow); err != nil {
		return err
	}

	// MVCC update: keep the old version (stamp Xmax so older snapshots still
	// see it) and write a brand-new version. We do NOT overwrite Xmin.
	oldRow.Xmax = txn.ID()
	if err := meta.HeapFile.Update(id, Serialize(oldRow)); err != nil {
		return err
	}

	row.Xmin = txn.ID()
	row.Xmax = 0
	newID, err := meta.HeapFile.Insert(Serialize(row))
	if err != nil {
		return err
	}

	// Index the new version (mirrors what Insert does).
	for i, col := range meta.Schema.Columns {
		if _, has := meta.IndexRoots[col.Name]; has && !row.Values[i].Null {
			if err := e.InsertIntoIndex(table, col.Name, row.Values[i], newID); err != nil {
				return fmt.Errorf("failed to update index on %s.%s: %w", table, col.Name, err)
			}
		}
	}

	return nil
}

func (e *Engine) Delete(txn core.Txn, table string, id core.RowID) error {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return err
	}

	data, err := meta.HeapFile.Get(id)
	if err != nil {
		return err
	}
	row := Deserialize(data)

	if err := e.checkWriteConflict(txn.ID(), row); err != nil {
		return err
	}

	// Logical (MVCC) delete: stamp Xmax, leave the tuple in place.
	row.Xmax = txn.ID()
	return meta.HeapFile.Update(id, Serialize(row))
}

func (e *Engine) Scan(txn core.Txn, table string) (core.RowIterator, error) {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return nil, err
	}
	cnt, err := meta.HeapFile.getPageCount()
	if err != nil {
		return nil, err
	}
	return &heapFileIterator{
		hf:        meta.HeapFile,
		engine:    e,
		txnID:     txn.ID(),
		pageCount: cnt,
	}, nil
}

// ==========================================
// 6. Iterators
// ==========================================

type heapFileIterator struct {
	hf        *HeapFile
	engine    *Engine
	txnID     uint64
	raw       bool // when true, skip MVCC visibility (used for index builds)
	pageCount int
	currPage  int
	currSlot  int
}

func (it *heapFileIterator) Next() (core.RowID, core.Row, bool, error) {
	for it.currPage < it.pageCount {
		page, err := it.hf.pool.FetchPage(it.currPage)
		if err != nil {
			return 0, core.Row{}, false, err
		}

		slotCount := int(page.Data[0]) | int(page.Data[1])<<8

		for it.currSlot < slotCount {
			slotOffset := 4 + (it.currSlot * 4)
			length := int(page.Data[slotOffset+2]) | int(page.Data[slotOffset+3])<<8

			id := encodeRowID(it.currPage, it.currSlot)
			it.currSlot++

			if length == 0 {
				continue
			}

			dataOffset := int(page.Data[slotOffset]) | int(page.Data[slotOffset+1])<<8
			row := Deserialize(page.Data[dataOffset : dataOffset+length])

			if it.raw || it.engine.isVisible(it.txnID, row.Xmin, row.Xmax) {
				return id, row, true, nil
			}
		}

		it.currPage++
		it.currSlot = 0
	}
	return 0, core.Row{}, false, nil
}

func (it *heapFileIterator) Close() error { return nil }

// ==========================================
// 7. Indexing
// ==========================================

func (e *Engine) HasIndex(table, column string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return false
	}
	_, exists := meta.IndexRoots[column]
	return exists
}

func (e *Engine) CreateIndex(txn core.Txn, table, column string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return err
	}

	if _, exists := meta.IndexRoots[column]; exists {
		return fmt.Errorf("index on %s.%s already exists", table, column)
	}

	pageCount, err := e.indexFile.getPageCount()
	if err != nil {
		return err
	}

	rootPageID := pageCount
	rootNode := NewLeafNode(rootPageID)

	pageData := make([]byte, PageSize)
	if err := e.writeNodeToBytes(rootNode, pageData); err != nil {
		return err
	}

	var page Page
	copy(page.Data[:], pageData)
	if err := e.indexFile.writePage(rootPageID, &page); err != nil {
		return err
	}

	cachedPage, err := e.indexPool.FetchPage(rootPageID)
	if err != nil {
		return err
	}
	copy(cachedPage.Data[:], pageData)

	meta.IndexRoots[column] = rootPageID

	colIdx := -1
	for i, col := range meta.Schema.Columns {
		if col.Name == column {
			colIdx = i
			break
		}
	}
	if colIdx == -1 {
		return fmt.Errorf("column %s not found in schema", column)
	}

	iter, err := e.scanTable(txn, table)
	if err != nil {
		return err
	}
	defer iter.Close()

	for {
		id, row, ok, err := iter.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}

		val := row.Values[colIdx]
		if !val.Null {
			if err := e.insertIntoIndexLocked(table, column, val, id); err != nil {
				return fmt.Errorf("failed to insert existing row into index: %w", err)
			}
		}
	}

	return e.catalog.Save(e.dataDir)
}

func (e *Engine) writeNodeToBytes(node *BTreeNode, data []byte) error {
	binary.LittleEndian.PutUint16(data[0:2], node.NodeType)
	binary.LittleEndian.PutUint16(data[2:4], node.NumKeys)
	binary.LittleEndian.PutUint32(data[4:8], uint32(node.NextLeaf))

	offset := 12
	for i := 0; i < int(node.NumKeys); i++ {
		binary.LittleEndian.PutUint64(data[offset:offset+8], node.Pointers[i])
		offset += 8
		binary.LittleEndian.PutUint64(data[offset:offset+8], uint64(node.Keys[i].Int))
		offset += 8
	}

	if node.NodeType == NodeTypeInternal {
		binary.LittleEndian.PutUint64(data[offset:offset+8], node.Pointers[node.NumKeys])
	}

	return nil
}

// scanTable returns a raw (non-MVCC) iterator over all physical rows. It is used
// for index builds, where every live tuple must be indexed. It must NOT apply
// visibility filtering: callers (CreateIndex) hold e.mu, and isVisible also takes
// e.mu, so a filtering scan here would self-deadlock.
func (e *Engine) scanTable(_ core.Txn, table string) (core.RowIterator, error) {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return nil, err
	}

	pageCount, err := meta.HeapFile.getPageCount()
	if err != nil {
		return nil, err
	}

	return &heapFileIterator{
		hf:        meta.HeapFile,
		raw:       true,
		pageCount: pageCount,
	}, nil
}

func (e *Engine) insertIntoIndexLocked(table, column string, key core.Value, rowID core.RowID) error {
	rootPageID, err := e.getIndexRootPageLocked(table, column)
	if err != nil {
		return err
	}

	leaf, path, err := e.findLeafWithPath(rootPageID, key)
	if err != nil {
		return err
	}

	insertIntoNodeArrays(leaf, key, uint64(rowID))

	if leaf.NumKeys < MaxKeys {
		return e.writeNode(leaf)
	}

	return e.handleSplitCascade(table, column, leaf, path)
}

func (e *Engine) DeleteFromIndex(table, column string, key core.Value, rowID core.RowID) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rootPageID, err := e.getIndexRootPageLocked(table, column)
	if err != nil {
		return err
	}

	leaf, _, err := e.findLeafWithPath(rootPageID, key)
	if err != nil {
		return err
	}

	found := false
	for i := 0; i < int(leaf.NumKeys); i++ {
		cmp, err := leaf.Keys[i].Compare(key)
		if err != nil {
			return err
		}
		if cmp == 0 && leaf.Pointers[i] == uint64(rowID) {
			copy(leaf.Keys[i:], leaf.Keys[i+1:])
			copy(leaf.Pointers[i:], leaf.Pointers[i+1:])
			leaf.Keys = leaf.Keys[:leaf.NumKeys-1]
			leaf.Pointers = leaf.Pointers[:leaf.NumKeys-1]
			leaf.NumKeys--
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("entry not found in index: key=%v, rowID=%d", key, rowID)
	}

	return e.writeNode(leaf)
}

func (e *Engine) getIndexRootPageLocked(table, column string) (int, error) {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return 0, err
	}

	rootPage, exists := meta.IndexRoots[column]
	if !exists {
		return 0, fmt.Errorf("index on %s.%s does not exist", table, column)
	}

	return rootPage, nil
}

func valuesEqual(a, b core.Value) bool {
	if a.Null != b.Null {
		return false
	}
	if a.Null && b.Null {
		return true
	}
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case core.TypeInt:
		return a.Int == b.Int
	case core.TypeBool:
		return a.Bool == b.Bool
	case core.TypeText:
		return a.Str == b.Str
	default:
		return false
	}
}

func (e *Engine) Analyze(txn core.Txn, table string) error {
	e.mu.Lock()
	meta, err := e.catalog.GetTable(table)
	e.mu.Unlock()
	if err != nil {
		return err
	}

	stats := &core.TableStats{
		Columns: make(map[string]core.ColumnStats),
	}

	iter, err := e.Scan(txn, table)
	if err != nil {
		return err
	}
	defer iter.Close()

	distinctSets := make(map[string]map[string]struct{})
	for _, col := range meta.Schema.Columns {
		distinctSets[col.Name] = make(map[string]struct{})
	}

	for {
		_, row, ok, _ := iter.Next() // Fixed: added '_' for RowID
		if !ok {
			break
		}
		stats.RowCount++

		for i, col := range meta.Schema.Columns {
			val := row.Values[i]
			colStats := stats.Columns[col.Name]

			if val.Null {
				colStats.NullCount++
			} else {
				// Track distinct count
				distinctSets[col.Name][val.String()] = struct{}{}

				// Track Min/Max
				// We must handle the error returned by Compare
				resMin, errMin := val.Compare(colStats.Min)
				if colStats.Min.Null || (errMin == nil && resMin == -1) {
					colStats.Min = val
				}

				resMax, errMax := val.Compare(colStats.Max)
				if colStats.Max.Null || (errMax == nil && resMax == 1) {
					colStats.Max = val
				}
			}
			stats.Columns[col.Name] = colStats
		}
	}

	for name, set := range distinctSets {
		c := stats.Columns[name]
		c.DistinctCount = int64(len(set))
		stats.Columns[name] = c
	}

	e.mu.Lock()
	meta.Stats = stats
	e.mu.Unlock()
	return nil
}

func (e *Engine) Stats(table string) (core.TableStats, bool) { // Fixed: added '*'
	e.mu.Lock()
	defer e.mu.Unlock()
	meta, err := e.catalog.GetTable(table)
	if err != nil || meta.Stats == nil {
		return core.TableStats{}, false
	}
	return *meta.Stats, true
}
