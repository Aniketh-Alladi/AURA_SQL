// Package parser turns SQL text into a core.Statement (the AST).
//
// Owned by: Varun (track 2).
//
// Expected entry point (part of the Phase 0 contract — keep this signature):
//
//	func Parse(sql string) (core.Statement, error)
//
// Build against the core package only. Test by parsing SQL strings and checking
// the AST that comes back; no storage or executor needed.
package parser
