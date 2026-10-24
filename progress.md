# progress.md

Running status log for **fyagent**. Keep this updated at every commit: tick milestones,
add dated notes, and record decisions/deviations from `docs/PLAN.md`.

## Milestones

- [ ] **M0** Skeleton: module + deps, GUI opens, Go↔Risor event round-trip
- [ ] **M1** Config + Strata provider + HealthMonitor + settings tab
- [ ] **M2** Agent loop + streaming chat + toolkit + stop + usage status
- [ ] **M3** Sessions UI + permission modes + approval bridge + todo
- [ ] **M4** tie index + session_search + compaction
- [ ] **M5** memory stores + memory tool + staging
- [ ] **M6** skills + skill_manage + subagents + slash commands
- [ ] **M7** background review + journey + notifications
- [ ] **M8** MCP + polish
- [ ] **M9** headless CLI (fyagent-headless)

Legend: mark `[~]` in progress, `[x]` done (note the commit hash in the notes below).

## Environment (verified 2026-10-24)

| Component | Where | Notes |
|---|---|---|
| Go | 1.27.2 | CGO/Fyne toolchain working |
| Strata serve | `http://127.0.0.1:8081` | `qwen3.8-flash-next-iq3_s`, n_ctx 262144; default port is 8080 but 8080 is taken by pwplay-server |
| tie triplestore | `http://storage:1161` | config `~/.config/tie/config.toml`; filehosts `:1162`; reverse-index = index-all unless server restricts |
| dive | pinned in go.mod | cloned reference at `/tmp/opencode/dive` (commit 50455f2) |
| fynerisor | v0.9.0 | whole GUI in Risor; missing widgets go upstream |

## Decisions

- 2026-10-24 — name `fyagent`; tie instead of SQLite; whole GUI in Risor; MCP + headless in first delivery (user).
- 2026-10-24 — dive JSONL FileStore stays conversation truth; tie is the rebuildable search index (collection `fyagent`). Word-token triples give exact-match AND search (tie has no FTS).
- 2026-10-24 — git: local repo, conventional commits per milestone/sub-step.

## Notes (newest first)

### 2026-10-24 — repo initialized
- git repo created (`main` branch); `docs/PLAN.md` (full plan), `progress.md`, README, .gitignore committed.
- Next: M0 — go.mod + skeleton GUI + bridge echo test.
