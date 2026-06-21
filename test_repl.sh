#!/usr/bin/env bash
# Smoke-test the REPL by piping ONE session (not one process per statement) and
# checking the printed output. Uses the in-memory engine for speed/determinism.
set -euo pipefail

go build -o bin/aura ./cmd/aura

out=$(./bin/aura -mem <<'SQL'
CREATE TABLE test (id INT, name TEXT);
INSERT INTO test VALUES (1, 'Alice');
SELECT * FROM test;
.tables
.schema test
SELECT * FROM non_existent;
SELECT *
FROM test
WHERE id = 1;
.exit
SQL
)

echo "$out"
echo "----"
fail=0
check() { grep -q "$1" <<<"$out" || { echo "MISSING: $1"; fail=1; }; }
check 'table "test" created'
check '1 row inserted'
check 'Alice'
check 'Error:'          # bad table reported, REPL keeps going
check '(1 row)'         # multi-line SELECT assembled
[ "$fail" -eq 0 ] && echo "ALL CHECKS PASSED" || { echo "SOME CHECKS FAILED"; exit 1; }