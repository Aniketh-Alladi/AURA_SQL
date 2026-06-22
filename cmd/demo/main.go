package main

import (
	"fmt"
	"os"

	"aurasql/core"
	"aurasql/executor"
	"aurasql/parser"
	"aurasql/storage"
)

func main() {
	dir := "./demo_db"
	os.RemoveAll(dir)
	eng, _ := storage.New(dir)
	defer eng.Close()

	// 1. Setup Table
	txn, _ := eng.Begin()
	tryExec(eng, txn, "CREATE TABLE accounts (id INT, bal INT)")
	tryExec(eng, txn, "INSERT INTO accounts VALUES (1, 100)")
	txn.Commit()

	banner("DEMO: Dirty Read Prevention")
	t1, _ := eng.Begin()
	t2, _ := eng.Begin()

	fmt.Println("T1: UPDATE accounts SET bal = 0 WHERE id = 1 (not committed)")
	tryExec(eng, t1, "UPDATE accounts SET bal = 0 WHERE id = 1")

	fmt.Println("T2: SELECT bal FROM accounts WHERE id = 1")
	res, _ := execQuery(eng, t2, "SELECT bal FROM accounts WHERE id = 1")
	if len(res.Rows) != 1 {
		fmt.Printf("UNEXPECTED: T2 saw %d rows, expected exactly 1\n", len(res.Rows))
	} else {
		bal := res.Rows[0].Values[0]
		fmt.Printf("Result: T2 sees %v (expected 100 — dirty read prevented!)\n", bal)
	}

	t1.Rollback()
	t2.Commit()

	banner("DEMO: Lost Update Prevention")
	t3, _ := eng.Begin()
	t4, _ := eng.Begin()

	fmt.Println("T3: UPDATE accounts SET bal = 150 WHERE id = 1; COMMIT")
	tryExec(eng, t3, "UPDATE accounts SET bal = 150 WHERE id = 1")
	t3.Commit()

	fmt.Println("T4: UPDATE accounts SET bal = 200 WHERE id = 1")
	err := tryExec(eng, t4, "UPDATE accounts SET bal = 200 WHERE id = 1")
	if err != nil {
		fmt.Printf("Result: %v (write conflict detected — lost update prevented!)\n", err)
	} else {
		fmt.Println("Result: NO conflict detected — lost update NOT prevented (see note below)")
	}
	t4.Rollback()
}

func tryExec(eng core.StorageEngine, txn core.Txn, sql string) error {
	stmt, _ := parser.Parse(sql)
	_, err := executor.Execute(eng, txn, stmt)
	return err
}

func execQuery(eng core.StorageEngine, txn core.Txn, sql string) (core.Result, error) {
	stmt, _ := parser.Parse(sql)
	return executor.Execute(eng, txn, stmt)
}

func banner(s string) { fmt.Printf("\n=== %s ===\n", s) }
