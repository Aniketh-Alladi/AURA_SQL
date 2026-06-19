package storage

import (
	"os"
	"path/filepath"
	"sync"

	"aurasql/core"
)

// ==========================================
// 1. The Transaction Shell (Phase 1)
// ==========================================

// txn is a dummy transaction for Phase 1. MVCC comes in Phase 4.
type txn struct{ id uint64 }

func (t *txn) ID() uint64      { return t.id }
func (t *txn) Commit() error   { return nil } // No-op
func (t *txn) Rollback() error { return nil } // No-op

// ==========================================
// 2. Engine Core & DDL (Data Definition)
// ==========================================

// Engine is the disk-backed implementation of core.StorageEngine.
type Engine struct {
	mu      sync.Mutex // Ensures thread safety for catalog operations
	catalog *Catalog
	dataDir string // The folder where heap files are saved
	nextTx  uint64
}

// New creates a new StorageEngine saving data to the specified directory.
func New(dataDir string) (*Engine, error) {
	// Ensure the data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	return &Engine{
		catalog: NewCatalog(),
		dataDir: dataDir,
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

	// Create the physical file
	hf, err := NewHeapFile(filepath)
	if err != nil {
		return err
	}

	// Spin up a buffer pool for this table (let's default to 100 pages in RAM)
	pool := NewBufferPool(hf, 100)
	hf.SetBufferPool(pool)

	// Register it in the catalog
	return e.catalog.AddTable(name, schema, hf)
}

func (e *Engine) DropTable(_ core.Txn, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	meta, err := e.catalog.GetTable(name)
	if err != nil {
		return err
	}

	// Flush cache, close file, delete from disk, and remove from catalog
	meta.HeapFile.pool.FlushAll()
	meta.HeapFile.Close()
	os.Remove(meta.HeapFile.file.Name())

	return e.catalog.DropTable(name)
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
	return meta.HeapFile.Insert(Serialize(row))
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

// heapFileIterator streams rows back to the executor one by one.
type heapFileIterator struct {
	hf        *HeapFile
	pageCount int
	currPage  int
	currSlot  int
}

// Next fetches the next visible row, skipping empty or deleted slots.
func (it *heapFileIterator) Next() (core.RowID, core.Row, bool, error) {
	for it.currPage < it.pageCount {
		page, err := it.hf.pool.FetchPage(it.currPage)
		if err != nil {
			return 0, core.Row{}, false, err
		}

		slotCount := int(endian.Uint16(page.Data[0:2]))

		for it.currSlot < slotCount {
			slotOffset := 4 + (it.currSlot * 4)
			length := int(endian.Uint16(page.Data[slotOffset+2 : slotOffset+4]))

			id := encodeRowID(it.currPage, it.currSlot)
			it.currSlot++

			// Skip deleted rows (tombstones)
			if length == 0 {
				continue
			}

			dataOffset := int(endian.Uint16(page.Data[slotOffset : slotOffset+2]))
			rawRow := page.Data[dataOffset : dataOffset+length]
			return id, Deserialize(rawRow), true, nil
		}

		// Move to the next page
		it.currPage++
		it.currSlot = 0
	}

	// End of file
	return 0, core.Row{}, false, nil
}

func (it *heapFileIterator) Close() error {
	return nil // Nothing to close for Phase 1
}
