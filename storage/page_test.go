package storage

import (
	"reflect"
	"testing"

	"aurasql/core"
)

func TestPageRoundTrip(t *testing.T) {
	// 1. Create a fresh page
	page := NewPage()

	// 2. Create a test row with mixed data types
	originalRow := core.Row{
		Values: []core.Value{
			core.NewInt(42),
			core.NewText("Hello, AURA_SQL!"),
			core.NewBool(true),
		},
	}

	// 3. Serialize and Insert
	rowBytes := Serialize(originalRow)
	slotIndex, err := page.Insert(rowBytes)
	if err != nil {
		t.Fatalf("Failed to insert row: %v", err)
	}

	// 4. Retrieve and Deserialize
	retrievedBytes, err := page.Get(slotIndex)
	if err != nil {
		t.Fatalf("Failed to get row at slot %d: %v", slotIndex, err)
	}

	retrievedRow := Deserialize(retrievedBytes)

	// 5. Compare (they should be exactly the same)
	if !reflect.DeepEqual(originalRow, retrievedRow) {
		t.Errorf("Mismatch!\nExpected: %v\nGot: %v", originalRow, retrievedRow)
	}
}

func TestPageDelete(t *testing.T) {
	page := NewPage()
	row := core.Row{Values: []core.Value{core.NewInt(100)}}

	slotIndex, _ := page.Insert(Serialize(row))

	// Delete the row
	err := page.Delete(slotIndex)
	if err != nil {
		t.Fatalf("Failed to delete row: %v", err)
	}

	// Try to get it back; it should have a length of 0 now
	retrievedBytes, _ := page.Get(slotIndex)
	if len(retrievedBytes) != 0 {
		t.Errorf("Expected deleted row to return 0 bytes, got %d bytes", len(retrievedBytes))
	}
}
