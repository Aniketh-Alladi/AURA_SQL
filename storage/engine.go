package storage

import (
	"encoding/binary" // Add this line
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
	return meta.HeapFile.Update(id, Serialize(row))
}

func (e *Engine) Delete(_ core.Txn, table string, id core.RowID) error {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return err
	}
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
	// First, write the page directly to disk
	pageData := make([]byte, PageSize)
	if err := e.writeNodeToBytes(rootNode, pageData); err != nil {
		return err
	}

	// Create a Page struct and write it to disk
	var page Page
	copy(page.Data[:], pageData)
	if err := e.indexFile.writePage(rootPageID, &page); err != nil {
		return err
	}

	// Now add it to the buffer pool's cache
	cachedPage, err := e.indexPool.FetchPage(rootPageID)
	if err != nil {
		return err
	}
	// Copy the data into the cached page
	copy(cachedPage.Data[:], pageData)

	// Update catalog
	meta.IndexRoots[column] = rootPageID
	return e.catalog.Save(e.dataDir)
}

// writeNodeToBytes packs a BTreeNode into a byte slice without using the buffer pool
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
