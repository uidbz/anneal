# e2e gates

Risor **driver scripts** that drive the real `fyagent` binary against live
infrastructure (Strata + optionally tie/MCP) and assert on the event stream.
They replace the GUI script (`FYAGENT_UI_DIR`), so no clicking is needed —
each prints `=== … OK/FAIL` markers and quits itself.

## Prerequisites

- Strata (or any OpenAI Chat-Completions server) running, default
  `http://127.0.0.1:8081/v1/chat/completions`.
- A scratch state dir: `FYAGENT_HOME=/tmp/opencode/fyagent-home` (any dir).
- A demo project dir to operate in (the tasks assume `/tmp/opencode/demo`
  with a tiny Go module containing `add.go` with a failing test — create it
  once if you want the M3/M8-style tasks to make sense).
- tie triplestore reachable for `m4a`/`m4b`/`m7` (collection `fyagent`).
- Build the MCP demo server for `m8`:

```sh
cd testing/e2e/mcp-demo && go build -o mcpdemo .
# then add to $FYAGENT_HOME/config.json:
#   "mcp": {"enabled": true,
#           "servers": {"demo": {"command": "<abs-path>/testing/e2e/mcp-demo/mcpdemo"}}}
```

## Run a gate

```sh
cd /tmp/opencode/demo            # or any cwd the tasks expect
FYAGENT_HOME=/tmp/opencode/fyagent-home \
FYAGENT_UI_DIR=<repo>/testing/e2e/<driver> \
FYAGENT_ENDPOINT=http://127.0.0.1:8081/v1/chat/completions \
timeout 300 <repo>/bin/fyagent
```

## Gates

| driver | proves |
|--------|--------|
| `e2e-ui/` (M3) | permission session-grant, todo tool, plan-mode auto-deny, session list/replay |
| `m4a/` + `m4b/` | session indexed to tie on run end (a); cross-launch `session_search` recall from a *new process* (b) |
| `m5/` | direct memory write + cross-session snapshot recall **without tools** |
| `m5s/` | `write_approval=true`: staging (file untouched) → `respondMemory` approve → applied |
| `m6/` | `skill_manage` create → next session `Skill` tool injects → behavioral compliance; `Agent` subagent (explore) |
| `m7/` | background reviewer fires while idle, writes lessons, journey populated |
| `m8/` | MCP: boot connect, `mcp__demo__echo` tool call through permission prompt, output verbatim |
| `m15/` | harness-panel bridge APIs: files/get/save round-trip, path jail, library + EXPERIENCE.md reads (`make gui-m15-smoke`) |

Notes:
- `m4b`/`m5`/`m6` create their own fresh sessions (`agent.newSession()`), so
  runs don't interfere.
- For `m7`, set `review.idle_settle_s` to a few seconds in the scratch config
  to keep the gate fast.
- The headless binary has its own gates (see `docs/USING.md` §headless); the
  simplest is `fyagent-headless -p "…" -learn`.
