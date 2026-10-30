# Anneal — original plan (historical)

> **Historical document.** This is the original all-Risor design, kept for
> context. The project is now **Anneal**, and the UI is
> migrating to a Go/Fyne shell with embedded fynerisor `CanvasObject` panes.
> See [ARCHITECTURE.md](ARCHITECTURE.md) and [progress.md](../progress.md).

A self-improving, opencode-style desktop coding agent in Go.

- **Agent runtime:** [dive](https://github.com/deepnoodle-ai/dive) — generate→tool-call→repeat loop, Claude-Code-shaped toolkit, permissions, sessions, skills, subagents, compaction, MCP.
- **GUI:** [fynerisor](https://github.com/uidbz/fynerisor) — Fyne desktop window whose UI is written in **Risor scripts** (the whole GUI lives in Risor; missing widget bindings get added upstream to fynerisor, v0.9.0+).
- **Model:** [Strata](https://github.com/Niko1221/Strata) local inference server, OpenAI Chat Completions at `http://127.0.0.1:8081/v1/chat/completions`, model `qwen3.8-flash-next-iq3_s`, 256K context (verified live on this machine; default port is 8080, this machine serves on 8081).
- **Learning loop:** Hermes-Agent-style (Nous Research): agent-authored skills, bounded MEMORY.md/USER.md, cross-session recall, background self-improvement review, write-approval staging, journey timeline.
- **Episodic memory backend:** [tie](https://github.com/uidbz/tie) triple store (instead of SQLite) — sessions and word-token index live in a dedicated tie collection.

## 0. Decisions (locked)

1. Name: **anneal** (module `anneal`, binary `anneal`, state in `~/.anneal/`).
2. Session search / episodic memory uses **tie**, not SQLite.
3. The **whole GUI is Risor** (`ui/*.risor`); if a component is missing, it is added to fynerisor, not hardcoded in Go.
4. **MCP and the headless CLI ship in the first delivery.**

## 1. Environment facts (verified 2026-10-24)

- Go 1.27.2; CGO/Fyne toolchain works (fynerisor builds here).
- Strata serve live: `GET /v1/models` → `qwen3.8-flash-next-iq3_s`, `n_ctx: 262144`, port 8081 on this box.
- Port 8080 is occupied by `pwplay-server` → endpoint is always configurable; health probe distinguishes *unreachable* vs *wrong service* (404 on `/v1/models`).
- tie: user config at `~/.config/tie/config.toml`; triplestore at `http://storage:1161` (reachable), filehosts at `:1162`. anneal uses its own collection (`[Collections.anneal]` or `--tie-collection`).
- dive `providers/openaicompletions` supports `WithEndpoint`, `WithModel`, `WithAPIKey`, `WithStreamTimeouts(firstEvent, idle)`, `WithMaxRetries`, Generate + Stream, reasoning params — exactly what Strata needs.
- tie queries match **exact association values** (tag algebra); the server reverse-indexes *all* relations when unconfigured (`nil reverseRelations ⇒ index everything`) ⇒ word-token AND search works (unless the server pins `ReverseRelations`).
- dive extras used: `skill.Loader` (SKILL.md dirs, agentskills-compatible), `session.FileStore` (JSONL, turns), `subagent` pkg (explore/plan prompts), `experimental/{compaction,mcp,todo}`.
- fynerisor v0.9.0 GUI API: `gui.NewApp`, `WithGlobal`, `LoadScript/Execute/ShowAndRun`, `(*Window).Do` for cross-thread GUI updates, `widget.NewMarkdown/RichText/MultiLineEntry/List/Tree/Accordion/Toolbar`, dialogs, shortcuts.

## 2. Architecture

```
anneal (single Go binary, CGO for Fyne)
├─ GUI shell (fynerisor)  ◄──bridge──  ui/*.risor  (go:embed, disk-overridable)
│      ▲ events (window.Do)  /  calls (non-blocking)
├─ core (pure Go): SessionMgr RunMgr PermissionBridge MemoryStore
│   SkillStore(+skill_manage) TieIndex ReviewQueue Config Event-bus
├─ agent runtime (dive): hooks + extensions
│   tools: toolkit + memory + skill_manage + session_search + todowrite
│          + task(subagent) + skill + mcp
└─ OpenAI Chat Completions (SSE, tools) ──► Strata :8081
```

All intelligence/state lives in Go; the Risor layer is presentation + interaction only. The same core runs headless (`fynerisor/core`, no Fyne).

## 3. Repository layout

```
anneal/
├── cmd/anneal/main.go            # GUI entry
├── cmd/anneal-headless/main.go   # same core, flag CLI, no Fyne
├── core/
│   ├── config/        config schema, env overrides, atomic save
│   ├── model/         strata provider factory + HealthMonitor (/health,/v1/models)
│   ├── agent/         dive.NewAgent composition; system-prompt assembly; event mapping
│   ├── session/       dive FileStore wrapper (titles, project scoping, fork)
│   ├── tieindex/      tie collection schema + indexer + word-token search
│   ├── memory/        MEMORY.md/USER.md store, memory tool, injection scanner
│   ├── skill/         skill dirs, skill_manage tool, staging, archive, usage ledger
│   ├── review/        background self-improvement fork (defer-until-idle queue)
│   ├── permission/    OpenCode-style rules, saved grants, GUI approval bridge
│   ├── uiapi/         the `agent` global exposed to Risor + event marshaling
│   └── paths/         ~/.anneal resolution
├── ui/                main/chat/sessions/approvals/skills/memory/settings .risor
├── skills/            bundled starter skills (SKILL.md dirs)
├── subagents/         subagent .md definitions
├── internal/testutil/ fake OpenAI server (scripted tool rounds) for CI
└── docs/ PLAN.md, CONFIG.md
```

State (per user):

```
~/.anneal/
├── config.json
├── sessions/<id>.jsonl        # dive session.FileStore — conversation truth
├── memories/MEMORY.md USER.md
├── skills/…                   # agent-authored (create_dir); skills-archive/<date>/ for deletes
├── pending/{skills,memory}/<id>.json   # staged writes (write_approval)
├── usage.jsonl  logs/anneal.log
```

Project-level: `./.anneal/skills/`, read-compat `./.claude/skills/`, `./.agents/skills/`, context file `./AGENTS.md`.

## 4. tie episodic index (`core/tieindex`)

Conversation truth stays in dive JSONL; tie is the searchable index and can be rebuilt from disk (`anneal index rebuild`). Collection `anneal` in the user's tie triplestore.

Schema (all values exact-match triples; search = reverse-association AND algebra):

```
(sessionID, tie-type,    session)      (sessionID, title, <t>)
(sessionID, project,     <abs path>)   (sessionID, updated-at, <RFC3339-UTC>)
(sessionID, model,       <id>)         (sessionID, session-msg, <msgID>)

(msgID, tie-type,   session-msg)       (msgID, session-of, <sessionID>)
(msgID, role,       user|assistant|tool)
(msgID, tool,       <tool name>)       (msgID, turn, <n>)
(msgID, msg-at,     <RFC3339-UTC>)     (msgID, snippet, <first ~400 chars>)
(msgID, word,       <lowercased token, len≥3, capped ~300/msg>)
```

- `msgID = sha1(sessionID + turn + index)` (hex), stable across re-index.
- **search** tool: tokens → `QuerySpec{Terms: toks, Scope: "session-msg", SortByValue: "updated-at", Descending: true, Expand: true}`; seed = reverse assoc of token[0], AND-intersect the rest — word-exact boolean search, ms-latency, zero LLM cost.
- scroll/browse: load JSONL session, window around hit's turn.
- Missing/stale collection: indexer creates on demand; degraded mode logs and keeps agent functional.

## 5. opencode-parity features

| opencode | anneal | M |
|---|---|---|
| sessions list/resume/fork | FileStore + sidebar + tie titles | M3 |
| plan/build modes + approvals | permission.ModePlan + approval inbox | M3 |
| subagents | dive subagent pkg, `task` tool | M6 |
| todo | experimental/todo | M3 |
| MCP | experimental/mcp toolset | M8 |
| compaction | experimental/compaction @82% ctx | M4 |
| keybinds, slash cmds | window.AddShortcut, `/skill` routing | M2/M6 |
| status bar | tok in/out, tok/s, ctx% | M2 |
| config | ~/.anneal/config.json | M1 |
| headless CLI | cmd/anneal-headless | M9 |

## 6. Self-improvement (Hermes loop)

- **memory tool**: MEMORY.md (2200 ch) + USER.md (1375 ch); ops add/patch/remove (unique-substring match), batch checked on final size; frozen snapshot at session start via model-only `ContextReminder`; dup-guard; injection scanner; `write_approval` staging.
- **skill_manage**: create/patch/write_file/remove→archive/list/view in `~/.anneal/skills`; SKILL.md standard (dive loader scans it); staging + GUI diff.
- **session_search**: tie-backed (§4).
- **background review**: PostGeneration enqueues; runs only when idle (no run + Strata quiet ≥45 s, coalescing, max-age fallback, re-queue on preemption); reviewer = small dive agent (memory + skill_manage + read-only), digest replay, iteration cap 6, input-token cap; notifications emitted from the **tool ledger** (never from model prose — guards against small models claiming saves they didn't make); `learnNow()` manual trigger.
- **journey**: timeline of skill_events + memory_events in the GUI.
- System-prompt learning rules: save corrections/conventions/env facts via `memory`; after non-trivial workflows offer `skill_manage` create; patch skills in place when improved during use; prefer `session_search` over memory bloat.

## 7. GUI (all Risor)

- Bridge is fire-and-forget + JSON events: `agent.send/interrupt/setMode/newSession/resume/fork/deleteSession/listSessions/pendingApprovals/answerApproval/pendingWrites/approveWrite/rejectWrite/writeDiff/skills/skillView/skillDelete/learnNow/memory/userProfile/memoryEdit/journey/provider/config/saveConfig/on(callback)`.
- Events: `run.started, text.delta, thinking.delta, tool.start, tool.result, run.finished{usage,reason}, approval.requested, approval.resolved, staged.created, learned{kind,name,gist}, provider.state, session.updated`; deltas batched at 50 ms; every handler wrapped in `window.Do`.
- Layout: toolbar (mode Select, model/status) | sessions sidebar + Skills/Memory/Journey tabs | markdown transcript (streaming plain tail block → finalized blocks; collapsible tool cards) | MultiLineEntry input with slash-commands; approval bar with Ctrl+Y/Ctrl+Shift+Y/Ctrl+N.
- Ctrl+R reloads the UI script without touching the core (fynerisor superpower).
- Missing bindings get upstreamed to fynerisor as needed (tracked in progress.md).

## 8. Security

core/permission rules/modes first (OpenCode-style allow/ask/deny, external_directory, doom_loop); path policy (project + ~/.anneal; deny `.ssh`, `.aws`, secrets); bash guard (danger patterns, no writes to ~/.anneal); store writes injection-scanned; skills deletions archive; MCP tools ask-by-default; API keys env-only, never logged.

## 9. Milestones (M0–M9)

- **M0** Skeleton: go.mod, GUI opens, Go↔Risor echo round-trip. ✔ gate: event storm ≥50/s no freeze.
- **M1** Config + Strata health + settings tab. ✔ gate: shows model id; wrong-service detected; hot endpoint change.
- **M2** Agent loop + streaming chat + toolkit + stop button + usage status. ✔ gate: real fix-a-test task on Strata.
- **M3** Sessions UI, permissions/plan-build modes, approval bridge, todo. ✔ gate: plan mode strips Write; approvals round-trip.
- **M4** tie index + session_search + compaction. ✔ gate: cross-launch search <50 ms; 40-turn run stays under window.
- **M5** memory stores + tool + staging. ✔ gate: correction lands, snapshot re-injected next session.
- **M6** skills consumption + skill_manage + subagents + slash cmds. ✔ gate: "save what we did" → loads next session.
- **M7** background review + journey + notifications. ✔ gate: idle-time consolidation observable.
- **M8** MCP + polish (logs, toggles, sparklines).
- **M9** headless CLI (`anneal-headless -p "…"`).

≈7–9 focused days; M0–M5 ≈ 4.

## 10. Testing

- Unit tests: config, memory, skill store, tieindex (against tie test-env `:2161/:2162` when up, else skip), uiapi bridge as plain Go.
- Fake OpenAI server (`httptest`, scripted tool-call rounds, SSE) → deterministic agent/permission/compaction/review tests; CI never needs Strata.
- `make e2e-strata` manual gate against live Strata.
- Golden system-prompt snapshots; GUI kept thin, manual 10-click checklist per milestone.

## 11. Risks → mitigations

| Risk | Mitigation |
|---|---|
| local model misses `memory`/skill tool calls | tool-ledger ground truth; results echo file state; prompt rules; retry on unknown tool |
| GPU contention (review/compaction vs interactive) | defer-until-idle queue; token caps; high compaction threshold (256K ctx) |
| port 8080 conflict / endpoint drift | HealthMonitor states; endpoint in config + settings UI |
| slow prompt processing stalls streams | generous first-event stream timeout; visible thinking state; dive retries |
| Risor VM threading | non-blocking API; JSON payloads; window.Do everywhere; delta batching |
| dive experimental API churn | pinned go.mod; thin wrappers in core/ |
| prompt-injection via learned stores | write-time scanner; staging mode; journey pruning |

## 12. Recursive Harnessing (M10–M16, added 2026-10-25)

Port of **Harness²** (arXiv 2610.14224, github.com/google-research/harness2,
Apache-2.0 — concepts ported, no code vendored): recursive self-improvement
of the *harness* (prompt/guardrails/memory/skills/tool-guidance/scripts/
subagents as editable files), not the model. Two coupled loops:

- **Task-level recursion** — contrastive **Propose–Probe–Compose** on the
  current task *without ground truth*: reference execution → two scoped
  editors (invariant deficiencies vs domain knowledge) propose candidate
  harnesses → probes re-run the task per candidate in a sandbox → behavioral
  contrasts → a composer selectively merges edits, decisions tied to
  observations.
- **Harnessing-level recursion** — an **improvement library**
  (`~/.anneal/harness-lib/<entry>/ = harness/ snapshot + EXPERIENCE.md +
  meta.json`) written only when probe evidence supports the lesson. Every
  task starts from the common ℋ₀ again; library entries are *re-drafted
  from*, never blindly inherited. (Paper ablations: library −3.1, editor
  scoping −2.9, composition −1.6 — all three are kept.)

Why: anneal's M7 review loop is the "ReasoningBank" baseline in the paper
(lessons → memory only), which plateaus or regresses. Evidence-driven
harness edits beat it consistently. Same-model editing is validated by the
paper (executor == editor improves every tested model), so local Strata
alone is viable.

Locked decisions (user, 2026-10-25):
1. **Default recursion mode: sequential** (k chained steps) — cheaper and
   deeper per unit cost on one local model; parallel (k candidates + k
   compositions + anonymized deliverable selector) is an explicit budget
   choice in M14.
2. **Successor harnesses never touch the global harness automatically.**
   Task successions live in `sessions/<id>/harness-overlay/` (session
   scope); only library entries accumulate automatically, and promoting one
   into `~/.anneal/harness/` is an explicit, approval-gated user action
   (`/harness promote`). This keeps the paper's no-inheritance property and
   the existing staging philosophy.

Milestones:

- **M10 harness component layer** ✔ — `~/.anneal/harness/` file-mounted
  components (`system.md`, `guardrails.md` prose + `deny:/ask:/allow:` rule
  lines translated to OpenCode-style permission rules, `tool-guidance.md`,
  `subagents/*.md` via dive's subagent FileLoader, `scripts/` manifest);
  prompt composition per run; `-check-harness`, `/harness`.
- **M11 probe infrastructure** ✔ — sandboxed task replay (Go tree copy with
  heavy-dir exclusions, dive-native WorkspaceDir jail + defense-in-depth
  PreToolUse hook, matched model config recorded), execution records
  `e=(τ,y)`, step records `ξ` under
  `~/.anneal/improvements/<runid>/`.
- **M12 one contrastive step (k=1)** ✔ — `core/improve`: invariant+domain
  editors (edit right jailed to a scratch harness copy), probe+contrast
  notes, evidence-tied composition, session overlay apply;
  `anneal-headless -improve`, GUI `/improve`. Ledger truth, never prose.
- **M13 improvement library** ✔ — `core/harnesslib` + reflector
  (EXPERIENCE.md only with evidence), index fetched into Propose/Compose
  prompts; tie-index EXPERIENCE bodies (`tag` + Reverse, reuse tieindex);
  `/harness promote <id>` explicit (REPL), with promote-backup snapshots.
- **M14 recursion budget** ✔ — `-k N -improve-mode sequential|parallel` +
  config keys (sequential default, per locked decision), anonymized
  deliverable selector for k>1, token/wall budgets, `-improve-resume <id>`
  skipping completed `improvements/<runid>/iter<i>/` stages. Parallel
  iterations run one-at-a-time on purpose (one local GPU; concurrency
  would only starve streams).
- **M15 GUI** ✔ — harness view (toolbar button / `/harness`): component
  files editable inline + guardrail rule counts (restart-to-apply note),
  improve stage events in the transcript, library browser with
  EXPERIENCE.md viewer + two-click promote (with backups), journey
  integration for promotes.
- **M16 docs + review-loop bridge** ✔ — reviewer's `harness_flag` queues
  work-style deficiencies to `harness-queue.json` (it never edits harness
  files: no evidence → no edits); the next `/improve` consumes up to 6 as
  CHECK-me hints; panel/REPL show pending flags; docs pass.

Out of scope: editable middleware/plugin component (dive Hooks stay
compiled Go), benchmark suites, second editor endpoint (config leaves room).
| tie server restricted ReverseRelations | detect at startup; degrade session_search with warning (JSONL browse still works) |
