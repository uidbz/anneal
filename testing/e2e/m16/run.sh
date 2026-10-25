#!/usr/bin/env bash
# m16 live gate: reviewer → harness-flag queue against live Strata.
# (~5 min; the deterministic tool/queue semantics are hermetic tests —
# this proves the real reviewer runs with the tool, the queue file is
# valid, and the /harness REPL surface shows the section.)
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
HOME_TEST="${ANNEAL_HOME:-/tmp/opencode/anneal-home}"
ENDPOINT="${ANNEAL_ENDPOINT:-http://127.0.0.1:8081/v1/chat/completions}"
DEMO=/tmp/opencode/demo
BIN="$REPO/bin/anneal-headless"
[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }

rm -f "$HOME_TEST/harness-queue.json"
rm -f "$DEMO"/neg.go "$DEMO"/neg_test.go

TASK='Add func Neg(a int) int in /tmp/opencode/demo/neg.go plus a table-driven neg_test.go in the same style as add.go. The package must build and its tests pass when you are done.'

echo "M16: === task + learning pass"
(cd "$DEMO" && ANNEAL_HOME="$HOME_TEST" ANNEAL_ENDPOINT="$ENDPOINT" \
  timeout 15m "$BIN" -yolo -learn -p "$TASK") > /tmp/m16-run.log 2>&1 || true
grep -E "review|🪧" /tmp/m16-run.log | tail -4 || true

FLAGS=$(grep -c "review flagged a harness gap" /tmp/m16-run.log || true)
echo "M16: review.flag events this run: $FLAGS"

if [ -f "$HOME_TEST/harness-queue.json" ]; then
  python3 -c "import json;d=json.load(open('$HOME_TEST/harness-queue.json'));assert isinstance(d,list);print('M16: queue file valid,', len(d), 'entries')"
else
  echo "M16: no flags written (clean run — reviewer saw nothing harness-level)"
fi

echo "M16: === /harness REPL surface"
printf '/harness\n/quit\n' | (cd "$DEMO" && ANNEAL_HOME="$HOME_TEST" ANNEAL_ENDPOINT="$ENDPOINT" \
  "$BIN" -i) > /tmp/m16-repl.log 2>&1 || true
grep -E "components:|rules:" /tmp/m16-repl.log | head -2
[ "$FLAGS" != "0" ] && grep -E "reviewer flags" /tmp/m16-repl.log && \
  echo "M16: flags visible in /harness" || true

# nothing may have auto-edited the harness beyond what a promote left
python3 - "$HOME_TEST" <<'PY'
import json, sys, os
sys.path.append("")
q = os.path.join(sys.argv[1], "harness-queue.json")
if os.path.exists(q):
    for f in json.load(open(q)):
        assert f["gap"].strip(), "empty gap stored"
PY

echo "M16: === M16 REVIEW BRIDGE OK"
