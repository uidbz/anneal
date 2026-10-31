# Working on several projects at once

Anneal's recommended pattern is **one instance per project**. That is about
*isolation*, not about working on one project at a time: run as many instances
as you need, side by side, one per project. This page explains what an instance
is, how to run several, and what to do when a task in project A needs a change
in project B before it can continue.

## The model

- An **instance** is one backend process: the embedded core of a local
  `anneal` window, or one `anneald` server (possibly in a container).
- A **project** is the instance's working directory. The permission jail,
  saved grants, project skills, the MCP server's view, gortex tracking and a
  session's recorded cwd are all anchored to it.
- There is deliberately **no `/cd`**: moving a live instance to another root
  would weaken every one of those boundaries. Switching projects means
  switching *instances* (a window, a terminal, a backend selection).
- The instance boundary is always a process boundary. **State** can be shared
  or separate, depending on `ANNEAL_HOME`:

| state | shared (`~/.anneal`) | separate (`ANNEAL_HOME=/…/a`) |
|---|---|---|
| config / UI prefs | shared (last write wins) | separate |
| sessions | one store, now tagged with cwd | separate |
| saved permission grants | keyed by project path (locked, safe across processes) | separate |
| MEMORY.md / USER.md | shared | separate |
| skills | shared (can also be shared via `skills.extra_dirs`) | separate |
| harness / improvement library | shared | separate |

## Pick a setup

| setup | isolation | what is shared | best for |
|---|---|---|---|
| **Two local windows** — `anneal -C ~/src/a`, `anneal -C ~/src/b` | process + project path for permissions, sessions tagged by cwd | config, memory, skills, session list (filtered per project) | everyday work; zero setup |
| **Local windows, one state dir each** — `ANNEAL_HOME=~/anneal-env/a anneal -C ~/src/a` | full (process + state) | nothing unless you add `skills.extra_dirs` | strict separation, different harnesses/models per project |
| **One `anneald` per project, GUI windows attached** | full (separate processes, state optional) | skills only if configured | recommended for many projects; survives window closes, can live in a container |
| **One container per project** | full + filesystem sandbox | nothing | untrusted code, heavy toolchains, remote machines |

Approvals and grants are always **per project path**, even when the state
directory is shared.

## Recipes

### Two local windows

```sh
anneal -C ~/src/project-a &
anneal -C ~/src/project-b &
```

Each window's title, status bar (`project: …`) and inspector CWD name its
project. A session records the directory it was created in, so on next start
each window resumes **its own** project's latest session; the sessions sidebar
has a **This project / All sessions** toggle.

### Separate state per project, shared skills

```sh
ANNEAL_HOME=~/anneal-env/a anneal -C ~/src/project-a
ANNEAL_HOME=~/anneal-env/b anneal -C ~/src/project-b
```

Each state dir is created with defaults on first run. To share skills (but
keep memory, sessions, harness and grants separate), add to each
`config.json`:

```json
"skills": { "extra_dirs": ["/home/me/.anneal/skills"] }
```

### One `anneald` per project, GUI attached

Start the backend only when needed; the GUI is a thin client over the wire
protocol, and `-server` accepts a socket or `host:port`.

```sh
anneal-proj() {
  local p=$1 dir=$HOME/src/$1 env=$HOME/anneal-env/$1
  mkdir -p "$env/state" "$env/work"
  local sock="$env/state/anneal.sock"
  if [ ! -S "$sock" ]; then
    ANNEAL_HOME="$env/state" nohup anneald -work "$dir" \
      -listen "unix:$sock" >"$env/anneald.log" 2>&1 &
    for _ in $(seq 1 50); do [ -S "$sock" ] && break; sleep 0.1; done
  fi
  anneal -server "unix:$sock"
}

anneal-proj project-a
anneal-proj project-b
```

Registering those backends in `config.json` (`ui.backends`) lets you switch
between them from the status-bar backend chip after a restart; see
[USING.md](USING.md#deployment--tenancy-recommended-usage-pattern). The
backend's work dir is fixed for its lifetime — one instance, one project.

### Containers

One container per project, each with its own mount and listener. Sockets avoid
port bookkeeping entirely:

```sh
docker run -d --rm --name anneal-a \
  -e ANNEAL_LISTEN=unix:/anneal/state/anneal.sock \
  -v "$HOME/anneal-env/a:/anneal" anneal:dev
anneal -server "unix:/home/me/anneal-env/a/state/anneal.sock"
```

If you prefer TCP, give each container its own host port (`-p
127.0.0.1:7777:7777`, `-p 127.0.0.1:7778:7777`, …). Idle auto-shutdown makes
each instance near-free; see [CONTAINERS.md](CONTAINERS.md).

### Desktop launcher

The launcher starts in your home directory unless you pick a folder. If Anneal
starts in `$HOME` (or `/`) it shows a **"Working in your home directory"** card
with **Choose project…**; the same action is under **File ▸ Open project…**.
It restarts the window in the chosen directory. Always prefer this over
working with `$HOME` as the project: grants saved there apply to everything
you can reach, and project-scoped tools have no repository.

## Project A needs a change in project B

This is the normal shape of work on tightly coupled repositories. Choose the
pattern by size and risk:

| situation | pattern |
|---|---|
| small, mechanical, trusted (a one-line fix in a sibling checkout, bumping a local dependency) | **A. edit B from A, with explicit approval** |
| substantial change (design, tests, review) | **B. hand off to B's instance** |
| want it automated from your own terminal | **C. scripted hand-off** |
| the repositories always change together | **D. make the workspace the project** |

### A. Edit B from A (approved outside-project access)

The instance still thinks in project A; B is simply outside its jail.

1. From A: *"fix the `Parse` bug in ../B/parser.go; the failing test is
   `TestParse`"*.
2. The agent's first access to `../B` raises an **outside the working
   directory** card naming `/path/B/*`. Approve once, or "Always allow" to save
   the grant for A.
3. The agent edits B and can run `cd ../B && go test ./...` (each `cd` target
   is part of the outside-directory check).
4. Commit B from A with `git -C ../B commit …` so the history stays separate.

Caveats: B's own harness, skills, saved grants and prior session are **not**
in effect; A's agent is doing the work. The grant is per directory — remove it
in Settings → Permissions when done if it was a one-off.

### B. Hand off to B's instance

Best for anything that deserves B's own context, tests and review.

1. In A: *"write HANDOFF.md for the change B needs"*. A useful hand-off names:
   - the goal and the exact contract (function/CLI/protocol shape);
   - a failing test or a minimal reproduction;
   - acceptance criteria;
   - what A will do with the result (the bump/commit it expects).
2. Mark A's todo "blocked on B" and stop A (Ctrl+D). A's stop marker shows
   where it paused; nothing is lost.
3. In B's window/instance: *"implement HANDOFF.md, run the tests, commit."*
4. Back in A: *"B landed the fix in <commit>; update the dependency and
   continue."* A resumes with its own context intact.

This keeps each project's jail, grants, harness and history. The cost is an
explicit context switch — which is the point.

### C. Scripted hand-off

From your own terminal (not from inside A's turn):

```sh
anneal-headless -server "unix:$HOME/anneal-env/b/state/anneal.sock" \
  --new -p "$(cat ~/src/a/HANDOFF.md)"
```

Do not ask A's agent to run a long delegated build/test through Bash: Bash
calls time out (2–10 min), A's turn budget keeps running, and B's approval
cards would appear in B's windows where A cannot answer them. A one-shot
`anneal-headless` is the right shape for automation.

### D. Make the workspace the project

If two repositories always change together (for example a library plus its
consumer), start the instance at their common parent, or use a `go.work`
workspace:

```sh
cd ~/src          # contains lib/ and app/
anneal -C ~/src
```

The permission jail then covers both trees and "outside-project" prompts stop.
The trade-off is a wider blast radius: grants cover the whole parent, so keep
it to repositories you would edit in the same change.

For a Go dependency loop, prefer `go.work` (or a temporary `replace`) while
iterating, and remove the replace before release.

## Running work in parallel

- Each instance has its own message queue; a message sent while that instance
  is busy waits for its run (see USING.md).
- **The model server is shared.** Two runs at once make each other slower and
  evict each other's prompt cache. Background learning pauses when you start
  typing in one instance, but two instances still compete: treat the GPU as
  one lane and let the other project queue.
- Two instances editing the same file have no cross-instance lock. The
  outside-project grant does not coordinate either; use one owner per file.

## Pitfalls checklist

- **Launched from the desktop menu?** The project is your home directory.
  Symptoms: gortex says nothing is tracked, every command starts with
  `cd <project> &&`, grants saved under your home path. Use `-C`, File ▸ Open
  project…, or the home-directory card.
- **Review saved grants** (Settings → Permissions) if you ever blanket-allowed
  `bash *` or `edit *`; broad grants are flagged.
- **Sessions** are tagged with their project; the sidebar can show all of them
  (`All sessions`) if you need to find work from another project.
- **Shared state**: config/UI preferences are last-write-wins between windows;
  permissions are locked across processes. If you want per-project UI layout
  and memory, give each project its own `ANNEAL_HOME`.
- **gortex** indexes repositories you explicitly track; each project needs
  `gortex track <dir>` once (`Track this project` in the UI).
