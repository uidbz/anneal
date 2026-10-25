# Using Anneal

## First run

1. Start your OpenAI-compatible server (Strata): it must answer
   `/v1/models` + `/v1/chat/completions`.
2. (Optional) start the tie triplestore and make sure `~/.config/tie/config.toml`
   points at it — session search + cross-session recall need it; everything
   else runs without it.
3. `./bin/anneal` — the header shows model health, the tie index state, and
   MCP server status.

Config resolution: `~/.anneal/config.json` (created with defaults on first
run) → env overrides (see `docs/CONFIG.md`). Provider settings are edited in
that file; the bridge also exposes `agent.config()` / `agent.saveConfig(doc)`
for scripted settings UIs (there is no built-in settings tab yet).

## GUI tour (hybrid shell)

The `anneal` binary is a plain Fyne shell with scripted panes.

- **Transcript** (center): user/assistant blocks, streaming tail, tool calls
  with previews, notices (✔/⚠/🧠). It appends incrementally and lays out at
  most 300 blocks; older blocks are kept in memory behind a **“show N earlier
  messages”** button (Phase 2 hardening).
- **Approval inbox** (above the transcript): `perm.request` cards with
  **allow** (this call), **session** (same tool/spec for the session), and
  **deny**. When `write_approval` is on, staged memory/skill writes appear as
  cards with an operation preview and **approve/reject**.
- **Header**: plan/build toggle, `+ new`, `sessions`, `settings`, model health,
  token/context usage, tie index state, MCP status. `settings` opens the full
  `config.json` in an editor; saving re-points the health monitor and applies
  live-capable store settings immediately.
- **Todo panel** (right): the model's live checklist for multi-step work.
- **Activity tab**: journey timeline + improvement-run records
  (`~/.anneal/improvements/*/step.json`): task, k/mode, candidates, decisions,
  library entry, successor overlay. Refresh-on-demand; no background model
  work.
- **Session picker** (`sessions` or `/sessions`): newest first — click to
  replay/resume.
- **Harness tab**: a fynerisor `CanvasObject` pane. Edit component files
  inline, review guardrail rule counts, browse the improvement library, read
  EXPERIENCE.md, promote with a two-click confirmation, inspect reviewer
  flags, and start `/improve`. The pane is pull-based and receives only a
  filtered harness/improve event feed that updates a notice line; it never
  gets chat deltas or host-window capabilities. Ctrl+R reloads it.

### Slash commands (entry box)

| command | effect |
|---------|--------|
| `/help` | list commands |
| `/new`, `/sessions` | session control |
| `/plan`, `/build` | permission mode |
| `/skills` | list installed skills |
| `/memory` | show MEMORY.md / USER.md with usage vs caps |
| `/journey` | self-improvement timeline |
| `/activity`, `/runs` | switch to the Activity tab |
| `/index` | rebuild the session search index |
| `/learn` | queue a background-review pass now (runs when idle) |
| `/improve` | one contrastive harness-improvement step for the last task |
| `/harness` | switch to the harness tab |
| `/quit` | exit |

### Keyboard

| shortcut | effect |
|----------|--------|
| Ctrl+N | new session |
| Ctrl+B | session picker |
| Ctrl+D | stop the current run |
| Ctrl+L | focus the composer |
| Ctrl+R | reload the scripted harness pane |
| Ctrl+1/2/3 | Chat / Harness / Activity |
| Ctrl+Q | quit |

For UI smoke tests without typing, `anneal -send "task" -quit-after-run` fills
the composer, submits it, and exits after the run finishes.

## Headless (`anneal-headless`)

Same core — same sessions, memory/skills/review, tie index, MCP.

```sh
anneal-headless -p "fix the flaky test in ./internal/x"   # one-shot, exit 0/1
anneal-headless -i                                       # REPL
anneal-headless -json -p "…"                             # JSONL event stream
anneal-headless --session 20261025-… -p "continue"       # resume a session
anneal-headless -p "…" -learn                            # review pass after
```

| flag | meaning |
|------|--------|
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
cd repo && anneal-headless -allow Read,Glob,Grep,Bash -json -p "run tests" \
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
  pass with `/learn`.
- **Harness improvement** (`/improve`, or `-improve` in headless): after a
  task, two scoped editors propose harness edits, each candidate is replayed
  in a sandbox and behaviorally contrasted, and a composer merges only the
  evidence-supported changes. The successor applies to **this session only**
  (overlay); nothing global changes. Evidence-backed steps land in the
  improvement library (Harness tab); making one permanent is your explicit
  call: two-click **promote** in the pane, or `/harness promote <id>` in the
  headless REPL (with backups). Full records: `~/.anneal/improvements/`.
  The background reviewer feeds the loop too: recurring work-style
  deficiencies it observes get queued via `harness_flag`
  (`~/.anneal/harness-queue.json`) and the next `/improve` consumes them as
  hints — evidence still decides.
- **Harness pane**: edits to prose components take effect next run; guardrail
  `deny:/ask:/allow:` rule lines need a restart.

## Scripting the UI

- **Panes (product):** `ANNEAL_PANE_DIR=/path/to/panes ./bin/anneal` loads
  `harness.risor` from that directory instead of the embedded copy. Compile
  with `anneal -check-ui`. A pane receives a restricted `agent` global
  (harness/library/flags/improve only) plus a filtered event feed, and is
  hosted by `gui.NewCanvasObject`.
- **Full-window script UI (e2e/custom hacking):**
  `ANNEAL_UI_DIR=/path/to/ui ./bin/anneal -script-ui` runs `main.risor` with
  the full `agent` bridge API. Compile with
  `ANNEAL_UI_DIR=… ./bin/anneal -check-script-ui`. This is the driver mode
  used by the e2e gates; there is no embedded full UI anymore.

Language gotchas (no `for`, `} else {` hugging, all event keys always present,
etc.) are listed in `docs/PLAN.md` §7 and the fynerisor skill notes.
