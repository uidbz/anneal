# Anneal

A self-improving, opencode-style desktop coding agent in Go.
**Anneal** is the former **fyagent** (renamed in 2026; see [Migration](#migration)).

- **Agent runtime:** [dive](https://github.com/deepnoodle-ai/dive)
- **UI:** a plain Go/[Fyne](https://fyne.io) shell for the stable surfaces
  (transcript, composer, permissions, sessions, todos) plus
  [fynerisor](https://github.com/uidbz/fynerisor) `CanvasObject` panes for
  editable/agent-adjacent surfaces (harness editor/library first). The original
  all-Risor UI is preserved as `anneal-legacy` during the migration.
- **Model:** local [Strata](https://github.com/Niko1221/Strata) or any
  OpenAI Chat-Completions server
- **Episodic memory / session search:** [tie](https://github.com/uidbz/tie)
  triple store
- **Learning loop (Hermes-style):** bounded memory files, agent-authored
  skills, idle-time background self-review, journey timeline, and a
  contrastive harness-improvement loop
- Also: MCP client, subagents, plan/build permission modes, session replay,
  context compaction, headless CLI.

All milestones (M0–M16) are complete. The hybrid UI migration has landed its
rename, Go shell, scripted harness pane, event-bus split, and headless
decoupling; transcript virtualization and the remaining shell surfaces are the
active follow-ups (see [progress.md](progress.md)).

## Features

| | |
|---|---|
| Streaming chat with tool calls | dive toolkit: Read/Write/Edit/Bash/Glob/Grep/WebFetch + todo |
| Sessions | JSONL truth under `~/.anneal/sessions`, replay, resume-at-boot |
| Permissions | plan/build modes, approval inbox (allow / session-grant / deny), allow-ask-deny rules |
| Cross-session recall | `session_search` over the tie index (word tokens, AND, newest-first) |
| Memory | `MEMORY.md`/`USER.md` with caps, dup-guard, injection scan, optional write approval |
| Skills | SKILL.md authoring (`skill_manage`), auto-discovery via dive skill loader, staging |
| Self-review | background reviewer mines finished sessions while idle; ledger-backed notifications |
| Harness improvement | Propose→Probe→Compose, evidence-gated library, session overlay, explicit promote |
| Compaction | mid-turn + pre-generation summarization at 82% of context window |
| MCP | stdio/HTTP servers, `mcp__server__tool` names, permission-gated |
| Subagents | `Agent` tool (general-purpose/explore/plan) inheriting permission hooks |
| Headless | `anneal-headless -p … / -i / -json` with permission policy flags |
| Scripted panes | `CanvasObject`-embedded Risor (harness editor now; more panes later) |

## Requirements

- Go ≥ 1.27 (`go.mod` pins `go 1.27.2`)
- Fyne C dependencies (X11/Wayland dev libs, OpenGL) for the GUI binaries
  (`anneal`, `anneal-legacy`) — same as any Fyne app.
- `anneal-headless` no longer imports Fyne/fynerisor (the event bus is
  UI-agnostic), so it builds and runs without GUI libraries.

## Build

```sh
make build            # bin/anneal, bin/anneal-headless, bin/anneal-legacy
make test             # hermetic unit tests
make ci               # vet + test + legacy/pane compile gates
make live-tie-test    # tie round-trip against the real triplestore (opt-in)
```

During development `go.mod` replaces fynerisor with a local checkout containing
unreleased fixes (issues #2–#6 and the `CanvasObject` embedding API, commit
`b2b1478`). Drop the replace once a release ships them.

## Run

```sh
./bin/anneal                       # hybrid Go shell + scripted harness pane
./bin/anneal-legacy                # original all-Risor UI (fallback/e2e)
./bin/anneal-headless -p "…"       # one-shot task, exit 0/1
./bin/anneal-headless -i           # REPL
./bin/anneal -rebuild-index        # rebuild the tie session index
./bin/anneal -check-ui             # compile-check the scripted pane
./bin/anneal-legacy -check-ui      # compile-check the legacy UI
./bin/anneal -send "task" -quit-after-run   # developer UI smoke
```

Model endpoint: `~/.anneal/config.json` or `ANNEAL_ENDPOINT` /
`ANNEAL_MODEL` (`FYAGENT_*` remains a deprecated alias). Everything degrades
gracefully: tie down ⇒ session search disabled (`index: offline`); Strata cold
⇒ health status and auto-reconnect.

## Migration

Anneal was previously `fyagent`. On first run:

- an existing `~/.fyagent` state directory is renamed to `~/.anneal` if the new
  one does not exist (sessions, memory, skills, harnesses, logs move together);
- `FYAGENT_HOME`, `FYAGENT_ENDPOINT`, `FYAGENT_MODEL`, `FYAGENT_UI_DIR`, and
  other `FYAGENT_*` variables are accepted as deprecated aliases;
- the script global remains `agent`, so existing `.risor` scripts keep working;
- existing tie collection names in `config.json` are preserved (new installs
  default to `anneal`);
- project-local `.fyagent/skills` is read as a compatibility path.

`bin/fyagent`, `bin/fyagent-headless`, and `bin/fyagent-legacy` symlinks are
created for one release.

## Docs

| doc | contents |
|-----|----------|
| [docs/USING.md](docs/USING.md) | GUI tour, slash commands, shortcuts, headless CLI, learning-loop operations |
| [docs/CONFIG.md](docs/CONFIG.md) | every config key, defaults, env overrides, state dir layout |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | packages, hybrid UI/threading rules, event table, permission/learning/MCP design |
| [docs/PLAN.md](docs/PLAN.md) | original design + milestones (historical) |
| [docs/FYNERISOR-GAPS.md](docs/FYNERISOR-GAPS.md) | fynerisor gaps hit + issue/fix status |
| [testing/e2e/README.md](testing/e2e/README.md) | Risor driver scripts: live end-to-end gates |
| [progress.md](progress.md) | dated milestone log with gotchas & decisions |

## License

BSD 3-Clause. See [LICENSE](LICENSE).

## Repo layout

```
cmd/anneal             hybrid Go/Fyne shell + embedded scripted harness pane
cmd/anneal-legacy      original all-Risor UI (fallback + e2e drivers)
cmd/anneal-headless    terminal entry (same core)
core/app               wiring + bridge API surface + journey tap + MCP connect
core/agent             Runner: dive agent per run, tools, hooks, transcripts
core/uiapi             Go ⇄ Risor event bridge (queue + guithread dispatch)
core/model             Strata provider factory + health monitor
core/memory skills tieindex review journey   learning-loop stores
core/harness harnesslib improve probe toolset  harness/improvement stack
core/config paths fsutil textutil            config, state paths, helpers
assets/ui/main.risor   legacy full UI
assets/ui/harness.risor scripted harness pane
testing/e2e            live driver-script gates
docs                   design + user/config/architecture docs
```
