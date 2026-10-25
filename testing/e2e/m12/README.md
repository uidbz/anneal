# m12 — contrastive step live gate (manual, ~20–25 min on Strata)

Runs one full Propose→Probe→Compose step against a real task, then asserts
on the recorded artifacts (never on model prose).

```sh
# from the repo root; Strata up, demo project at /tmp/opencode/demo:
rm -f /tmp/opencode/demo/sub.go /tmp/opencode/demo/sub_test.go
cd /tmp/opencode/demo
FYAGENT_HOME=/tmp/opencode/fyagent-home \
FYAGENT_ENDPOINT=http://127.0.0.1:8081/v1/chat/completions \
  <repo>/bin/fyagent-headless -yolo -improve \
  -p 'Add func Sub(a, b int) int in /tmp/opencode/demo/sub.go plus a table-driven sub_test.go in the same style as add.go. The package must build and its tests pass when you are done.'
```

Assertions (run last):

1. `~/.fyagent/improvements/<run>/step.json` exists; each candidate has a
   probe record (`steps > 0`) or an explicit `skipped` reason; `decisions`
   is non-empty when a successor was composed.
2. Probe sandboxes live under the run dir; `/tmp/opencode/demo` contains
   only the main run's deliverables (sandbox copies under
   `improvements/<run>/{probes,successor}/*/ws/`).
3. If a successor exists: `~/.fyagent/overlays/<session>/` holds the
   composed component files; `~/.fyagent/harness/` is UNCHANGED (global
   harness only moves via explicit promote, M13).
4. Events on the stream: `improve.start`, 2×`improve.propose`,
   `improve.probe`, `improve.compose`, `improve.done` (all keys present).

Known-good outcome (2026-10-25, qwen3.8-flash-next): both candidates
probed (9/6 steps), 27-line decisions file, successor composed and applied
as session overlay. The composer correctly REJECTED a candidate's false
"transient MCP failure, just retry" claim, citing five deterministic
identical failures in the reference — evidence-bound composition working
on a 27B local model.

GPU note: the idle review pass is deferred while an improvement step runs
(busy-hook) — without that, contrast calls hit stream timeouts.
