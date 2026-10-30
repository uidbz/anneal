# Anneal code review

> **Post-review note (2026-10-25):** the project is now **Anneal**. The
> hybrid UI migration (Go/Fyne shell + fynerisor
> `CanvasObject` panes) is tracked in the maintainer plan
> `~/.opencode/plan/anneal-hybrid-migration.md` and in `progress.md`. This
> document is the historical review; product references were updated.

**Date:** 2026-10-25
**Scope:** all first-party Go (`core/`, `cmd/`, `assets/`) and the Risor UI
(`assets/ui/main.risor`), plus repo/docs hygiene. Third-party modules were not
reviewed.
**Baseline:** `go vet ./...` clean; `go test ./...` green (16 packages, 6 with
tests). Review is based on reading every first-party file end-to-end, then
implementing the fixes listed here (see §8 for status).

**Verdict:** the architecture is coherent and unusually well documented for
its size (~9.2k lines). The event bridge, permission flow, learning stores, and
improvement pipeline are defensible. The main risks are a handful of real
concurrency/lifecycle bugs (probe jail self-denial, event queue loss, provider
probe panic, RPC ordered-delivery), plus duplicated helpers and stringly-typed
data that will drift. None require redesign; most are small, local fixes.

---

## 1. What works well

- **Clear layering.** `cmd → core/app → core/agent|review|improve|...` with a
  narrow `uiapi.Bridge` between Go and the Risor UI. Dependencies point one
  way; no cycles.
- **Graceful degradation.** tie/MCP/provider failures are surfaced as state,
  not fatal. A missing index disables `session_search` but never blocks runs.
- **Evidence discipline.** The improvement library only records steps with a
  probe record; reviewer flags are hints, probe evidence decides. This is easy
  to subvert accidentally and the code is careful about it.
- **Security posture.** Memory injection scanning, unique-substring edits,
  skill path jail, harness path jail, probe jail, sandboxed probes, explicit
  promote-with-backup, and permission inheritance for subagents.
- **Test style.** Scripted LLMs instead of network mocks; temp `ANNEAL_HOME`;
  behavioral assertions (`probe` jail table, review ledger, k-step resume).
- **Hermetic CI gate.** `make ci` = vet + test + Risor compile check catches the
  expensive "black window" class of failures.

---

## 2. Critical

### C1. `Monitor.ProbeNow` dereferences a nil response when the provider is down
`core/model/monitor.go:108-148`

```go
resp, err := m.client.Get(modelsURL)
switch {
case err != nil: /* resp is normally nil here */
...
}
resp.Body.Close() // ← inside the else, but still runs for err != nil
```

`http.Client.Get` returns `(nil, err)` for connection refused/timeout. The
unconditional `resp.Body.Close()` is therefore a nil-pointer panic, and
`ProbeNow` runs every 10 s (plus once per `/models` refresh). The one scenario
the UI is explicitly designed for — a cold/down model server — crashes the
process. Secondary: concurrent probes (ticker + `SetProvider`) can publish
results out of order.

**Fix:** close only after a successful `Get`; serialize probes; keep the
health state transitions atomic.

## 3. High

### H1. `Bridge.Ready` drops events queued before the UI handler registers
`core/uiapi/uiapi.go:75-86`

`Ready()` clears `b.queue` unconditionally but only flushes it when
`b.handler != nil`. `cmd/anneal` calls `fw.Execute()` then `Bridge.Ready()`;
the script may not have reached `agent.on(...)` yet (it is a goroutine). In
that window, `session.opened`, `mode.changed`, and `todo.updated` emitted by
`NewRunner` are silently lost. The comment "call right after fw.Execute"
guarantees the race exists rather than avoiding it.

**Fix:** `Ready` keeps the queue when no handler is installed; `setOn` flushes
it when the handler arrives. (Also add a regression test.)

### H2. Headless boots tie and MCP twice
`cmd/anneal-headless/main.go:236-238`, `core/app/app.go:163-170`

`a.Start(ctx)` spawns `go a.ConnectTie()` and `go a.ConnectMCP(ctx)`. The
headless entrypoint then calls `a.ConnectTie()` and `a.ConnectMCP(ctx)`
synchronously. Result: two MCP managers (the first connection is leaked and
replaced), duplicate tie connections, and racy/duplicate `tie.state` /
`mcp.state` events.

**Fix:** split startup: `Start` (background connects) and `StartBlocking`
(synchronous connects) sharing one `startCore`.

### H3. Headless event tap breaks event ordering
`cmd/anneal-headless/main.go:232`

```go
a.Bridge.SetTap(func(v any) { go s.handle(v) })
```

Every event gets its own goroutine. `text.delta` chunks can be reordered (text
scrambles), and `run.finished` can be handled before `message.final` or
`tool.result`. The sink is otherwise sequential.

**Fix:** one buffered channel + one consumer goroutine.

### H4. Probe jail denies the probe's own sandbox in production
`core/probe/sandbox.go:111-130`, callers `core/improve/improve.go:407,474`

The sandbox workspace for `/improve` is created under
`~/.anneal/improvements/<run>/…/ws`. `jailDeniedPath` checks
`inside(p, paths.Root())` **before** it checks `inside(p, s.WS)`, so every
absolute-path read/write inside the sandbox is rejected as "anneal state is
off-limits". The probe system prompt explicitly instructs the model to use
absolute paths under the sandbox, so the two rules contradict each other.
Tests pass only because their sandbox lives outside `ANNEAL_HOME`, and the
scripted probe uses a relative path.

**Fix:** exempt `s.WS` first in `jailDeniedPath`; keep the state/src denials for
everything outside the sandbox. Add a regression test with the improvement
layout (sandbox inside `ANNEAL_HOME`).

### H5. Review scheduler has a data race on `notify`
`core/review/review.go:63-71,132,144,158-165`

`Start` assigns `m.notify` while `Enqueue`/`LearnNow` read `m.notify` without
locking. Concurrent UI events can race with scheduler startup. The field also
exists only to be overwritten.

**Fix:** create one persistent buffered wake channel in `NewManager`; `Start`
selects on it; signal with a non-blocking send. Remove the `notify` field.

### H6. Parallel selector returns the first candidate but says "kept last"
`core/improve/improve.go:346-356`

When the selector model answers `NONE`/garbage, `idx` stays `0`, so the first
eligible iteration is returned while the emitted reason says
`"none clearly better (kept last)"`. Either behavior is defensible, but the
result and the audit record must agree.

**Fix:** keep the last eligible iteration (matching the message) and add a
regression test for `NONE`.

### H7. Token governor does not count editor/composer/reflection tokens; wall budget ignored at k=1
`core/improve/improve.go:115-127,141-213,217-228,539-580`

`tokens()` sums only probe records. Editors, composer, contrast/selector/
reflector calls are the dominant cost and are discarded (`CreateResponse`
returns usage that is never read). `MaxTotalTokens` can therefore be exceeded
by a large factor. Additionally, `TotalWallS` is applied only in `stepK`; the
k=1 fast path ignores it. `Result.K`/`Result.Mode` are also left unset for the
fast path.

**Fix:** accumulate stage usage into the iteration record, include it in
`tokens()`, wrap the whole `Step` in the wall deadline, and set `K/Mode`
consistently.

### H8. Staged-op resolution accepts path-traversal ids
`core/memory/memory.go:301-321`, `core/skills/store.go:332-352`

`Resolve(id, …)` builds `filepath.Join(pendDir(), id+".json")`. `id` arrives
from the UI/script (`agent.respondMemory/respondSkill`). `../x` reads and
deletes an arbitrary `.json` file and then applies its contents as an op.
The id is generated by anneal and has a known shape (`mem-<16 hex>`,
`skill-<16 hex>`), so validating it is trivial.

**Fix:** validate ids against their generated pattern before any filesystem
access. Add tests.

### H9. MCP manager races with the first run
`core/agent/runner.go:313`, `core/agent/mcp.go:13`

`ConnectMCP` runs in a goroutine and calls `Runner.SetMCP`; `run()` reads
`r.mcp` without synchronization. A user who sends immediately after boot can
read the field while it is written (`-race` reports it).

**Fix:** guard the field with a small mutex/accessor.

### H10. Probe records never carry the harness hash from `/improve`
`core/improve/improve.go:419-424,483-488`

`probe.Options.HarnessHash` is declared and copied into `Record.HarnessHash`,
but neither candidate nor successor probes set it. The audit fields that make
probe contrasts reproducible are therefore empty for the only production
caller.

**Fix:** compute `probe.HarnessHash` from the layers actually mounted and pass
it for candidate and successor runs.

### H11. `agent.probe()` blocks the Risor/UI thread
`core/app/app.go:177-180`, `core/model/monitor.go:99`

The bridge contract is "script → core calls are fire-and-forget (never block
the VM or the GUI)". `probe` calls `ProbeNow()` synchronously; `ProbeNow` does
an HTTP GET with a 5 s timeout. The boot call in `main.risor` can freeze the UI
for up to 5 s on a cold server.

**Fix:** trigger the probe in the background and return the current snapshot
immediately; `provider.state` delivers the result.

### H12. Runtime config changes do not reach copied store configs
`core/app/app.go:66-68,84-95`, `core/memory/memory.go:44-53`,
`core/skills/store.go:58-64`, `core/review/review.go:46-81`

`config.Apply` mutates the shared `*Config`, so `Runner` sees changes. But
`memory.Store`, `skills.Store`, and `review.Manager` received **value copies**
at construction. Editing memory caps or `write_approval` through
`agent.saveConfig` shows the new caps in `/memory` while the stores keep
enforcing the old ones. Docs describe these settings as hot-applied.

**Fix:** stores hold `*config.Memory` / `*config.Skills`; review reads
`WriteApproval()` from the stores instead of duplicating config copies.

### H13. A 33 MB build artifact is tracked in git
`anneal-headless` at the repo root (and the file is not in `.gitignore`;
`bin/` is ignored, this copy is not).

**Fix:** untrack it and ignore it. Builds belong in `bin/`.

---

## 4. Medium

### M1. Two harness stores where one is authoritative
`core/agent/runner.go:196`, `core/app/app.go:70-74`

`NewRunner` creates its own `harness.New(paths.Harness())` and builds the
permission manager from it; `app.New` creates another store, seeds/validates
it, then calls `Runner.SetHarness`, replacing the runner's store. Both point at
the same directory today, so rules work, but the wiring invites a split-brain
bug. **Fix:** create the store in `app.New`, pass it to `NewRunner`, delete
`SetHarness`.

### M2. `ConnectTie` has an if/else with identical branches
`core/app/app.go:151-161` — both branches call `LogState()`. Simplify to
connect-then-log (the error is already recorded as degraded state).

### M3. tie liveness probe leaks a response body and has no timeout
`core/tieindex/tieindex.go:88-94`

`http.Get` uses the default client (no timeout) and the body is never closed.
`client()` also reads `x.lastErr` outside the mutex while `setDegraded` writes
it. **Fix:** dedicated client with a short timeout, `defer Close`, drain a
small amount, copy `lastErr` under the lock.

### M4. `HarnessInfo` is a `map[string]any` with unchecked assertions
`core/agent/runner.go:77-119`

`FormatHarnessInfo` asserts `info["warnings"].([]string)`,
`info["components"].([]map[string]any)`, `rules["allow"].([]string)`. One
shape change panics a CLI command. `nzs` exists only because of this shape.
**Fix:** typed `HarnessStatus`/`HarnessRules`/`HarnessComponent` structs; JSON
keys stay identical, so the UI is unaffected.

### M5. Reviewer re-implements the memory/skill tools
`core/review/review.go:278-414` vs `core/agent/memory.go`,
`core/agent/skillmanage.go`

Two copies of validation/staging/apply logic, two approval gates, two result
texts. They already differ in small ways (ledger, event emission, apply-error
handling). This is the largest remaining duplication and a drift risk.
**Deferred:** extracting a shared `toolset` package is a worthwhile follow-up,
but it needs a deliberate callback design and is not required for the fixes
below. Recommended shape: `toolset.MemoryTool(store, opts{OnStage, OnApply,
Announce})` and the same for skills.

### M6. `AgentAPI` is a 190-line closure map with repetitive nil guards
`core/app/app.go:173-365`

Every list endpoint repeats `if x == nil { return toJSON([]any{}) }`. A generic
`jsonList[T]` removes ~40 lines and the risk of forgetting the guard (Risor
errors on `null` arrays). Split the function into `core/app/api.go` to keep
wiring and surface separate.

### M7. Dead code / unused seams

- `core/uiapi/uiapi.go:184-191` + `time.go`: M0 `agent.echo`/`agent.ping` and
  `nowSec` have no callers (only a progress-log mention).
- `core/agent/runner.go:59,135,489-492`: `lastDeliv`/`LastDeliverable` is
  write-only.
- `core/harness/harness.go:286-288`: `FormatDoc` has no callers.
- `core/paths/paths.go`: `Probes`, `Bundled`, `Logs` are unused (Logs is
  documented as "reserved", so leave the documented one).
- `core/agent/skillmanage.go:89`: `skillLoader(ctx)` ignores `ctx`.
- `core/improve/improve.go:700-718`: hand-rolled `sortStrings`; use `sort.Strings`.
- `core/memory/memory.go:118-123,187-189`: dummy `f` lookup and `countSub`
  wrapper around `strings.Count`.
- `core/probe/probe.go:88-117`: builds the unrestricted tool list, then throws
  it away when a sandbox is present.

### M8. `jailBash` misses relative traversal
`core/probe/sandbox.go:134-167`

The jail validates `working_directory` and absolute path tokens, but
`cd .. && rm -rf <project>` uses no absolute token and passes. Probe jails are
defense-in-depth, not OS isolation, but this hole defeats the specific promise
("the original project is off-limits"). **Fix:** reject `..`/`../` tokens (and
document that a real sandbox needs namespaces/containers).

### M9. Inconsistent apply-error handling for staged memory
`core/memory/memory.go:313-320` vs `core/skills/store.go:344-349`

Memory keeps the pending file "so the user can retry", but
`agent.respondMemory` always emits `memory.resolved`, so the UI drops the card
and the file is orphaned. Skills deletes the file. **Fix:** make memory match
skills (delete after a failed apply); the user can ask the agent to retry.

### M10. `App.lastHealth` is unsynchronized
`core/app/app.go:461-468`

`onHealth` may be called from the ticker goroutine and from `SetProvider`'s
probe. Serializing `ProbeNow` reduces the window but the field still needs a
mutex. **Fix:** small `healthMu`.

### M11. Probe sandbox does not clear a stale workspace
`core/probe/sandbox.go:36-50`

`Prepare` reuses `<root>/ws` and copies over it. Re-running the same run id
(without `Resume`) can leave files from an earlier attempt in the replay,
invalidating the evidence. **Fix:** remove `ws` before copying.

### M12. `reasoning_effort` is documented but never applied
`core/config/config.go:21`, `core/model/strata.go`

`Provider.ReasoningEffort` is stored and hashed (`probe.ConfigHash`) but never
reaches `dive.ModelSettings`, so the documented behavior ("forwarded when the
endpoint supports it") does not happen. **Fix:** central
`model.Settings(cfg)` that sets `MaxTokens`, `Temperature`, and a non-empty
`ReasoningEffort`; use it in the runner and probe.

### M13. `Config.Apply` resets omitted keys to zero values
`core/config/config.go:195-205`

The settings doc is documented as a full replacement, but a partial document
(e.g. `{"provider":{"endpoint":…}}`) silently drops `agent.allow`,
`compaction.enabled`, `tie.enabled`, etc. `normalize()` only repairs a subset.
**Fix:** unmarshal into `*Default()` (omitted keys keep defaults), then
normalize. Add a test.

### M14. `agent.quit()` hard-exits instead of closing the window
`core/app/app.go:357-363`

`os.Exit(0)` after 150 ms skips Fyne shutdown and any future cleanup.
fynerisor now exposes `app.Quit()` (and `guithread.Do`). **Fix:** add
`Bridge.Quit()` that closes via `FyneApp.Quit()` / `FyneWindow.Close()` on the
GUI thread, and keep a short forced-exit fallback for pathological cases.

### M15. Docs drift
- `README.md:17`: "All milestones (M0–M9)" but progress reaches M16.
- `docs/USING.md:14-15`, `docs/CONFIG.md:47`: claim a GUI **Settings tab**
  that edits provider config live; the UI has no settings view (the
  `saveConfig` bridge API exists but is unused by `main.risor`).
- `docs/ARCHITECTURE.md:111-112`: health states listed as
  connecting/online/degraded/offline; the code uses
  connecting/online/unreachable/wrong_service.

### M16. UI event handling gaps
`assets/ui/main.risor:488-710`

- `mcp.state` is the only branch without `return`; harmless today, inconsistent.
- Emitted `harness.warn`, `harness.promoted`, `harness.overlay`, `skill.error`,
  and `harness.warning` are ignored. Most are redundant with direct return
  values, but `harness.warning` (bad subagent files) is lost to the user.
- The promote message always prints `backup: ` even when nothing was replaced
  (see L3).
- Session sidebar builds one button per session with no cap; thousands of
  sessions make `compose()` expensive (recursion through every session).

### M17. Headless can consume a stale `run.finished`
`cmd/anneal-headless/main.go:261-295`

On timeout/interrupt the `done` channel may receive the finishing run's result
after `runTask` returned; the next task can read it immediately. Fixing this
cleanly needs run-id correlation (e.g. `Runner.Send` returning the run id) and
a "drop until the next `run.started`" rule. Deferred: one-shot mode exits, and
the REPL case needs a small state machine; noted so it is not mistaken for
working.

### M18. Reviewer builds an agent before checking for an empty digest
`core/review/review.go:220-245`

If all digests are empty, the agent and its tools are constructed and then
discarded; `Digest(ctx)` also uses the scheduler ctx rather than the cancellable
`rctx`. **Fix:** compute the digest first, then build the agent; use `rctx`.

### M19. Reviewer flags are consumed even when improve bails
`core/app/app.go:429-432`

`ConsumeFlags(runid, 6)` marks up to six flags consumed before `Step` runs. If
improve fails immediately (busy/disabled/Step error), those reviewer
observations are lost. **Fix:** add `FlagStore.Release(runid)` and release on
error.

### M20. Library entries can be recorded without successor evidence
`core/improve/improve.go:494-520`

If the successor probe fails to start or errors, `SuccessorProbe` is nil but
reflection still creates an entry (candidate evidence may still exist). The
paper rule is "execution-supported evidence". Acceptable if candidate probes
are considered evidence, but the condition should be explicit and documented.

---

## 5. Low / nits

- **L1** `app.New` ignores `os.Getwd()`; `SystemPrompt` ignores
  `EvalSymlinks` errors and can emit an empty working directory.
- **L2** `harness.Validate` names the `[]string` notes return `err`
  (`if _, err := s.Subagents(); err != nil`) — misleading.
- **L3** `harnesslib.slug` rebuilds the whole string per rune to inspect the
  last byte; `Promote` returns a backup path even when no file was replaced.
- **L4** `harnesslib.Context` reads each `EXPERIENCE.md` twice (score + render).
- **L5** `improve.changedFiles` ignores removed files; `dirDiff` does not.
- **L6** `Config.SetMode` always rewrites `config.json`, even when the mode is
  unchanged.
- **L7** `Runner.SetSessionOverlay` calls `SessionID()` twice.
- **L8** `SetSessionOverlay`'s `copyTree` copies every top-level file
  (including `decisions.md`, harmless because layer loading filters it).
- **L9** `review.runOnce` ledger appends are safe because the agent is
  single-threaded, but the slice is shared with the emit callback; document.
- **L10** `journey.Tail` reads/scans the entire log before slicing the tail;
  fine at current scale, worth a bounded read later.
- **L11** `TestLiveReplay` hardcodes `/tmp/opencode/probe-live`; acceptable for
  an opt-in live gate but should use `t.TempDir()`.
- **L12** `go.mod` pins fynerisor via an absolute local `replace`; documented,
  but a release bump should remove it.
- **L13** Duplicated `clip` helpers in six packages (with intentionally
  different markers). Centralizing would primarily standardize truncation;
  deferred as churn-for-its-own-sake.
- **L14** Duplicated flat-directory copy helpers (`runner.copyTree`,
  `improve.copyFiles`, `harnesslib` file copies). A `core/fsutil.CopyFlatDir`
  would remove ~50 lines if another caller appears.
- **L15** Atomic writes are reimplemented in `config`, `memory`, `skills`,
  `review/flags`, `harnesslib`; a shared `WriteFileAtomic` is reasonable when
  touched next.
- **L16** The UI `agent.on` handler is a long if-chain (Risor has no `switch`
  over strings worth using); keep, but add a final debug branch for unknown
  event types when running under `ANNEAL_UI_DEBUG`.

---

## 6. Test coverage gaps

Highest-value additions (implemented where marked):

1. **Provider monitor** (none today): online/ctx resolution, 404 wrong service,
   unreachable endpoint must not panic. ✅ added
2. **Bridge queue semantics** (none today): events queued before `Ready` are
   preserved until `agent.on`. ✅ added
3. **Probe jail** (good table): sandbox under `ANNEAL_HOME` must be writable;
   `..` traversal must be denied. ✅ added
4. **Config** (none): `Apply` keeps defaults for omitted keys. ✅ added
5. **Improve** (good): non-empty probe harness hashes; selector `NONE` keeps
   the last iteration. ✅ added
6. **Staged ops** (good): path-traversal ids rejected in both stores. ✅ added
7. **Headless ordering / duplicate init**: not unit-testable without a
   refactor of `main`; covered by review + manual REPL check.
8. **Review scheduler race**: covered by `-race` once `notify` is removed;
   could add a stress test later.
9. **GUI harness panel** relies on the opt-in `m15` driver; keep.

---

## 7. Implementation plan (executed in this pass)

Ordered by risk reduction, then cleanup:

1. Provider monitor nil-close + probe serialization + tests (C1).
2. Bridge: keep pre-`Ready` queue, tap outside lock, safe `Attach`, graceful
   quit (H1, H7/mutual-lock, M14); queue test.
3. Probe jail: sandbox exemption, `..` traversal, clean workspace, harness
   hashes; tests (H4, H10, M8, M11).
4. Improve: token accounting, wall budget, selector, `changedFiles`, hints
   release, typed usage; tests (H6, H7, M19).
5. Review: wake channel (H5), digest-before-agent (M18), store config pointers
   (H12).
6. Runner/app: single harness store, typed `HarnessStatus`, MCP lock, async
   `probe`, `StartBlocking`, `lastHealth` lock, `jsonList`, API file split,
   defaults-preserving `Apply`, `model.Settings` (M1, M2, M4, M6, M10, M12,
   M13, H2, H9, H11, H12).
7. Headless: single ordered event consumer, no duplicate connects (H2, H3).
8. Stores: staged-id validation, memory apply-error cleanup, config pointers
   (H8, H12, M9).
9. Dead code removal, tie probe client, slug/promote/changed-files nits
   (M3, M7, L2-L5).
10. Docs + repo hygiene: milestone range, settings-tab claim, health states,
    untrack the binary (M15, H13).

Deferred deliberately (documented, not silently dropped): shared reviewer
toolset (M5), headless stale-run correlation (M17), library entry condition
(M20), clip/copy/atomic helper consolidation (L13-L15), UI settings view.

---

## 8. Post-fix verification

All fixes below were implemented, then verified with:

```sh
gofmt -w core cmd assets
go build ./...
go vet ./...
go test ./...
go test -race ./core/...
make check-ui        # ANNEAL_HOME=$(mktemp -d) ./bin/anneal -check-ui
```

Result: build clean, `go vet` clean, all tests pass, race detector clean,
`anneal: UI compiles`. Also verified live against the local Strata server
that a request carrying `reasoning_effort: "high"` is accepted (HTTP 200), so
M12 does not break the default endpoint.

### Implemented

| ID | change | new/updated tests |
|----|--------|-------------------|
| C1 | `ProbeNow` closes only real responses, drains/closes once, serializes probes | `core/model/monitor_test.go` (online, override, 404, unreachable) |
| H1 | `Ready` keeps the queue until `agent.on`; `setOn` flushes | `core/uiapi/bridge_test.go` |
| H2 | `App.StartBlocking`; headless calls it instead of double-connect | build/UI gate |
| H3 | headless uses one buffered, ordered event consumer | build + manual REPL check |
| H4 | probe jail checks the sandbox before the anneal-state denial | `TestJailAllowsSandboxInsideStateDir` |
| H5 | persistent `wake` channel; no reassigned `notify`; hooks locked | `-race` suite |
| H6 | selector defaults to the last eligible iteration | `TestSelectorNoneKeepsLast` |
| H7 | stage-token accounting (`StageTokens`), wall deadline for all k, K/Mode set | `TestStepFullFlow` (k/mode + token math), budget test unchanged |
| H8 | staged ids validated against `^mem-[0-9a-f]{16}$` / `^skill-[0-9a-f]{16}$` | `TestResolveRejectsUnsafeIDs` (both stores) |
| H9 | MCP manager behind its own mutex + accessor | `-race` suite |
| H10 | candidate/successor probes carry `HarnessHash` | `TestStepFullFlow` asserts hashes |
| H11 | `agent.probe()` triggers the probe in the background | UI gate |
| H12 | memory/skills/review hold live config pointers; review reads `WriteApproval()` from stores | review/memory/skills tests updated |
| H13 | untracked `anneal-headless`; `.gitignore` covers `/anneal*` | `git status` |
| M1 | one harness store, constructed before the runner and passed in; `SetHarness` removed | `core/agent` tests |
| M2 | `ConnectTie` has one path | — |
| M3 | tie probe uses a 3 s client, closes the body; `client()` copies `lastErr` under lock | — |
| M4 | typed `HarnessStatus`/`HarnessComponent`/`HarnessRules` | `TestHarnessInfoTyped` |
| M6 | `AgentAPI` moved to `core/app/api.go`; generic `jsonList` removes nil-array guards | build + UI gate |
| M7 | removed `agent.echo/ping`, `nowSec`, `LastDeliverable`, `FormatDoc`, `countSub`, dummy lookup, `sortStrings`, unused loader ctx; probe tools built once | build/vet |
| M8 | `jailBash` rejects `..`/`../` traversal | `TestJailDeniesParentTraversal` |
| M9 | failed memory apply deletes the staged file (matches skills) | memory tests |
| M10 | `healthMu` guards `lastHealth` | `-race` suite |
| M11 | `Prepare` removes a stale workspace first | `TestPrepareClearsStaleWorkspace` |
| M12 | `model.Settings` forwards `reasoning_effort`; used by runner and probe | `TestSettingsCarriesReasoningEffort` |
| M13 | `Apply` merges over defaults; path preserved | `core/config/config_test.go` |
| M14 | `Bridge.Quit` uses Fyne quit/close on the GUI thread with a 2 s failsafe | `TestQuitWithoutWindowIsNoop` |
| M15 | docs: milestone range, no settings tab, real health states | docs diff |
| M16 | `mcp.state` returns; `skill.error` / `harness.warning` surfaced; backup text conditional | UI compile gate |
| M18 | review digest computed before the agent; uses `rctx`; empty digest returns once | review tests |
| M19 | `FlagStore.Release` + `Manager.ReleaseFlags` on improve failure | `TestFlagStoreRelease` |
| L1-L5 | `Getwd`/`EvalSymlinks` fallbacks; notes naming; slug rewrite; CreateEntry id base; Promote reports no backup when none was made; Context reads experience once; changedFiles includes removals | harnesslib/probe tests |
| L6 | `SetMode` no-ops when the mode is unchanged | — |
| L7 | `SetSessionOverlay` resolves the session id once | — |

### Deferred items — second pass (implemented)

| ID | change | tests |
|----|--------|-------|
| M5 | new `core/toolset` package with one memory tool and one skill tool; the agent supplies UI-event callbacks, the reviewer supplies ledger callbacks | `core/toolset/toolset_test.go`; review/agent tests unchanged and green |
| M17 | `Runner.StartTask` returns the run id; headless awaits it and drops events from aborted runs (`sink.acceptRun`/`discardRun`) | `cmd/anneal-headless/main_test.go` |
| M20 | library admission now goes through `hasExecutionEvidence` (a probe must have executed at least one step; candidate evidence counts) | `TestExecutionEvidencePredicate` |
| L13 | `core/textutil.Clip` centralizes the five identical truncation helpers (harness keeps its distinct marker) | `core/textutil/textutil_test.go` |
| L14 | `core/fsutil.CopyFlatDir` replaces the runner/improve flat-copy loops | `core/fsutil/fsutil_test.go` |
| L15 | `core/fsutil.WriteFileAtomic` replaces the config/memory/flags temp-file+rename copies | `TestWriteFileAtomic` |
| L16 | `main.risor` has an opt-in `traceEvents` fallback for unhandled event types | UI compile gate |

The second pass is committed separately; `go test -count=1 ./...`,
`go test -race ./core/...`, `make build`, and `anneal -check-ui` are green.

### Residual risks worth knowing

1. **Probe jail is not an OS sandbox.** It blocks the paths and shell forms the
   agent is likely to use, but a determined model can still run arbitrary code
   as the user (`python -c`, encodings, variables). Treat `/improve` probes as
   trusted local execution; use containers/namespaces if real isolation is
   required.
2. **fynerisor's callback channel has capacity 10.** The bridge queue can hold
   thousands of events, but `window.Do` backpressures at 10 while the script
   main is still running. Boot emits far fewer than 10 events before
   `agent.on`, so the synchronous flush is safe today; a future change that
   emits many pre-handler events should move delivery to an ordered dispatcher
   goroutine.
3. **Model health `unreachable` vs `wrong_service`.** The UI treats any
   non-online state as not usable; keep the strings in sync if more states are
   added.

