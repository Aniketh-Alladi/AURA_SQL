# Storage Engine: AURA_SQL

This package implements the `core.StorageEngine` interface, providing a disk-backed, transactional storage layer with Snapshot Isolation.

## Core Architecture

1. **Physical Layout (`page.go`, `heapfile.go`):** - Uses a 4KB slotted-page architecture to store variable-length rows.
   - The `HeapFile` manages the physical `.db` file on disk, abstracting I/O into 4KB chunks.
   - Managed by a `BufferPool` with a dirty-page tracking system to minimize disk I/O.

2. **Indexing (`btree.go`):**
   - A B+-tree structure provides logarithmic-time lookups for `WHERE` clauses, bypassing the cost of full table scans.
   - `SeekIndex` and `Scan` iterators provide the primary interface for data retrieval.

## Transactional MVCC Layer

The engine implements **Multi-Version Concurrency Control (MVCC)** to allow concurrent read/write operations without table-level locking.

* **Row Versioning (`xmin`, `xmax`)**: 
    - Every tuple is tagged with an `Xmin` (the ID of the transaction that created it) and an `Xmax` (the ID of the transaction that deleted it).
    - An `UPDATE` does not overwrite the row; it creates a new version and marks the old row's `Xmax` to maintain history for concurrent transactions.
* **Snapshot Isolation (`isVisible`)**:
    - Upon calling `Begin()`, each transaction captures a "snapshot" of all currently committed transaction IDs.
    - The `isVisible` function uses this snapshot to ensure a transaction only sees rows that were committed *before* it began, effectively ignoring dirty writes from concurrent transactions.
* **Write-Conflict Detection (`checkWriteConflict`)**:
    - Implements a "First-Committer-Wins" policy. If a transaction attempts to modify a row that has been updated or deleted by another transaction committed after the current one began, the engine returns a `write conflict` error to prevent lost updates.

## Implementation Details
- **Tombstones**: Deleted rows are not removed physically; instead, they are logically marked via `Xmax` (setting length to 0).
- **Index Builds**: The `CreateIndex` operation performs a raw scan (ignoring MVCC visibility) to ensure all physical rows are indexed, and then the engine maintains index integrity during `Insert` and `Update` operations.
- **Persistence**: Table schemas are serialized to `catalog.json`. All index nodes and data pages are tracked via the `BufferPool` and flushed to disk on `Close()`.