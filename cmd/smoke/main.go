// Command smoke is a throwaway sanity check that the core types and the storage
// contract fit together. It builds a tiny AST by hand, runs CREATE / INSERT /
// SELECT-style operations against the in-memory engine, and prints the rows. It
// is not part of the real system — it just proves the Phase 0 contract is sound.
package main

import (
	"fmt"
	"log"

	"aurasql/core"
	"aurasql/memstore"
)

func main() {
	eng := memstore.New()

	tx, err := eng.Begin()
	if err != nil {
		log.Fatal(err)
	}

	// CREATE TABLE users (id INT, name TEXT, active BOOL)
	schema := core.Schema{Columns: []core.Column{
		{Name: "id", Type: core.TypeInt},
		{Name: "name", Type: core.TypeText},
		{Name: "active", Type: core.TypeBool},
	}}
	if err := eng.CreateTable(tx, "users", schema); err != nil {
		log.Fatal(err)
	}

	// INSERT two rows.
	rows := []core.Row{
		{Values: []core.Value{core.NewInt(1), core.NewText("varun"), core.NewBool(true)}},
		{Values: []core.Value{core.NewInt(2), core.NewText("tejus"), core.NewBool(false)}},
	}
	for _, r := range rows {
		if _, err := eng.Insert(tx, "users", r); err != nil {
			log.Fatal(err)
		}
	}

	// A hand-built WHERE predicate: active = true
	where := &core.BinaryExpr{
		Op:    core.OpEq,
		Left:  &core.ColumnRef{Name: "active"},
		Right: &core.Literal{Value: core.NewBool(true)},
	}
	activeIdx := schema.ColumnIndex(where.Left.(*core.ColumnRef).Name)

	// Scan and apply the predicate by hand (the real executor will do this).
	it, err := eng.Scan(tx, "users")
	if err != nil {
		log.Fatal(err)
	}
	defer it.Close()

	fmt.Println("rows where active = true:")
	for {
		_, row, ok, err := it.Next()
		if err != nil {
			log.Fatal(err)
		}
		if !ok {
			break
		}
		match, err := row.Values[activeIdx].Compare(core.NewBool(true))
		if err != nil {
			log.Fatal(err)
		}
		if match == 0 {
			fmt.Printf("  id=%s name=%s active=%s\n",
				row.Values[0], row.Values[1], row.Values[2])
		}
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}
