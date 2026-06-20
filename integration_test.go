package tests

import (
    "testing"
    "aurasql/storage"
    "aurasql/core"
    "os"
)

func TestFullIntegration(t *testing.T) {
    // 1. Setup a clean database directory
    dataDir := "./test_db"
    os.RemoveAll(dataDir) 
    defer os.RemoveAll(dataDir)

    // 2. Initialize the Engine (your code)
    engine, err := storage.New(dataDir)
    if err != nil {
        t.Fatal(err)
    }

    // 3. Simulate DDL (Create Table + Create Index)

    txn, _ := engine.Begin()
    schema := core.Schema{Columns: []core.Column{{Name: "id"}, {Name: "age"}}}
    
    // Explicitly check for errors
    if err := engine.CreateTable(txn, "users", schema); err != nil {
        t.Fatal("CreateTable failed:", err)
    }
    
    // Add this print to debug if the index creation is hitting the error
    if err := engine.CreateIndex(txn, "users", "age"); err != nil {
        t.Fatal("CreateIndex failed:", err)
    }
    
    // Ensure the catalog is flushed to disk
    if err := engine.Close(); err != nil {
        t.Fatal("Engine close failed:", err)
    }
    
    // Re-open the engine to ensure it loads the catalog from disk properly
    engine, err = storage.New(dataDir)
    txn, _ = engine.Begin()
    engine.CreateTable(txn, "users", schema)
    engine.CreateIndex(txn, "users", "age")

    // 4. Simulate DML (Insert)
    row := core.Row{Values: []core.Value{core.NewInt(1), core.NewInt(25)}}
    engine.Insert(txn, "users", row)

    // 5. Test the Index (The B-Tree logic you built!)
    // If SeekIndex works, we should be able to find the row by age 25
    iter, err := engine.SeekIndex(txn, "users", "age", core.NewInt(25))
    if err != nil {
        t.Fatal("Index lookup failed:", err)
    }

    _, _, found, _ := iter.Next()
    if !found {
        t.Fatal("Expected to find row via B-Tree index, but index lookup returned nothing!")
    }
}