package storage

import (
	"os"
	"reflect"
	"testing"

	"aurasql/core"
)

func TestHeapFilePersistence(t *testing.T) {
	filename := "test_heap.db"

	// Clean up any old test files before and after the test
	os.Remove(filename)
	defer os.Remove(filename)

	// 1. Open a new heap file
	hf, err := NewHeapFile(filename)
	if err != nil {
		t.Fatalf("Failed to create heap file: %v", err)
	}

	// NEW: Spin up the Buffer Pool cache and link it to the file
	pool1 := NewBufferPool(hf, 10) // Give it a capacity of 10 pages
	hf.SetBufferPool(pool1)

	// 2. Create a test row
	row := core.Row{
		Values: []core.Value{core.NewInt(44), core.NewText("Lewis Hamilton"), core.NewBool(true)},
	}
	rowBytes := Serialize(row)

	// 3. Insert the row into the cache
	id, err := hf.Insert(rowBytes)
	if err != nil {
		t.Fatalf("Failed to insert into heap file: %v", err)
	}

	// 4. NEW: We must flush the dirty cache to the hard drive before closing!
	if err := pool1.FlushAll(); err != nil {
		t.Fatalf("Failed to flush buffer pool: %v", err)
	}
	hf.Close()

	// 5. Reopen the file (simulating a database startup)
	hf2, err := NewHeapFile(filename)
	if err != nil {
		t.Fatalf("Failed to reopen heap file: %v", err)
	}
	defer hf2.Close()

	// NEW: Spin up a fresh cache for the newly opened file
	pool2 := NewBufferPool(hf2, 10)
	hf2.SetBufferPool(pool2)

	// 6. Retrieve the row through the new cache
	retrievedBytes, err := hf2.Get(id)
	if err != nil {
		t.Fatalf("Failed to get row from reopened file: %v", err)
	}

	retrievedRow := Deserialize(retrievedBytes)

	// 7. Verify the data perfectly matches
	if !reflect.DeepEqual(row, retrievedRow) {
		t.Errorf("Data loss detected!\nExpected: %v\nGot: %v", row, retrievedRow)
	}
}
