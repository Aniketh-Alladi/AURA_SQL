// Command demo runs a fixed, scripted session that showcases the Phase 3
// feature: B-tree indexing. It runs the same equality query against a plain
// table (full scan) and an indexed table (SeekIndex) over identical data and
// reports the *measured* speed-up. It never asserts numbers it did not measure.
//
// An A/B over two tables with identical data is a clean comparison: same rows,
// same selectivity, the only difference is the access path (full Scan vs
// SeekIndex). The index is built on the empty table and maintained incrementally
// as rows are inserted.
//
// Current status: the full-scan baseline runs at full scale. The indexed half
// is gated on a storage B-tree bug — index inserts/reads fail with EOF once a
// leaf page splits (readPage ReadAt past end-of-file on a freshly-allocated
// page). When that is fixed this harness shows the real speed-up with no change.
// In Phase 4 it grows into the MVCC anomaly demo, the project's centerpiece.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"aurasql/core"
	"aurasql/executor"
	"aurasql/parser"
	"aurasql/storage"
)

const (
	rowCount   = 10000 // rows loaded into each table
	queryIters = 300   // repetitions per timed measurement (averages out noise)
	matchValue = 42    // WHERE age = 42; ages are i%100, so ~rowCount/100 matches
)

func main() {
	dir, err := os.MkdirTemp("", "aura-demo-")
	if err != nil {
		fail("create temp dir", err)
	}
	defer os.RemoveAll(dir)

	eng, err := storage.New(dir)
	if err != nil {
		fail("open storage", err)
	}
	defer eng.Close()

	banner("AURA_SQL — Phase 3 demo: B-tree index speed-up")

	query := fmt.Sprintf("SELECT * FROM %%s WHERE age = %d", matchValue)

	// --- baseline: full table scan, real scale (this path works today) ---
	fmt.Printf("Loading %d rows into a plain (un-indexed) table...\n", rowCount)
	loadPlain(eng, "plain", rowCount)
	plainQ := fmt.Sprintf(query, "plain")
	scanRows := runOnce(eng, plainQ)
	scanTime := timeQuery(eng, plainQ, queryIters)
	fmt.Printf("\nFull table scan (no index), %d rows:\n", rowCount)
	fmt.Printf("  matched %d rows · %s/query\n", scanRows, perQuery(scanTime, queryIters))

	// --- indexed path: show the win if storage can support it ---
	idxRows, idxTime, ok := tryIndexedPath(eng, "indexed", rowCount, fmt.Sprintf(query, "indexed"))
	if !ok {
		fmt.Printf("\nIndexed path unavailable: index build/read fails in storage\n")
		fmt.Printf("(B-tree readPage hits EOF on a freshly-split page). The harness is\n")
		fmt.Printf("ready; rerun after the B-tree fix to see the speed-up.\n")
		banner("demo complete (baseline only)")
		return
	}

	fmt.Printf("\nB-tree index on age (SeekIndex path):\n")
	fmt.Printf("  matched %d rows · %s/query\n", idxRows, perQuery(idxTime, queryIters))
	if scanRows != idxRows {
		fail("correctness", fmt.Errorf("row count differs: scan=%d index=%d", scanRows, idxRows))
	}
	fmt.Printf("\nResult: index returns the same %d rows ", idxRows)
	if idxTime > 0 {
		fmt.Printf("and is %.1fx faster on this query.\n", float64(scanTime)/float64(idxTime))
	} else {
		fmt.Println("(indexed time below timer resolution).")
	}
	banner("demo complete")
}

// loadPlain creates an un-indexed table and loads exactly n rows.
func loadPlain(eng core.StorageEngine, name string, n int) {
	txn, err := eng.Begin()
	if err != nil {
		fail("begin seed", err)
	}
	execTxn(eng, txn, fmt.Sprintf("CREATE TABLE %s (id INT, name TEXT, age INT)", name))
	for i := 1; i <= n; i++ {
		execTxn(eng, txn, insertSQL(name, i))
	}
	if err := txn.Commit(); err != nil {
		fail("commit seed", err)
	}
}

// tryIndexedPath builds an indexed table and runs the query against it. It
// returns ok=false (rather than aborting) if any index insert or the indexed
// read fails, so the demo can degrade to the baseline instead of crashing.
func tryIndexedPath(eng core.StorageEngine, name string, n int, query string) (rows int, elapsed time.Duration, ok bool) {
	txn, err := eng.Begin()
	if err != nil {
		return 0, 0, false
	}
	if err := tryExec(eng, txn, fmt.Sprintf("CREATE TABLE %s (id INT, name TEXT, age INT)", name)); err != nil {
		return 0, 0, false
	}
	if err := tryExec(eng, txn, fmt.Sprintf("CREATE INDEX idx_%s_age ON %s (age)", name, name)); err != nil {
		return 0, 0, false
	}
	for i := 1; i <= n; i++ {
		if err := tryExec(eng, txn, insertSQL(name, i)); err != nil {
			return 0, 0, false
		}
	}
	if err := txn.Commit(); err != nil {
		return 0, 0, false
	}
	if !eng.HasIndex(name, "age") {
		return 0, 0, false
	}

	stmt, err := parser.Parse(query)
	if err != nil {
		return 0, 0, false
	}
	qtxn, _ := eng.Begin()
	res, err := executor.Execute(eng, qtxn, stmt)
	_ = qtxn.Commit()
	if err != nil {
		return 0, 0, false
	}
	return len(res.Rows), timeQuery(eng, query, queryIters), true
}

func insertSQL(table string, i int) string {
	return fmt.Sprintf("INSERT INTO %s VALUES (%d, 'User%d', %d)", table, i, i, i%100)
}

// runOnce executes a query once and returns the number of rows returned.
func runOnce(eng core.StorageEngine, sql string) int {
	return len(mustQuery(eng, sql).Rows)
}

// timeQuery runs sql iters times and returns the total elapsed time.
func timeQuery(eng core.StorageEngine, sql string, iters int) time.Duration {
	stmt, err := parser.Parse(sql)
	if err != nil {
		fail("parse "+sql, err)
	}
	start := time.Now()
	for i := 0; i < iters; i++ {
		txn, err := eng.Begin()
		if err != nil {
			fail("begin query", err)
		}
		if _, err := executor.Execute(eng, txn, stmt); err != nil {
			fail("execute query", err)
		}
		_ = txn.Commit()
	}
	return time.Since(start)
}

func mustQuery(eng core.StorageEngine, sql string) core.Result {
	stmt, err := parser.Parse(sql)
	if err != nil {
		fail("parse "+sql, err)
	}
	txn, err := eng.Begin()
	if err != nil {
		fail("begin", err)
	}
	res, err := executor.Execute(eng, txn, stmt)
	if err != nil {
		fail("execute "+sql, err)
	}
	_ = txn.Commit()
	return res
}

func execTxn(eng core.StorageEngine, txn core.Txn, sql string) {
	if err := tryExec(eng, txn, sql); err != nil {
		fail("execute "+sql, err)
	}
}

// tryExec runs one statement and returns its error instead of aborting.
func tryExec(eng core.StorageEngine, txn core.Txn, sql string) error {
	stmt, err := parser.Parse(sql)
	if err != nil {
		return err
	}
	_, err = executor.Execute(eng, txn, stmt)
	return err
}

func perQuery(total time.Duration, iters int) time.Duration {
	return total / time.Duration(iters)
}

func banner(s string) {
	line := strings.Repeat("=", len(s)+4)
	fmt.Printf("%s\n  %s\n%s\n", line, s, line)
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "demo failed at %s: %v\n", what, err)
	os.Exit(1)
}
