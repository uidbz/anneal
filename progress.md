# progress.md

Running status log for **fyagent**. Keep this updated at every commit: tick milestones,
add dated notes, and record decisions/deviations from `docs/PLAN.md`.

## Milestones

- [x] **M0** Skeleton: module + deps, GUI opens, Go↔Risor event round-trip
- [x] **M1** Config + Strata provider + HealthMonitor + settings tab
- [x] **M2** Agent loop + streaming chat + toolkit + stop + usage status
- [x] **M3** Sessions UI + permission modes + approval bridge + todo
- [ ] **M4** tie index + session_search + compaction
- [ ] **M5** memory stores + memory tool + staging
- [ ] **M6** skills + skill_manage + subagents + slash commands
- [ ] **M7** background review + journey + notifications
- [ ] **M8** MCP + polish
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
