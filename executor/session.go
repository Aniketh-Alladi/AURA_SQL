package executor

import (
	"errors"
	"fmt"

	"aurasql/core"
)

// ErrAlreadyInTxn is returned when BEGIN is called while already in a transaction.
var ErrAlreadyInTxn = errors.New("already in a transaction")

// ErrNoTxn is returned when COMMIT or ROLLBACK is called without an active transaction.
var ErrNoTxn = errors.New("no active transaction")

// Session manages the transaction lifecycle for a database connection.
type Session struct {
	eng core.StorageEngine
	cur core.Txn // nil = autocommit mode (no open transaction)
}

// NewSession creates a new session with the given storage engine.
func NewSession(eng core.StorageEngine) *Session {
	return &Session{eng: eng}
}

// Engine returns the underlying storage engine.
func (s *Session) Engine() core.StorageEngine {
	return s.eng
}

// ListTables returns all table names from the underlying engine.
func (s *Session) ListTables() []string {
	return s.eng.ListTables()
}

// GetSchema returns the schema for a table from the underlying engine.
func (s *Session) GetSchema(name string) (core.Schema, bool) {
	return s.eng.GetSchema(name)
}

// Exec executes a statement within the session's transaction context.
func (s *Session) Exec(stmt core.Statement) (core.Result, error) {
	switch stmt.(type) {
	case *core.BeginStmt:
		if s.cur != nil {
			return core.Result{}, ErrAlreadyInTxn
		}
		tx, err := s.eng.Begin()
		if err != nil {
			return core.Result{}, fmt.Errorf("begin transaction: %w", err)
		}
		s.cur = tx
		return core.Result{}, nil

	case *core.CommitStmt:
		if s.cur == nil {
			return core.Result{}, ErrNoTxn
		}
		err := s.cur.Commit()
		s.cur = nil
		if err != nil {
			return core.Result{}, fmt.Errorf("commit transaction: %w", err)
		}
		return core.Result{}, nil

	case *core.RollbackStmt:
		if s.cur == nil {
			return core.Result{}, ErrNoTxn
		}
		err := s.cur.Rollback()
		s.cur = nil
		if err != nil {
			return core.Result{}, fmt.Errorf("rollback transaction: %w", err)
		}
		return core.Result{}, nil

	default:
		// Explicit transaction: run in s.cur, don't auto-commit
		if s.cur != nil {
			res, err := Execute(s.eng, s.cur, stmt)
			if isWriteConflict(err) {
				// Transaction is dead - rollback and clear
				s.cur.Rollback()
				s.cur = nil
				return res, fmt.Errorf("write conflict: %w", err)
			}
			return res, err
		}

		// Autocommit: one statement = one transaction
		tx, err := s.eng.Begin()
		if err != nil {
			return core.Result{}, fmt.Errorf("begin autocommit transaction: %w", err)
		}

		res, err := Execute(s.eng, tx, stmt)
		if err != nil {
			tx.Rollback()
			return res, fmt.Errorf("execute in autocommit: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return res, fmt.Errorf("commit autocommit transaction: %w", err)
		}
		return res, nil
	}
}

// isWriteConflict checks if the error is a write conflict from the storage engine.
// This should be coordinated with Aniketh to match the exact error sentinel.
func isWriteConflict(err error) bool {
	if err == nil {
		return false
	}
	// Check for known write conflict error patterns
	errMsg := err.Error()
	return errors.Is(err, core.ErrWriteConflict) ||
		errors.Is(err, core.ErrSerializationConflict) ||
		errMsg == "write conflict" ||
		errMsg == "write-write conflict" ||
		errMsg == "row modified by a concurrent transaction"
}

// InTxn returns true if the session is currently in an explicit transaction.
func (s *Session) InTxn() bool {
	return s.cur != nil
}
