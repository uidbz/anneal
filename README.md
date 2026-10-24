# fyagent

A self-improving, opencode-style desktop coding agent in Go.

- **Agent runtime:** [dive](https://github.com/deepnoodle-ai/dive)
- **GUI:** [fynerisor](https://github.com/uidbz/fynerisor) — the *entire* UI is
  a Risor script (`assets/ui/main.risor`), not Go
- **Model:** local [Strata](https://github.com/Niko1221/Strata) or any
  OpenAI Chat-Completions server
- **Episodic memory / session search:** [tie](https://github.com/uidbz/tie)
  triple store
- **Learning loop (Hermes-style):** bounded memory files, agent-authored
  skills, idle-time background self-review, journey timeline
- Also: MCP client, subagents, plan/build permission modes, session replay,
  context compaction, headless CLI.

All milestones (M0–M9) are complete — see [progress.md](progress.md) for the
running log and [docs/PLAN.md](docs/PLAN.md) for the original design.

## Features

| | |
|---|---|
| Streaming chat with tool calls | dive toolkit: Read/Write/Edit/Bash/Glob/Grep/WebFetch + todo |
| Sessions | JSONL truth under `~/.fyagent/sessions`, sidebar replay, resume-at-boot |
| Permissions | plan/build modes, approval inbox (allow / session-grant / deny), allow-ask-deny rules |
| Cross-session recall | `session_search` over the tie index (word tokens, AND, newest-first) |
| Memory | `MEMORY.md`/`USER.md` with caps, dup-guard, injection scan, optional write approval |
| Skills | SKILL.md authoring (`skill_manage`), auto-discovery via dive skill loader, staging |
| Self-review | background reviewer mines finished sessions while idle; ledger-backed notifications |
| Compaction | mid-turn + pre-generation summarization at 82% of context window |
| MCP | stdio/HTTP servers, `mcp__server__tool` names, permission-gated |
| Subagents | `Agent` tool (general-purpose/explore/plan) inheriting permission hooks |
| Headless | `fyagent-headless -p … / -i / -json` with permission policy flags |

## Requirements

- Go ≥ 1.27 (dive pins a very recent toolchain; `go.mod` says `go 1.27.2`)
- Fyne C dependencies for the GUI build (X11/Wayland dev libs, OpenGL) —
  same as any Fyne app; the headless binary links the same core, so it needs
  them at build time too, but runs without a display.

## Build

```sh
make build      # bin/fyagent + bin/fyagent-headless
make test       # hermetic unit tests
make ci         # vet + test + UI compile check
make live-tie-test   # tie round-trip against the real triplestore (opt-in)
```

> During development `go.mod` contains a `replace` pointing fynerisor at a
> local checkout with unreleased fixes (issues #2–#6, see
> [docs/FYNERISOR-GAPS.md](docs/FYNERISOR-GAPS.md)). Drop the replace once
> fynerisor v0.9.1 is released.

## Run

```sh
./bin/fyagent                       # desktop GUI
./bin/fyagent-headless -p "…"       # one-shot task, exit 0/1
./bin/fyagent-headless -i           # REPL
./bin/fyagent -rebuild-index        # rebuild the tie session index
./bin/fyagent -check-ui             # compile-check assets/ui/main.risor
```

Model endpoint: `~/.fyagent/config.json` or `FYAGENT_ENDPOINT` /
`FYAGENT_MODEL`. Everything degrades gracefully: tie down ⇒ session search
disabled (status line goes `index: offline`); Strata cold ⇒ health dots and
auto-reconnect.

## Docs

| doc | contents |
|-----|----------|
| [docs/USING.md](docs/USING.md) | GUI tour, slash commands, shortcuts, headless CLI, learning-loop operations |
| [docs/CONFIG.md](docs/CONFIG.md) | every config key, defaults, env overrides, state dir layout |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | packages, threading rule, event table, permission/learning/MCP design, upstream workarounds |
| [docs/PLAN.md](docs/PLAN.md) | original design + milestones |
| [docs/FYNERISOR-GAPS.md](docs/FYNERISOR-GAPS.md) | fynerisor gaps hit + issue/fix status (all fixed upstream on `main`) |
| [testing/e2e/README.md](testing/e2e/README.md) | Risor driver scripts: live end-to-end gates |
| [progress.md](progress.md) | dated milestone log with gotchas & decisions |

## Repo layout

```
cmd/fyagent            GUI entry (embeds UI, flags incl. -check-ui)
cmd/fyagent-headless   terminal entry (same core)
core/app               wiring + bridge API surface + journey tap + MCP connect
core/agent             Runner: dive agent per run, tools, hooks, transcripts
core/uiapi             Go ⇄ Risor event bridge (queue + guithread dispatch)
core/model             Strata provider factory + health monitor
core/memory skills tieindex review journey   learning-loop stores
core/config paths      config (defaults/env/save) and state-dir paths
assets/ui/main.risor   the whole GUI
testing/e2e            live driver-script gates
docs                   design + user/config/architecture docs
```
