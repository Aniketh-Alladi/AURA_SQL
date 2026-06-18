// Package executor runs a core.Statement against a core.StorageEngine and
// returns a core.Result.
//
// Owned by: Tejus (track 3).
//
// Expected entry point (part of the Phase 0 contract — keep this signature):
//
//	func Execute(eng core.StorageEngine, txn core.Txn, stmt core.Statement) (core.Result, error)
//
// Build against the core package only, and test against memstore.New() so you
// have a working backend long before the real storage engine exists.
package executor
