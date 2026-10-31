# Using Anneal

## First run

1. Start your OpenAI-compatible server (Strata): it must answer
   `/v1/models` + `/v1/chat/completions`.
2. (Optional) start the tie triplestore and make sure `~/.config/tie/config.toml`
   points at it — session search + cross-session recall need it; everything
   else runs without it.
3. `./bin/anneal` (or `anneal` after `make install`) — the header shows model
   health, the tie index state, and MCP server status. `make install` also adds
   an `Anneal` desktop launcher through the XDG applications directory.

Config resolution: `~/.anneal/config.json` (created with defaults on first
run) → env overrides (see `docs/CONFIG.md`). The Settings view edits the
common keys as a form (Appearance, Model, Agent, Learning, Context,
Integrations, Permissions) and keeps **Edit JSON…** for advanced keys; the
bridge also exposes `agent.config()` / `agent.saveConfig(doc)` for scripted
settings UIs.

## GUI tour (hybrid shell)

The `anneal` binary is a plain Fyne shell with scripted panes, laid out like a
small IDE.

- **Activity rail** (far left): Chat, Harness, Activity, and Settings. The
  active view gets an accent bar; Settings is pinned to the bottom.
- **Sessions sidebar** (left, Ctrl+B): stored sessions grouped by Today /
  Yesterday / Previous 7 days / Older, with a filter box. The current session
  is highlighted; right-click a row to copy its session ID. New sessions use
  the `+` button. The split handle resizes it (180–480 px, remembered).
- **Transcript** (center): "You" turns on raised cards; "Anneal" turns with
  prose, code/table surfaces, and compact tool rows (`✔` done, `✖` failed);
  click a tool row to expand its result. System notices are muted lines with
  `✔ ⚠ ✖` markers. Improve/review progress updates one live `●` line. The
  column is width-capped for readability, and at most 300 blocks are laid out;
  older blocks stay in memory behind a **Show N earlier messages** button.
- **Composer** (bottom): multi-line; **Enter sends**, **Shift+Enter** adds a
  newline, and it grows from 2 to 8 rows. The toolbar has the Build/Plan
  switch, the model (click → Settings), and one primary button that turns
  into **Stop** while a run is going.
- **Approval dock** (above the composer): permission, memory, and skill cards
  with details and buttons. Permissions offer **Reject / Always allow / Allow
  once**; Ctrl+Enter (with an empty composer) allows the oldest card once.
- **Inspector** (right, Ctrl+J): task checklist with progress count, a context
  meter (amber at 70 %, red at 82 %), token totals, compaction count, session
  ID and **working directory** (both copyable), mode, and whether an
  improvement overlay is active. The working directory is the process cwd:
  where you launched `anneal`, or the `server.work_dir`/process cwd for
  `anneald`; sessions do not store their own cwd.
- **Status bar**: run state and elapsed time on the left; model, index, MCP,
  and token/context chips on the right (the model and MCP chips open Settings;
  the index chip asks before re-indexing).
- **Header**: the session title, the PLAN/BUILD badge, and view actions
  (sidebar/inspector toggles, Improve, overflow menu). The native menu bar
  (File / View / Session / Help) owns the shortcuts.
- **Settings view**: a real form over a draft of `~/.anneal/config.json`
  (Appearance applies live; Save/Revert/Edit JSON; unsaved changes are
  guarded). Some integrations reconnect on restart.
- **Activity view**: journey timeline rows (time, kind badge, readable text)
  and one card per improvement run (outcome, candidates, decisions, library
  entry). Refresh-on-demand; no background model work.
- **Harness view**: a fynerisor `CanvasObject` pane below a Go header. Edit
  component files inline (Save), review component and guardrail counts, browse
  the improvement library, read experience, promote with a two-click
  confirmation, and inspect reviewer flags. Refresh asks before discarding
  unsaved edits. The pane only receives a filtered harness/improve event feed
  and never chat deltas or host-window capabilities. Ctrl+R reloads it.

### Slash commands (entry box)

| command | effect |
|---------|--------|
| `/help` | list commands |
| `/new`, `/sessions` | session control |
| `/plan`, `/build` | permission mode |
| `/textsize [120%\|+10%\|1.2\|reset]` | show/change the UI text size |
| `/cwd`, `/pwd` | show the agent's working directory (also in the inspector) |
| `/backend [name\|addr]` | show the attached backend / choose it for the next launch |
| `/skills` | list installed skills |
| `/memory` | show MEMORY.md / USER.md with usage vs caps |
| `/journey` | self-improvement timeline |
| `/activity`, `/runs` | switch to the Activity view |
| `/index` | rebuild the session search index |
| `/learn` | queue a background-review pass now (runs when idle) |
| `/improve` | one contrastive harness-improvement step for the last task |
| `/harness` | switch to the Harness view |
| `/theme system\|light\|dark` | switch the theme |
| `/quit` | exit |

### Keyboard

| shortcut | effect |
|----------|--------|
| Ctrl+N | new session |
| Ctrl+B | toggle the sessions sidebar |
| Ctrl+J | toggle the inspector |
| Ctrl+M | toggle plan/build mode |
| Ctrl+D | stop the current run |
| Ctrl+I | improve the harness |
| Ctrl+L | focus the composer |
| Ctrl+R | reload the scripted harness pane |
| Ctrl+, | Settings |
| Ctrl+= / Ctrl+- / Ctrl+0 | text size up / down / reset |
| Ctrl+1/2/3/4 | Chat / Harness / Activity / Settings |
| Ctrl+Q | quit |
| Enter / Shift+Enter | send / newline in the composer |
| Ctrl+Enter | allow the oldest pending approval |

For UI smoke tests without typing, `anneal -send "task" -quit-after-run` fills
the composer, submits it, and exits after the run finishes.

## Deployment & tenancy (recommended usage pattern)

Anneal's unit of isolation is the **instance**: one backend process == one
environment == one project (or one user's workspace). Scoping is achieved by
*which instance/mount you connect to*, never by switching projects inside a
running backend. There is deliberately no `/cd`: the permission jail, saved
grants, project skills, and audit trail are all anchored to the instance's
work dir, and moving them at runtime would weaken the isolation that makes
the container model safe.

Three tiers, simplest first:

1. **Local, single project** — run `anneal` from the project directory. The
   core is embedded in the GUI process; state lives in `~/.anneal`. Fine for
   solo work on one project at a time.
2. **Container per scope (the main way)** — one bind mount + one `anneald`
   container per project/user (see [CONTAINERS.md](CONTAINERS.md)). Idle
   auto-shutdown (`ANNEAL_IDLE_TIMEOUT_S`) makes each instance near-free;
   history persists in the mount. Switching projects = starting/attaching a
   different instance.
3. **One frontend, many backends** — the GUI can attach to a remote
   `anneald` instead of embedding a core:

   ```sh
   anneal -server 127.0.0.1:7777            # ANNEAL_AUTH_TOKEN in env
   anneal -server unix:/anneal/state/anneal.sock
   ```

   Everything (sessions, permissions, harness, learning loop) then lives on
   the backend; the GUI is a thin client over the same wire protocol the
   headless CLI uses. Register several backends in `config.json` under
   `ui.backends` and switch between them with the status-bar backend chip or
   `/backend`:

   ```json
   "ui": {
     "backends": [
       {"name": "local"},
       {"name": "proj-a", "addr": "127.0.0.1:7777", "token_env": "ANNEAL_AUTH_TOKEN"},
       {"name": "proj-b", "addr": "unix:/home/me/anneal-env/b/state/anneal.sock"}
     ]
   }
   ```

   `"name": "local"` means the embedded core. The inspector shows the
   connected backend's work dir, so you always know which project you are
   touching.

**Project-switch recipe (containers):** keep one env directory per project
(`~/anneal-env/<proj>/{state,work}`) and a launcher:

```sh
anneal-up() {
  local proj=$1
  docker run -d --rm --name "anneal-$proj" \
    -e ANNEAL_AUTH_TOKEN="$(cat ~/anneal-env/$proj/token)" \
    -e ANNEAL_LISTEN=127.0.0.1:7777 \
    -e ANNEAL_IDLE_TIMEOUT_S=900 \
    -v "$HOME/anneal-env/$proj:/anneal" \
    -p 127.0.0.1:7777:7777 anneal:dev
}
```

…then `anneal -server 127.0.0.1:7777` (or switch backends in the GUI). One
host port per running instance; idle containers exit themselves, so several
projects can share a port over time.

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
| `-allow a,b,*` | auto-answer permission prompts by action or tool name (anything not allowed → denied) |
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
  improvement library (Harness view); making one permanent is your explicit
  call: two-click **promote** in the pane, or `/harness promote <id>` in the
  headless REPL (with backups). Full records: `~/.anneal/improvements/`.
  The background reviewer feeds the loop too: recurring work-style
  deficiencies it observes get queued via `harness_flag`
  (`~/.anneal/harness-queue.json`) and the next `/improve` consumes them as
  hints — evidence still decides.
- **Harness pane**: edits to prose components take effect next run; guardrail
  `deny:/ask:/allow:` rule lines need a restart.

## Scripting the UI

- **Agent-side `script` tool:** when the agent needs data glue — parse
  session JSONL, reshape CSV, tabulate harness files — it runs a Risor v2
  script in a sandboxed in-process VM (json/csv/strings/filepath/time +
  confined fs; no commands, no network). You see an approval card like
  any write-capable tool. Details: `docs/CONFIG.md` §`script`.
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
