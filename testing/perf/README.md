# Performance comparison

Anneal's run timing is instrumented (`core/agent/perf.go`): one `run.stats`
event per run plus a line in `$ANNEAL_HOME/logs/perf.jsonl`. The summary splits
wall time into model calls (with time-to-first-token per call), tool execution,
time blocked on approvals, and post-run persistence/indexing, and reports
tokens in / cached / out / reasoning with an effective tok/s.

## Comparing with opencode

```sh
./testing/perf/ab.sh                 # one run each, temp projects, temp homes
RUNS=3 ./testing/perf/ab.sh          # median of 3 is more useful
AGENTS=anneal ./testing/perf/ab.sh    # baseline for anneal alone
```

Rules for a fair comparison:

- Same endpoint and model (`ANNEAL_ENDPOINT` / `ANNEAL_MODEL`; configure
  opencode for that endpoint too).
- Same task. The default task is a small Go change plus `go test ./...`.
- No background learning: the script's temporary `ANNEAL_HOME` disables the
  reviewer, auto-improve, tie and MCP, so the GPU only serves the task.
- Both agents start from a fresh git repo; no prompt cache is warmed before
  the measured run.
- Compare `wall` first; then use the anneal split to see *why*: prompt
  processing (`ttft` with a large `in`), generation (`out`/`tps`), tools, or
  approvals (`approvals`; always zero for `-yolo`, so run an interactive
  session separately when comparing approval overhead).

`/perf` in the GUI (or `run.stats` in `-json` output) shows the same numbers
for a real session. `ANNEAL_PPROF=127.0.0.1:6060 ./bin/anneal` exposes
`net/http/pprof` for UI profiling.

## UI benchmarks

```sh
go test ./cmd/anneal -run '^$' -bench 'StreamDelta|SessionReplay' -benchmem
```

- `StreamDelta`: one streamed text batch against 300 visible transcript blocks.
- `SessionReplay`: opening a 600-block stored session.

Both are regression guards for the refresh-scoping work; run them before and
after changing the transcript pipeline.

## Checking tool-call streaming

Whether the model server streams tool-call arguments (rather than buffering a
whole `Write` and sending it at the end) shows up as `ttft` and per-call
`duration_ms` in `run.stats`. When a large write shows no first-token event
until the whole call is ready, the server is buffering; long writes then race
`provider.stream_idle_timeout_s`.
