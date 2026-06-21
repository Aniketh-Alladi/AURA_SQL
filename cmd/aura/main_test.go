package main

import (
	"strings"
	"testing"

	"aurasql/memstore"
)

// run drives a REPL against a fresh memstore with the given input script and
// returns everything it printed.
func run(t *testing.T, script string) string {
	t.Helper()
	var out strings.Builder
	r := NewREPL(memstore.New(), strings.NewReader(script), &out)
	if err := r.Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return out.String()
}

func TestBasicSession(t *testing.T) {
	out := run(t, `CREATE TABLE t (id INT, name TEXT);
INSERT INTO t VALUES (1, 'Alice');
SELECT * FROM t;
`)
	for _, want := range []string{"table \"t\" created", "1 row inserted", "Alice", "(1 row)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestMultiLineStatement(t *testing.T) {
	out := run(t, "CREATE TABLE t (id INT, name TEXT);\nINSERT INTO t VALUES (1, 'Alice');\nSELECT *\nFROM t\nWHERE id = 1;\n")
	if !strings.Contains(out, "Alice") || !strings.Contains(out, "(1 row)") {
		t.Errorf("multi-line statement not assembled correctly\n%s", out)
	}
}

func TestEmptySelectIsNotReportedAsAffectedRows(t *testing.T) {
	out := run(t, `CREATE TABLE t (id INT, name TEXT);
SELECT * FROM t WHERE id = 99;
`)
	if strings.Contains(out, "affected") {
		t.Errorf("empty SELECT should not say 'affected'\n%s", out)
	}
	if !strings.Contains(out, "(0 rows)") {
		t.Errorf("empty SELECT should report (0 rows)\n%s", out)
	}
}

func TestErrorRecoveryKeepsRunning(t *testing.T) {
	out := run(t, `SELECT * FROM nope;
CREATE TABLE t (id INT, name TEXT);
INSERT INTO t VALUES (1, 'Bob');
SELECT * FROM t;
`)
	if !strings.Contains(out, "Error:") {
		t.Errorf("expected an error line for the bad statement\n%s", out)
	}
	// The REPL must continue: the later statements still run.
	if !strings.Contains(out, "Bob") {
		t.Errorf("REPL did not continue after an error\n%s", out)
	}
}

func TestMetaCommandsAfterError(t *testing.T) {
	// Regression: a statement followed by a meta-command on the next line. The
	// leftover newline must not be treated as "mid-statement".
	out := run(t, `CREATE TABLE t (id INT, name TEXT);
SELECT * FROM nope;
.tables
.schema t
`)
	if !strings.Contains(out, "  t\n") {
		t.Errorf(".tables did not list table t\n%s", out)
	}
	if !strings.Contains(out, "id") || !strings.Contains(out, "TEXT") {
		t.Errorf(".schema did not print columns\n%s", out)
	}
}

func TestWriteMessages(t *testing.T) {
	out := run(t, `CREATE TABLE t (id INT, name TEXT);
INSERT INTO t VALUES (1, 'Alice');
INSERT INTO t VALUES (2, 'Bob');
UPDATE t SET name = 'Carol' WHERE id = 1;
DELETE FROM t WHERE id = 2;
`)
	for _, want := range []string{"1 row updated", "1 row deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
}
