package storage

import (
	"fmt"
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
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	cat := NewCatalog()
	// Ask the catalog to rebuild itself from disk
	if err := cat.Load(dataDir); err != nil {
		return nil, err
	}

	return &Engine{
		catalog: cat,
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
	err = e.catalog.AddTable(name, schema, hf)
	if err != nil {
		return err
	}

	// Flush the new catalog state to disk
	return e.catalog.Save(e.dataDir)
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

	err = e.catalog.DropTable(name)
	if err != nil {
		return err
	}

	// Flush the updated catalog state to disk
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

		// Assuming you have an endian variable defined elsewhere in your package,
		// otherwise you might need to use binary.LittleEndian here.
		// Example: binary.LittleEndian.Uint16(...)
		slotCount := int(page.Data[0]) | int(page.Data[1])<<8 // simplified example of endian logic

		for it.currSlot < slotCount {
			slotOffset := 4 + (it.currSlot * 4)
			length := int(page.Data[slotOffset+2]) | int(page.Data[slotOffset+3])<<8

			// Assuming encodeRowID is defined elsewhere in your package
			id := encodeRowID(it.currPage, it.currSlot)
			it.currSlot++

			// Skip deleted rows (tombstones)
			if length == 0 {
				continue
			}

			dataOffset := int(page.Data[slotOffset]) | int(page.Data[slotOffset+1])<<8
			rawRow := page.Data[dataOffset : dataOffset+length]

			// Assuming Deserialize is defined elsewhere in your package
			return id, Deserialize(rawRow), true, nil
		}

		// Move to the next page
		it.currPage++
		it.currSlot = 0
	}

	// End of file
	return 0, core.Row{}, false, nil
}

// Close gracefully shuts down the engine, flushing caches and releasing file locks.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, meta := range e.catalog.tables {
		meta.HeapFile.pool.FlushAll()
		meta.HeapFile.Close()
	}
	return nil
}

// Close cleanly shuts down the iterator.
func (it *heapFileIterator) Close() error {
	return nil // We don't have any active file locks tied to the iterator itself right now
}

// ==========================================
// 4. Indexing (Phase 3 Stubs)
// ==========================================

func (e *Engine) CreateIndex(txn core.Txn, table, column string) error {
	// TODO: Phase 3 - Implement B+-Tree initialization
	return nil
}

func (e *Engine) HasIndex(table, column string) bool {
	// TODO: Phase 3 - Check catalog for B+-Tree existence
	return false
}

func (e *Engine) SeekIndex(txn core.Txn, table, column string, key core.Value) (core.RowIterator, error) {
	// TODO: Phase 3 - Implement B+-Tree search
	return nil, fmt.Errorf("SeekIndex not yet implemented in real storage")
}
