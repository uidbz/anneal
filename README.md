# fyagent

A self-improving, opencode-style desktop coding agent in Go.

- Agent runtime: [dive](https://github.com/deepnoodle-ai/dive)
- GUI: [fynerisor](https://github.com/uidbz/fynerisor) — the whole UI is written in Risor scripts
- Model: local [Strata](https://github.com/Niko1221/Strata) (OpenAI-compatible endpoint, default `http://127.0.0.1:8081/v1/chat/completions`)
- Learning loop: Hermes-Agent-style — agent-authored skills, bounded memory files,
  tie-backed cross-session recall, background self-improvement review
- Episodic memory: [tie](https://github.com/uidbz/tie) triple store

Status: early build — see `progress.md` for the running log and `docs/PLAN.md` for
the full design and milestones.

## Build

```sh
make build        # fyagent (GUI, requires Fyne deps) + fyagent-headless
make test         # unit + fake-server integration tests (no GPU needed)
```

## Run

```sh
./bin/fyagent                       # desktop GUI
./bin/fyagent-headless -p "task"    # non-interactive
```

Requires a running Strata (or any OpenAI Chat-Completions server) — the endpoint and
model are set in `~/.fyagent/config.json` or env `FYAGENT_ENDPOINT` / `FYAGENT_MODEL`.
Session search requires a running tie triplestore; everything else degrades gracefully.
