package storage

import (
	"os"

	"aurasql/core"
)

// ==========================================
// 1. RowID Encoding / Decoding
// ==========================================

// encodeRowID packs a 48-bit pageID and a 16-bit slotIndex into a single uint64.
func encodeRowID(pageID int, slotIndex int) core.RowID {
	return core.RowID((uint64(pageID) << 16) | uint64(slotIndex))
}

// decodeRowID extracts the pageID and slotIndex from a core.RowID.
func decodeRowID(id core.RowID) (int, int) {
	pageID := int(uint64(id) >> 16)
	slotIndex := int(uint64(id) & 0xFFFF)
	return pageID, slotIndex
}

// ==========================================
// 2. Heap File Structure
// ==========================================

// HeapFile manages a single on-disk file made up of 4KB pages.
type HeapFile struct {
	file *os.File
	pool *BufferPool // Added reference to the cache layer
}

// NewHeapFile opens an existing file or creates a new one if it doesn't exist.
func NewHeapFile(filepath string) (*HeapFile, error) {
	f, err := os.OpenFile(filepath, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	return &HeapFile{file: f}, nil
}

// SetBufferPool links the cache to the heap file.
func (hf *HeapFile) SetBufferPool(bp *BufferPool) {
	hf.pool = bp
}

// Close gracefully shuts down the file.
func (hf *HeapFile) Close() error {
	return hf.file.Close()
}

// ==========================================
// 3. Disk I/O Helpers
// ==========================================

// readPage pulls exactly 4096 bytes from disk into a Page struct.
func (hf *HeapFile) readPage(pageID int) (*Page, error) {
	page := &Page{}
	offset := int64(pageID * PageSize)

	// ReadAt reads exactly len(page.Data) bytes starting at the given offset
	_, err := hf.file.ReadAt(page.Data[:], offset)
	if err != nil {
		return nil, err
	}
	return page, nil
}

// writePage flushes a Page struct directly to disk at the correct offset.
func (hf *HeapFile) writePage(pageID int, page *Page) error {
	offset := int64(pageID * PageSize)
	_, err := hf.file.WriteAt(page.Data[:], offset)
	return err
}

// getPageCount calculates how many 4KB pages currently exist in the file.
func (hf *HeapFile) getPageCount() (int, error) {
	stat, err := hf.file.Stat()
	if err != nil {
		return 0, err
	}
	return int(stat.Size() / PageSize), nil
}

// ==========================================
// 4. Data Manipulation (DML) Operations
// ==========================================

// Insert finds a page with free space, writes the row, and returns the RowID.
func (hf *HeapFile) Insert(rowBytes []byte) (core.RowID, error) {
	pageCount, err := hf.getPageCount()
	if err != nil {
		return 0, err
	}

	// Scan existing pages for free space using the BUFFER POOL
	for i := 0; i < pageCount; i++ {
		page, err := hf.pool.FetchPage(i) // Replaced direct disk read
		if err != nil {
			return 0, err
		}

		slotIndex, err := page.Insert(rowBytes)
		if err == nil {
			// Success! Mark dirty instead of writing directly to disk
			hf.pool.MarkDirty(i)
			return encodeRowID(i, slotIndex), nil
		}
	}

	// If all pages are full (or file is empty), append a new page
	newPage := NewPage()
	slotIndex, err := newPage.Insert(rowBytes)
	if err != nil {
		return 0, err
	}

	// Write the new page directly to establish it on disk, then manually cache it
	err = hf.writePage(pageCount, newPage)
	if err != nil {
		return 0, err
	}

	// Inject the newly created page into the cache
	hf.pool.pages[pageCount] = newPage

	return encodeRowID(pageCount, slotIndex), nil
}

// Get fetches a single row from memory via the Buffer Pool.
func (hf *HeapFile) Get(id core.RowID) ([]byte, error) {
	pageID, slotIndex := decodeRowID(id)

	// Fetch from cache instead of disk
	page, err := hf.pool.FetchPage(pageID)
	if err != nil {
		return nil, err
	}

	return page.Get(slotIndex)
}

// Update modifies an existing row. It attempts an in-place update first.
// If the new row is too large, it deletes the old row and inserts the new one.
func (hf *HeapFile) Update(id core.RowID, rowBytes []byte) error {
	pageID, slotIndex := decodeRowID(id)

	page, err := hf.pool.FetchPage(pageID)
	if err != nil {
		return err
	}

	// Try Gear 1: In-place update
	err = page.Update(slotIndex, rowBytes)
	if err == nil {
		// Success! It fit perfectly.
		hf.pool.MarkDirty(pageID)
		return nil
	}

	// Try Gear 2: If it didn't fit, we delete and re-insert
	if err.Error() == "row too large" {
		// Tombstone the old row
		if err := hf.Delete(id); err != nil {
			return err
		}

		// Insert the new row (this handles finding free space or creating a new page)
		_, err := hf.Insert(rowBytes)
		return err
	}

	return err
}

// Delete removes a row logically in memory.
func (hf *HeapFile) Delete(id core.RowID) error {
	pageID, slotIndex := decodeRowID(id)

	page, err := hf.pool.FetchPage(pageID)
	if err != nil {
		return err
	}

	err = page.Delete(slotIndex)
	if err != nil {
		return err
	}

	// Flag for disk write later
	hf.pool.MarkDirty(pageID)
	return nil
}
