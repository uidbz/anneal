# Anneal architecture

Anneal was renamed from `fyagent`; the hybrid UI migration is in progress.
The stable shell is Go/Fyne; scripted `CanvasObject` panes use fynerisor.

```
                    ┌────────────────────────────────────────────────┐
                    │ cmd/anneal (hybrid)      cmd/anneal-headless   │
                    │  Go/Fyne shell            terminal sink        │
                    │  + harness.risor pane     (-p/-i/-json)        │
                    │ cmd/anneal-legacy (all-Risor fallback/e2e)     │
                    └──────────┬────────────────────────┬────────────┘
                               │        core/app (wiring)
                    ┌──────────┴────────────────────────────────────┐
                    │ core/events.Bus (UI-agnostic fan-out)         │
                    │ core/uiapi (script adapter: queue/Ready/vmguard)│
                    ├───────────────────────────────────────────────┤
                    │ core/agent  Runner: dive Agent per run        │
                    │   toolkit+meta tools · permissions · hooks    │
                    │   compaction · digests · transcripts          │
                    ├──────────┬──────────┬──────────┬──────────────┤
                    │ core/    │ core/    │ core/    │ core/review  │
                    │ memory   │ skills   │ tieindex │ (idle        │
                    │ (bounded │ (SKILL.md│ (tie     │  reviewer)   │
                    │  stores) │  store)  │  search) │              │
                    ├──────────┴──────────┴──────────┴──────────────┤
                    │ core/model (Strata provider + HealthMonitor)  │
                    │ core/config · paths · journey · fsutil        │
                    │ harness · harnesslib · improve · probe · tools│
                    └───────────────────────────────────────────────┘
              truth: dive FileStore JSONL   index: tie (rebuildable)
```

## Runtime model

- **One foreground run at a time.** `Runner.Send`/`StartTask` starts a
  goroutine (`run()`); a second send gets a `run.busy` event. Each run builds
  a fresh dive `Agent` (tools, hooks, skill extension) against the shared
  session.
- **Conversation truth = dive `session.FileStore`** JSONL under
  `~/.anneal/sessions`. tie is only a *rebuildable search index* over it
  (collection `anneal` for new installs; existing configs keep their value).
  Session title = first task; todos live in session metadata (`store.Put`
  rewrites the header — message appends never do).
- **Hybrid UI.** `cmd/anneal` owns the window in Go/Fyne. The transcript,
  composer, permission inbox, sessions, todos, and status are native widgets.
  The harness editor/library is a fynerisor `CanvasObject`
  (`assets/ui/harness.risor`) embedded in a tab; it receives a restricted
  `agent` API (pull/actions only) and never controls the host window content.
- **Legacy UI.** `cmd/anneal-legacy` still runs the original all-Risor
  `assets/ui/main.risor` (`ANNEAL_UI_DIR` hot reload) and remains the target
  for the existing e2e drivers during the migration.
- **Threading rule:** Go never touches Fyne widgets from its own goroutines.
  `core/events.Bus` is the UI-agnostic fan-out: the journey log, Go shell, and
  headless sink are taps. The Go shell installs one `AddTap` consumer; a single
  goroutine normalizes events and updates widgets through `fyne.Do`.
  `uiapi.Bridge` is the script adapter: it subscribes to the bus, marshals to
  JSON, and queues onto fynerisor's `functionCalls` channel; pre-handler events
  are buffered (≤4096) until `Ready()`/`setOn`.
- **Compile gate:** the embedded harness pane is compiled at boot and by
  `anneal -check-ui`; the legacy full UI is compiled by
  `anneal-legacy -check-ui`. A broken script fails fast instead of producing
  an empty pane/window.

## Events (Go → UI)

Every payload maps to a JSON object on the script boundary and carries **all
keys** (empty slices marshal as `[]`, never `null`) because Risor dot-access
on missing keys is a hard error. The Go shell consumes the same values as maps
(normalized through JSON once) and uses safe accessors.

| event | when |
|-------|------|
| `provider.state` | health monitor change |
| `run.started` / `run.finished` / `run.busy` | run lifecycle (finished includes ok/text/error/usage/ctx_pct) |
| `text.delta` / `message.final` | streaming + final assistant text |
| `thinking.delta` | reasoning stream |
| `tool.start` / `tool.result` | tool calls (result has 600-char preview) |
| `perm.request` / `perm.resolved` | permission inbox (reply: `agent.respondPerm(id, allow|session|deny)`) |
| `session.started` / `session.opened` / `session.error` | session lifecycle (opened carries replay blocks) |
| `mode.changed` | permission mode |
| `todo.updated` | checklist (always the full list) |
| `tie.state` / `tie.rebuilt` | index status (header label + notices) |
| `memory.stage` / `memory.resolved` / `memory.updated` | staged/applied memory writes |
| `skill.stage` / `skill.resolved` / `skill.applied` / `skill.error` | staged/applied skill writes; loader problems |
| `mcp.state` | per-server connection/tool counts |
| `review.start` / `review.done` / `review.skipped` | background review (done carries the ledger) |
| `review.flag` | reviewer queued a harness deficiency (M16 → improve hints) |
| `improve.start/propose/probe/compose/iterdone/library/selector/budget/done/error` | improvement step (selector/budget only k>1) |
| `harness.warn` / `harness.warning` / `harness.promoted` / `harness.overlay` | component save re-validation, bad subagent file, library promote result, session overlay installed |
| `compaction.done` | context compaction (before/after tokens, msgs) |

## Permissions

`permission.Manager` (dive) with rules from config + `HookFromManager` on
PreToolUse. Prompts route through anneal's `dive.Dialog` implementation:
emit `perm.request`, block on a channel until `respondPerm` (or interrupt ⇒
deny). Plan-mode enforcement lives in the dialog (ask rules evaluate before
the mode gate — non-read-only tools are denied, not prompted). Subagents are
spawned through a **custom AgentFactory** that re-attaches the permission
hook — dive's default factory would spawn unguarded agents.

## Learning loop (Hermes-style)

1. **session_search** — word-token index in tie. Schema (§ in PLAN): session
   rows + msg rows; tokens under relation `tag` because tie servers
   reverse-index only `tag`/`tie-type` by default. Search = reverse-value AND
   query (`QuerySpec{Terms, Reverse, Scope:"session-msg", SortByValue:
   "msg-at"}`) + client-side collision filter; titles expanded lazily.
   Indexing: incremental per session after each run (`-rebuild-index`
   rebuilds). A tie outage degrades `session_search` but never blocks runs.
2. **memory** — `MEMORY.md` (2200 ch) + `USER.md` (1375 ch); add/patch/remove
   with dup-guard, unique-substring semantics, final-size checks, injection
   scanner. Optional staging (approval cards). Frozen snapshot injected per
   session via `dive.SetSystemReminder`.
3. **skills** — SKILL.md dirs discovered by the dive skill loader (`Skill`
   tool + catalog reminder); `skill_manage` create/patch/write_file/remove
   (archive); optional staging with diff previews.
4. **background review** — queue of finished sessions; idle scheduler
   (settle/max-age/preempt/requeue); bounded reviewer agent; **tool ledger**
   drives notifications (guards against small models claiming saves that
   never happened); timeline in `journey.jsonl`.

## Model layer

`core/model`: thin config→dive `openaicompletions.Provider` factory for
Strata, plus a `HealthMonitor` that probes `/v1/models` every 10 s (states:
`connecting`/`online`/`unreachable`/`wrong_service`) so the UI never blocks on
a cold server; first send probes synchronously once. Probes are serialized and
a transport error reports `unreachable` without touching a nil response.

## Known upstream issues & workarounds

- **MCP tool input** — dive hands tool input as plain `[]byte`; the mcp-go
  adapter used to miss it (base64 default branch, broke every MCP call).
  Fixed **upstream** (dive #309, shipped in `experimental/mcp v1.33.0`);
  anneal pins mcp **v1.42.0**, so the input-normalization wrapper was
  removed. The `qualifiedTool` rename wrapper stays: the Manager still
  registers unqualified tool names (`mcp__<server>__<tool>` collisions with
  built-ins are real — an unqualified `echo` misled the model). My issue
  dive#347 was a duplicate of #309 — root cause: I pinned "latest" from a
  *lexicographically* sorted tag list (v1.9.0 sorts above v1.33.0). Rule:
  verify with `go list -m -versions` / `sort -V` / `@latest` before ever
  claiming an upstream bug, and never file issues in repos the user doesn't
  own without explicit permission.
- **fynerisor issues #2–#6** (all fixed on `main`, unreleased): `app.Quit`,
  `Markdown.SetText`, container `Append/Remove/Refresh`, `tie.connect`
  collection hijack, `CompileScript`. **`NewCanvasObject`** (`b2b1478`) adds
  the embedding API used by the hybrid harness pane: a script renders inside a
  `fyne.CanvasObject` while the host owns the window; `WithHostWindow` opts
  into dialogs/shortcuts/close. anneal `go.mod` `replace`s fynerisor to the
  local checkout until a release ships these. Tracker:
  `docs/FYNERISOR-GAPS.md`.
- **tie query dialect**: value search requires `Reverse: true` and only
  whitelisted relations are reverse-indexed — hence the `tag` schema choice.

## Testing strategy

- Hermetic Go unit tests (scripted LLMs for compaction/reviewer, temp
  ANNEAL_HOME for stores) — `make test`.
- Go shell event-rendering tests with `fyne.io/fyne/v2/test` (no display).
- Live-infrastructure tests opt-in (`make live-tie-test`).
- Legacy Risor driver scripts exercising the real binary end-to-end —
  `testing/e2e/README.md` (targets `anneal-legacy`).
- `make ci` = vet + test + legacy `-check-ui` + pane `-check-ui`.
