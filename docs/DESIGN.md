I can't directly create a downloadable file, but I can give you the complete content to copy and save as `docs/DESIGN.md`. Here it is:

---

## Complete `docs/DESIGN.md`

```markdown
# AURA_SQL — Design Document

> A relational database engine built from scratch in Go — SQL parsing, disk-backed storage with a B-tree index, and MVCC concurrency control.

## 1. Overview

AURA_SQL is a complete relational database engine implemented in Go from the ground up, using no external database libraries. It runs the full query path end to end: a SQL parser turns text into an AST, a query executor built on the iterator model processes the query, and a disk-backed storage layer with a B-tree index handles persistence. The engine provides snapshot isolation through multi-version concurrency control (MVCC), demonstrably preventing transaction anomalies like dirty reads and lost updates.

### Layered Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     SQL Text (User Input)                    │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                      Parser (Varun)                         │
│               SQL text → core.Statement (AST)               │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                     Executor (Tejus)                        │
│       AST → Operator Tree → StorageEngine Interface         │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    Storage (Aniketh)                        │
│       Heap Files → Buffer Pool → B-tree → MVCC             │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                          Disk                               │
└─────────────────────────────────────────────────────────────┘
```

### Key Design Principle: The `core` Contract

All three tracks — parser, executor, and storage — depend **only** on the `core` package. This shared contract defines:

- **Value/Row/Schema types** — the data vocabulary everyone speaks
- **The StorageEngine interface** — what the executor calls, what storage implements
- **The SQL AST** — what the parser produces, what the executor consumes

This design enabled parallel development: each track could be built and tested in isolation against the `memstore` (an in-memory stand-in for the real storage engine) before the real storage was ready.

---

## 2. The `core` Contract

The `core` package is the single source of truth for all types shared across layers.

### Values, Rows, and Schemas

A `Value` represents a typed cell in the database:

```go
type Value struct {
    Type ColumnType  // TypeInt, TypeText, or TypeBool
    Int  int64
    Str  string
    Bool bool
    Null bool
}
```

A `Row` is a slice of `Value`s, and a `Schema` describes the columns of a table or result set:

```go
type Row struct {
    Values []Value
    Xmin   uint64 // Transaction ID that created this version
    Xmax   uint64 // Transaction ID that deleted this version (0 if active)
}

type Schema struct {
    Columns []Column
}

type Column struct {
    Name string
    Type ColumnType
}
```

### The StorageEngine Interface

The executor consumes this interface; the storage engine implements it:

```go
type StorageEngine interface {
    // Transaction management
    Begin() (Txn, error)

    // Schema / DDL
    CreateTable(txn Txn, name string, schema Schema) error
    DropTable(txn Txn, name string) error
    GetSchema(name string) (Schema, bool)
    ListTables() []string

    // Data manipulation
    Insert(txn Txn, table string, row Row) (RowID, error)
    Update(txn Txn, table string, id RowID, row Row) error
    Delete(txn Txn, table string, id RowID) error

    // Scanning
    Scan(txn Txn, table string) (RowIterator, error)

    // Indexing
    CreateIndex(txn Txn, table, column string) error
    HasIndex(table, column string) bool
    SeekIndex(txn Txn, table, column string, key Value) (RowIterator, error)
}
```

MVCC visibility is handled **inside** the engine — `Scan` only returns rows visible to the transaction, so the executor never has to reason about isolation.

### The SQL AST (Abstract Syntax Tree)

The parser produces AST nodes that the executor walks. All statements implement the `Statement` interface:

```go
type Statement interface { isStatement() }

// DDL Statements
type CreateTableStmt struct {
    Table   string
    Columns []Column
}

type CreateIndexStmt struct {
    Name   string // Optional; "" means auto-generated
    Table  string
    Column string
}

// DML Statements
type InsertStmt struct {
    Table  string
    Values []Expr
}

type SelectStmt struct {
    Projection []Expr
    From       string
    Join       *JoinClause // nil if absent
    Where      Expr        // nil if absent
}

type UpdateStmt struct {
    Table string
    Set   []Assignment
    Where Expr // nil means every row
}

type DeleteStmt struct {
    Table string
    Where Expr // nil means every row
}

// Transaction Control Statements
type BeginStmt    struct{}
type CommitStmt   struct{}
type RollbackStmt struct{}

// Join Clause
type JoinClause struct {
    Table string
    On    Expr
}

// Assignment (for UPDATE)
type Assignment struct {
    Column string
    Value  Expr
}
```

### Expressions

Expressions form the building blocks of SQL conditions and projections:

```go
type Expr interface { isExpr() }

// Star represents "*" in a projection
type Star struct{}

// ColumnRef references a column, optionally qualified by table
type ColumnRef struct {
    Table string // "" if unqualified
    Name  string
}

// Literal is a constant value
type Literal struct {
    Value Value
}

// Binary expression: Left Op Right
type BinaryExpr struct {
    Op    BinOp
    Left  Expr
    Right Expr
}

// Binary operators supported
type BinOp int

const (
    OpEq  BinOp = iota // =
    OpNe               // != / <>
    OpLt               // <
    OpLe               // <=
    OpGt               // >
    OpGe               // >=
    OpAnd              // AND
    OpOr               // OR
    OpAdd              // +
    OpSub              // -
)
```

### Why This Design Matters

By defining all shared types in `core`, we achieved:

1. **Parallel Development**: Parser, executor, and storage could be built simultaneously
2. **Isolated Testing**: Each layer could be tested against `memstore` without waiting for others
3. **Clear Contracts**: No ambiguity about what each layer expects and produces
4. **Swapability**: The real storage engine plugs in wherever `memstore` was used — no other code changes

---

## 3. Parser Layer (`parser/`)

The AURA_SQL Parser is built from scratch as a highly deterministic, zero-dependency Lexer and Recursive-Descent Parser matching standard SQL lexical scoping conventions.

### A. Lexical Analysis (`lexer.go`)

The pipeline begins with the Lexer, which transforms a raw query string into a stream of structured `Token` objects. It tracks keywords (`SELECT`, `CREATE`, `INDEX`, `BEGIN`, `COMMIT`), identifiers, literal values (`INT`, `TEXT`, `BOOL`), and operators (`=`, `<`, `>`, `<=`, `>=`, `!=`).

- **State Preservation:** The lexer consumes characters sequentially, automatically ignoring white-space variations.
- **Safety:** String formatting checks ensure unclosed text values and illegal characters gracefully surface tokenization errors rather than causing runtime context faults.

### B. Syntactic Analysis & Precedence Handling (`parser.go`)

The parsed token stream is fed into a recursive-descent compiler tracking discrete statement pathways matching the core types defined in the `core` contract.

- **Precedence Trees:** Expressions within `WHERE` clauses are constructed into a hierarchical operator binary tree. Logical operators follow true SQL precedence constraints (evaluating mathematical evaluations and comparison bounds first, followed by structural `AND` groupings, and finishing at outer `OR` constraints).
- **Multi-Dialect Transaction Mapping:** To accommodate distinct execution scripts, the parser normalizes variant transaction syntax dialects automatically into single uniform execution instructions:
  - `BEGIN`, `BEGIN TRANSACTION`, and `START TRANSACTION` generate a `*core.BeginStmt`.
  - `COMMIT`, `COMMIT TRANSACTION`, and `END` generate a `*core.CommitStmt`.
  - `ROLLBACK` and `ROLLBACK TRANSACTION` generate a `*core.RollbackStmt`.

---

## 4. Executor & Query Processing (`executor/`)

### The Volcano Operator Model

The executor uses the **volcano / iterator model**, where every operator implements this interface:

```go
type Operator interface {
    Next() (core.Row, bool, error) // Pulls the next row; ok=false at end
    Schema() core.Schema            // Output schema so parents can resolve columns
    Close() error                   // Releases resources
}
```

This model allows the executor to build a tree of operators, where each operator pulls rows from its child. The root operator is the final result set.

### Core Operators

| Operator | Purpose |
|----------|---------|
| `ScanOp` | Reads rows from a table via `eng.Scan()`; source of data |
| `FilterOp` | Applies a WHERE predicate; emits only matching rows |
| `ProjectOp` | Evaluates projection expressions; builds output rows |
| `NestedLoopJoinOp` | Performs a nested-loop join; for each left row, scans the right table |

### How a SELECT Becomes an Operator Tree

A `SELECT` statement is transformed into a tree:

```sql
SELECT name FROM users WHERE active = true
```

becomes:

```
ProjectOp (name)
    │
    └── FilterOp (active = true)
            │
            └── ScanOp (users)
```

### Index Integration

When a `WHERE` clause uses an equality predicate on an indexed column, the executor can use `SeekIndex` instead of a full `Scan`:

```sql
SELECT name FROM users WHERE id = 5
```

**Without index:** `ProjectOp(FilterOp(ScanOp(users)))` — scans all rows, filters `id = 5`

**With index:** `ProjectOp(IndexSeekOp(users, id, 5))` — seeks directly to the matching row(s)

This provides **~60x performance improvement** on equality lookups.

### The `Session` Layer

The `Session` wraps the executor and manages transaction lifecycle:

```go
type Session struct {
    eng core.StorageEngine
    cur core.Txn // nil = autocommit mode
}

func (s *Session) Exec(stmt core.Statement) (core.Result, error) {
    switch stmt.(type) {
    case *core.BeginStmt:
        tx, err := s.eng.Begin()
        s.cur = tx
        return core.Result{}, nil
    case *core.CommitStmt:
        err := s.cur.Commit()
        s.cur = nil
        return core.Result{}, err
    case *core.RollbackStmt:
        err := s.cur.Rollback()
        s.cur = nil
        return core.Result{}, err
    default:
        if s.cur != nil {
            // Explicit transaction
            return Execute(s.eng, s.cur, stmt)
        }
        // Autocommit: one statement = one transaction
        tx, _ := s.eng.Begin()
        res, err := Execute(s.eng, tx, stmt)
        tx.Commit()
        return res, err
    }
}
```

**Autocommit mode:** Each statement runs in its own transaction. This is the default for the REPL.

**Explicit transaction:** After `BEGIN`, all statements run in `s.cur` and persist only on `COMMIT`; `ROLLBACK` discards them.

**Write conflict handling:** When storage returns a conflict error inside an explicit transaction, the transaction is automatically aborted — the user must `BEGIN` again.

### Worked Example: Tracing a Query

Let's trace `SELECT name FROM users WHERE id = 5` through the system with an index on `id`.

**Step 1: SQL Text**
```sql
SELECT name FROM users WHERE id = 5
```

**Step 2: Parser → AST**
```go
&SelectStmt{
    Projection: []Expr{&ColumnRef{Name: "name"}},
    From: "users",
    Where: &BinaryExpr{
        Op: OpEq,
        Left: &ColumnRef{Name: "id"},
        Right: &Literal{Value: NewInt(5)},
    },
}
```

**Step 3: Executor builds the plan**

The executor checks if `id` has an index:
```go
if eng.HasIndex("users", "id") {
    root = NewIndexSeekOp(eng, txn, "users", "id", value)
} else {
    scan := NewScanOp(eng, txn, "users")
    root = NewFilterOp(scan, whereExpr)
}
```

**Step 4: Execution (IndexSeekOp)**

1. `IndexSeekOp` calls `eng.SeekIndex(txn, "users", "id", 5)` → returns an iterator over exactly the rows where `id = 5`
2. `ProjectOp` pulls rows from `IndexSeekOp`, evaluates `name`, and returns the result

**Step 5: Result**
```
+-------+
| name  |
+-------+
| alice |
+-------+
(1 row)
```

---

## 5. Storage Layer (`storage/`)

The AURA_SQL storage engine provides a disk-backed, transactional foundation for the database, implementing snapshot isolation via Multi-Version Concurrency Control (MVCC).

#### 1. Page Management and I/O
* **Slotted-Page Architecture**: Data is stored in 4KB pages using a slotted-page design, allowing for variable-length row storage.
* **Buffer Pool**: A memory-resident cache tracks active pages, utilizing a "dirty page" bit-field to ensure only modified data is flushed to disk upon engine `Close()`.
* **Persistence**: The storage layer manages the physical `.db` files and a JSON-serialized catalog to maintain table schemas and index roots across restarts.

#### 2. Indexing Strategy
* **B+-Tree Indexing**: Secondary indexes are implemented via a B+-tree structure, providing logarithmic-time ($O(\log n)$) complexity for equality lookups.
* **Performance Impact**: By utilizing index seeks instead of full table scans, the engine achieves a ~60x performance improvement for point lookups.

#### 3. MVCC & Transactional Integrity
* **Row Versioning**: Each tuple is tagged with `Xmin` (the ID of the creating transaction) and `Xmax` (the ID of the deleting transaction). 
* **Snapshot Isolation**: Transactions capture a frozen "snapshot" of the committed transaction set at `Begin()`. The `isVisible` function uses this to determine visibility, ensuring that transactions only view data that was committed prior to their start time.
* **Conflict Detection**: The engine enforces a "First-Committer-Wins" policy. The `checkWriteConflict` method inspects row headers to detect if a record has been modified by a concurrent transaction that committed *after* the current transaction's snapshot was taken.
---

## 6. Transactions & Isolation

The engine provides **snapshot isolation** through MVCC.

### Row Versioning

Every row stores two transaction IDs:
- `Xmin`: the transaction ID that created this version
- `Xmax`: the transaction ID that deleted this version (0 if active)

### Visibility Rules

A row is visible to a transaction if:
1. The creating transaction (`Xmin`) committed before the snapshot, AND
2. The deleting transaction (`Xmax`) is 0 or committed after the snapshot

### Write-Write Conflict Detection

The engine uses a **first-committer-wins** policy:
- When a transaction attempts to update a row, the engine checks if the row was modified by a concurrent transaction
- If so, the update is rejected with a `write conflict` error
- The transaction must be rolled back

### Anomaly Prevention

| Anomaly | How It's Prevented |
|---------|-------------------|
| Dirty Read | Uncommitted writes have `Xmin` > snapshot; not visible |
| Lost Update | First-committer-wins: second writer gets conflict error |
| Non-Repeatable Read | Snapshot isolation prevents this within a transaction |

---

## 7. What We'd Do Next

Future work to make AURA_SQL a production-ready system:

1. **Vacuum / Garbage Collection**
   - Dead row versions accumulate; need background cleanup

2. **Cost-Based Optimizer**
   - Currently uses simple index vs. scan decision; could use statistics

3. **WAL / Crash Recovery**
   - Write-ahead logging for durability

4. **More SQL Features**
   - Aggregate functions (COUNT, SUM, AVG)
   - GROUP BY, ORDER BY, LIMIT
   - Subqueries

5. **Better Join Strategies**
   - Hash join, merge join for larger datasets

6. **Authentication / Authorization**
   - Basic user management and permissions

---

## 8. Performance

B-tree indexing provides significant performance improvements:

| Access Path | Latency (ns/op) | Rows |
|-------------|-----------------|------|
| Full Table Scan | 10,956,391 | 100,000 |
| B-Tree Index Seek | 182,301 | 1 |

---

## 9. Team

This project was built by a team of three as a systems project to understand how databases actually work beneath the SQL.

| Role | Team Member |
|------|-------------|
| Storage Engine (Heap Files, Buffer Pool, B-tree, MVCC) | Aniketh |
| Parser (SQL → AST) | Varun |
| Executor (Operator Model, Session, REPL) | Tejus |

---

*Last Updated: June 2026*
```

---

## How to Save This File

1. **Copy the entire content above** (from `# AURA_SQL — Design Document` to the end)

2. **Create the `docs/` directory** if it doesn't exist:
   ```bash
   mkdir -p docs
   ```

3. **Create the file**:
   ```bash
   # On Linux/Mac
   cat > docs/DESIGN.md
   # Then paste the content and press Ctrl+D

   # Or use your preferred text editor
   nano docs/DESIGN.md
   # or
   code docs/DESIGN.md
   ```

4. **Or on Windows (PowerShell)**:
   ```powershell
   New-Item -ItemType Directory -Force docs
   # Then open docs/DESIGN.md in your editor
   notepad docs/DESIGN.md
   ```

5. **Add to git**:
   ```bash
   git add docs/DESIGN.md
   git commit -m "docs: add comprehensive design document"
   git push
   ```
