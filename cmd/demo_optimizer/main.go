package main

import (
	"fmt"
	"time"

	"aurasql/core"
	"aurasql/executor"
	"aurasql/parser"
	"aurasql/storage"
)

func main() {
	fmt.Println("=== AURA_SQL Cost-Based Optimizer Demo ===")

	// Setup database
	eng, _ := storage.New("./demo_opt_data")
	defer eng.Close()
	sess := executor.NewSession(eng)

	// Create tables
	fmt.Println("Creating tables...")
	exec(sess, "CREATE TABLE customers (id INT, name TEXT, city TEXT)")
	exec(sess, "CREATE TABLE orders (id INT, customer_id INT, product TEXT, amount INT)")
	exec(sess, "CREATE TABLE shipments (id INT, order_id INT, carrier TEXT, status TEXT)")

	// Insert data
	fmt.Println("Inserting data...")
	for i := 1; i <= 100; i++ {
		exec(sess, fmt.Sprintf("INSERT INTO customers VALUES (%d, 'Customer%d', 'City%d')", i, i, i%10))
		exec(sess, fmt.Sprintf("INSERT INTO orders VALUES (%d, %d, 'Product%d', %d)", i, i, i%20, i*10))
		exec(sess, fmt.Sprintf("INSERT INTO shipments VALUES (%d, %d, 'Carrier%d', 'Shipped')", i, i, i%5))
	}

	// Run ANALYZE to collect stats
	fmt.Println("Running ANALYZE...")
	exec(sess, "ANALYZE customers")
	exec(sess, "ANALYZE orders")
	exec(sess, "ANALYZE shipments")

	// Query with inefficient order
	query := `
        SELECT customers.name, orders.product, shipments.carrier
        FROM customers
        JOIN orders ON customers.id = orders.customer_id
        JOIN shipments ON orders.id = shipments.order_id
        WHERE customers.id < 50
    `

	fmt.Println("\n=== Query ===")
	fmt.Println(query)

	// EXPLAIN the query
	fmt.Println("\n=== EXPLAIN (Optimized Plan) ===")
	explainQuery := "EXPLAIN " + query
	res := exec(sess, explainQuery)
	fmt.Println(res.Rows[0].Values[0].Str)

	// Time the query
	fmt.Println("\n=== Executing Query ===")
	start := time.Now()
	res = exec(sess, query)
	elapsed := time.Since(start)

	fmt.Printf("Query returned %d rows in %v\n", len(res.Rows), elapsed)

	fmt.Println("\n✅ Demo complete!")
}

func exec(sess *executor.Session, sql string) core.Result {
	stmt, err := parser.Parse(sql)
	if err != nil {
		panic(err)
	}
	res, err := sess.Exec(stmt)
	if err != nil {
		panic(err)
	}
	return res
}
