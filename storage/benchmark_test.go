package storage

import (
	"aurasql/core"
	"os"
	"testing"
)

func BenchmarkScanVsIndex(b *testing.B) {
	dir, _ := os.MkdirTemp("", "bench-")
	defer os.RemoveAll(dir)
	eng, _ := New(dir)
	
	// Seed 100k rows
	txn, _ := eng.Begin()
	eng.CreateTable(txn, "bench", core.Schema{Columns: []core.Column{{Name: "id", Type: core.TypeInt}}})
	eng.CreateIndex(txn, "bench", "id")
	for i := 0; i < 100000; i++ {
		eng.Insert(txn, "bench", core.Row{Values: []core.Value{core.NewInt(int64(i))}})
	}
	txn.Commit()

	// Benchmark Full Scan
	b.Run("FullScan", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			t, _ := eng.Begin()
			it, _ := eng.Scan(t, "bench")
			for {
				_, _, ok, _ := it.Next()
				if !ok { break }
			}
			t.Commit()
		}
	})

	// Benchmark Index Seek
	b.Run("IndexSeek", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			t, _ := eng.Begin()
			it, _ := eng.SeekIndex(t, "bench", "id", core.NewInt(50000))
			it.Next()
			t.Commit()
		}
	})
}