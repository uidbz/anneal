# Using fyagent

## First run

1. Start your OpenAI-compatible server (Strata): it must answer
   `/v1/models` + `/v1/chat/completions`.
2. (Optional) start the tie triplestore and make sure `~/.config/tie/config.toml`
   points at it — session search + cross-session recall need it; everything
   else runs without it.
3. `./bin/fyagent` — the header shows model health (`● online` etc.) and the
   index line (`index: fyagent`, `index: offline`).

Config resolution: `~/.fyagent/config.json` (created with defaults on first
run) → env overrides (see `docs/CONFIG.md`). The GUI **Settings** tab edits
provider settings live.

## GUI tour

- **Transcript** (center): your messages, streaming assistant text, tool
  calls with previews, notices (✔/⚠/🧠). Long sessions scroll.
- **Approval inbox** (top): appears for `perm.request` — tool + input
  preview; buttons **allow** (this call), **session** (same tool/spec for the
  session), **deny**. Staged memory/skill writes show as cards with a diff
  preview and **approve/reject**.
- **Right column**: token/context usage (`ctx %`), tie index status, MCP
  server status.
- **Todo panel**: the model's live checklist for multi-step work.
- **Sidebar** (`sessions` or `/sessions`): all sessions, newest first — click
  to replay/resume. `+ new` starts a fresh session.
- **Mode button**: toggles plan (📋, read-only: mutating tools are denied) /
  build (🛠).

### Slash commands (entry box)

| command | effect |
|---------|--------|
| `/help` | list commands |
| `/new`, `/sessions` | session control |
| `/plan`, `/build` | permission mode |
| `/skills` | list installed skills |
| `/memory` | show MEMORY.md / USER.md with usage vs caps |
| `/index` | rebuild the session search index |
| `/journey` | self-improvement timeline |
| `/learn` | queue a background-review pass now (runs when idle) |
| `/improve` | one contrastive harness-improvement step for the last task |
| `/harness` | open the harness panel (components, guardrail rules, improvement library) |
| `/quit` | exit |

Shortcuts: **Ctrl+N** new session · **Ctrl+B** sessions sidebar ·
**Ctrl+D** stop the current run.

## Headless (`fyagent-headless`)

Same core — same sessions, memory/skills/review, tie index, MCP.

```sh
fyagent-headless -p "fix the flaky test in ./internal/x"   # one-shot, exit 0/1
fyagent-headless -i                                       # REPL
fyagent-headless -json -p "…"                             # JSONL event stream
fyagent-headless --session 20261025-… -p "continue"       # resume a session
fyagent-headless -p "…" -learn                            # review pass after
```

| flag | meaning |
|------|---------|
| `-p TASK` | one-shot task |
| `-i` | REPL (`/new`, `/interrupt`, `/improve`, `/harness [list|promote <id>]`, `/quit`) |
| `-k N` | improve recursion depth with `-improve` (default from config) |
| `-improve-mode` | `sequential` (default) or `parallel` |
| `-json` | emit every GUI event as a JSONL line (machine-readable) |
| `-v` | log every event to stderr |
| `-session ID` / `--new` | resume / fresh session (default: resume most recent) |
| `-yolo` | bypass permission mode entirely |
| `-allow a,b,*` | auto-answer permission prompts (anything not allowed → denied) |
| `-learn` | run the self-improvement review after the task |

Exit codes: `0` ok · `1` run error · `124` timeout · `130` interrupted.
SIGINT interrupts a running task; a second SIGINT exits.

CI-ish example:

```sh
cd repo && fyagent-headless -allow Read,Glob,Grep,Bash -json -p "run tests" \
  | jq -r 'select(.type=="run.finished") | .text'
```

## The learning loop in practice

- The agent saves **memory** when you correct it or state preferences
  (`write_approval` config decides direct vs approval cards). Recall: check
  `/memory`.
- Ask for a skill explicitly — *"save that as a skill"* — or it offers one
  after a non-trivial workflow it expects to repeat (`skill_manage`).
  `/skills` lists them; next sessions load them automatically (catalog +
  `Skill` tool).
- **Cross-session recall**: just ask *"what did we do about X last week?"* —
  the model uses `session_search` (tie). `/index` after restoring backups or
  if `index: offline` at boot.
- **Background review** runs automatically after you stop working
  (idle ≥ `review.idle_settle_s`); watch 🧠 lines, check `/journey`, force a
  pass with `/learn`. Approve/deny what it wants to save via the inbox cards
  (when `write_approval` is on).
- **Harness improvement** (`/improve`, or `-improve` in headless): after a
  task, two scoped editors propose harness edits, each candidate is replayed
  in a sandbox and behaviorally contrasted, and a composer merges only the
  evidence-supported changes. The successor applies to **this session only**
  (overlay); nothing global changes. Evidence-backed steps land in the
  improvement library (`/harness list` in the headless REPL); making one
  permanent is your explicit call: `/harness promote <id>` (with backups).
  Full records: `~/.fyagent/improvements/`. The background reviewer feeds
  the loop too: recurring work-style deficiencies it observes get queued
  via `harness_flag` (`~/.fyagent/harness-queue.json`) and the next
  `/improve` consumes them as hints — evidence still decides.
- **Harness panel** (GUI): the "harness" toolbar button (or `/harness`)
  opens a view over `~/.fyagent/harness/`: each component file is editable
  inline (saves take effect next run — except guardrail `deny:/ask:/allow:`
  rule lines, which need a restart), and the improvement library lists
  every entry with its EXPERIENCE.md and a two-click **promote** button
  (replaced files are backed up under `harness-lib/promote-backup/`).

## Scripting the GUI (UI hacking)

`FYAGENT_UI_DIR=/path/to/ui ./bin/fyagent` loads `/path/to/ui/main.risor`
instead of the embedded copy — edit–restart, no rebuild. The script sees one
global, `agent` (the bridge API: `send`, `on`, `respondPerm`, `sessions`,
`setMode`, …) and all fynerisor widget bindings (`widget`, `container`,
`window`, …). Compile-check any candidate with `fyagent -check-ui` (uses
`FYAGENT_UI_DIR` too). Language gotchas (no `for`, `} else {` hugging, all
event keys always present, etc.) are listed in `docs/PLAN.md` §7 and the
fynerisor skill notes.
