# Anneal

A self-improving, opencode-style desktop coding agent in Go.

- **Agent runtime:** [dive](https://github.com/deepnoodle-ai/dive)
- **UI:** a plain Go/[Fyne](https://fyne.io) shell for the stable surfaces
  (transcript, composer, permissions, sessions, todos, activity) plus
  [fynerisor](https://github.com/uidbz/fynerisor) `CanvasObject` panes for
  editable/agent-adjacent surfaces (harness editor/library first). A
  `-script-ui` developer mode keeps full-window scripted e2e drivers and
  custom UI hacking working.
- **Model:** local [Strata](https://github.com/Niko1221/Strata) or any
  OpenAI Chat-Completions server
- **Episodic memory / session search:** [tie](https://github.com/uidbz/tie)
  triple store
- **Learning loop (Hermes-style):** bounded memory files, agent-authored
  skills, idle-time background self-review, journey timeline, and a
  contrastive harness-improvement loop
- Also: MCP client, optional gortex code-intelligence auto-detect, subagents,
  plan/build permission modes, session replay,
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
| Permissions | OpenCode-style allow/ask/deny rules (last match wins, wildcards), approval inbox (once / always / reject), saved project grants, plan/build modes |
| Cross-session recall | `session_search` over the tie index (word tokens, AND, newest-first) |
| Memory | `MEMORY.md`/`USER.md` with caps, dup-guard, injection scan, optional write approval |
| Skills | SKILL.md authoring (`skill_manage`), auto-discovery via dive skill loader, staging |
| Self-review | background reviewer mines finished sessions while idle; ledger-backed notifications |
| Harness improvement | Propose→Probe→Compose, evidence-gated library, session overlay, explicit promote |
| Compaction | mid-turn + pre-generation summarization at 82% of context window |
| MCP | stdio/HTTP servers, `mcp__server__tool` names, permission action per tool |
| Subagents | `Agent` tool (general-purpose/explore/plan) inheriting permission hooks |
| Headless | `anneal-headless -p … / -i / -json` with permission policy flags |
| Backend/frontend split | `anneald` server + WebSocket protocol ([docs/PROTOCOL.md](docs/PROTOCOL.md)); `anneal-headless -server <addr>` drives it remotely; container deployment with idle auto-shutdown ([docs/CONTAINERS.md](docs/CONTAINERS.md)) |
| Scripted panes | `CanvasObject`-embedded Risor (harness editor now; more panes later) |
| Script tool | sandboxed Risor v2 data glue for the agent (fynerisor headless `core`; confined fs, no exec/net) |

## Requirements

- Go ≥ 1.27 (`go.mod` pins `go 1.27.2`)
- Fyne C dependencies (X11/Wayland dev libs, OpenGL) for `anneal` — same as any
  Fyne app.
- `anneal-headless` does not import Fyne (the event bus is UI-agnostic and
  the `script` tool uses fynerisor's GUI-free `core` package), so it builds
  and runs without GUI libraries. Same for `anneald` (the backend server) —
  it is the CGO-free container binary.

## Build

```sh
make build            # bin/anneal, bin/anneal-headless
make test             # hermetic unit tests
make ci               # vet + test + pane/script-UI compile gates
make live-tie-test    # tie round-trip against the real triplestore (opt-in)
```

## Install

```sh
make install                        # ~/.local/bin + XDG desktop entry/icon
make install PREFIX=/usr/local      # system-wide (run with appropriate privileges)
make uninstall
```

`make install` installs `anneal`, `anneal-headless`, an
`anneal.desktop` launcher under `$PREFIX/share/applications`, and the
hicolor icon under `$PREFIX/share/icons/hicolor/scalable/apps`.
`DESTDIR` is supported for packaging.

During development `go.mod` replaces fynerisor with a local checkout containing
unreleased fixes (issues #2–#6 and the `CanvasObject` embedding API, commit
`b2b1478`). Drop the replace once a release ships them.

## Run

```sh
./bin/anneal                       # hybrid Go shell + scripted harness pane
./bin/anneal-headless -p "…"       # one-shot task, exit 0/1
./bin/anneal-headless -i           # REPL
./bin/anneald                      # backend server (docs/PROTOCOL.md)
./bin/anneal-headless -server 127.0.0.1:7777 -p "…"   # drive a remote anneald
./bin/anneal -rebuild-index        # rebuild the tie session index
./bin/anneal -check-ui             # compile-check the scripted pane
./bin/anneal -script-ui -check-script-ui  # compile-check a driver UI
./bin/anneal -send "task" -quit-after-run   # developer UI smoke
```

Container deployment (image carries the runtime + dev toolchain; config and
state come from one bind mount; server idles itself out): see
[docs/CONTAINERS.md](docs/CONTAINERS.md). `make docker` builds `anneal:dev`.

Model endpoint: `~/.anneal/config.json` or `ANNEAL_ENDPOINT` /
`ANNEAL_MODEL`. Everything degrades gracefully: tie down ⇒ session search
disabled (`index: offline`); Strata cold ⇒ health status and auto-reconnect.

## Docs

| doc | contents |
|-----|----------|
| [docs/USING.md](docs/USING.md) | GUI tour, slash commands, shortcuts, headless CLI, learning-loop operations |
| [docs/CONFIG.md](docs/CONFIG.md) | every config key, defaults, env overrides, state dir layout |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | packages, hybrid UI/threading rules, event table, permission/learning/MCP design |
| [docs/PROTOCOL.md](docs/PROTOCOL.md) | anneald wire protocol (normative) |
| [docs/CONTAINERS.md](docs/CONTAINERS.md) | containerized backend, mount layout, idle lifecycle |
| [docs/PLAN.md](docs/PLAN.md) | original design + milestones (historical) |
| [docs/FYNERISOR-GAPS.md](docs/FYNERISOR-GAPS.md) | fynerisor gaps hit + issue/fix status |
| [testing/e2e/README.md](testing/e2e/README.md) | Risor driver scripts: live end-to-end gates |
| [progress.md](progress.md) | dated milestone log with gotchas & decisions |

## License

BSD 3-Clause. See [LICENSE](LICENSE).

## Repo layout

```
cmd/anneal             hybrid Go/Fyne shell + embedded scripted harness pane
cmd/anneal-headless    terminal entry (same core, no GUI deps; -server drives a remote anneald)
cmd/anneald            backend server (WebSocket protocol, idle auto-shutdown)
core/app               wiring + bus API surface + journey tap + MCP connect
core/server            protocol server: hello/RPC/event fan-out over the bus
core/client            protocol client (used by -server modes, future GUI remote)
core/agent             Runner: dive agent per run, tools, hooks, transcripts
core/events            UI-agnostic event bus (Go shell, journey, headless)
core/uiapi             script adapter (queue/Ready/vmguard) for fynerisor
core/model             Strata provider factory + health monitor
core/memory skills tieindex review journey   learning-loop stores
core/harness harnesslib improve probe toolset  harness/improvement stack
core/config paths fsutil textutil            config, state paths, helpers
assets/ui/harness.risor scripted harness pane
testing/e2e            live driver-script gates (`-script-ui`)
docs                   design + user/config/architecture docs
```
