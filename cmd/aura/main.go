// Command aura is the interactive SQL shell (REPL) for AURA_SQL.
//
// It is the front door to the engine: it reads SQL from the user, pushes each
// statement through parser -> Session -> storage, and prints the result.
//
// Transaction model: The Session manages the transaction lifecycle. By default,
// each statement runs in autocommit mode. Explicit transactions are started
// with BEGIN, ended with COMMIT or ROLLBACK.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"aurasql/core"
	"aurasql/executor"
	"aurasql/memstore"
	"aurasql/parser"
	"aurasql/storage"
)

const version = "0.4.0"

const (
	promptMain = "aura> "
	promptCont = " ...> "      // shown while a statement is still being typed
	promptTxn  = "aura(txn)> " // shown when in an explicit transaction
)

// REPL drives one interactive session against a storage engine.
type REPL struct {
	session *executor.Session
	scanner *bufio.Scanner
	out     *bufio.Writer
	running bool
	buf     strings.Builder // accumulates a not-yet-terminated statement
}

// NewREPL creates a new REPL with the given storage engine.
func NewREPL(eng core.StorageEngine, in io.Reader, out io.Writer) *REPL {
	return &REPL{
		session: executor.NewSession(eng),
		scanner: bufio.NewScanner(in),
		out:     bufio.NewWriter(out),
		running: true,
	}
}

// Run starts the REPL main loop.
func (r *REPL) Run() error {
	fmt.Fprintf(r.out, "AURA_SQL v%s — type SQL terminated with ';'\n", version)
	fmt.Fprintln(r.out, "Meta-commands: .help, .tables, .schema <t>, .read <file>, .exit")
	fmt.Fprintln(r.out, "Transactions: BEGIN, COMMIT, ROLLBACK")
	r.out.Flush()

	for r.running {
		if r.buf.Len() == 0 {
			if r.session.InTxn() {
				fmt.Fprint(r.out, promptTxn)
			} else {
				fmt.Fprint(r.out, promptMain)
			}
		} else {
			fmt.Fprint(r.out, promptCont)
		}
		r.out.Flush()

		if !r.scanner.Scan() {
			break // EOF (Ctrl-D or end of piped input)
		}
		r.feedLine(r.scanner.Text())
		r.out.Flush()
	}
	r.out.Flush()
	return r.scanner.Err()
}

// feedLine processes one line of input: a meta-command (only at the start of a
// statement) or a fragment of SQL appended to the buffer.
func (r *REPL) feedLine(line string) {
	trimmed := strings.TrimSpace(line)

	// Meta-commands are only recognised when we are not mid-statement.
	if r.buf.Len() == 0 && strings.HasPrefix(trimmed, ".") {
		r.handleMeta(trimmed)
		return
	}
	if trimmed == "" && r.buf.Len() == 0 {
		return
	}

	r.buf.WriteString(line)
	r.buf.WriteString("\n")
	r.drainStatements()
}

// drainStatements executes every complete ';'-terminated statement currently in
// the buffer and leaves any trailing, not-yet-terminated fragment behind so the
// next line can continue it.
//
// Note: this splits on ';' naively, so a ';' inside a string literal would split
// a statement early. That is a known limitation kept simple for core scope.
func (r *REPL) drainStatements() {
	rest := r.buf.String()
	for {
		i := strings.IndexByte(rest, ';')
		if i < 0 {
			break
		}
		stmt := strings.TrimSpace(rest[:i])
		rest = rest[i+1:]
		if stmt != "" {
			r.runOne(stmt)
		}
	}
	// Keep only a genuine partial statement. A pure-whitespace remainder (e.g.
	// the newline after a ';') must NOT count as "mid-statement", or the next
	// line's meta-command would be mis-read as SQL.
	r.buf.Reset()
	if strings.TrimSpace(rest) != "" {
		r.buf.WriteString(rest)
	}
}

// runOne parses and executes a single statement through the Session.
// A panic from any layer is recovered so one bad statement never kills the REPL.
func (r *REPL) runOne(sql string) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(r.out, "Error: internal panic: %v\n", p)
		}
	}()

	stmt, err := parser.Parse(sql)
	if err != nil {
		fmt.Fprintf(r.out, "Error: %v\n", err)
		return
	}

	res, err := r.session.Exec(stmt)
	if err != nil {
		// Handle transaction-specific errors with user-friendly messages
		switch err {
		case executor.ErrAlreadyInTxn:
			fmt.Fprintln(r.out, "Error: already in a transaction (use COMMIT or ROLLBACK first)")
		case executor.ErrNoTxn:
			fmt.Fprintln(r.out, "Error: no active transaction (use BEGIN first)")
		default:
			// Check for write conflict
			if strings.Contains(err.Error(), "write conflict") ||
				strings.Contains(err.Error(), "serialization conflict") ||
				strings.Contains(err.Error(), "row modified by a concurrent transaction") {
				fmt.Fprintf(r.out, "Error: WRITE CONFLICT - transaction aborted\n")
				fmt.Fprintf(r.out, "       %v\n", err)
				// The Session already cleared the transaction on conflict
				if r.session.InTxn() {
					fmt.Fprintln(r.out, "       (transaction rolled back automatically)")
				}
			} else {
				fmt.Fprintf(r.out, "Error: %v\n", err)
			}
		}
		return
	}

	fmt.Fprint(r.out, formatResult(stmt, res))
}

// ---- meta-commands ----

func (r *REPL) handleMeta(line string) {
	parts := strings.Fields(line)
	switch parts[0] {
	case ".exit", ".quit":
		// If in a transaction, warn the user
		if r.session.InTxn() {
			fmt.Fprintln(r.out, "Warning: you are in an active transaction. Use COMMIT or ROLLBACK first.")
			fmt.Fprintln(r.out, "Type .exit again to force exit (transaction will be rolled back).")
			// Simple approach: just exit (the session will be discarded)
		}
		r.running = false
	case ".help":
		fmt.Fprint(r.out, `Commands:
  .help            show this help
  .tables          list tables
  .schema <table>  show a table's columns
  .read <file>     run SQL statements from a file
  .exit            quit

SQL Statements:
  CREATE TABLE, INSERT, SELECT, UPDATE, DELETE
  BEGIN, COMMIT, ROLLBACK
  CREATE INDEX

Notes:
  - Statements must end with a semicolon (;)
  - Multi-line input is supported
  - Transactions: BEGIN ... COMMIT / ROLLBACK
  - Write conflicts abort the transaction automatically
`)
	case ".tables":
		names := r.session.ListTables()
		if len(names) == 0 {
			fmt.Fprintln(r.out, "(no tables)")
			return
		}
		fmt.Fprintln(r.out, "Tables:")
		for _, n := range names {
			fmt.Fprintf(r.out, "  %s\n", n)
		}
	case ".schema":
		if len(parts) < 2 {
			fmt.Fprintln(r.out, "usage: .schema <table>")
			return
		}
		r.printSchema(parts[1])
	case ".read":
		if len(parts) < 2 {
			fmt.Fprintln(r.out, "usage: .read <file>")
			return
		}
		r.readFile(parts[1])
	default:
		fmt.Fprintf(r.out, "unknown command %q (try .help)\n", parts[0])
	}
}

func (r *REPL) printSchema(table string) {
	schema, ok := r.session.GetSchema(table)
	if !ok {
		fmt.Fprintf(r.out, "Error: no such table: %s\n", table)
		return
	}
	fmt.Fprintf(r.out, "CREATE TABLE %s (\n", table)
	for i, c := range schema.Columns {
		comma := ","
		if i == len(schema.Columns)-1 {
			comma = ""
		}
		fmt.Fprintf(r.out, "  %s %s%s\n", c.Name, c.Type, comma)
	}
	fmt.Fprintf(r.out, ");\n")
}

// readFile runs every statement in a .sql file in order, continuing past errors
// so a single bad line does not abort the script.
func (r *REPL) readFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(r.out, "Error: %v\n", err)
		return
	}
	var ran int
	rest := string(data)
	for {
		i := strings.IndexByte(rest, ';')
		if i < 0 {
			break
		}
		stmt := strings.TrimSpace(rest[:i])
		rest = rest[i+1:]
		if stmt == "" {
			continue
		}
		ran++
		r.runOne(stmt)
	}
	if tail := strings.TrimSpace(rest); tail != "" {
		fmt.Fprintf(r.out, "warning: ignoring trailing input without ';': %q\n", tail)
	}
	fmt.Fprintf(r.out, "(%d statement(s) from %s)\n", ran, path)
}

// ---- output formatting ----

// formatResult renders a result based on the statement kind so writes get an
// "OK" line and SELECTs get an aligned table (never "0 rows affected" for a
// SELECT that simply matched nothing).
func formatResult(stmt core.Statement, res core.Result) string {
	switch s := stmt.(type) {
	case *core.SelectStmt:
		if len(res.Rows) == 0 {
			return "(0 rows)\n"
		}
		return formatTable(res)
	case *core.CreateTableStmt:
		return fmt.Sprintf("OK, table %q created\n", s.Table)
	case *core.CreateIndexStmt:
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("%s_%s", s.Table, s.Column)
		}
		return fmt.Sprintf("OK, index %q on %s(%s) created\n", name, s.Table, s.Column)
	case *core.InsertStmt:
		return fmt.Sprintf("OK, %s\n", rows(res.RowsAffected, "inserted"))
	case *core.UpdateStmt:
		return fmt.Sprintf("OK, %s\n", rows(res.RowsAffected, "updated"))
	case *core.DeleteStmt:
		return fmt.Sprintf("OK, %s\n", rows(res.RowsAffected, "deleted"))
	case *core.BeginStmt:
		return "BEGIN\n"
	case *core.CommitStmt:
		return "COMMIT\n"
	case *core.RollbackStmt:
		return "ROLLBACK\n"
	default:
		if len(res.Schema.Columns) > 0 {
			return formatTable(res)
		}
		return fmt.Sprintf("OK, %s\n", rows(res.RowsAffected, "affected"))
	}
}

func rows(n int, verb string) string {
	if n == 1 {
		return fmt.Sprintf("1 row %s", verb)
	}
	return fmt.Sprintf("%d rows %s", n, verb)
}

// formatTable renders a SELECT result as an aligned ASCII table.
func formatTable(res core.Result) string {
	cols := res.Schema.Columns
	if len(cols) == 0 {
		return "(no columns)\n"
	}

	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c.Name)
	}
	cells := make([][]string, len(res.Rows))
	for r, row := range res.Rows {
		cells[r] = make([]string, len(cols))
		for i := range cols {
			var s string
			if i < len(row.Values) {
				s = row.Values[i].String()
			}
			cells[r][i] = s
			if len(s) > widths[i] {
				widths[i] = len(s)
			}
		}
	}

	var b strings.Builder
	sep := func() {
		b.WriteByte('+')
		for _, w := range widths {
			b.WriteString(strings.Repeat("-", w+2))
			b.WriteByte('+')
		}
		b.WriteByte('\n')
	}
	writeRow := func(vals []string) {
		b.WriteByte('|')
		for i, w := range widths {
			fmt.Fprintf(&b, " %-*s |", w, vals[i])
		}
		b.WriteByte('\n')
	}

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Name
	}
	sep()
	writeRow(header)
	sep()
	for _, row := range cells {
		writeRow(row)
	}
	sep()
	fmt.Fprintf(&b, "(%s)\n", rowsCount(len(res.Rows)))
	return b.String()
}

func rowsCount(n int) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

// ---- entry point ----

func main() {
	mem := flag.Bool("mem", false, "use the in-memory engine instead of on-disk storage")
	dir := flag.String("data", "./aura_data", "data directory for the on-disk engine")
	flag.Parse()

	var eng core.StorageEngine
	if *mem {
		eng = memstore.New()
		fmt.Println("Using in-memory engine (data will not be persisted)")
	} else {
		e, err := storage.New(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open storage at %s: %v\n", *dir, err)
			os.Exit(1)
		}
		defer e.Close()
		eng = e
		fmt.Printf("Using on-disk storage at %s\n", *dir)
	}

	if err := NewREPL(eng, os.Stdin, os.Stdout).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "repl error: %v\n", err)
		os.Exit(1)
	}
}
