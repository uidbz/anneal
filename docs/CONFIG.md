# Anneal configuration

State lives under `~/.anneal/` (override with `ANNEAL_HOME`).

| path | what |
|------|------|
| `config.json` | the configuration below |
| `sessions/` | dive JSONL session files — **conversation truth** (newest resumed at boot) |
| `memories/` | `MEMORY.md`, `USER.md` (bounded learning stores) |
| `skills/` | agent-authored skills (`<name>/SKILL.md`) |
| `skills-archive/` | removed skills land here (`<name>-<timestamp>`) |
| `pending/` | staged writes awaiting approval: `pending/memory/`, `pending/skills/` |
| `permissions.json` | project-scoped `always` grants saved by the approval inbox |
| `journey.jsonl` | self-improvement timeline (memory/skill writes, review batches) |
| `logs/` | reserved |

## Environment overrides

| var | effect |
|-----|--------|
| `ANNEAL_HOME` | state directory root |
| `ANNEAL_ENDPOINT` | provider endpoint URL |
| `ANNEAL_MODEL` | model id (`auto` = first from `/v1/models`) |
| `ANNEAL_UI_DIR` | full-window script UI directory (`main.risor`, `-script-ui`) |
| `ANNEAL_PANE_DIR` | scripted-pane directory (`harness.risor`) |
| `ANNEAL_TEXT_SCALE` | UI text-size multiplier (clamped to 0.6–2.5) |
| `ANNEAL_LISTEN` | anneald listen addresses (comma-separated: `unix:/path`, `host:port`) |
| `ANNEAL_IDLE_TIMEOUT_S` | anneald idle auto-shutdown seconds (0 = off) |
| `ANNEAL_WORK_DIR` | anneald agent working directory |
| `ANNEAL_AUTH_TOKEN` | anneald bearer token (see `server.auth_token_env`) |

## `server` — anneald backend (server only; GUI/headless ignore it)

Full contract in [PROTOCOL.md](PROTOCOL.md); container guide in
[CONTAINERS.md](CONTAINERS.md).

| key | default | meaning |
|-----|---------|---------|
| `listen` | `["127.0.0.1:7777"]` | listeners: `unix:/path` (0600 socket) and/or `host:port` |
| `auth_token_env` | `"ANNEAL_AUTH_TOKEN"` | env var holding the bearer token; set + non-empty ⇒ hello must carry it |
| `idle_timeout_s` | `0` | seconds without client frames/run activity → clean shutdown (0 = never) |
| `drain_grace_s` | `20` | grace after shutdown start before connections close |
| `work_dir` | `""` | agent cwd ("" = process cwd; containers use `/anneal/work`) |

## `ui`

| key | default | meaning |
|-----|---------|---------|
| `ui_dir` | `""` | full-window script UI override (`ANNEAL_UI_DIR`) |
| `pane_dir` | `""` | scripted harness-pane override (`ANNEAL_PANE_DIR`) |
| `text_scale` | 1.0 | text-size multiplier, clamped to `0.6`–`2.5`; also `A-` / `A+`, Ctrl+= / Ctrl+- / Ctrl+0, or `/textsize` |

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

## `permission` — OpenCode-style rules

Anneal uses the same rule model as OpenCode: each rule resolves to `allow`,
`ask`, or `deny`, rules are matched against an **action** (tool kind) and a
**resource** (path, command, URL, subagent type, ...), and the **last matching
rule wins**. The default when nothing matches is `ask`.

```json
{
  "permission": {
    "*": "ask",
    "bash": {
      "*": "ask",
      "git status *": "allow",
      "git diff *": "allow",
      "git push *": "deny",
      "rm *": "deny"
    },
    "edit": {
      "*": "deny",
      "docs/*.md": "allow"
    },
    "webfetch": "ask"
  }
}
```

Shapes accepted:

- a single effect: `"permission": "allow"`
- action → effect: `{"bash": "ask", "edit": "allow"}`
- action → resource → effect (ordered): `{"bash": {"git *": "allow", "*": "ask"}}`
- OpenCode's native array form under either `permission` or `permissions`:
  `[{"action":"bash","resource":"git *","effect":"allow"}]`

The native `permissions` array is appended after the object-form `permission`
section, so it takes precedence when both are present.

Within an action, write the catch-all `"*"` first and specific rules after it —
that is the OpenCode convention and matches the last-match-wins evaluation.
Supported wildcards are `*` (any sequence, including `/`) and `?` (one
character). A pattern ending in ` *` also matches the bare prefix, so
`"git status *"` allows both `git status` and `git status --porcelain`.
Patterns may start with `~`, `$HOME`, or `${HOME}`; the home directory is
expanded when the config is loaded.

### Actions and resources

| action | covers | resource |
|--------|--------|----------|
| `read` | Read, ListDirectory, TextEditor view | file/directory path |
| `edit` | Write, Edit, TextEditor create/replace, patch | file path |
| `bash` | Bash | each parsed command segment (`git status`, `rm -rf build`) |
| `glob` | Glob | glob pattern |
| `grep` | Grep | regex pattern |
| `todowrite` | todo tool | `*` |
| `skill` | Skill loader | skill name |
| `skill_manage` | skill_manage | skill name or `*` |
| `memory` | memory | `memory` / `user` |
| `session_search` | session_search | `*` |
| `task` | Agent tool | subagent type (`explore`, `plan`, ...) |
| `script` | sandboxed Risor script tool | `*` |
| `webfetch` | WebFetch | URL |
| `external_directory` | any path outside the working directory | `<dir>/*` |
| `doom_loop` | the same tool call repeated 3× | tool name |
| MCP tool name | `mcp__server__tool` | `*` unless the tool has inputs |

Compound shell commands contribute one resource per unquoted `;`, `&&`,
`||`, `|`, `&`, or newline segment. Every segment must be allowed; a denied
segment denies the whole call, and any asking segment prompts once for the
batch. Commands with `$(...)`/backticks are kept whole because the substituted
part cannot be analyzed safely.

`external_directory` is checked **before** the tool's own action. Approving
`always` on it saves a `<dir>/*` grant for the project, so later accesses below
that directory do not prompt. Paths are symlink-resolved first; a symlink that
escapes the working directory counts as external.

### Defaults

Anneal keeps its safer legacy defaults, expressed as rules:

- reads/searches/todo/session search/memory/skill tools: `allow`
- Bash, Write/Edit, WebFetch, script: `ask`
- unknown tools and MCP tools: `ask`
- `.env` files ask even though reads are allowed; `.env.example` is allowed
  (this built-in guard is applied by the manager, not written into
  `config.json`; add a later `read` rule to override it)
- `external_directory` and `doom_loop` ask

To get OpenCode's permissive out-of-the-box behavior, add `"*": "allow"` at the
**end** of the rules. A configured `deny` cannot be overridden by a saved
`always` grant or `bypassPermissions`; a later config rule can still supersede
it (last match wins).

### Approving: once / always / reject

When an `ask` rule fires, the UI shows:

- **once** — run this call only (`agent.respondPerm(id, "allow")`)
- **always** — save the suggested patterns for this project and auto-allow
  matching pending cards (`agent.respondPerm(id, "session")`; `"always"` is
  also accepted)
- **reject** — deny the call (`agent.respondPerm(id, "deny")`)

The patterns come from the tool. Bash suggests a safe command prefix
(`git status *`); skills suggest the skill name; file/edit/read tools suggest
`*` for that action; external access suggests `<dir>/*`. `always` grants are
stored in `~/.anneal/permissions.json` under the project root. Delete an entry
(or the file) to revoke it.

### Modes

| mode | behavior |
|------|----------|
| `default` | evaluate rules normally |
| `plan` | non-read-only tools are denied (not prompted); reads still follow rules |
| `acceptEdits` | an `ask` on `edit` becomes `allow` |
| `bypassPermissions` | asks become `allow`; explicit denies still win |
| `dontAsk` | asks become `deny` (headless/automation) |

### Precedence

Rules are evaluated in this order, later winning:

1. legacy `agent.allow` / `agent.ask` / `agent.deny`
2. top-level `permission`, then `permissions` (native array)
3. `agent.permission`, then `agent.permissions` (per-agent override)
4. harness `guardrails.md` rules

The legacy lists remain supported and default to the safe anneal policy shown
above. A new-style rule appended later can therefore override them.

## `agent` — permissions & toolkit

| key | default | meaning |
|-----|---------|---------|
| `permission_mode` | `default` | `default` \| `plan` \| `acceptEdits` \| `bypassPermissions` \| `dontAsk` |
| `permission` | — | per-agent OpenCode-style rules (overrides top-level `permission`) |
| `allow` | `Read Glob Grep ListDirectory todo session_search memory skill_manage Skill` | legacy: never prompt |
| `ask` | `Bash Write Edit WebFetch script` | legacy: prompt (GUI inbox card / headless policy) |
| `deny` | — | legacy: auto-deny |
| `tool_iteration_limit` | 80 | max tool rounds per run |
| `subagents` | true | register the `Agent` tool (general-purpose / explore / plan) |
| `script` | true | register the `script` tool (sandboxed Risor data-glue runner, see below) |

Legacy rule strings support tool-name patterns and specifiers:
`"Bash(go test *)"`. They are canonicalized to the actions above
(`Write(...)` → `edit`, `Read`/`ListDirectory` → `read`, `Agent` → `task`), so
both syntaxes can be mixed, but prefer the `permission` section for new rules.
The meta-tools (`memory`, `skill_manage`, `Skill`, `session_search`) are
read-only hints — their real gate is `write_approval` staging; `Agent` keeps
prompting (spawning costs compute).

### `script` — sandboxed Risor data-glue tool

The `script` tool runs agent-authored Risor v2 in-process on fynerisor's
headless `core` runtime (fresh VM per call, 30s cap). It is glue for data
work — JSON/CSV/string reshaping, computing over files — not a command
runner: **no exec, no os module, no network, no import()**. Modules
available: `json`, `csv`, `strings`, `filepath`, `time`, and the
host-provided `fs` (`fs.read`, `fs.write`, `fs.list`, `fs.exists`,
`fs.remove`). The `fs` sandbox resolves symlinks before containment
checks: reads reach the project dir and `~/.anneal` (session logs,
harness files); writes reach the project dir and `~/.anneal/scratch`
only; secret dot-dirs (`.ssh`, `.aws`, …) are denied regardless. The
last script expression is returned as JSON; `print()` output is capped
at 8 KiB. Plan mode denies the tool (it can write); `dontAsk` headless
runs need an explicit `-allow script`.

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
injected once per session as a `<system-reminder name="anneal-memory">` block
(frozen; compaction-safe).

## `skills`

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | `skill_manage` tool + dive skill loader (`Skill` tool, catalog reminder) |
| `write_approval` | false | stage create/patch/write_file/remove instead of applying |
| `extra_dirs` | — | additional skill roots scanned besides `~/.anneal/skills`, `./.anneal/skills`, and project `.claude/.agents/.dive` compat dirs |

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
it calls `harness_flag`, which queues the gap in `~/.anneal/harness-queue.json`.
The reviewer cannot edit harness files — flagging is its only output here.
The next `/improve` consumes up to 6 pending flags as hints for its
editors/composer (framed as claims to CHECK; probe evidence still decides).
The GUI harness panel and headless `/harness` list pending flags.

## `tie` — cross-session search index

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | registers `session_search`, indexes runs |
| `collection` | `anneal` | tie collection holding the index (rebuildable); existing configs keep their old value |
| `config_name` | `""` | tie client config name (`client.LoadConfig`); `""` = default |

The server URL/credentials come from the **tie client config**
(`~/.config/tie/config.toml`). Note: always pass the collection explicitly —
a config-file `DefaultCollection` can otherwise hijack queries (fynerisor
issue #5 was exactly this trap). The index only uses relations every tie
server reverse-indexes (`tag` + `tie-type`); rebuild with `anneal
-rebuild-index` or `/index`.

## `mcp`

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | connect servers at boot |
| `auto_detect` | true | register installed built-in servers; currently `gortex` |
| `servers` | `{}` | name → `{type?, command, args, env, url, disabled}`; `type` inferred (`command`→stdio, else http) |

When `auto_detect` is on and a `gortex` binary is on `PATH`, Anneal registers
it as an MCP server (`gortex mcp`) unless a `gortex` key already exists in
`servers`. It runs the read-only `localization` preset (`GORTEX_TOOLS=localization`)
and allowlists its navigation tools (`mcp__gortex__explore`,
`batch_symbols`, `find_implementations`, plus catalog discovery), so code
lookups do not prompt on every call. Start the shared daemon once with
`gortex daemon start --detach` and index a repo with `gortex track <path>`;
without a daemon, `gortex mcp` exits unless its own `mcp.allow_embedded` is
enabled. Set `"auto_detect": false` or define your own `gortex` entry to opt
out.

Tools are exposed as `mcp__<server>__<tool>` and use that name as their
permission action (so `"mcp__gortex__explore": "allow"` works). Unlisted MCP
tools default to `ask`. State is shown in the header (`mcp: 1/1 up, N tools`).

## `harness` — editable components (M10)

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | mount `~/.anneal/harness/` components into every run |
| `char_limit` | 6000 | per-component cap on injected prompt text |

Components are plain files (see `~/.anneal/harness/README.md`):
`system.md`, `guardrails.md` (prose + `allow:/ask:/deny:` rule lines using the
same `Tool(specifier)` syntax as the legacy lists; they are translated to
OpenCode actions and evaluated **after** config rules, so a guardrail deny can
veto a config allow; applied at startup, restart to re-read),
`tool-guidance.md`, `subagents/*.md` (dive FileLoader format;
override built-ins), `scripts/` (manifest surfaced to the model).
Check with `anneal -check-harness` or `/harness`.

## `improve` — recursive harness improvement (M12–M13)

| key | default | meaning |
|-----|---------|---------|
| `enabled` | true | `/improve`, `-improve`, headless `/improve` |
| `library` | true | reflection + `~/.anneal/harness-lib/` improvement library |
| `k` | 1 | recursion depth (iterations under `improvements/<run>/iter<i>/`) |
| `mode` | `sequential` | `sequential` chains each probed successor into the next iteration; `parallel` runs k independent steps and an anonymized selector picks the winner |
| `total_wall_s` | 0 | whole-step wall budget (0 = unbounded) |
| `max_total_tokens` | 0 | stop before starting an iteration past this usage |

One step = Propose (invariant + domain editors) → Probe (sandboxed replay +
behavioral contrast) → Compose (evidence-tied successor + `decisions.md`).
Successors apply as **session overlays** (`~/.anneal/overlays/<session>/`);
evidence-supported steps also store a library entry
(`harness-lib/<id>/{harness/,EXPERIENCE.md,meta.json}`). Library entries are
re-drafted from by later steps (never auto-inherited) and become global
only via explicit `/harness promote <id>` (headless REPL; backups under
`harness-lib/promote-backup/`). Records: `~/.anneal/improvements/<run>/`.

## `ui`

| key | default | meaning |
|-----|---------|---------|
| `ui_dir` | `""` | full-window script UI directory (`main.risor`, `-script-ui`); `ANNEAL_UI_DIR` env does the same |
| `pane_dir` | `""` | scripted-pane directory (`harness.risor`); `ANNEAL_PANE_DIR` env does the same |

## Live-DB test opt-ins

Go tests stay hermetic unless opted in: `ANNEAL_LIVE_TIE=1 go test
./core/tieindex` exercises the real triplestore (and cleans up after itself).
