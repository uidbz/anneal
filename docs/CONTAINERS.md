# Anneal in a container

The backend image contains **everything an agent run needs** — the anneald
server plus a default development toolchain (git, ripgrep, jq, curl, bash).
Nothing else has to be installed where the *client* runs. Everything
mutable — config, sessions, memory, skills, harness, and the project work
tree — lives in **one bind mount**. The container itself is disposable:
start it on demand, let it idle out, start it again.

## The model in one paragraph

One running backend instance == one environment == one project (or one
user's workspace). Configuration scoping is achieved by *which mount you
point at*, not by switching projects inside a server: spin up one container
per project/user with its own mount and `docker run` flags, and each gets a
fully independent config, session history, and permission grants. Idle
auto-shutdown keeps the cost of that near zero.

## The mount

```
<host-dir>/            # bind-mounted read-write at /anneal
├── state/             # ANNEAL_HOME: config.json, sessions/, memory/,
│                      #   skills/, harness/, permissions.json, logs/
└── work/              # agent working directory (the project tree,
                       #   or a checkout inside it)
```

`state/config.json` is an ordinary anneal config (see [CONFIG.md](CONFIG.md))
with the `server` group added. First boot writes defaults if absent.
Ownership: the container runs as uid 1000; `chown -R 1000:1000 <host-dir>`
or arrange your uid map.

## Running it

```sh
docker build -t anneal:dev .          # or a published image tag

docker run -d --name anneal-proj \
  -e ANNEAL_AUTH_TOKEN="$(openssl rand -hex 16)" \
  -e ANNEAL_LISTEN=0.0.0.0:7777 \
  -e ANNEAL_IDLE_TIMEOUT_S=900 \
  -v "$HOME/anneal-env/proj:/anneal" \
  -p 127.0.0.1:7777:7777 \
  anneal:dev
```

Env knobs (all also settable in `state/config.json` under `server`):

| env | meaning |
|-----|---------|
| `ANNEAL_HOME` | state root (image default `/anneal/state`) |
| `ANNEAL_WORK_DIR` / `-work` | agent cwd (image default `/anneal/work`) |
| `ANNEAL_LISTEN` | comma-separated listeners (`unix:/path`, `host:port`) |
| `ANNEAL_IDLE_TIMEOUT_S` | seconds of client inactivity → clean exit (0 = off) |
| `ANNEAL_AUTH_TOKEN` | bearer token required in the hello frame |
| `ANNEAL_ENDPOINT` / `ANNEAL_MODEL` | model server (point at a host/port or LAN inference server) |

Then from any machine with just the client binary:

```sh
anneal-headless -server 127.0.0.1:7777 -p "…"    # ANNEAL_AUTH_TOKEN in env
```

Health: `curl -s localhost:7777/healthz`.

Compose sketch:

```yaml
services:
  anneal:
    image: anneal:dev
    environment:
      ANNEAL_LISTEN: 0.0.0.0:7777
      ANNEAL_IDLE_TIMEOUT_S: "900"
      ANNEAL_AUTH_TOKEN: "${ANNEAL_AUTH_TOKEN}"
    volumes:
      - ./env:/anneal
    ports: ["127.0.0.1:7777:7777"]
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://127.0.0.1:7777/healthz"]
```

## Idle shutdown (why environments are cheap)

With `ANNEAL_IDLE_TIMEOUT_S=900`, 15 minutes without a client frame (and
no run in flight) makes anneald emit `server.shutting_down` and exit; with
`--rm` the container disappears. The next client start is a fresh
`docker run` against the same mount — all history persists in the volume.
A client orchestrator that wants "always available" can restart on exit;
one that wants "only while I use it" does lazy start-on-demand (connect
fails → launch → reconnect). Nothing in the design assumes the container
process survives: config, sessions, and learning state are all in the
mount.

## Multiple users / many projects

Instance-per-scope, not multi-tenancy inside one process:

| need | how |
|------|-----|
| per-project config, sessions, permissions | one mount + one container per project |
| per-user isolation | one mount + one container per user (distinct `-p` host ports) |
| shared model server | all instances point `ANNEAL_ENDPOINT` at the same inference server; that server is the shared GPU resource and has its own queueing |

This keeps blast radius small (a project's agent can only touch its own
work tree and state), keeps images stateless, and matches the idle-exit
lifecycle. Shared multi-user servers would need per-user permission/audit
layers; deliberately out of scope until instances-per-scope proves
insufficient.

## No-port mode (single host, exec transport)

If the client can `docker exec`, no listener or token is needed at all —
the stdio stream carries the same event objects:

```sh
docker exec -e ANNEAL_HOME=/anneal/state anneal-proj \
  anneal-headless -json -p "one-shot task"
```

For interactive use the WS transport is still preferred (reconnect +
replay). The `unix:/anneal/state/anneal.sock` listener combined with
`docker exec` clients is the middle ground for tools already inside the
container.

## The default development environment

The runtime image ships a conservative toolchain (git, ripgrep, jq, curl,
bash) so the agent's Bash/Glob/Grep tools work out of the box. Teams build
on top: `FROM anneal:dev` plus whatever language SDKs, package caches
(volume-mounted), or CLIs the project needs. The image is where the
environment lives; the mount is where *your* configuration lives — that
line is the whole contract.
