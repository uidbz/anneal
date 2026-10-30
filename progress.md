# progress.md

Running status log for **Anneal**. Keep this updated at every commit: tick milestones,
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
- [x] **M9** headless CLI (anneal-headless)
- [x] **M10** editable harness components + Harness² plan
- [x] **M11** sandboxed probe/replay infrastructure
- [x] **M12** contrastive Propose–Probe–Compose step (k=1)
- [x] **M13** improvement library + evidence-gated reflection
- [x] **M14** k-step recursion with governors and resume
- [x] **M15** Go harness panel (now a scripted `CanvasObject` pane)
- [x] **M16** review→improve bridge via `harness_flag`
- [x] **M17** backend/frontend split: `anneald` + wire protocol, remote headless, container image with idle lifecycle
- [~] **H1** hybrid Go shell + scripted panes (rename to Anneal)

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

- 2026-10-24 — name `anneal`; tie instead of SQLite; whole GUI in Risor; MCP + headless in first delivery (user).
- 2026-10-24 — dive JSONL FileStore stays conversation truth; tie is the rebuildable search index (collection `anneal`). Word-token triples give exact-match AND search (tie has no FTS).
- 2026-10-24 — git: local repo, conventional commits per milestone/sub-step.
- 2026-10-26 — fynerisor dependency: replace-directive removed; go.mod pins the published pseudo-version `v0.9.1-0.20261026124728-058fa5aa952a` (= `origin/main` of fynerisor at 058fa5a, the last commit before the upstream `vmguard`→`vmexec` refactor). Reproducible on any machine without the `/go/src` container path; bump when anneal migrates to `vmexec`.

## Upstream TODO (fynerisor)

- [ ] `Markdown.SetText(string)` / writable `Text` attr (chat transcripts need updatable blocks)
- [ ] (nice) constants exposure for `fyne.TextWrap*` instead of raw ints

## Notes (newest first)

### 2026-10-30 — M17 review round (issues #2–#12)
- Post-implementation review (self + fresh-eyes pass) found 11 corrections;
  one GitHub issue each under tracking issue #1, fixed one by one.
- Highest-impact: the hello read deadline persisted into the RPC loop (every
  idle connection died at 10 s) — fixed + ping/pong keepalive (30s/75s,
  documented in PROTOCOL.md); client events channel never closed on death
  (goroutine leak) + a genuine send-on-closed race found by the new flood
  test; headless -server hung for up to 60 min when anneald died mid-run.
- Policy hardening: tokenless anneald now refuses browser-origin handshakes
  (no CORS on WS) and wildcard/remote TCP binds — loopback TCP + 0600 unix
  sockets remain the default trust model.
- Lifecycle correctness: busy edges (run/improve/review start *and finish*)
  fold into the idle timer so a long improve no longer triggers instant exit
  the moment it completes.
- Docker: global ARG VERSION was invisible in the build stage (empty stamp);
  re-declared; dropreplace comment corrected (committed go.mod has no
  replace; it is defensive for dev trees).
- Conformance: notifications never get replies; parse errors reply with
  id null; invalid-request frames get -32600; harness RPC surface mirrors
  the local AgentAPI (restart_for_rules, warns, promote journey entry);
  index.rebuild is single-flight.
- New tests: idle-survives-handshake-deadline, unix roundtrip (+0600
  check), slow-consumer disconnect + survivor, notification no-reply,
  origin rejection, tokenless-bind policy, client teardown, proto mismatch.

### 2026-10-30 — M17: backend/frontend split (anneald + protocol + containers)
- New backend binary `cmd/anneald` hosts the core behind a WebSocket JSON-RPC
  protocol (`core/server`): hello handshake (`proto` + optional bearer token
  via `server.auth_token_env`), a method table mapping 1:1 onto the
  AgentAPI/Runner surface, and one `Bus.AddTap` fanned out per connection
  (bounded queue 1024; slow consumers are disconnected, never blocking the
  bus). Events are the raw bus objects — identical to what the GUI/headless
  sinks render. Normative spec: docs/PROTOCOL.md.
- `core/client`: protocol client (dial unix:/ws URLs, Call/CallInto, ordered
  event channel with newest-shed backpressure). Validated by
  `anneal-headless -server <addr>`: local vs remote core behind a small
  `backend` interface; sink/run-correlation unchanged.
- Config gains the `server` group (listen/auth/idle_timeout_s/drain_grace_s/
  work_dir) + env overrides (`ANNEAL_LISTEN`, `ANNEAL_IDLE_TIMEOUT_S`,
  `ANNEAL_WORK_DIR`, `ANNEAL_AUTH_TOKEN`). Idle shutdown: no client frame
  and no run/improve activity for N s → `server.shutting_down` → exit after
  drain grace; a run in flight defers the timer.
- Containers: Dockerfile (dropreplace fynerisor — server binary needs only
  its GUI-free core; CGO-free static build) + default dev toolchain; one
  bind mount (`state/` = ANNEAL_HOME, `work/` = agent cwd). One project/user
  per instance is the scoping model; idle-exit makes instances disposable.
  Guide: docs/CONTAINERS.md. `make docker`, `make anneald`.
- Decisions: single foreground run per server (unchanged), permission cards
  global across clients (any client may answer), session/project scoping by
  *instance-per-scope* — no in-server project registry (would force
  FileStore/cwd hot-swaps for no benefit given cheap idle-exit instances).
  GUI remote mode deliberately deferred: its ~30 direct call sites need the
  RPC surface proven stable first.
- Codename cleanup: the pre-release codename's migration shims (state-dir
  rename, legacy reminder detection) removed — never shipped, no legacy
  state exists.
- Verified: `make test` + new `core/server` tests (ping/auth/fanout/idle/
  quit), live smoke: anneald ↔ headless -server (send → provider-unreachable
  error path, exit 1), Docker run with token auth (bad token rejected),
  healthz, and idle self-exit of the container.

### 2026-10-27 — OpenCode-style permission system
- Reimplemented OpenCode v2's permission model in `core/permission`: ordered
  `{action, resource, effect}` rules with last-match-wins wildcards (`*`, `?`,
  optional-argument `"cmd *"`), `allow`/`ask`/`deny`, `once`/`always`/`reject`
  replies, and project-scoped saved grants (`~/.anneal/permissions.json`).
  Reference: OpenCode v2.0.30 `packages/core/src/permission.ts`,
  `permission/saved.ts`, `v1/config/permission.ts`, `shell/parse.ts`, and
  `packages/web/src/content/docs/permissions.mdx`.
- Config now has the OpenCode shapes: top-level `permission` (string, action →
  effect, or action → resource → effect) plus an `agent.permission` override.
  Legacy `agent.allow/ask/deny` are translated to the same actions and
  evaluated first, so existing configs keep working and new rules can override
  them. Harness guardrails evaluate last. Default `.env` read asks match
  OpenCode's secret-file guard; anneal keeps its safer ask-on-write/bash
  defaults (add `"*": "allow"` for OpenCode's permissive default).
- Tool mapping: Read/ListDirectory → `read`, Write/Edit/TextEditor → `edit`,
  Bash → `bash` (per-segment resources + safe `always` prefixes), Glob/Grep,
  WebFetch, todo → `todowrite`, Skill → `skill`, Agent → `task`, plus
  `external_directory` (symlink-resolved; checked first) and `doom_loop`
  (three identical calls). MCP tools use their qualified name as the action.
- Approval bridge no longer uses dive's `permission.Manager`/`Dialog`; the
  custom PreToolUse hook works for subagents too. UI buttons are now
  once/always/reject (legacy `allow|session|deny` still accepted by
  `agent.respondPerm`); `always` saves the suggested patterns and immediately
  releases matching pending cards. Headless `-allow` matches action or tool.
- Tests: `core/permission` (wildcards, ordered config, compound commands,
  saved grants, modes) and `core/agent` (default policy, external paths, shell
  parsing, doom loop, always-release). `go test ./...` green.

### 2026-10-26 — configurable text size, responsive streaming, selectable text, gortex
- **Text size**: `ui.text_scale` (0.6–2.5) applied via a theme wrapper that
  multiplies text/caption/heading/inline-icon sizes. Controls: header `A-`/`A+`,
  Ctrl+= / Ctrl+- / Ctrl+0, `/textsize [120%|+10%|1.2|reset]`, or
  `ANNEAL_TEXT_SCALE`. The base theme is captured once so repeated config
  changes never compound the multiplier.
- **Streaming responsiveness**: a word-wrapped Fyne label re-lays out its whole
  text on every `SetText`; one tail label growing to ~20 KB froze the UI (and
  the stop button) for seconds. Streamed text is now frozen into ~2 KB
  immutable chunks with only a small live tail. Measured in the test driver:
  180 KB of text went from ~60 s of cumulative layout to ~4 s, bounded per
  update.
- **Selectable transcript**: user/tool/notice/todo/activity labels are
  selectable. Assistant markdown is rendered as selectable plain text
  (Fyne's `RichText` has no selection support); syntax is stripped by
  `markdownToPlain`.
- **Todo panel**: fixed 260 px min width and hidden while empty; it no longer
  collapses to the minimum width of its wrapped labels.
- **Stop**: the run context is created in `StartTask` (no stop/start race),
  `clearRunning` cancels it, and a cancelled run reports `stopped: true` instead
  of a provider error. UI status shows `stopping…` then `stopped`.
- **gortex integration**: when `gortex` is on `PATH`, Anneal auto-registers its
  MCP server (`mcp.auto_detect`, default true) with the read-only
  `localization` preset; only `explore`/`batch_symbols`/
  `find_implementations` (plus catalog discovery) are allowlisted. Verified
  live: 14 tools connected, `mcp__gortex__explore` located `SetTextScale`.
- **Prompt**: dependency work now checks `go env GOMODCACHE` / `vendor/`
  before WebFetch; code navigation prefers the gortex tools when present.

### 2026-10-26 — `script` tool: sandboxed Risor data-glue for the agent
- New `core/script`: agent-authored Risor v2 runs headless on fynerisor's GUI-free `core` runtime — fresh VM per call, static `AnalyzeRequirements` module gate before anything executes, 30s ctx timeout (the VM honors `ctx.Done()`), result JSON-capped and `print()` output capped at 8 KiB.
- Capability set: `json`, `csv`, `strings`, `filepath`, `time` + host-injected `fs` (read/write/list/exists/remove) and `import()`/`app` deliberately disabled. **No exec/os/http** in v1: external commands stay on the Bash permission gate (fynerisor `exec` policy-hook is the phase-2 path).
- `fs` confinement enforced inside the VM: read roots = project + `~/.anneal`; write roots = project + `~/.anneal/scratch`; symlink-resolved containment; secret dot-dirs (`.ssh/.aws/.gnupg/.azure/.kube/.docker`, `~/.config/gh`) denied regardless. Denials are Go errors from builtins → the VM raises them, so a denied call cannot be swallowed as a value (fynerisor's `object.Errorf`-as-value style returns the error object *as data*; Risor v2 does not abort on it).
- Tool wiring: `core/agent/script.go` (`dive.FuncTool "script"`), gated by `agent.script` (default true), ask-by-default in `agent.ask` (no ReadOnlyHint → plan mode denies, dontAsk denies unless `-allow script`); subagents inherit it. The background reviewer keeps its narrower toolset for now.
- Dependency note: the `/go/src` replace died with this change (path exists only in the build container); pinned to the fynerisor `origin/main` pseudo-version instead (see Decisions). `anneal-headless` still builds with zero Fyne deps (`go list -deps` count 0).
- Tests: `core/script` (module gate, parse-error no-side-effect, timeout kill, fs roots/deny-list/symlink-escape, output caps, print capture) + `core/agent/script_test.go` smoke. All hermetic; full `go test ./...` green.
- Next: exec policy-hook upstream (then `Script(exec)` grants), optional `anneal` global module (sessions/tie as scriptable objects), maybe reviewer gets the tool.

### 2026-10-25 — rename to **Anneal** + hybrid UI Phase 1
- Name decision: **Anneal** (heat/cool cycles harden metal; maps to
  Propose→Probe→Compose). Kept tie as an integration, not a `tie-agent`
  prefix. `Gyre`/`Haft`/`Hone`/`Weft`/`Whetstone`/`Skein`/`Gimlet` were
  rejected after finding collisions in adjacent agent tooling.
- Renamed module/imports (`anneal`), binaries (`anneal`, `anneal-headless`),
  and docs; initially added `anneal-legacy` for the original all-Risor UI and
  e2e drivers (later removed in Phase 6).
- Rename hygiene: the pre-release codename was never shipped, so no
  compatibility shims are kept (no state-directory migration, no env
  aliases, no legacy reminder detection; the `agent` script global and
  existing tie collection values stay).
- New hybrid shell (`cmd/anneal`): Go/Fyne transcript/composer/permission
  inbox/todos/status/session picker, plus the harness editor/library as an
  embedded `gui.NewCanvasObject` pane (`assets/ui/harness.risor`).
- Bridge: `AddTap` (compose observers), `SetTapOnly` (Go UI, no JSON queue),
  typed `Runner.Sessions()`, narrow `App.HarnessAPI()`.
- Tests: paths migration/env precedence, config legacy env alias, tap
  composition/tap-only, shell event rendering with the Fyne test driver.
- Phase 3 progress: memory/skill staged approval cards in the Go shell, a
  `settings` dialog editing the full `config.json`, and Ctrl+N/B/D/R/Q
  shortcuts.
- Phase 4: `core/events.Bus` is now the UI-agnostic fan-out; `core/uiapi` is
  only the script adapter. `go list -deps ./cmd/anneal-headless` has no
  Fyne/fynerisor/risor dependency. The harness pane no longer receives
  `WithHostWindow` (no script access to close/dialogs/shortcuts).
- Phase 2 first step: transcript renders at most 300 blocks and restores older
  blocks on demand, so long sessions don't re-layout every historical widget
  per event.
- Phase 5: pane has no host-window capability; Ctrl+R reloads it; a filtered
  `agent.on` feed (harness/improve lifecycle only) updates a notice label
  without rebuilding or losing unsaved editor text. Capabilities are explicit
  (`paneCapabilities`), with pull-only as the default.
- Accessibility/composer: Ctrl+L focuses the entry, focus is restored after
  send/session changes, and `-send`/`-quit-after-run` verified the real
  composer path (input → submit → streamed reply → persisted session → exit).
  License added: BSD 3-Clause (same as tie).
- Phase 6 cleanup: removed `anneal-legacy` and the embedded full UI; full
  scripted UIs run via `anneal -script-ui` (e2e/custom hacking). Removed
  the codename's env aliases, legacy skills read path, old binary symlinks,
  and the root build artifact. (The one-time state-directory migration and
  legacy reminder-detection shims were also removed later — the codename
  never shipped, so there is no legacy state to migrate.)
- Activity tab: journey timeline + improvement-run records with candidate
  outcomes/decisions/successor/library entry; Ctrl+1/2/3 switch tabs.
- Local checkout renamed `fynerisor-agent` → `~/src/anneal`; GitHub remote is
  `uidbz/anneal`.
- `make install` installs binaries plus the XDG `anneal.desktop` launcher and
  hicolor icon; `make uninstall` and `DESTDIR`/`PREFIX` overrides included.
- Gates: build, vet, hermetic tests, race, legacy `-check-ui`, pane
  `-check-ui`; manual launch/screenshot of the hybrid shell and legacy replay.

### 2026-10-25 — full code review + fix pass ([codereview.md](codereview.md))
- Reviewed every first-party Go file + the Risor UI. Fixed the provider-probe
  nil-response panic (down server crashed the monitor), the probe jail denying
  its own sandbox under `~/.anneal`, `Bridge.Ready` dropping pre-handler
  events, headless double tie/MCP init + out-of-order event goroutines, the
  review scheduler wake race, selector "kept last" mismatch, improve token
  accounting/wall budget, staged-op id traversal, MCP attach race, live config
  copies, and tracked build artifacts. Typed `HarnessStatus`, async
  `agent.probe`, `StartBlocking`, graceful `Bridge.Quit`, defaults-preserving
  `Config.Apply`, forwarded `reasoning_effort`.
- New regression tests: provider monitor (4), bridge queue/tap (3), probe jail
  sandbox/traversal/clean-workspace (3), config defaults/apply (3), improve
  selector/hash/token (2), staged-id rejection (2), flag release (1), harness
  status (1), promote-without-backup (1).
- Gate: `go vet`, `go test`, `go test -race ./core/...`, `make build`,
  `anneal -check-ui` all green.
- Deferred items were completed in a second pass: `core/toolset` (one
  memory/skill tool implementation for the agent + reviewer), headless run-id
  correlation (late `run.finished` from an aborted run can no longer satisfy
  the next task), an explicit library `hasExecutionEvidence` predicate,
  `core/textutil.Clip`, `core/fsutil` atomic write/flat copy, and an opt-in
  `traceEvents` UI fallback for unknown events.

### 2026-10-24 — fynerisor gaps fixed upstream (issues #2–#5 closed)
- User green-lit fixing fynerisor directly (issue-first). Local checkout `/go/src/github.com/uidbz/fynerisor`; anneal `go.mod` now `replace`s fynerisor to it until a release ships these.
- Shipped to fynerisor main: **a0280fb** app.Quit/window.Close (#2), **45e3eca** Markdown SetText/.Text (#3), **dc84d6b** container Append/Remove/Refresh + Border setters + Scroll.SetContent (#4), **1c0e93f** tie.connect options vs config.toml (#5). Tests added upstream; all green (pre-existing tie test was #5 itself).
- Bug #5 mattered for M4: a local `~/.config/tie/config.toml` with `[Collections.x]` + `DefaultCollection` silently hijacked namespace/collection **and** the connect URL.
- Verified live: anneal boots against patched fynerisor; NEWAPI-OK script probe (Append/Remove, SetText, SetTop, SetContent, Quit/Close present). UI workarounds (re-compose) still in place — adopt Append/SetText opportunistically from M4 on.

### 2026-10-25 — M16 review→improve bridge (+ closing docs pass)
- `review.FlagStore` (`~/.anneal/harness-queue.json`, atomic writes, oldest
  first, cap w/ consumed-history): reviewer gets a `harness_flag` tool —
  its ONLY harness output (it has no write tools at all, so "reviewer never
  edits the harness" is structural, not prompt-based).
- `improve` Options.Hints → `hintsBlock()` appended to the editors'/
  composer's context, framed as claims to CHECK (probe evidence still
  decides); consumed flags recorded in step.json (`hints`), marked
  `consumed_by=<runid>`. app.ImproveWith pre-computes runid and drains up
  to 6 flags per step.
- Surfaces: `review.flag` events (UI line + journey `review-flag`),
  GUI panel "reviewer flags" section + "improve last task" button,
  headless `/harness` shows pending flags, printImprove lists consumed
  hints.
- Gotcha: headless `-p` only runs the learning pass with `-learn` — the
  first m16 gate silently reviewed nothing until the flag was added.
- Live gate (`make live-m16-test`, ~5 min): reviewer flagged a REAL
  deficiency ("re-sends the identical failing tool call unchanged across
  many turns…"), queue file valid, flag visible in `/harness`; 4 memory
  lessons still flowed. Hermetic: store lifecycle/persistence, tool
  rejects <8-char gaps, hints reach editor+composer prompts, Result.Hints
  recorded.

### 2026-10-25 — M15 GUI harness panel
- New bridge APIs: `harnessFiles/harnessGet/harnessSave` (path jail: only
  `*.md` at root or under `subagents/`, no `..`/absolute/backslash),
  `libExperience`, `libPromote`; `harness.warn`/`harness.promoted` events;
  promote also lands in the journey (`harness-promote` kind).
- UI: "harness" toolbar button + `/harness` open a viewMode panel —
  component files as MultiLineEntry editors with save buttons (prose
  applies next run; guardrail RULE lines need restart — stated inline),
  library list with experience viewer and TWO-CLICK promote (first click
  arms, second commits; backup path shown). compose() swaps the center;
  panel widgets persist across recomposes (entries aren't rebuilt unless
  dirty), so typing survives transcript events.
- Risor gotchas hit again: identifiers referenced from earlier callbacks
  need `let` placeholder slots (openHarness/buildHarness); plain
  `name = fn` assignment for a never-declared name is a compile error
  (closeHarness needed its own `let`).
- Prompt fix found by the smoke: componentRules advertised a `scripts.md`
  component that doesn't exist (the scripts layer is GENERATED from the
  scripts/ dir manifest) — composers wrote it and promotion warned
  "unknown entry". Now the prompt lists subagents/<name>.md and forbids
  inventing files; stale scripts.md purged from test-home entries.
- Verified: harness Files/jail tests, harnesslib Experience + id-collision
  test, GUI smoke driver (`ANNEAL_UI_DIR=testing/e2e/m15 bin/anneal`)
  round-trips every panel API against the promoted test-home harness —
  `=== M15 UI API OK`; desktop restarted on the M15 binary (BOOT-DONE).

### 2026-10-25 — M14 recursion budget (k-step chaining + governors)
- `improve.Step` splits into orchestration (`stepK`) + pipeline
  (`stepOnce`). **sequential** (default per the locked decision): each
  iteration's probed successor becomes the next iteration's base harness
  AND its successor probe becomes the next reference — iter<i+1>/harness0
  starts file-for-file from iter<i>/compose (asserted hermetically).
  **parallel**: k independent steps + anonymized selector (options A-H,
  iteration identity hidden, "letter or NONE").
- Governors: `improve.max_total_tokens` (stops before starting an
  over-budget iteration, `improve.budget` event), `improve.total_wall_s`;
  `-improve-resume <runid>` skips iterations with a completed step.json.
- config `improve.{k,mode,total_wall_s,max_total_tokens}`; headless `-k`,
  `-improve-mode`, `-improve-resume`; UI labels for selector/budget
  events; k=1 wire-compatible with M12/M13 tests.
- Live gates (`make live-m14-test` x2): green. Both live runs exercised
  the honest-stop path (clean references → composer kept the harness,
  chain note recorded, full iter1 evidence kept) — chaining itself proven
  by the hermetic file-for-file test. Note: make ci does NOT rebuild
  the headless binary — rebuild before live gates or the new flags
  "don't exist".

### 2026-10-25 — M13 improvement library (harnessing-level recursion)
- `core/harnesslib`: entries at `harness-lib/<id>/{harness/,EXPERIENCE.md,
  meta.json}` + auto-rebuilt INDEX.md. Reflector agent distills each
  evidence-backed step (`NONE` short-circuits — no entry without
  evidence, the paper's rule). `Context(task)` ranks entries by local
  token overlap and injects up to 3 excerpts into BOTH editors' and the
  composer's prompts — selective re-drafting, never inheritance (every
  task still starts from ℋ₀ + library, not the last successor).
- tie: `IndexExperience`/`SearchExperience` (key `harness-exp:<id>`,
  tokens under `tag` + `Reverse` — old-server-safe); fired async after
  entry creation.
- Promote = the ONLY path agent-authored components take into the global
  harness: headless REPL `/harness promote <id>` (typed by the user =
  the approval), replaced files backed up under `promote-backup/<ts>/`.
  `/harness list` in REPL; GUI `/harness` shows the library;
  config `improve.{enabled,library}`.
- **Live gate green** (`make live-m13-test`, ~50 min): run 1 (Sub) and
  run 2 (Mul) each produced a full entry with parsed situation lines;
  run 2's composer correctly DROPPED probe-infrastructure "rules"
  (preseeded files, jail paths) as not agent behavior — and the live
  gate itself caught a real bug: the promote command had to move from an
  exact-match case to a prefix match, or `/harness promote <id>` fell
  through to the model (which politely refused to fabricate output).

### 2026-10-25 — M12 contrastive step (propose→probe→compose, k=1)
- `core/improve`: one Φ step per Harness² §2.2.1 — reference = last
  finished session's digest; invariant+domain editors draft candidate
  harnesses in sandboxed dirs (dive WorkspaceDir tools); each candidate is
  replayed (task text path-rewritten to sandbox) and a contrast note is
  generated; composer merges supported edits into `compose/` + mandatory
  `decisions.md` (every line cites an observation); successor probe gives
  e′; full step record at `improvements/<run>/step.json`.
- Successor installs as **session overlay** (`overlays/<sessID>/`, mounted
  on every following run of that session) — global harness untouched.
  `/improve` in GUI + headless REPL, `-improve` flag; `improve.*` events;
  journey entries.
- Gotchas fixed: dive tool path validator resolves RELATIVE paths against
  process cwd → prompts now mandate absolute sandbox paths; the idle
  review competes for GPU → review's busy hook now includes
  `ImproveBusy()` + improve preempts review (first live attempt failed
  exactly this way: "stream produced no event before the idle timeout").
- **Live gate green** (testing/e2e/m12/README.md): both candidates probed
  (9/6 steps), 27-line evidence-tied decisions, successor composed +
  overlay applied; composer REJECTED a candidate's false "transient
  failure, retry it" claim citing 5 deterministic failures — the
  contrastive discipline works on a local 27B.

### 2026-10-25 — M11 probe infrastructure (sandboxed task replay)
- `core/probe`: `Prepare(src,root)` copies the workspace (`SkipDirs` for
  heavy junk, `.git` kept for fidelity); the probe agent runs dive-native
  `WorkspaceDir`-jailed tools **plus** a belt-and-braces PreToolUse hook
  (protects the original tree + anneal state, demands absolute paths and
  an in-sandbox `working_directory`).
- `Run` executes the task once with the base toolkit (no memory/skills/
  subagents/MCP — probes isolate harness-file effects) and emits a
  `Record` = e(τ,y): condensed tool ledger, deliverable, usage, duration,
  plus audit hashes — `ConfigHash` (matched model conditions) and
  `HarnessHash` (component fingerprint). Written to
  `improvements/<runid>/<name>.json`; `Record.Digest(max)` renders the
  editor-facing bounded view.
- Gates: hermetic (copy/exclusions, 15-case jail table, scripted-LLM run
  asserting jailed write blocked + in-sandbox write applied + record
  roundtrip); **live gate green** (`make live-probe-test`): real Strata
  replay mutated the sandbox, original byte-identical, 3 steps in 11 s.

### 2026-10-25 — M10 harness component layer (Recursive Harnessing plan, PLAN.md §12)
- Ported Harness² (arXiv 2610.14224) concepts into the plan; locked
  decisions: **sequential default** recursion mode; successors never touch
  the global harness automatically (session overlay + evidence-gated
  library; promote is an explicit approval action).
- `core/harness`: `~/.anneal/harness/` components — `system.md`,
  `guardrails.md` (prose + `allow:/ask:/deny:` rule lines → dive permission
  rules; applied at startup, restart to re-read), `tool-guidance.md`,
  `subagents/*.md` via dive's subagent FileLoader (override built-ins),
  `scripts/` manifest. Char-capped layers (config `harness.char_limit`),
  Seed() with format README, Validate(). `/harness` slash cmd (GUI +
  headless REPL), `anneal -check-harness`, warnings logged at boot.
- Gates: hermetic (`core/harness`, prompt-order + perm-merge tests) green;
  **live m10 gate green** (`make live-m10-test`): `subagents/summarizer.md`
  callable as Agent type (one-line answer), and the deny rule blocked a
  real `rm -rf` **twice under `-yolo`** ("denied by harness guardrail",
  junkdir intact) — dive deny rules beat bypassPermissions, and dive's
  specifier matcher caught the rm inside a compound command too.

### 2026-10-25 — MCP module upgrade: dive#347 was my duplicate of upstream #309
- Maintainer response to dive#347: already fixed in **#309**, shipped in
  `experimental/mcp v1.33.0`. Verified: v1.33.0's adapter has
  `case json.RawMessage, []byte:` — exactly the bug. Upgraded to
  **mcp v1.42.0** (aligns with dive v1.42.0; mcp-go bumped to v1.0.0 as an
  internal dep), deleted the `repairInput` normalization wrapper (the
  double-encoded-string case never existed — `%q` log escaping fooled me),
  replaced local `qualifiedName` with exported `mcp.QualifiedToolName`.
  Kept the `qualifiedTool` rename wrapper: v1.42.0 Manager still registers
  **unqualified** names (collision with built-ins; the earlier `echo`
  misbehavior was exactly that).
- **Why the bad issue**: pinned "latest" from `git ls-remote --tags | tail` —
  lexicographic sort puts v1.9.0 above v1.33.0/v1.42.0. Never ran `@latest`.
  **New workflow rules (saved to skill)**: never file issues in repos the
  user doesn't own unless explicitly permitted (suggest first); before
  declaring an upstream bug, check latest versions version-aware
  (`go list -m -versions` / `sort -V`) and grep upstream fix history.
- Gates: `make ci` green; live m8 e2e (repo driver) green on v1.42.0:
  `mcp__demo__echo` → `ECHO(hello mcp)` with no wrapper. #347 thread left to
  the user per instruction.

### 2026-10-25 — code review pass + documentation
- Review fixes (2676eab): MCP tools registered before the subagent tool so
  `ParentTools` includes `mcp__*`; todo list always persisted (empty
  overwrites stale) + boot replay + panel cleared on session switch;
  explore/plan subagents can't touch memory/skills; skills `Stage` under
  mutex; tieindex tags written via `Set` (post-compaction re-index replaces
  stale tokens); headless second SIGINT hard-exits.
- Docs: new `Makefile` (build/test/live-tie-test/check-ui/ci), README full
  rewrite, `docs/CONFIG.md` (every key + env + state layout),
  `docs/ARCHITECTURE.md` (packages, threading rule, event table, learning
  loop, upstream workarounds), `docs/USING.md` (GUI/headless/learning ops),
  `testing/e2e/` — all driver scripts moved into the repo with a README.

### 2026-10-25 — M9 done (anneal-headless) — ALL MILESTONES DONE
- `cmd/anneal-headless`: same core (config/sessions/tie/memory/skills/review/MCP), no GUI. `-p "task"` one-shot (exit 0/1), `-i` REPL (/new /interrupt /quit), `-json` raw JSONL event stream (exact events the GUI gets), `-v`, `--session ID` resume, `--new`, permission policy: `-yolo` (bypassPermissions mode) / `-allow a,b,*` (answers prompts; anything unlisted denied when no policy given), `--learn` runs the review pass after the task.
- Event sink = Bridge tap (sees every emitted event; handlers dispatched on goroutines to stay off the lock path). SIGINT interrupts a running task, double-ctrl-C exits.
- Live e2e: one-shot Bash run + policy auto-allow + usage line + exit 0; REPL streamed "hello repl"; JSONL shows the full event vocabulary incl. memory.updated (agent saved a fact itself); `-p "Just reply hi." -learn` → `🧠 review saved 4 lessons`.
- **M0–M9 complete.** Next: self code-review + fixes + full documentation pass.

### 2026-10-24 — M8 done (MCP)
- `config.MCP{Enabled, Servers}` — Anthropic-mcp.json-shaped (command/args/env/url, type inferred stdio|http); dive `experimental/mcp` (own module, tagged separately — v1.9.0).
- App: `ConnectMCP` at Start (async), `mcp.state` event (per-server connected/tools), UI status line `mcp: 1/1 up, N tools`; MCP tools re-read per run (live after refresh) and **permission-gated** like any non-allowlisted tool.
- Tools renamed to Claude-Code-style `mcp__<server>__<tool>` (v1.9.0 keeps the helper unexported; local `qualifiedName`): unqualified names collided with built-ins and misled models.
- **dive bug found + upstreamed: [deepnoodle-ai/dive #347](https://github.com/deepnoodle-ai/dive/issues/347)** — dive core passes tool input as plain `[]byte`, which `ToolAdapter.Call`'s type switch misses → default branch base64-mangles it → *every* MCP call failed with "cannot unmarshal string into map". Workaround in anneal: wrapper normalizes input to `json.RawMessage` (+ unwraps double-encoded JSON strings some local servers emit). Unit-tested (`TestRepairInputShapes`).
- Debug war story: the `%q` log rendering fooled me into thinking input was double-quoted (`%q` adds its own quotes) — root cause was the []byte branch all along; a capture-tool unit test cracked it in minutes vs three live runs of guessing.
- e2e gate (live): stdio demo server (mcp-go) connects at boot → model calls `mcp__demo__echo` (approval prompt granted) → `ECHO(hello mcp)` returned verbatim.
- Next: M9 headless CLI (anneal-headless).

### 2026-10-24 — M7 done (background review + journey + notifications)
- `core/review`: queue of finished sessions (dedupe) + idle scheduler: runs only when not busy and idle ≥ `review.idle_settle_s`, max-age (`max_age_s`) fallback, batch drain, requeue on preemption (foreground `Send` cancels via `Interrupt`), `LearnNow()` manual trigger.
- Reviewer = bounded dive agent (iter cap 6): Read/Glob/Grep/ListDir + memory + skill_manage + session_search; `source:"review"`; respects write_approval staging (emits memory.stage/skill.stage cards like the main tools). **Tool ledger** drives notifications — never model prose (guards fake-save claims).
- Digests: `Runner.Digest` renders bounded per-message transcripts (600-char clips, MaxInputTokens*4 bytes) from the FileStore.
- `core/journey`: ~/.anneal/journey.jsonl timeline via a Bridge tap (memory/skill writes incl. staged-approvals + review batches); `/journey` + `/learn` slash cmds; review.start/done/skipped → status + transcript notices (ledger lines listed).
- Tests: scripted-LLM reviewer tests (apply+ledger exactness, staging events+pending source=review), idle/max-age/busy gating. Live e2e: run → idle 3s → reviewer PATCHED the memory entry (consolidation) → journey shows 2 entries.
- Bridge: `agent.learnNow()`, `agent.journey(n)`.
- Next: M8 MCP + polish.

### 2026-10-24 — M6 done (skills + skill_manage + subagents + slash commands)
- `core/skills`: SKILL.md dirs under `~/.anneal/skills` (agentskills frontmatter, validated by dive's `skill.ParseContent`); create/patch (unique-substring)/write_file (path-escape guarded)/remove→`skills-archive/<name>-<ts>`; staging mirror of memory (`pending/skills/<id>.json`, cap 10, `Preview` diff for UI).
- Skills at runtime: dive `skill.Loader` as **Extension** per run — dirs: `~/.anneal/skills`, `./.anneal/skills`, project `.claude/.agents/.dive` read-compat + `skills.extra_dirs`; gives the `Skill` tool + catalog reminder + rules for free. New skills visible next run (loader rebuilt per run).
- Subagents: dive `toolkit/orchestration.NewAgentTool` with **custom AgentFactory** — default factory spawns UNGUARDED agents; ours re-attaches `permission.HookFromManager` + iteration cap 40. Types: general-purpose / explore / plan.
- `skill_manage` FuncTool (list/view/create/patch/write_file/remove) + staging + UI cards (`respondSkill`).
- UI: slash commands `/help /new /sessions /plan /build /skills /memory /index /quit`; keybinds Ctrl+N (new), Ctrl+B (sessions), Ctrl+D (stop) via `window.AddShortcut`.
- Config: default allow list now includes `session_search memory skill_manage Skill` (their safety gate is write_approval staging); `Agent` keeps prompting.
- **fynerisor issue #6** filed+fixed upstream (0b826e9): `Window.CompileScript()` (risor.Compile wrapper). New `anneal -check-ui` flag; main.risor now compile-checked at EVERY boot (fail-fast, kills the M2 "black window" failure class). Found a stale-`else` bug in the M4 UI code this way — the main UI had never been compiled since M3!
- e2e (live Strata): create markhor-skill → next session `Skill` tool loads it → answer begins SNOWLEOPARD; Agent(explore) subagent finds add.go through its own tools; skill store unit tests all green.
- Next: M7 background review + journey + notifications.

### 2026-10-24 — M5 done (memory stores + memory tool + staging)
- `core/memory`: MEMORY.md/USER.md under `~/.anneal/memories`, caps 2200/1375 (config), atomic writes; ops add/patch/remove (patch/remove = **unique-substring** match, count-checked), dup-guard (case/whitespace-normalized entry compare), final-size batch check, injection scanner (instruction-override/role-forgery/`<system-reminder` patterns rejected — memory is re-injected forever).
- Staging (`memory.write_approval=true`): ops persist `~/.anneal/pending/memory/<id>.json`, dedupe identical, queue cap 10, survive restart (`agent.memoryPending()`); `respondMemory(id, approve|reject)` → apply/drop + `memory.stage`/`memory.resolved` events; tool tells the model it is NOT saved yet (no fake-save prose).
- Snapshot injection: PreGeneration hook → `dive.SetSystemReminder(msgs, "anneal-memory", snapshot)` — named block in first user message, **frozen per session** (only inserted if absent), compaction-safe (re-added if lost). UI: memory approval cards (approve/reject) in the inbox row.
- System prompt: learning-loop rules (what belongs in memory vs session_search; patch stale entries).
- e2e (live Strata): direct add → new session → model recalls "tabs for indentation" **without tools** (snapshot worked); staging: mid-check file untouched → approve → updated; memory unit tests (caps, dup, injection, queue, resolve).
- Risor gotcha: `else` must be on the same line as the closing `}` (newline-terminated statements) — split `}\nelse {` = parse error.
- Next: M6 skills + skill_manage + subagents + slash commands.

### 2026-10-24 — M4 done (tie index + session_search + compaction)
- `core/tieindex`: rebuildable word-token index in tie collection `anneal` (schema in §4 of PLAN); msgID=sha1(sess:pos); incremental per-session indexing after each run (`-rebuild-index` flag + `agent.rebuildIndex()` for full rebuilds); degraded mode never blocks the agent (probe = any HTTP answer counts).
- **tie gotcha (big)**: triplestore queries match by **key** unless `Reverse: true`; and reverse hits only cover the server's **reverse-relation whitelist** — the live server reverse-indexes `tag` + `tie-type` only. Private relations (`word`) are invisible to value search. Schema fix: tokens under relation **`tag`**; role/snippet/etc as forward attrs via `Expand`. (Recorded in tie skill.)
- `session_search` tool (FuncTool, ReadOnlyHint): token AND-search via `QuerySpec{Terms, Reverse, Scope:"session-msg", SortByValue:"msg-at"}` + client-side collision filter; grouped hits w/ title/date/snippet.
- Compaction: custom `preGenCompactionHook` (PreGeneration, est tokens≈bytes/4, `CompactMessages`, state event) + `compaction.MidTurnCompactionHook` (in-run, notify → `compaction.done` UI line); threshold = ctxWindow × pct (82% default), floor 8000.
- UI: tie status label in header (`tie.state`), `tie.rebuilt` + `compaction.done` transcript notices.
- Gates: live tie roundtrip **search latency ~2 ms** (gate <50 ms ✔); e2e cross-launch recall OK (driver A marks session with "markhor mountains" → driver B fresh session → model calls session_search → snippet found); compaction hook unit-tested w/ scripted recap LLM (+ threshold math).
- Next: M5 memory stores + memory tool + staging.

### 2026-10-24 — M3 done (sessions + permissions + plan/build + todo)
- Sessions: dive `session.FileStore` under `~/.anneal/sessions`; boot resumes most recent; sidebar lists/restores (transcript replay as normalized blocks); title = first task (flushed via `store.Put` after run — appends never rewrite the header).
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
- `cmd/anneal` boots fynerisor, embeds `assets/ui/main.risor` (`ANNEAL_UI_DIR` overrides for hot dev).
- Bridge (`core/uiapi`): `agent.on(cb)` captures the VM CallFunc; Go→script events dispatch as JSON strings via `fw.Do` + `vmguard.Call`, reschedule-on-contention. Startup `agent.ping()` round-trip verified live: `ui: [001 pong]`.
- fynerisor gap (upstream candidate): **Markdown has no `SetText`/`Text`** — transcript design uses writable `Label` for streaming + appended immutable `Markdown` blocks.
- Risor v2 notes for the UI layer: **no `while`/`for` loops, no ternary**; no list `.len()`/`.slice()` (builtin `len()` exists, no `slice()`); `if (cond)` needs parens; json module is `json.parse`/`json.marshal`; `strings.join` via `@strings`.
- Next: M1 config + Strata health.

### 2026-10-24 — repo initialized
- git repo created (`main` branch); `docs/PLAN.md` (full plan), `progress.md`, README, .gitignore committed.
- Next: M0 — go.mod + skeleton GUI + bridge echo test.
