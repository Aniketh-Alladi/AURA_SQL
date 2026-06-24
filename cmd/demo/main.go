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

	// --- NEW: Run Benchmark Seeding ---
	fmt.Println("Seeding benchmark data...")
	seedTxn, _ := eng.Begin()

	// Create tables first
	tryExec(eng, seedTxn, "CREATE TABLE customers (id INT, name TEXT)")
	tryExec(eng, seedTxn, "CREATE TABLE products (id INT, name TEXT, price INT)")
	tryExec(eng, seedTxn, "CREATE TABLE orders (id INT, customer_id INT, product_id INT)")

	seedData(eng, seedTxn)

	if err := seedTxn.Commit(); err != nil {
		fmt.Printf("Seeding failed: %v\n", err)
	} else {
		fmt.Println("Seeding complete and committed.")
	}

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

func seedData(eng core.StorageEngine, txn core.Txn) {
	// 1. Run CREATE TABLEs
	// 2. Run INSERTs (programmatically or by parsing the SQL above)
	// 3. Run ANALYZE for customers, products, and orders

	for i := 4; i <= 10000; i++ {
		customerID := (i % 3) + 1
		productID := 101 + (i % 2)
		eng.Insert(txn, "orders", core.Row{Values: []core.Value{core.NewInt(int64(i)), core.NewInt(int64(customerID)), core.NewInt(int64(productID))}})
	}

	eng.Analyze(txn, "customers")
	eng.Analyze(txn, "products")
	eng.Analyze(txn, "orders")

	stats, _ := eng.Stats("orders")
	fmt.Printf("Orders table analyzed: %d rows\n", stats.RowCount)

}
