# progress.md

Running status log for **fyagent**. Keep this updated at every commit: tick milestones,
add dated notes, and record decisions/deviations from `docs/PLAN.md`.

## Milestones

- [x] **M0** Skeleton: module + deps, GUI opens, Go↔Risor event round-trip
- [x] **M1** Config + Strata provider + HealthMonitor + settings tab
- [x] **M2** Agent loop + streaming chat + toolkit + stop + usage status
- [x] **M3** Sessions UI + permission modes + approval bridge + todo
- [x] **M4** tie index + session_search + compaction
- [x] **M5** memory stores + memory tool + staging
- [x] **M6** skills + skill_manage + subagents + slash commands
- [x] **M7** background review + journey + notifications
- [x] **M8** MCP + polish
- [ ] **M9** headless CLI (fyagent-headless)

Legend: mark `[~]` in progress, `[x]` done (note the commit hash in the notes below).

## Environment (verified 2026-10-24)

| Component | Where | Notes |
|---|---|---|
| Go | 1.27.2 | CGO/Fyne toolchain working |
| Strata serve | `http://127.0.0.1:8081` | `qwen3.8-flash-next-iq3_s`, n_ctx 262144; default port is 8080 but 8080 is taken by pwplay-server |
| tie triplestore | `http://storage:1161` | config `~/.config/tie/config.toml`; filehosts `:1162`; reverse-index = index-all unless server restricts |
| dive | pinned in go.mod | cloned reference at `/tmp/opencode/dive` (commit 50455f2) |
| fynerisor | v0.9.0 | whole GUI in Risor; missing widgets go upstream |

## Decisions

- 2026-10-24 — name `fyagent`; tie instead of SQLite; whole GUI in Risor; MCP + headless in first delivery (user).
- 2026-10-24 — dive JSONL FileStore stays conversation truth; tie is the rebuildable search index (collection `fyagent`). Word-token triples give exact-match AND search (tie has no FTS).
- 2026-10-24 — git: local repo, conventional commits per milestone/sub-step.

## Upstream TODO (fynerisor)

- [ ] `Markdown.SetText(string)` / writable `Text` attr (chat transcripts need updatable blocks)
- [ ] (nice) constants exposure for `fyne.TextWrap*` instead of raw ints

## Notes (newest first)

### 2026-10-24 — fynerisor gaps fixed upstream (issues #2–#5 closed)
- User green-lit fixing fynerisor directly (issue-first). Local checkout `/go/src/github.com/uidbz/fynerisor`; fyagent `go.mod` now `replace`s fynerisor to it until a release ships these.
- Shipped to fynerisor main: **a0280fb** app.Quit/window.Close (#2), **45e3eca** Markdown SetText/.Text (#3), **dc84d6b** container Append/Remove/Refresh + Border setters + Scroll.SetContent (#4), **1c0e93f** tie.connect options vs config.toml (#5). Tests added upstream; all green (pre-existing tie test was #5 itself).
- Bug #5 mattered for M4: a local `~/.config/tie/config.toml` with `[Collections.x]` + `DefaultCollection` silently hijacked namespace/collection **and** the connect URL.
- Verified live: fyagent boots against patched fynerisor; NEWAPI-OK script probe (Append/Remove, SetText, SetTop, SetContent, Quit/Close present). UI workarounds (re-compose) still in place — adopt Append/SetText opportunistically from M4 on.

### 2026-10-24 — M8 done (MCP)
- `config.MCP{Enabled, Servers}` — Anthropic-mcp.json-shaped (command/args/env/url, type inferred stdio|http); dive `experimental/mcp` (own module, tagged separately — v1.9.0).
- App: `ConnectMCP` at Start (async), `mcp.state` event (per-server connected/tools), UI status line `mcp: 1/1 up, N tools`; MCP tools re-read per run (live after refresh) and **permission-gated** like any non-allowlisted tool.
- Tools renamed to Claude-Code-style `mcp__<server>__<tool>` (v1.9.0 keeps the helper unexported; local `qualifiedName`): unqualified names collided with built-ins and misled models.
- **dive bug found + upstreamed: [deepnoodle-ai/dive #347](https://github.com/deepnoodle-ai/dive/issues/347)** — dive core passes tool input as plain `[]byte`, which `ToolAdapter.Call`'s type switch misses → default branch base64-mangles it → *every* MCP call failed with "cannot unmarshal string into map". Workaround in fyagent: wrapper normalizes input to `json.RawMessage` (+ unwraps double-encoded JSON strings some local servers emit). Unit-tested (`TestRepairInputShapes`).
- Debug war story: the `%q` log rendering fooled me into thinking input was double-quoted (`%q` adds its own quotes) — root cause was the []byte branch all along; a capture-tool unit test cracked it in minutes vs three live runs of guessing.
- e2e gate (live): stdio demo server (mcp-go) connects at boot → model calls `mcp__demo__echo` (approval prompt granted) → `ECHO(hello mcp)` returned verbatim.
- Next: M9 headless CLI (fyagent-headless).

### 2026-10-24 — M7 done (background review + journey + notifications)
- `core/review`: queue of finished sessions (dedupe) + idle scheduler: runs only when not busy and idle ≥ `review.idle_settle_s`, max-age (`max_age_s`) fallback, batch drain, requeue on preemption (foreground `Send` cancels via `Interrupt`), `LearnNow()` manual trigger.
- Reviewer = bounded dive agent (iter cap 6): Read/Glob/Grep/ListDir + memory + skill_manage + session_search; `source:"review"`; respects write_approval staging (emits memory.stage/skill.stage cards like the main tools). **Tool ledger** drives notifications — never model prose (guards fake-save claims).
- Digests: `Runner.Digest` renders bounded per-message transcripts (600-char clips, MaxInputTokens*4 bytes) from the FileStore.
- `core/journey`: ~/.fyagent/journey.jsonl timeline via a Bridge tap (memory/skill writes incl. staged-approvals + review batches); `/journey` + `/learn` slash cmds; review.start/done/skipped → status + transcript notices (ledger lines listed).
- Tests: scripted-LLM reviewer tests (apply+ledger exactness, staging events+pending source=review), idle/max-age/busy gating. Live e2e: run → idle 3s → reviewer PATCHED the memory entry (consolidation) → journey shows 2 entries.
- Bridge: `agent.learnNow()`, `agent.journey(n)`.
- Next: M8 MCP + polish.

### 2026-10-24 — M6 done (skills + skill_manage + subagents + slash commands)
- `core/skills`: SKILL.md dirs under `~/.fyagent/skills` (agentskills frontmatter, validated by dive's `skill.ParseContent`); create/patch (unique-substring)/write_file (path-escape guarded)/remove→`skills-archive/<name>-<ts>`; staging mirror of memory (`pending/skills/<id>.json`, cap 10, `Preview` diff for UI).
- Skills at runtime: dive `skill.Loader` as **Extension** per run — dirs: `~/.fyagent/skills`, `./.fyagent/skills`, project `.claude/.agents/.dive` read-compat + `skills.extra_dirs`; gives the `Skill` tool + catalog reminder + rules for free. New skills visible next run (loader rebuilt per run).
- Subagents: dive `toolkit/orchestration.NewAgentTool` with **custom AgentFactory** — default factory spawns UNGUARDED agents; ours re-attaches `permission.HookFromManager` + iteration cap 40. Types: general-purpose / explore / plan.
- `skill_manage` FuncTool (list/view/create/patch/write_file/remove) + staging + UI cards (`respondSkill`).
- UI: slash commands `/help /new /sessions /plan /build /skills /memory /index /quit`; keybinds Ctrl+N (new), Ctrl+B (sessions), Ctrl+D (stop) via `window.AddShortcut`.
- Config: default allow list now includes `session_search memory skill_manage Skill` (their safety gate is write_approval staging); `Agent` keeps prompting.
- **fynerisor issue #6** filed+fixed upstream (0b826e9): `Window.CompileScript()` (risor.Compile wrapper). New `fyagent -check-ui` flag; main.risor now compile-checked at EVERY boot (fail-fast, kills the M2 "black window" failure class). Found a stale-`else` bug in the M4 UI code this way — the main UI had never been compiled since M3!
- e2e (live Strata): create markhor-skill → next session `Skill` tool loads it → answer begins SNOWLEOPARD; Agent(explore) subagent finds add.go through its own tools; skill store unit tests all green.
- Next: M7 background review + journey + notifications.

### 2026-10-24 — M5 done (memory stores + memory tool + staging)
- `core/memory`: MEMORY.md/USER.md under `~/.fyagent/memories`, caps 2200/1375 (config), atomic writes; ops add/patch/remove (patch/remove = **unique-substring** match, count-checked), dup-guard (case/whitespace-normalized entry compare), final-size batch check, injection scanner (instruction-override/role-forgery/`<system-reminder` patterns rejected — memory is re-injected forever).
- Staging (`memory.write_approval=true`): ops persist `~/.fyagent/pending/memory/<id>.json`, dedupe identical, queue cap 10, survive restart (`agent.memoryPending()`); `respondMemory(id, approve|reject)` → apply/drop + `memory.stage`/`memory.resolved` events; tool tells the model it is NOT saved yet (no fake-save prose).
- Snapshot injection: PreGeneration hook → `dive.SetSystemReminder(msgs, "fyagent-memory", snapshot)` — named block in first user message, **frozen per session** (only inserted if absent), compaction-safe (re-added if lost). UI: memory approval cards (approve/reject) in the inbox row.
- System prompt: learning-loop rules (what belongs in memory vs session_search; patch stale entries).
- e2e (live Strata): direct add → new session → model recalls "tabs for indentation" **without tools** (snapshot worked); staging: mid-check file untouched → approve → updated; memory unit tests (caps, dup, injection, queue, resolve).
- Risor gotcha: `else` must be on the same line as the closing `}` (newline-terminated statements) — split `}\nelse {` = parse error.
- Next: M6 skills + skill_manage + subagents + slash commands.

### 2026-10-24 — M4 done (tie index + session_search + compaction)
- `core/tieindex`: rebuildable word-token index in tie collection `fyagent` (schema in §4 of PLAN); msgID=sha1(sess:pos); incremental per-session indexing after each run (`-rebuild-index` flag + `agent.rebuildIndex()` for full rebuilds); degraded mode never blocks the agent (probe = any HTTP answer counts).
- **tie gotcha (big)**: triplestore queries match by **key** unless `Reverse: true`; and reverse hits only cover the server's **reverse-relation whitelist** — the live server reverse-indexes `tag` + `tie-type` only. Private relations (`word`) are invisible to value search. Schema fix: tokens under relation **`tag`**; role/snippet/etc as forward attrs via `Expand`. (Recorded in tie skill.)
- `session_search` tool (FuncTool, ReadOnlyHint): token AND-search via `QuerySpec{Terms, Reverse, Scope:"session-msg", SortByValue:"msg-at"}` + client-side collision filter; grouped hits w/ title/date/snippet.
- Compaction: custom `preGenCompactionHook` (PreGeneration, est tokens≈bytes/4, `CompactMessages`, state event) + `compaction.MidTurnCompactionHook` (in-run, notify → `compaction.done` UI line); threshold = ctxWindow × pct (82% default), floor 8000.
- UI: tie status label in header (`tie.state`), `tie.rebuilt` + `compaction.done` transcript notices.
- Gates: live tie roundtrip **search latency ~2 ms** (gate <50 ms ✔); e2e cross-launch recall OK (driver A marks session with "markhor mountains" → driver B fresh session → model calls session_search → snippet found); compaction hook unit-tested w/ scripted recap LLM (+ threshold math).
- Next: M5 memory stores + memory tool + staging.

### 2026-10-24 — M3 done (sessions + permissions + plan/build + todo)
- Sessions: dive `session.FileStore` under `~/.fyagent/sessions`; boot resumes most recent; sidebar lists/restores (transcript replay as normalized blocks); title = first task (flushed via `store.Put` after run — appends never rewrite the header).
- Permissions: `permission.Manager` + `HookFromManager` PreToolUse; approval inbox bridged via `dive.Dialog` impl (event out, channel back; interrupt ⇒ deny). Actions: allow / session-grant (`AllowSession`, exact-specifier semantics from dive) / deny.
- **Gotcha**: dive evaluates ask rules *before* the mode gate, so plan mode would *prompt* instead of deny — the Dialog now auto-denies non-`ReadOnlyHint` tools in plan mode. todo tool marked ReadOnlyHint (planning is read-only).
- Modes: plan/build toggle (`agent.setMode`), persisted in config; plan adds a system-prompt note.
- todo tool (`dive.FuncTool`): full-list `set`/`list`; `todo.updated` events → right panel; list persisted in session **metadata** and restored on resume/reopen.
- Race fixed: `running` must clear *before* `run.finished` is emitted (UI chained-send hit `run.busy`).
- e2e (live Strata, 4 phases): Bash ask→session grant→ok; todo events; plan mode: Write denied with NO prompt, Glob allowed; sessions n=1, reopen replay 40 blocks, todos restored. (One earlier e2e run died mid-Bash ~50s, rc=0, unreproduced twice since — watching.)
- `timeout`+fyne note: this fyne honors SIGTERM → graceful exit (rc=0 under `timeout` when the *harness* kills the wrapper); don't read too much into rc.

### 2026-10-24 — fynerisor gap tracking + upstream issues
- New living tracker `docs/FYNERISOR-GAPS.md`; policy: hit a gap → add row → file issue (gh, uidbz).
- Filed: **uidbz/fynerisor #2** app.Quit/window.Close unbound (docs promise them), **#3** Markdown.SetText, **#4** container Append/Remove/Refresh.
- Correction: `constants` global **does** expose Fyne enums (`constants.TextWrapWord` etc.) — main.risor now uses them instead of magic ints.

### 2026-10-24 — M2 done (agent loop + streaming chat) + black-window debug
- `core/agent`: dive Runner (toolkit Read/Write/Edit/TextEditor/Glob/Grep/ListDir/Bash/Fetch, in-mem session, iteration cap), delta batching (240 chars/60 ms), normalized event payloads (every key always present).
- **E2E gate on live Strata** (twice): fixed a seeded bug in `/tmp/opencode/demo` (`a-b`→`a+b`) via Edit tool, verified with Bash `go test`, streamed summary, `run.finished ok=true`.
- Black-window report debugged: Risor **parse/compile errors killed the script before SetContent**. Culprits: map key `in` (`u.in` → reserved word → renamed `tin/tout`); closures capture free vars at compile time (placeholder-rebind pattern for circular layouts); no `map` builtin (recomposed layout via array `+`). Verified rendering with swaymsg + grim + pixel stats (theme #181819, widgets present). sway tiles full-size — window "size" is the tile, pre-show Resize ignored (GLFW/Wayland).
- fynerisor learnings recorded as a skill: **~/.agents/skills/fynerisor/SKILL.md** (bridge pattern, functionCalls/Ready threading, Risor v2 gotchas, `strings.trim(s,cutset)` arity, `json.parse`, no `strings.replace`/`map`, app.Quit unbound → `agent.quit`, GoFunc reflection limits, debug via `risor.Eval` + file bisection).
- Next: M3 sessions + permissions + plan/build modes.

### 2026-10-24 — M1 done (config + Strata health)
- `core/paths`, `core/config` (JSON, defaults, env overrides, atomic save), `core/model` (dive provider factory + HealthMonitor), `core/app` (glue: AgentAPI = bridge + provider/config/probe/saveConfig).
- Health states verified live: pwplay-on-8080 → `wrong_service` (404 on /v1/models), Strata :8081 → `online model=qwen3.8-flash-next-iq3_s`, ctx 262144 reported.
- fynerisor threading truth: `window.Do` sends on the `functionCalls` channel **created in `Execute()`**; callbacks run on the script goroutine (serialized with main). Bridge now gates delivery behind `Ready()` (called after Execute) — events before that are queued. vmguard contention retry kept as defense.
- Next: M2 dive agent + streaming chat transcript.

### 2026-10-24 — M0 done (skeleton + bridge)
- `cmd/fyagent` boots fynerisor, embeds `assets/ui/main.risor` (`FYAGENT_UI_DIR` overrides for hot dev).
- Bridge (`core/uiapi`): `agent.on(cb)` captures the VM CallFunc; Go→script events dispatch as JSON strings via `fw.Do` + `vmguard.Call`, reschedule-on-contention. Startup `agent.ping()` round-trip verified live: `ui: [001 pong]`.
- fynerisor gap (upstream candidate): **Markdown has no `SetText`/`Text`** — transcript design uses writable `Label` for streaming + appended immutable `Markdown` blocks.
- Risor v2 notes for the UI layer: **no `while`/`for` loops, no ternary**; no list `.len()`/`.slice()` (builtin `len()` exists, no `slice()`); `if (cond)` needs parens; json module is `json.parse`/`json.marshal`; `strings.join` via `@strings`.
- Next: M1 config + Strata health.

### 2026-10-24 — repo initialized
- git repo created (`main` branch); `docs/PLAN.md` (full plan), `progress.md`, README, .gitignore committed.
- Next: M0 — go.mod + skeleton GUI + bridge echo test.
