# AURA_SQL

A relational database engine built from scratch in Go — SQL parsing, a disk-backed
storage layer with a B-tree index, a query executor, and multi-version concurrency
control (MVCC) you can watch prevent transaction anomalies live.

> Status: in development. Built by a team of three as a from-scratch systems project.

<!-- TODO: add the CI status badge once the repo is on GitHub:
[![CI](https://github.com/<owner>/AURA_SQL/actions/workflows/ci.yml/badge.svg)](https://github.com/<owner>/AURA_SQL/actions/workflows/ci.yml)
-->

## What it does

You type SQL; the engine parses it, plans it, runs it against real on-disk storage,
and returns rows — the same loop a real database runs, implemented end to end with no
database libraries. The headline feature is MVCC: multiple transactions run at once,
and the engine demonstrably prevents anomalies like dirty reads and lost updates.

<!-- TODO: drop in a sample session (SQL in, rows out) once the CLI works. -->
<!-- TODO: add the architecture diagram and the MVCC anomaly demo here. -->

## Architecture

Everything is built against one shared contract, the `core` package: the value/row/
schema types, the `StorageEngine` interface, and the SQL AST. The three tracks depend
only on `core`, never on each other, so each can be built and tested in isolation.

```
SQL text  ──►  parser  ──►  AST  ──►  executor  ──►  StorageEngine  ──►  rows
                                          │                 ▲
                                          └── reads schema ─┘
```

`memstore` is a throwaway in-memory `StorageEngine` used while the real disk-backed
engine is built — it lets the parser and executor run end to end from day one. The
real engine drops into the same interface with no other code changes.

## Repo layout

```
core/        Shared contract: types, StorageEngine interface, SQL AST   (everyone)
storage/     Real engine: heap files, buffer pool, B-tree, MVCC         (track 1)
parser/      SQL text -> core.Statement (the AST)                       (track 2)
executor/    Runs a core.Statement against a core.StorageEngine         (track 3)
memstore/    In-memory StorageEngine stand-in (for tests + early dev)
cmd/         Runnable programs (smoke test now; SQL REPL later)
```

## Build & run

Requires Go 1.22+.

```sh
go build ./...        # compile everything
go test ./...         # run all tests
go vet ./...          # static checks
gofmt -l .            # list any unformatted files (should print nothing)
```

Run the smoke test (proves the core types and storage contract fit together):

```sh
go run ./cmd/smoke
```

## Contributing (team workflow)

- Work on a branch and open a pull request; don't push straight to `main`.
- CI must be green before merging.
- The `core` package is append-mostly: adding a new AST node is fine, but changing
  an existing interface signature needs all three of us — it breaks the other tracks.

## Roadmap (core scope)

- [ ] SQL parser: CREATE / INSERT / SELECT (with WHERE) / UPDATE / DELETE, one join
- [ ] Executor: scan, filter, project, nested-loop join, insert/update/delete
- [ ] Storage: heap files + buffer pool
- [ ] B-tree index
- [ ] Transactions + MVCC, with a live anomaly-prevention demo
- [ ] SQL REPL and final write-up
