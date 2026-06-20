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
// 1. The Transaction Shell (Phase 1)
// ==========================================

type txn struct{ id uint64 }

func (t *txn) ID() uint64      { return t.id }
func (t *txn) Commit() error   { return nil }
func (t *txn) Rollback() error { return nil }

// ==========================================
// 2. Engine Core & DDL (Data Definition)
// ==========================================

type Engine struct {
	mu        sync.Mutex
	catalog   *Catalog
	dataDir   string
	nextTx    uint64
	indexFile *HeapFile
	indexPool *BufferPool
}

// New creates a new StorageEngine saving data to the specified directory.
func New(dataDir string) (*Engine, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	cat := NewCatalog()
	if err := cat.Load(dataDir); err != nil {
		return nil, err
	}

	// Spin up the dedicated file and Buffer Pool for our B-Tree Indexes
	indexPath := filepath.Join(dataDir, "indexes.db")

	// Check if it exists before creating
	_, err := os.Stat(indexPath)
	isNew := os.IsNotExist(err)

	idxFile, err := NewHeapFile(indexPath)
	if err != nil {
		return nil, err
	}

	if isNew {
		// Write the first page AND initialize it as a proper page
		emptyPage := NewPage() // Use NewPage() instead of raw bytes
		// Write page 0
		if _, err := idxFile.file.WriteAt(emptyPage.Data[:], 0); err != nil {
			return nil, err
		}
	}

	// Create a buffer pool specifically for indexes (e.g., 100 pages)
	idxPool := NewBufferPool(idxFile, 100)
	idxFile.SetBufferPool(idxPool)

	// Pre-load page 0 into the pool if it's new
	if isNew {
		// Force page 0 into the cache
		_, err := idxPool.FetchPage(0)
		if err != nil {
			return nil, err
		}
	}

	return &Engine{
		catalog:   cat,
		dataDir:   dataDir,
		indexFile: idxFile,
		indexPool: idxPool,
	}, nil
}

func (e *Engine) Begin() (core.Txn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextTx++
	return &txn{id: e.nextTx}, nil
}

func (e *Engine) CreateTable(_ core.Txn, name string, schema core.Schema) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	filepath := filepath.Join(e.dataDir, name+".db")
	hf, err := NewHeapFile(filepath)
	if err != nil {
		return err
	}

	pool := NewBufferPool(hf, 100)
	hf.SetBufferPool(pool)

	err = e.catalog.AddTable(name, schema, hf)
	if err != nil {
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

	err = e.catalog.DropTable(name)
	if err != nil {
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

// ==========================================
// 3. DML Wrappers & Iterator
// ==========================================

func (e *Engine) Insert(_ core.Txn, table string, row core.Row) (core.RowID, error) {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return 0, err
	}

	id, err := meta.HeapFile.Insert(Serialize(row))
	if err != nil {
		return 0, err
	}

	for colIdx, col := range meta.Schema.Columns {
		if _, hasIndex := meta.IndexRoots[col.Name]; hasIndex {
			val := row.Values[colIdx]
			if val.Null {
				continue
			}
			err := e.InsertIntoIndex(table, col.Name, val, id)
			if err != nil {
				return 0, fmt.Errorf("failed to update index on %s.%s: %w", table, col.Name, err)
			}
		}
	}

	return id, nil
}

func (e *Engine) Update(_ core.Txn, table string, id core.RowID, row core.Row) error {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return err
	}

	// Get the old row BEFORE updating
	oldRowBytes, err := meta.HeapFile.Get(id)
	if err != nil {
		return err
	}
	oldRow := Deserialize(oldRowBytes)

	// Try to update in-place first
	err = meta.HeapFile.Update(id, Serialize(row))
	if err != nil && err.Error() == "row too large" {
		// Row grew too large - need to delete and re-insert
		// First delete from indexes
		for colIdx, col := range meta.Schema.Columns {
			if _, hasIndex := meta.IndexRoots[col.Name]; hasIndex {
				oldVal := oldRow.Values[colIdx]
				if !oldVal.Null {
					if err := e.DeleteFromIndex(table, col.Name, oldVal, id); err != nil {
						return fmt.Errorf("failed to delete from index on %s.%s: %w", table, col.Name, err)
					}
				}
			}
		}

		// Delete from heap file
		if err := meta.HeapFile.Delete(id); err != nil {
			return err
		}

		// Insert the new row (gets a new RowID)
		newID, err := meta.HeapFile.Insert(Serialize(row))
		if err != nil {
			return err
		}

		// Insert into indexes with the new RowID
		for colIdx, col := range meta.Schema.Columns {
			if _, hasIndex := meta.IndexRoots[col.Name]; hasIndex {
				newVal := row.Values[colIdx]
				if !newVal.Null {
					if err := e.InsertIntoIndex(table, col.Name, newVal, newID); err != nil {
						return fmt.Errorf("failed to insert into index on %s.%s: %w", table, col.Name, err)
					}
				}
			}
		}
		return nil
	} else if err != nil {
		return err
	}

	// In-place update worked, update indexes
	for colIdx, col := range meta.Schema.Columns {
		if _, hasIndex := meta.IndexRoots[col.Name]; hasIndex {
			oldVal := oldRow.Values[colIdx]
			newVal := row.Values[colIdx]

			// If the value changed, update the index
			if !valuesEqual(oldVal, newVal) {
				// Remove old entry
				if !oldVal.Null {
					if err := e.DeleteFromIndex(table, col.Name, oldVal, id); err != nil {
						return fmt.Errorf("failed to delete from index on %s.%s: %w", table, col.Name, err)
					}
				}
				// Add new entry
				if !newVal.Null {
					if err := e.InsertIntoIndex(table, col.Name, newVal, id); err != nil {
						return fmt.Errorf("failed to insert into index on %s.%s: %w", table, col.Name, err)
					}
				}
			}
		}
	}

	return nil
}

func (e *Engine) Delete(_ core.Txn, table string, id core.RowID) error {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return err
	}

	// Get the row first to remove from indexes
	rowBytes, err := meta.HeapFile.Get(id)
	if err != nil {
		return err
	}
	row := Deserialize(rowBytes)

	// Remove from indexes
	for colIdx, col := range meta.Schema.Columns {
		if _, hasIndex := meta.IndexRoots[col.Name]; hasIndex {
			val := row.Values[colIdx]
			if !val.Null {
				if err := e.DeleteFromIndex(table, col.Name, val, id); err != nil {
					return fmt.Errorf("failed to delete from index on %s.%s: %w", table, col.Name, err)
				}
			}
		}
	}

	// Delete from heap file
	return meta.HeapFile.Delete(id)
}

func (e *Engine) Scan(_ core.Txn, table string) (core.RowIterator, error) {
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
		pageCount: pageCount,
		currPage:  0,
		currSlot:  0,
	}, nil
}

type heapFileIterator struct {
	hf        *HeapFile
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
			rawRow := page.Data[dataOffset : dataOffset+length]

			return id, Deserialize(rawRow), true, nil
		}

		it.currPage++
		it.currSlot = 0
	}
	return 0, core.Row{}, false, nil
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

func (it *heapFileIterator) Close() error { return nil }

// ==========================================
// 4. Indexing (Phase 3)
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

	// Get current page count to determine where to write
	pageCount, err := e.indexFile.getPageCount()
	if err != nil {
		return err
	}

	// Create the new root node at the next available page ID
	rootPageID := pageCount

	// Create a new leaf node in memory
	rootNode := NewLeafNode(rootPageID)

	// Write the node to disk at the new page ID
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

	// Populate the index with existing rows
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

func (e *Engine) scanTable(txn core.Txn, table string) (core.RowIterator, error) {
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
		pageCount: pageCount,
		currPage:  0,
		currSlot:  0,
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
