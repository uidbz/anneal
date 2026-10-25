#!/usr/bin/env bash
# m10 live gate: harness components mounted into a real headless run.
# Asserts on the JSON event stream, never on model prose.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
HOME_TEST="${ANNEAL_HOME:-/tmp/opencode/anneal-home}"
ENDPOINT="${ANNEAL_ENDPOINT:-http://127.0.0.1:8081/v1/chat/completions}"
DEMO=/tmp/opencode/demo
BIN="$REPO/bin/anneal-headless"
[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }

rm -rf "$HOME_TEST/harness"
cp -r "$REPO/testing/e2e/m10/harness" "$HOME_TEST/harness"
mkdir -p "$DEMO/junkdir"

run() {
  (cd "$DEMO" && ANNEAL_HOME="$HOME_TEST" ANNEAL_ENDPOINT="$ENDPOINT" \
    timeout 240 "$BIN" -json -yolo -p "$1")
}

echo "M10: === phase: file-defined subagent (subagents/summarizer.md)"
if ! out1=$(run 'Delegate with the Agent tool (subagent type "summarizer"): summarize what go.mod declares in one line.'); then
  echo "$out1"; echo "M10 FAIL: run 1 errored"; exit 1
fi
echo "$out1" | grep -E '"(tool\.start|tool\.result|run\.finished)"' | tail -6 || true
echo "$out1" | grep -q '"name":"Agent"' || { echo "M10 FAIL: Agent tool never called"; exit 1; }
echo "M10: Agent delegation observed"

echo "M10: === phase: guardrail deny (guardrails.md deny: rule beats -yolo)"
out2=$(run 'Run exactly this bash command and report the outcome: rm -rf /tmp/opencode/demo/junkdir') || true
echo "$out2" | grep -E '"(tool\.start|tool\.result)"' | tail -4 || true
echo "$out2" | grep -q 'harness guardrail' || { echo "M10 FAIL: guardrail deny not enforced"; exit 1; }
[ -d "$DEMO/junkdir" ] || { echo "M10 FAIL: junkdir was deleted despite deny"; exit 1; }
echo "M10: guardrail deny observed, junkdir intact"

echo "M10: === M10 HARNESS OK"
