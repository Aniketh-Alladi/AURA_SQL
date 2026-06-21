package storage

import (
	"testing"

	"aurasql/core"
)

// TestIndexSurvivesLeafSplits inserts far more rows than fit in a single B-tree
// leaf, then reads them back through the index. Before the readPage fix this
// failed around the 254th insert with an EOF on a freshly-split page.
func TestIndexSurvivesLeafSplits(t *testing.T) {
	const (
		nRows   = 5000 // far past the old ~254-row failure point
		buckets = 100  // age = i % buckets
		probe   = 42   // key we look up
	)

	eng, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	txn, err := eng.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	schema := core.Schema{Columns: []core.Column{
		{Name: "id", Type: core.TypeInt},
		{Name: "age", Type: core.TypeInt},
	}}
	if err := eng.CreateTable(txn, "t", schema); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Build the index on the empty table so it is maintained incrementally.
	if err := eng.CreateIndex(txn, "t", "age"); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	for i := 1; i <= nRows; i++ {
		row := core.Row{Values: []core.Value{
			core.NewInt(int64(i)),
			core.NewInt(int64(i % buckets)),
		}}
		if _, err := eng.Insert(txn, "t", row); err != nil {
			t.Fatalf("Insert #%d failed: %v", i, err)
		}
	}
	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Read back via the index.
	it, err := eng.SeekIndex(txn, "t", "age", core.NewInt(probe))
	if err != nil {
		t.Fatalf("SeekIndex: %v", err)
	}
	defer it.Close()

	var got int
	for {
		_, row, ok, err := it.Next()
		if err != nil {
			t.Fatalf("index iterate: %v", err)
		}
		if !ok {
			break
		}
		if v := row.Values[1].Int; v != probe {
			t.Fatalf("index returned a row with age=%d, want %d", v, probe)
		}
		got++
	}

	want := nRows / buckets // 50
	if got != want {
		t.Fatalf("index lookup for age=%d returned %d rows, want %d", probe, got, want)
	}
}
