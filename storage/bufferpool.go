package storage

import (
	"errors"
)

// ==========================================
// 1. Buffer Pool Structure
// ==========================================

// BufferPool caches pages in memory to reduce expensive disk I/O.
type BufferPool struct {
	heapFile *HeapFile
	pages    map[int]*Page // Maps a Page ID to the actual Page struct
	dirty    map[int]bool  // Tracks if a Page ID has been modified
	capacity int           // The maximum number of pages we can hold in RAM
}

// NewBufferPool creates a fresh cache sitting on top of a HeapFile.
func NewBufferPool(hf *HeapFile, capacity int) *BufferPool {
	return &BufferPool{
		heapFile: hf,
		pages:    make(map[int]*Page),
		dirty:    make(map[int]bool),
		capacity: capacity,
	}
}

// ==========================================
// 2. Core Cache Operations
// ==========================================

// FetchPage retrieves a page from the cache, or loads it from disk if missing.
func (bp *BufferPool) FetchPage(pageID int) (*Page, error) {
	// Cache Hit: The page is already in RAM
	if page, exists := bp.pages[pageID]; exists {
		return page, nil
	}

	// Cache Miss: If the cache is full, we must evict a page to make room
	if len(bp.pages) >= bp.capacity {
		if err := bp.evictPage(); err != nil {
			return nil, err
		}
	}

	// Load the requested page from the hard drive
	page, err := bp.heapFile.readPage(pageID)
	if err != nil {
		return nil, err
	}

	// Store it in the cache and return it
	bp.pages[pageID] = page
	return page, nil
}

// MarkDirty flags a page as modified so it gets saved to disk later.
func (bp *BufferPool) MarkDirty(pageID int) {
	bp.dirty[pageID] = true
}

// Flush writes a specific dirty page back to the hard drive.
func (bp *BufferPool) Flush(pageID int) error {
	if bp.dirty[pageID] {
		page, exists := bp.pages[pageID]
		if !exists {
			return errors.New("cannot flush: page not in cache")
		}

		// Write to disk
		if err := bp.heapFile.writePage(pageID, page); err != nil {
			return err
		}

		// Unmark as dirty now that it is safely stored
		delete(bp.dirty, pageID)
	}
	return nil
}

// FlushAll forces all dirty pages in the cache to be written to disk.
func (bp *BufferPool) FlushAll() error {
	for pageID := range bp.dirty {
		if err := bp.Flush(pageID); err != nil {
			return err
		}
	}
	return nil
}

// ==========================================
// 3. Internal Eviction Logic
// ==========================================

// evictPage frees up one slot in the cache.
// It prioritizes evicting clean pages to avoid unnecessary disk writes.
func (bp *BufferPool) evictPage() error {
	var targetID = -1

	// Step 1: Look for any clean page we can just throw away
	for id := range bp.pages {
		if !bp.dirty[id] {
			targetID = id
			break
		}
	}

	// Step 2: If no clean pages exist, we have to evict a dirty one.
	// We must flush it to disk first so we don't lose the data.
	if targetID == -1 {
		for id := range bp.pages {
			targetID = id
			break // Just grab the first one we see
		}

		if err := bp.Flush(targetID); err != nil {
			return err
		}
	}

	// Step 3: Remove it from memory
	delete(bp.pages, targetID)
	delete(bp.dirty, targetID) // Just in case it was dirty

	return nil
}
