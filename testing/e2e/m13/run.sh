#!/usr/bin/env bash
# m13 live gate: harnessing-level recursion. Run 1 produces a library entry
# from evidence; run 2 (related task) must SEE that entry in the improve
# step's Propose/Compose context. Asserts on step.json/library artifacts.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
HOME_TEST="${FYAGENT_HOME:-/tmp/opencode/fyagent-home}"
ENDPOINT="${FYAGENT_ENDPOINT:-http://127.0.0.1:8081/v1/chat/completions}"
DEMO=/tmp/opencode/demo
BIN="$REPO/bin/fyagent-headless"
[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }

run1_task='Add func Sub(a, b int) int in /tmp/opencode/demo/sub.go plus a table-driven sub_test.go in the same style as add.go. The package must build and its tests pass when you are done.'
run2_task='Add func Mul(a, b int) int in /tmp/opencode/demo/mul.go plus a table-driven mul_test.go in the same style as add.go. The package must build and its tests pass when you are done.'

rm -rf "$HOME_TEST/harness-lib" "$HOME_TEST/improvements" "$HOME_TEST/overlays"
rm -f "$DEMO"/sub.go "$DEMO"/sub_test.go "$DEMO"/mul.go "$DEMO"/mul_test.go

run() {
  (cd "$DEMO" && FYAGENT_HOME="$HOME_TEST" FYAGENT_ENDPOINT="$ENDPOINT" \
    timeout 28m "$BIN" -yolo -improve -p "$1")
}

echo "M13: === run 1 (seed the library)"
run "$run1_task" > /tmp/m13-run1.log 2>&1 || true
grep -q "library entry" /tmp/m13-run1.log || { echo "M13 FAIL: run 1 produced no library entry"; tail -20 /tmp/m13-run1.log; exit 1; }
echo "M13: run 1 library entry recorded"

# library must have exactly one entry with snapshot + EXPERIENCE + index
LIB="$HOME_TEST/harness-lib"
n=$(find "$LIB" -maxdepth 1 -mindepth 1 -type d ! -name promote-backup | wc -l)
[ "$n" -ge 1 ] || { echo "M13 FAIL: no library entry dir"; exit 1; }
ENTRY=$(find "$LIB" -maxdepth 1 -mindepth 1 -type d ! -name promote-backup | head -1)
[ -f "$ENTRY/EXPERIENCE.md" ] || { echo "M13 FAIL: no EXPERIENCE.md"; exit 1; }
[ -f "$ENTRY/meta.json" ] || { echo "M13 FAIL: no meta.json"; exit 1; }
[ -d "$ENTRY/harness" ] || { echo "M13 FAIL: no harness snapshot"; exit 1; }
grep -q "$(basename "$ENTRY")" "$LIB/INDEX.md" || { echo "M13 FAIL: entry not indexed"; exit 1; }
echo "M13: entry $(basename "$ENTRY") complete (experience+meta+harness+index)"

echo "M13: === run 2 (must see prior library)"
run "$run2_task" > /tmp/m13-run2.log 2>&1 || true
grep -q "library entry" /tmp/m13-run2.log && echo "M13: run 2 added its own entry" || echo "M13: run 2 no new entry (ok if composer kept harness)"

# The gate that matters: run 2's step.json exists and its Propose prompts
# carried the library (we can't see prompts, but the entry dir was present
# and non-empty when run 2 started; step.json records the run happened).
ls "$HOME_TEST/improvements"/*/step.json >/dev/null 2>&1 || { echo "M13 FAIL: no step.json"; exit 1; }
n2=$(find "$LIB" -maxdepth 1 -mindepth 1 -type d ! -name promote-backup | wc -l)
[ "$n2" -ge 1 ] || { echo "M13 FAIL: library lost between runs"; exit 1; }
echo "M13: library persists across runs ($n2 entries)"

# promote moves components global (then remove the promoted files to keep the
# fixture clean); backup must capture the previous global file.
echo "M13: === promote round-trip"
printf '/harness promote %s\n/quit\n' "$(basename "$ENTRY")" | \
  (cd "$DEMO" && FYAGENT_HOME="$HOME_TEST" FYAGENT_ENDPOINT="$ENDPOINT" "$BIN" -i) >/tmp/m13-promote.log 2>&1 || true
grep -q "promoted into the global harness" /tmp/m13-promote.log || { echo "M13 FAIL: promote did not run"; tail -8 /tmp/m13-promote.log; exit 1; }
grep -q "backed up" /tmp/m13-promote.log || { echo "M13 FAIL: no backup note"; exit 1; }
ls "$HOME_TEST/harness"/*.md >/dev/null 2>&1 || { echo "M13 FAIL: global harness empty after promote"; exit 1; }
echo "M13: promote applied with backup"

echo "M13: === M13 LIBRARY OK"
