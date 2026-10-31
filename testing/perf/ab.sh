#!/usr/bin/env bash
# Side-by-side wall-clock comparison: anneal-headless vs opencode on the same
# local model server and the same coding task. See README.md.
#
#   ./testing/perf/ab.sh            # both agents (opencode skipped if absent)
#   AGENTS=anneal ./testing/perf/ab.sh
#   TASK="..." ./testing/perf/ab.sh
#
# Requires: a running OpenAI-compatible server, bin/anneal-headless (built by
# this script), and optionally `opencode` configured for the same endpoint.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
agents=${AGENTS:-anneal,opencode}
task=${TASK:-"Create math.go in this Go module with func Add(a, b int) int and a table test in math_test.go. Run go test ./... and make it pass."}
model=${ANNEAL_MODEL:-auto}
endpoint=${ANNEAL_ENDPOINT:-}
runs=${RUNS:-1}
work=$(mktemp -d /tmp/anneal-perf.XXXXXX)
trap 'rm -rf "$work"' EXIT

echo "== building anneal-headless"
(cd "$repo_root" && go build -o "$work/anneal-headless" ./cmd/anneal-headless)

# A fresh project per agent so files never leak between runs.
seed_project() {
  local dir=$1
  mkdir -p "$dir"
  (cd "$dir" && git init -q && cat > go.mod <<'EOF'
module perfdemo

go 1.27
EOF
  printf 'package perfdemo\n' > doc.go
  git add -A && git -c user.email=p@p -c user.name=perf commit -qm seed)
}

write_config() {
  local home=$1
  mkdir -p "$home"
  cat > "$home/config.json" <<EOF
{
  "provider": {"endpoint": "${endpoint:-http://127.0.0.1:8081/v1/chat/completions}", "model": "$model", "api_key": "strata", "context_window": 0, "max_tokens": 16384, "temperature": 0.4, "reasoning_effort": "medium", "stream_first_event_timeout_s": 240, "stream_idle_timeout_s": 120, "max_retries": 2},
  "agent": {"permission_mode": "default", "allow": ["Read", "Glob", "Grep", "ListDirectory", "todo"], "ask": ["Bash", "Write", "Edit", "WebFetch"], "deny": null, "tool_iteration_limit": 80, "subagents": true, "script": true},
  "compaction": {"enabled": true, "threshold_pct": 82, "keep_recent_turns": 4},
  "memory": {"enabled": false, "memory_char_limit": 2200, "user_char_limit": 1375, "write_approval": false},
  "skills": {"enabled": false, "write_approval": false, "extra_dirs": null},
  "review": {"enabled": false, "defer_until_idle": true, "idle_settle_s": 45, "max_age_s": 1800, "max_input_tokens": 48000, "notify": false},
  "tie": {"enabled": false, "collection": "perf", "config_name": ""},
  "mcp": {"enabled": false, "auto_detect": false, "servers": {}},
  "harness": {"enabled": false, "char_limit": 6000},
  "improve": {"enabled": false, "auto": false, "auto_idle_s": 300, "library": false, "k": 1, "mode": "sequential", "total_wall_s": 0, "max_total_tokens": 0},
  "ui": {"text_scale": 1.0, "theme": "system", "sidebar_open": true, "inspector_open": true, "sidebar_width": 240, "inspector_width": 280},
  "server": {"listen": ["127.0.0.1:0"], "auth_token_env": "ANNEAL_AUTH_TOKEN", "idle_timeout_s": 0, "drain_grace_s": 20, "work_dir": ""}
}
EOF
}

run_anneal() {
  local n=$1
  local dir="$work/anneal-$n"
  local home="$work/home-$n"
  seed_project "$dir"; write_config "$home"
  local start end json
  start=$(date +%s.%N)
  json=$(cd "$dir" && ANNEAL_HOME="$home" "$work/anneal-headless" -yolo -json -p "$task" 2>"$work/anneal-$n.err")
  end=$(date +%s.%N)
  echo "$json" > "$work/anneal-$n.json"
  python3 - "$work/anneal-$n.json" "$start" "$end" <<'PY'
import json, sys
path, start, end = sys.argv[1], float(sys.argv[2]), float(sys.argv[3])
stats, ok, calls = None, None, 0
for line in open(path):
    line = line.strip()
    if not line:
        continue
    try:
        e = json.loads(line)
    except json.JSONDecodeError:
        continue
    if e.get("type") == "run.stats":
        stats = e.get("stats")
    if e.get("type") == "run.finished":
        ok = bool(e.get("ok"))
    if e.get("type") == "tool.start":
        calls += 1
s = stats or {}
print(f"anneal: wall {end-start:.1f}s ok={ok} tools={calls} "
      f"model {s.get('model_ms',0)/1000:.1f}s tools {s.get('tool_ms',0)/1000:.1f}s "
      f"approvals {s.get('perm_wait_ms',0)/1000:.1f}s "
      f"ttft {s.get('ttft_ms',0)/1000:.1f}s prompt {s.get('in',0)+s.get('cached',0)} "
      f"(uncached {s.get('in',0)}) out {s.get('out',0)} tps {s.get('tps',0):.0f}")
PY
}

run_opencode() {
  local n=$1
  local dir="$work/opencode-$n"
  seed_project "$dir"
  local start end
  start=$(date +%s.%N)
  (cd "$dir" && opencode run "$task") >"$work/opencode-$n.log" 2>&1 || true
  end=$(date +%s.%N)
  echo "opencode: wall $(python3 -c "print(f'{$end-$start:.1f}')")s (see $work/opencode-$n.log)"
}

IFS=, read -ra want <<<"$agents"
for i in $(seq 1 "$runs"); do
  echo "== run $i/$runs"
  for a in "${want[@]}"; do
    case "$a" in
      anneal) run_anneal "$i" ;;
      opencode)
        if command -v opencode >/dev/null 2>&1; then run_opencode "$i"; else echo "opencode: not installed, skipped"; fi
        ;;
      *) echo "unknown agent $a" >&2; exit 2 ;;
    esac
  done
done
echo "== done (perf log: \$ANNEAL_HOME/logs/perf.jsonl in each temp home)"
