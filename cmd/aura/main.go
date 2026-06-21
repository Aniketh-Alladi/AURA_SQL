// Command aura is the interactive SQL shell (REPL) for AURA_SQL.
//
// It is the front door to the engine: it reads SQL from the user, pushes each
// statement through parser -> executor -> storage, and prints the result. It
// orchestrates the existing packages and implements no parsing, execution, or
// storage logic of its own.
//
// Transaction model: AUTOCOMMIT. Each top-level statement runs in its own
// transaction (Begin -> Execute -> Commit, or Rollback on error). This keeps a
// failed statement from poisoning later ones and, unlike a single long-lived
// session transaction, lets work actually persist on the real on-disk engine.
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

const version = "0.3.0"

const (
	promptMain = "aura> "
	promptCont = " ...> " // shown while a statement is still being typed
)

// REPL drives one interactive session against a storage engine.
type REPL struct {
	engine  core.StorageEngine
	scanner *bufio.Scanner
	out     *bufio.Writer
	running bool
	buf     strings.Builder // accumulates a not-yet-terminated statement
}

func NewREPL(engine core.StorageEngine, in io.Reader, out io.Writer) *REPL {
	return &REPL{
		engine:  engine,
		scanner: bufio.NewScanner(in),
		out:     bufio.NewWriter(out),
		running: true,
	}
}

func (r *REPL) Run() error {
	fmt.Fprintf(r.out, "AURA_SQL v%s — type SQL terminated with ';'\n", version)
	fmt.Fprintln(r.out, "Meta-commands: .help, .tables, .schema <t>, .read <file>, .exit")
	r.out.Flush()

	for r.running {
		if r.buf.Len() == 0 {
			fmt.Fprint(r.out, promptMain)
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

// runOne parses and executes a single statement in its own transaction, printing
// the result or a clear error. A panic from any layer is recovered so one bad
// statement never kills the REPL.
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

	txn, err := r.engine.Begin()
	if err != nil {
		fmt.Fprintf(r.out, "Error: begin: %v\n", err)
		return
	}

	res, err := executor.Execute(r.engine, txn, stmt)
	if err != nil {
		_ = txn.Rollback()
		fmt.Fprintf(r.out, "Error: %v\n", err)
		return
	}
	if err := txn.Commit(); err != nil {
		fmt.Fprintf(r.out, "Error: commit: %v\n", err)
		return
	}

	fmt.Fprint(r.out, formatResult(stmt, res))
}

// ---- meta-commands ----

func (r *REPL) handleMeta(line string) {
	parts := strings.Fields(line)
	switch parts[0] {
	case ".exit", ".quit":
		r.running = false
	case ".help":
		fmt.Fprint(r.out, `Commands:
  .help            show this help
  .tables          list tables
  .schema <table>  show a table's columns
  .read <file>     run SQL statements from a file
  .exit            quit
`)
	case ".tables":
		names := r.engine.ListTables()
		if len(names) == 0 {
			fmt.Fprintln(r.out, "(no tables)")
			return
		}
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
	schema, ok := r.engine.GetSchema(table)
	if !ok {
		fmt.Fprintf(r.out, "Error: no such table: %s\n", table)
		return
	}
	fmt.Fprintf(r.out, "%s\n", table)
	for _, c := range schema.Columns {
		fmt.Fprintf(r.out, "  %-16s %s\n", c.Name, c.Type)
	}
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
		return formatTable(res)
	case *core.CreateTableStmt:
		return fmt.Sprintf("OK, table %q created\n", s.Table)
	case *core.CreateIndexStmt:
		return fmt.Sprintf("OK, index on %s(%s) created\n", s.Table, s.Column)
	case *core.InsertStmt:
		return fmt.Sprintf("OK, %d row inserted\n", res.RowsAffected)
	case *core.UpdateStmt:
		return fmt.Sprintf("OK, %s\n", rows(res.RowsAffected, "updated"))
	case *core.DeleteStmt:
		return fmt.Sprintf("OK, %s\n", rows(res.RowsAffected, "deleted"))
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
	dir := flag.String("data", "./data", "data directory for the on-disk engine")
	flag.Parse()

	var eng core.StorageEngine
	if *mem {
		eng = memstore.New()
	} else {
		e, err := storage.New(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open storage at %s: %v\n", *dir, err)
			os.Exit(1)
		}
		defer e.Close()
		eng = e
	}

	if err := NewREPL(eng, os.Stdin, os.Stdout).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "repl error: %v\n", err)
		os.Exit(1)
	}
}
