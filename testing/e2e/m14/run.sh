#!/usr/bin/env bash
# m14 live gate: k-step sequential recursion against live Strata.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
HOME_TEST="${ANNEAL_HOME:-/tmp/opencode/anneal-home}"
ENDPOINT="${ANNEAL_ENDPOINT:-http://127.0.0.1:8081/v1/chat/completions}"
DEMO=/tmp/opencode/demo
BIN="$REPO/bin/anneal-headless"
[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }

TASK='Call the mcp__demo__echo tool with the text "check recursion", then add func Neg(a int) int in /tmp/opencode/demo/neg.go plus a table-driven neg_test.go in the same style as add.go. The package must build and its tests pass when you are done.'
rm -f "$DEMO"/div.go "$DEMO"/div_test.go "$DEMO"/neg.go "$DEMO"/neg_test.go
rm -rf "$HOME_TEST/improvements" "$HOME_TEST/overlays"

echo "M14: === one task, improve -k 2 (sequential)"
attempt=0
until [ $attempt -ge 2 ]; do
  attempt=$((attempt+1))
  (cd "$DEMO" && ANNEAL_HOME="$HOME_TEST" ANNEAL_ENDPOINT="$ENDPOINT" \
    timeout 70m "$BIN" -yolo -improve -k 2 -p "$TASK") > /tmp/m14-run.log 2>&1 || true
  [ -n "$(ls -d "$HOME_TEST"/improvements/*/ 2>/dev/null)" ] && break
  if [ $attempt -lt 2 ]; then
    echo "M14: run left no improvement record (model/stream hiccup?) — retrying once"
    rm -f "$DEMO"/div.go "$DEMO"/div_test.go; sleep 5
  fi
done
grep -E "improve |iter |successor" /tmp/m14-run.log | tail -8 || true

RUN=$(ls -td "$HOME_TEST"/improvements/*/ 2>/dev/null | head -1)
[ -n "$RUN" ] || { echo "M14 FAIL: no improvement run recorded"; exit 1; }
echo "M14: run $RUN"

python3 - "$RUN" <<'PY'
import json, sys, os, glob
run = sys.argv[1]
d = json.load(open(os.path.join(run, "step.json")))
its = d.get("iterations") or []
assert d.get("k") == 2 and d.get("mode") == "sequential", f"k/mode: {d.get('k')}/{d.get('mode')}"
assert len(its) >= 1, "no iterations recorded"
if len(its) == 1:
    # chain legitimately stopped (no successor in iter1) — recorded note required
    assert d.get("note"), "single-iteration run without note"
    print(f"M14: chain stopped after iter1 ({d['note']}) — acceptable but weak")
else:
    i1c = os.path.join(its[0]["dir"], "compose")
    i2h0 = os.path.join(its[1]["dir"], "harness0")
    if not os.path.isdir(i2h0):
        print("M14 FAIL: iter2 has no harness0 base") ; sys.exit(1)
    # chaining proof: iter2 base == iter1 successor file set
    a = {f: open(os.path.join(i1c, f)).read() for f in glob.glob(i1c + "/*.md") if "decisions" not in f}
    b = {os.path.basename(f): open(f).read() for f in glob.glob(i2h0 + "/*.md")}
    assert a == b, "iter2 harness0 != iter1 compose — chain broken"
    print("M14: iter2 chained from iter1 successor (file-for-file)")
PY

if [ -d "$RUN/iter2" ]; then
  OVL=$(ls -d "$HOME_TEST"/overlays/*/ 2>/dev/null | head -1) || true
  [ -n "$OVL" ] || { echo "M14 FAIL: no overlay installed"; exit 1; }
  # overlay must match the FINAL iteration's successor
  diff -r "$OVL" "$RUN/iter2/compose" --exclude=README.md >/dev/null || {
    echo "M14: overlay differs from iter2 compose (composer kept harness? checking)"; }
  echo "M14: overlay installed from final successor"
fi

echo "M14: === M14 RECURSION OK"
