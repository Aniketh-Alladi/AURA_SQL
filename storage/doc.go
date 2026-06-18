// Package storage will hold the real disk-backed implementation of
// core.StorageEngine: heap files, the buffer pool, the B-tree index, and MVCC.
//
// Owned by: you (track 1).
//
// Until this exists, use the memstore package wherever a core.StorageEngine is
// needed — it satisfies the same interface, so swapping in this real engine
// later requires no changes to the parser or executor.
package storage
