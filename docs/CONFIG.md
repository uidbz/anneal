# fyagent configuration

State lives under `~/.fyagent/` (override with `FYAGENT_HOME`):

| path | what |
|------|------|
| `config.json` | the configuration below |
| `sessions/` | dive JSONL session files — **conversation truth** (newest resumed at boot) |
| `memories/` | `MEMORY.md`, `USER.md` (bounded learning stores) |
| `skills/` | agent-authored skills (`<name>/SKILL.md`) |
| `skills-archive/` | removed skills land here (`<name>-<timestamp>`) |
| `pending/` | staged writes awaiting approval: `pending/memory/`, `pending/skills/` |
| `journey.jsonl` | self-improvement timeline (memory/skill writes, review batches) |
| `logs/` | reserved |

## Environment overrides

| var | effect |
|-----|--------|
| `FYAGENT_HOME` | state directory root |
| `FYAGENT_ENDPOINT` | provider endpoint URL |
| `FYAGENT_MODEL` | model id (`auto` = first from `/v1/models`) |
| `FYAGENT_UI_DIR` | load `main.risor` from this dir instead of the embedded UI (hot-reload during development) |

## `provider`

| key | default | meaning |
|-----|---------|---------|
| `endpoint` | `http://127.0.0.1:8080/v1/chat/completions` | OpenAI Chat-Completions server (Strata default 8081 in practice) |
| `model` | `"auto"` | model id, or `auto` → first id from the endpoint's `/v1/models` |
| `api_key` | `"strata"` | Strata expects the literal `strata` |
| `context_window` | `0` | 0 = trust the provider-reported window; drives compaction threshold |
| `max_tokens` | 16384 | generation cap |
| `temperature` | 0.4 | |
| `reasoning_effort` | `"medium"` | `""|low|medium|high` (forwarded when the endpoint supports it) |
| `stream_first_event_timeout_s` | 240 | give up if the stream never starts |
| `stream_idle_timeout_s` | 120 | abort if a live stream goes silent |
| `max_retries` | 2 | provider-level retries (dive) |

Edit this section in `config.json` (there is no built-in settings tab yet;
UI scripts can call `agent.config()` / `agent.saveConfig(doc)`). Stores that
keep a live config pointer (memory, skills, review) re-read changed limits and
`write_approval` immediately; edited guardrail rule lines still need a restart.
## `agent` — permissions & toolkit

| key | default | meaning |
|-----|---------|---------|
| `permission_mode` | `default` | `default` \| `plan` \| `acceptEdits` \| `bypassPermissions` \| `dontAsk` |
| `allow` | `Read Glob Grep ListDirectory todo session_search memory skill_manage Skill` | never prompt |
| `ask` | `Bash Write Edit WebFetch` | prompt the user (GUI inbox card / headless policy) |
| `deny` | — | auto-deny |
| `tool_iteration_limit` | 80 | max tool rounds per run |
| `subagents` | true | register the `Agent` tool (general-purpose / explore / plan) |

Rule strings support specifiers: `"Bash(go test *)"`. Gotcha: **ask rules run
before dive's mode gate**, so plan mode is enforced in the approval dialog
(non-read-only tools are auto-denied, not prompted). The meta-tools
(`memory`, `skill_manage`, `Skill`, `session_search`) are read-only hints —
their real gate is `write_approval` staging; `Agent` keeps prompting (spawning
costs compute).

## `compaction`

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | |
| `threshold_pct` | 82 | compact when estimated tokens ≥ window × pct (floor 8000; est ≈ bytes/4) |
| `keep_recent_turns` | 4 | verbatim tail kept after summarizing |

Two hooks: mid-turn (dive `MidTurnCompactionHook`) and pre-generation
(`compaction.CompactMessages`); the UI gets a `compaction.done` notice.

## `memory`

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | registers the `memory` tool + snapshot injection |
| `memory_char_limit` | 2200 | cap for `MEMORY.md` |
| `user_char_limit` | 1375 | cap for `USER.md` |
| `write_approval` | false | true → writes are **staged** (`pending/memory/`) as approval cards instead of applied |

Writes are validated: dup-guard, unique-substring patch/remove, final-size
check, prompt-injection scanner (instruction-override/role-forgery patterns are
rejected — memory is re-injected into every future session). The snapshot is
injected once per session as a `<system-reminder name="fyagent-memory">` block
(frozen; compaction-safe).

## `skills`

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | `skill_manage` tool + dive skill loader (`Skill` tool, catalog reminder) |
| `write_approval` | false | stage create/patch/write_file/remove instead of applying |
| `extra_dirs` | — | additional skill roots scanned besides `~/.fyagent/skills`, `./.fyagent/skills`, and project `.claude/.agents/.dive` compat dirs |

## `review` — background self-improvement

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | finished sessions are queued for review |
| `defer_until_idle` | true | only run while idle |
| `idle_settle_s` | 45 | quiet time after the last run before reviewing |
| `max_age_s` | 1800 | …but run anyway once the queue is this old |
| `max_input_tokens` | 48000 | digest budget (≈bytes/4) |
| `notify` | true | emit review events to the UI (ledger-based notifications) |

Reviewer: bounded dive agent (6 iterations) with read-only tools + `memory` +
`skill_manage` + `session_search` + `harness_flag`, `source: "review"`.
Foreground runs preempt it (its batch is requeued). `/learn` (GUI) or
`-learn` (headless) forces a pass. Notifications derive from the **tool
ledger**, never model prose.

**Review→improve bridge (M16):** when the reviewer spots a recurring
*work-style* deficiency (unverified "done", repeated failing calls, etc.)
it calls `harness_flag`, which queues the gap in `~/.fyagent/harness-queue.json`.
The reviewer cannot edit harness files — flagging is its only output here.
The next `/improve` consumes up to 6 pending flags as hints for its
editors/composer (framed as claims to CHECK; probe evidence still decides).
The GUI harness panel and headless `/harness` list pending flags.

## `tie` — cross-session search index

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | registers `session_search`, indexes runs |
| `collection` | `fyagent` | tie collection holding the index (rebuildable) |
| `config_name` | `""` | tie client config name (`client.LoadConfig`); `""` = default |

The server URL/credentials come from the **tie client config**
(`~/.config/tie/config.toml`). Note: always pass the collection explicitly —
a config-file `DefaultCollection` can otherwise hijack queries (fynerisor
issue #5 was exactly this trap). The index only uses relations every tie
server reverse-indexes (`tag` + `tie-type`); rebuild with `fyagent
-rebuild-index` or `/index`.

## `mcp`

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | connect servers at boot |
| `servers` | `{}` | name → `{type?, command, args, env, url, disabled}`; `type` inferred (`command`→stdio, else http) |

Tools are exposed as `mcp__<server>__<tool>` and permission-gated like any
non-allowlisted tool. State is shown in the header (`mcp: 1/1 up, N tools`).

## `harness` — editable components (M10)

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | mount `~/.fyagent/harness/` components into every run |
| `char_limit` | 6000 | per-component cap on injected prompt text |

Components are plain files (see `~/.fyagent/harness/README.md`):
`system.md`, `guardrails.md` (prose + `allow:/ask:/deny:` rule lines —
same syntax as `agent.allow/ask/deny`, applied at startup, restart to
re-read), `tool-guidance.md`, `subagents/*.md` (dive FileLoader format;
override built-ins), `scripts/` (manifest surfaced to the model).
Check with `fyagent -check-harness` or `/harness`.

## `improve` — recursive harness improvement (M12–M13)

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | `/improve`, `-improve`, headless `/improve` |
| `library` | true | reflection + `~/.fyagent/harness-lib/` improvement library |
| `k` | 1 | recursion depth (iterations under `improvements/<run>/iter<i>/`) |
| `mode` | `sequential` | `sequential` chains each probed successor into the next iteration; `parallel` runs k independent steps and an anonymized selector picks the winner |
| `total_wall_s` | 0 | whole-step wall budget (0 = unbounded) |
| `max_total_tokens` | 0 | stop before starting an iteration past this usage |

One step = Propose (invariant + domain editors) → Probe (sandboxed replay +
behavioral contrast) → Compose (evidence-tied successor + `decisions.md`).
Successors apply as **session overlays** (`~/.fyagent/overlays/<session>/`);
evidence-supported steps also store a library entry
(`harness-lib/<id>/{harness/,EXPERIENCE.md,meta.json}`). Library entries are
re-drafted from by later steps (never auto-inherited) and become global
only via explicit `/harness promote <id>` (headless REPL; backups under
`harness-lib/promote-backup/`). Records: `~/.fyagent/improvements/<run>/`.

## `ui`

| key | default | meaning |
|-----|---------|---------|
| `ui_dir` | `""` | load UI script from this directory (dev hot-reload); `FYAGENT_UI_DIR` env does the same |

## Live-DB test opt-ins

Go tests stay hermetic unless opted in: `FYAGENT_LIVE_TIE=1 go test
./core/tieindex` exercises the real triplestore (and cleans up after itself).
