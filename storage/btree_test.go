package storage

import (
	"testing"

	"aurasql/core"
)

// TestBTreeBasic tests the B-tree index with index created AFTER inserting rows
// This tests the bulk loading functionality of CreateIndex
func TestBTreeBasic(t *testing.T) {
	// Create temp dir
	dir := t.TempDir()
	engine, err := New(dir)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer engine.Close()

	// Create table
	schema := core.Schema{
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}

	txn, _ := engine.Begin()
	if err := engine.CreateTable(txn, "users", schema); err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	// Insert some rows
	rows := []core.Row{
		{Values: []core.Value{core.NewInt(1), core.NewText("Alice")}},
		{Values: []core.Value{core.NewInt(2), core.NewText("Bob")}},
		{Values: []core.Value{core.NewInt(3), core.NewText("Charlie")}},
		{Values: []core.Value{core.NewInt(5), core.NewText("Eve")}},
	}

	for _, row := range rows {
		if _, err := engine.Insert(txn, "users", row); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Create index on id (should bulk load existing rows)
	if err := engine.CreateIndex(txn, "users", "id"); err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	// Check HasIndex
	if !engine.HasIndex("users", "id") {
		t.Error("HasIndex returned false for existing index")
	}

	// Verify root page exists in catalog
	meta, err := engine.catalog.GetTable("users")
	if err != nil {
		t.Fatalf("Failed to get table: %v", err)
	}

	rootPage, exists := meta.IndexRoots["id"]
	if !exists {
		t.Fatal("Index root not set in catalog")
	}
	t.Logf("Root page ID: %d", rootPage)

	// Try to fetch the root page
	node, err := engine.fetchNode(rootPage)
	if err != nil {
		t.Fatalf("Failed to fetch root node: %v", err)
	}
	t.Logf("Root node: type=%d, numKeys=%d, nextLeaf=%d",
		node.NodeType, node.NumKeys, node.NextLeaf)

	// Test SeekIndex for key that exists
	iter, err := engine.SeekIndex(txn, "users", "id", core.NewInt(2))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	found := false
	for {
		id, row, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		if id != 0 {
			found = true
			if row.Values[0].Int != 2 {
				t.Errorf("Expected id=2, got %d", row.Values[0].Int)
			}
			if row.Values[1].Str != "Bob" {
				t.Errorf("Expected name=Bob, got %s", row.Values[1].Str)
			}
		}
	}
	if !found {
		t.Error("SeekIndex did not find row with id=2")
	}

	// Test SeekIndex for key that doesn't exist
	iter, err = engine.SeekIndex(txn, "users", "id", core.NewInt(4))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	count := 0
	for {
		_, _, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		count++
	}
	if count != 0 {
		t.Errorf("SeekIndex found %d rows for non-existent id=4, expected 0", count)
	}
}

// TestBTreeIndexBeforeInsert tests the B-tree with index created BEFORE inserting rows
// This tests that Insert properly updates the index
func TestBTreeIndexBeforeInsert(t *testing.T) {
	// Create temp dir
	dir := t.TempDir()
	engine, err := New(dir)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer engine.Close()

	// Create table
	schema := core.Schema{
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}

	txn, _ := engine.Begin()
	if err := engine.CreateTable(txn, "users", schema); err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	// Create index FIRST (before inserting rows)
	if err := engine.CreateIndex(txn, "users", "id"); err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	// Check HasIndex
	if !engine.HasIndex("users", "id") {
		t.Error("HasIndex returned false for existing index")
	}

	// Now insert rows (they'll be automatically indexed)
	rows := []core.Row{
		{Values: []core.Value{core.NewInt(1), core.NewText("Alice")}},
		{Values: []core.Value{core.NewInt(2), core.NewText("Bob")}},
		{Values: []core.Value{core.NewInt(3), core.NewText("Charlie")}},
		{Values: []core.Value{core.NewInt(5), core.NewText("Eve")}},
	}

	for _, row := range rows {
		if _, err := engine.Insert(txn, "users", row); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Test SeekIndex for key that exists
	iter, err := engine.SeekIndex(txn, "users", "id", core.NewInt(2))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	found := false
	for {
		id, row, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		if id != 0 {
			found = true
			if row.Values[0].Int != 2 {
				t.Errorf("Expected id=2, got %d", row.Values[0].Int)
			}
			if row.Values[1].Str != "Bob" {
				t.Errorf("Expected name=Bob, got %s", row.Values[1].Str)
			}
		}
	}
	if !found {
		t.Error("SeekIndex did not find row with id=2")
	}
}

// TestBTreeUpdate tests that index updates correctly when rows are updated
func TestBTreeUpdate(t *testing.T) {
	dir := t.TempDir()
	engine, err := New(dir)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer engine.Close()

	schema := core.Schema{
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}

	txn, _ := engine.Begin()
	if err := engine.CreateTable(txn, "users", schema); err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	// Create index first
	if err := engine.CreateIndex(txn, "users", "id"); err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	// Insert a row
	rowID, err := engine.Insert(txn, "users", core.Row{
		Values: []core.Value{core.NewInt(1), core.NewText("Alice")},
	})
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Update the row (change the indexed column value)
	if err := engine.Update(txn, "users", rowID, core.Row{
		Values: []core.Value{core.NewInt(10), core.NewText("Alice Updated")},
	}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Should NOT find id=1 anymore
	iter, err := engine.SeekIndex(txn, "users", "id", core.NewInt(1))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	count := 0
	for {
		_, _, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		count++
	}
	if count != 0 {
		t.Errorf("Found %d rows with id=1 after update, expected 0", count)
	}

	// Should find id=10
	iter, err = engine.SeekIndex(txn, "users", "id", core.NewInt(10))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	found := false
	for {
		_, row, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		found = true
		if row.Values[1].Str != "Alice Updated" {
			t.Errorf("Expected name=Alice Updated, got %s", row.Values[1].Str)
		}
	}
	if !found {
		t.Error("SeekIndex did not find row with id=10 after update")
	}
}

// TestBTreeDelete tests that index updates correctly when rows are deleted
func TestBTreeDelete(t *testing.T) {
	dir := t.TempDir()
	engine, err := New(dir)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer engine.Close()

	schema := core.Schema{
		Columns: []core.Column{
			{Name: "id", Type: core.TypeInt},
			{Name: "name", Type: core.TypeText},
		},
	}

	txn, _ := engine.Begin()
	if err := engine.CreateTable(txn, "users", schema); err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	// Create index first
	if err := engine.CreateIndex(txn, "users", "id"); err != nil {
		t.Fatalf("CreateIndex failed: %v", err)
	}

	// Insert a row
	rowID, err := engine.Insert(txn, "users", core.Row{
		Values: []core.Value{core.NewInt(1), core.NewText("Alice")},
	})
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Delete the row
	if err := engine.Delete(txn, "users", rowID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Should NOT find id=1 anymore
	iter, err := engine.SeekIndex(txn, "users", "id", core.NewInt(1))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	count := 0
	for {
		_, _, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		count++
	}
	if count != 0 {
		t.Errorf("Found %d rows with id=1 after delete, expected 0", count)
	}
}

// TestBTreePersistence tests that indexes survive engine restarts
func TestBTreePersistence(t *testing.T) {
	dir := t.TempDir()

	// First session: Create table, index, and insert data
	func() {
		engine, err := New(dir)
		if err != nil {
			t.Fatalf("Failed to create engine: %v", err)
		}
		defer engine.Close()

		schema := core.Schema{
			Columns: []core.Column{
				{Name: "id", Type: core.TypeInt},
				{Name: "name", Type: core.TypeText},
			},
		}

		txn, _ := engine.Begin()
		if err := engine.CreateTable(txn, "users", schema); err != nil {
			t.Fatalf("CreateTable failed: %v", err)
		}

		if err := engine.CreateIndex(txn, "users", "id"); err != nil {
			t.Fatalf("CreateIndex failed: %v", err)
		}

		if _, err := engine.Insert(txn, "users", core.Row{
			Values: []core.Value{core.NewInt(42), core.NewText("Answer")},
		}); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}()

	// Second session: Reopen engine and verify index still works
	engine, err := New(dir)
	if err != nil {
		t.Fatalf("Failed to reopen engine: %v", err)
	}
	defer engine.Close()

	txn, _ := engine.Begin()

	// Should find id=42
	iter, err := engine.SeekIndex(txn, "users", "id", core.NewInt(42))
	if err != nil {
		t.Fatalf("SeekIndex failed: %v", err)
	}
	defer iter.Close()

	found := false
	for {
		_, row, ok, err := iter.Next()
		if err != nil {
			t.Fatalf("Iterator Next failed: %v", err)
		}
		if !ok {
			break
		}
		found = true
		if row.Values[1].Str != "Answer" {
			t.Errorf("Expected name=Answer, got %s", row.Values[1].Str)
		}
	}
	if !found {
		t.Error("SeekIndex did not find persisted row with id=42")
	}
}
