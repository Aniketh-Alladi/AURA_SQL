# Storage Engine: AURA_SQL

This package implements the `core.StorageEngine` interface using a disk-backed, slotted-page architecture.

## Architecture
The engine is structured into three primary layers:

1. **Slotted Page Manager (`page.go`):** Each 4KB page uses a slotted directory to store variable-length rows. 
   - **Header:** Contains slot count and a free-space pointer.
   - **Slot Directory:** Grows downwards from the header, storing `(offset, length)` pairs.
   - **Payloads:** Grow upwards from the end of the page, allowing rows to be variable length.



2. **Heap File & Buffer Pool (`heapfile.go`, `bufferpool.go`):** - The `HeapFile` manages the physical `.db` file on disk, abstracting I/O into 4KB chunks.
   - The `BufferPool` acts as a cache (defaulting to 100 pages), employing an eviction policy to minimize disk access. It uses a **Dirty Page** flag to ensure only modified pages are flushed back to disk.

3. **Two-Gear Update Mechanism:**
   Because our `TEXT` fields can vary in length, we use a two-tiered strategy for `UPDATE` operations:
   - **Gear 1 (In-Place):** If the new row is smaller than or equal to the old row, we overwrite the existing bytes and update the slot directory length.
   - **Gear 2 (Re-Insert):** If the new row is larger, we tombstone the old row (zero-out the slot) and append the new row to the free space at the end of the page.

## Important Implementation Notes
- **Tombstones:** Deleted rows are marked by setting their slot length to 0. Scans automatically skip these tombstones.
- **Persistence:** All table schemas are serialized to `catalog.json` in the data directory. The engine automatically reloads and rebuilds the catalog map on `New()`.
- **Integration:** The `Scan` iterator is the primary way the executor retrieves data. If performing mutations, the executor must materialize rows first, close the scan, and then apply changes to avoid iterator invalidation during page shifts.