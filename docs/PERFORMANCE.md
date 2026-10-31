# Performance

Anneal measures each run instead of guessing. `core/agent/perf.go` implements
dive's `Tracer` and produces one `run.stats` event per run (also written to
`~/.anneal/logs/perf.jsonl`):

- per model call: time to first token, duration, prompt tokens (uncached and
  cache-read), output and reasoning tokens;
- per tool call: duration, error flag;
- time blocked on approval cards;
- post-run persistence/indexing time;
- effective output tok/s.

`/perf` in the GUI and `run.stats` in `anneal-headless -json` show the same
numbers. `ANNEAL_PPROF=127.0.0.1:6060 ./bin/anneal` exposes `net/http/pprof`.

## Comparing with another agent

`testing/perf/ab.sh` runs the same small Go task in fresh temporary projects
against the same endpoint, one run for anneal and (if installed) one for
opencode. It uses a temporary `ANNEAL_HOME` with the reviewer, auto-improve,
tie and MCP disabled, so only the task uses the model server. See
`testing/perf/README.md`.

```sh
RUNS=3 ./testing/perf/ab.sh
AGENTS=anneal ./testing/perf/ab.sh
```

Two rules when reading the result: wall time is dominated by the model, not
the harness; and one run is not a measurement (`RUNS=3`, same task, same
model, no other load).

## Baseline vs. after the first performance pass

Same machine, same local Strata server, same task (create `math.go`, a table
test, run `go test`), one-shot `-yolo` run:

| metric | before | after |
|---|---|---|
| wall time | 20.7 s | 18.7 s |
| prompt tokens | 26.7k (20.8k cached) | 23.8k (18.5k cached) |
| time to first token (median) | 2.0 s | 1.6 s |
| output tokens | 756 | 718 |
| output tok/s | 79 | 89 |
| streamed UI batch, 300 blocks | 2.23 ms | 0.47 ms |
| open a 600-block session | 366 ms | 57 ms |

What moved the numbers:

- **Prompt size**: removed the duplicate `TextEditor` tool; prompt guidance
  prefers Read/Grep/Glob and Edit over whole-file rewrites.
- **Prompt cache**: background learning (review/improve) is preempted when a
  run starts or the user starts typing, and its idle windows are 10/30 min.
  A background pass on the one local server evicts the conversation's KV
  cache: turns after one had re-processed 96k–153k uncached prompt tokens in
  the evidence session.
- **UI**: streaming deltas refresh only the changed tail instead of the whole
  transcript; reasoning deltas are coalesced at the source; the session list
  is cached; remote cwd is fetched once.
- **Critical path**: tie indexing runs after the run is reported finished.

Not yet benchmarked: `reasoning_effort` levels (the value is model-specific;
per-iteration `reasoning` tokens are in `run.stats`, so an A/B is a config
change plus `RUNS=3`), and parallel read-only tool batches (enabled by default
with `agent.parallel_tools`; the effect depends on how the model batches
calls).
