# fyagent architecture

```
                    ┌──────────────────────────────────────────────┐
                    │  cmd/fyagent (GUI)        cmd/fyagent-headless│
                    │   fynerisor window        terminal sink       │
                    │   + assets/ui/main.risor  (-p/-i/-json)       │
                    └──────────┬───────────────────────┬───────────┘
                               │        core/app (wiring)          
                    ┌──────────┴───────────────────────────────────┐
                    │ core/uiapi Bridge  (Go ⇄ Risor event bridge) │
                    ├──────────────────────────────────────────────┤
                    │ core/agent  Runner: dive Agent per run       │
                    │   toolkit+meta tools · permissions · hooks   │
                    │   compaction · digests · transcripts         │
                    ├──────────┬──────────┬──────────┬─────────────┤
                    │ core/    │ core/    │ core/    │ core/review │
                    │ memory   │ skills   │ tieindex │ (idle       │
                    │ (bounded │ (SKILL.md│ (tie     │  reviewer)  │
                    │  stores) │  store)  │  search) │             │
                    ├──────────┴──────────┴──────────┴─────────────┤
                    │ core/model (Strata provider + HealthMonitor) │
                    │ core/config · core/paths · core/journey      │
                    └──────────────────────────────────────────────┘
              truth: dive FileStore JSONL   index: tie (rebuildable)
```

## Runtime model

- **One foreground run at a time.** `Runner.Send` starts a goroutine
  (`run()`); a second send gets a `run.busy` event. Each run builds a fresh
  dive `Agent` (tools, hooks, skill extension) against the shared session.
- **Conversation truth = dive `session.FileStore`** JSONL under
  `~/.fyagent/sessions`. tie is only a *rebuildable search index* over it
  (collection `fyagent`). Session title = first task; todos live in session
  metadata (`store.Put` rewrites the header — message appends never do).
- **The GUI is a Risor script** (`assets/ui/main.risor`, embedded;
  `FYAGENT_UI_DIR` overrides for hot reload). Everything user-visible is
  driven by events; everything user-initiated is an `agent.*` call.
- **Threading rule:** Go never touches Fyne widgets from its own goroutines.
  All Go→UI communication goes through `uiapi.Bridge`, which marshals the
  event to JSON and queues it onto fynerisor's `functionCalls` channel
  (guithread-safe). Events emitted before the script registers its handler
  are buffered (≤4096) and flushed at `Ready()`. `Bridge.SetTap` installs a
  Go-side observer (journey log; headless sink).
- **Compile gate:** `fw.CompileScript()` (fynerisor ≥ main, issue #6) runs at
  every boot and in `fyagent -check-ui`; a broken UI script fails fast with a
  line number instead of a black window.

## Events (Go → Risor, JSON)

Every payload carries **all keys** (empty slices marshal as `[]`, never
`null`) because Risor dot-access on missing keys is a hard error.

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
| `skill.stage` / `skill.resolved` / `skill.applied` | staged/applied skill writes |
| `mcp.state` | per-server connection/tool counts |
| `review.start` / `review.done` / `review.skipped` | background review (done carries the ledger) |
| `compaction.done` | context compaction (before/after tokens, msgs) |

## Permissions

`permission.Manager` (dive) with rules from config + `HookFromManager` on
PreToolUse. Prompts route through fyagent's `dive.Dialog` implementation:
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
connecting/online/degraded/offline) so the UI never blocks on a cold server;
first send probes synchronously once.

## Known upstream issues & workarounds

- **dive#347**: dive hands tool input as plain `[]byte`; the mcp-go adapter's
  type switch misses it (base64 default branch) → every MCP call failed.
  fyagent wraps MCP tools (`qualifiedTool`) and normalizes input to
  `json.RawMessage` (+ unwraps double-encoded JSON strings some local servers
  emit).
- **fynerisor issues #2–#6** (all fixed on `main`, unreleased): `app.Quit`,
  `Markdown.SetText`, container `Append/Remove/Refresh`, `tie.connect`
  collection hijack, `CompileScript`. fyagent `go.mod` `replace`s fynerisor
  to the local checkout until v0.9.1. Tracker: `docs/FYNERISOR-GAPS.md`.
- **tie query dialect**: value search requires `Reverse: true` and only
  whitelisted relations are reverse-indexed — hence the `tag` schema choice.

## Testing strategy

- Hermetic Go unit tests (scripted LLMs for compaction/reviewer, temp
  FYAGENT_HOME for stores) — `make test`.
- Live-infrastructure tests opt-in (`make live-tie-test`).
- Risor driver scripts exercising the real binary end-to-end —
  `testing/e2e/README.md`.
- `make ci` = vet + test + check-ui.
