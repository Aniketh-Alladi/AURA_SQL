
# AURA_SQL

A relational database engine built from scratch in Go. Features include a custom SQL parser, disk-backed storage with B-tree indexing, a query executor, and multi-version concurrency control (MVCC) that provably prevents transaction anomalies.

[![CI](https://github.com/Aniketh-Alladi/AURA_SQL/actions/workflows/ci.yml/badge.svg)](https://github.com/Aniketh-Alladi/AURA_SQL/actions/workflows/ci.yml)

## MVCC Demo
Our engine maintains snapshot isolation to prevent common anomalies.

```text
=== DEMO: Dirty Read Prevention ===
T1: UPDATE accounts SET bal = 0 WHERE id = 1 (not committed)
T2: SELECT bal FROM accounts WHERE id = 1
Result: T2 sees 100 (Dirty read prevented!)

=== DEMO: Lost Update Prevention ===
T3: UPDATE accounts SET bal = 150 WHERE id = 1; COMMIT
T4: UPDATE accounts SET bal = 200 WHERE id = 1
Result: write conflict: row modified by a concurrent transaction (Lost update prevented!)

```

## Performance

B-tree indexing provides a **~60x performance improvement** on equality lookups compared to full table scans.

| Access Path | Latency (ns/op) |
| --- | --- |
| Full Table Scan (100k rows) | 10,956,391 |
| B-Tree Index Seek | 182,301 |

## Architecture

The system follows a modular architecture where all components communicate through the `core` contract, allowing independent development of the parser, executor, and storage layers.

* **Parser/Executor**: Transforms SQL text into an Abstract Syntax Tree (AST), which is then mapped to an operator tree for execution.
* **Storage Engine**: Manages physical data via heap files and a `BufferPool`. Secondary indexes are implemented using a B+-tree.
* **Transactions**: Uses MVCC with `xmin`/`xmax` versioning and a first-committer-wins policy to ensure consistency.

## How it works

The engine is built on a modular storage layer that separates physical data management from transactional logic.

* **Disk-Backed Storage**: The `HeapFile` manages rows within 4KB pages, while the `BufferPool` handles memory caching to ensure efficient disk I/O.
* **B-tree Indexing**: Secondary indexes are implemented using a B+-tree, providing logarithmic-time lookups for `WHERE` clauses. This structure allows the engine to bypass full table scans, resulting in the significant performance gains seen in our benchmarks.
* **MVCC & Snapshot Isolation**:
* **Versioning**: Every row is tagged with `Xmin` (the ID of the creating transaction) and `Xmax` (the ID of the deleting transaction).
* **Visibility**: When a transaction begins, it freezes a snapshot of all currently committed transaction IDs. The `isVisible` function uses this snapshot to filter out rows created by "future" transactions or rows deleted by transactions that committed after the current one began.
* **Conflict Detection**: We enforce a "First-Committer-Wins" policy. If a transaction attempts to modify a row that was already updated by another transaction committed since the current one began, the engine rejects the update to prevent lost updates.


## Phase 6: Cost-Based Optimizer (Extension)

To facilitate advanced query optimization, AURA_SQL now includes a statistics subsystem:

* **`ANALYZE`**: Scans table data to compute per-column row counts, distinct value counts (NDV), null counts, and min/max values.
* **Cost-Based Decisions**: The optimizer uses these statistics to estimate query costs, most notably allowing for intelligent join-order reordering to minimize intermediate result sizes.
* **Benchmark Harness**: Includes automated seeding and benchmark tools in `main.go` to measure query latency improvements, proving the efficacy of cost-based planning over naive execution.


## Repo layout

```
core/        Shared contract: types, StorageEngine interface, SQL AST
storage/     Real engine: heap files, buffer pool, B-tree, MVCC
parser/      SQL text -> core.Statement (the AST)
executor/    Runs a core.Statement against a core.StorageEngine
cmd/         Runnable programs and demo harness

```

## Build & run

Requires Go 1.22+.

```sh
go build ./...
go test ./...
go run ./cmd/demo    # Run the MVCC anomaly prevention demo

```

## Roadmap status

* [x] SQL parser: CREATE / INSERT / SELECT / UPDATE / DELETE
* [x] Executor: scan, filter, project, nested-loop join
* [x] Storage: heap files + buffer pool
* [x] B-tree index
* [x] Transactions + MVCC + Anomaly Prevention Demo

```


```