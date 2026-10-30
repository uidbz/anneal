# Anneal wire protocol v1

The backend (`anneald`, package `core/server`) and any frontend (GUI remote
mode, `anneal-headless -server`, CI scripts, future web UI) speak one
protocol. This document is normative: additive changes are allowed at any
time (clients ignore unknown fields/methods); anything that breaks a
existing frame shape or method semantic bumps `proto`.

## Transport

One WebSocket connection per client. HTTP upgrade on path `/ws` (any path is
upgraded today; `/ws` is the documented default). Also served: `GET
/healthz` → `{"ok":true,"version":"…","reason":""}` for container health
probes (plain HTTP, no auth).

Listeners (config `server.listen`, comma-separated):

| form | meaning |
|------|---------|
| `unix:/path/to/sock` | Unix domain socket (owner-only `0600`) |
| `host:port` | TCP |

Clients dial `ws://host:port/ws`, bare `host:port` (client adds the rest),
or `unix:/path` (client connects to the socket and performs the HTTP
handshake over it).

## Framing

Text frames, JSON objects, UTF-8. Max inbound frame 1 MiB.

* **Request** (client → server): JSON-RPC 2.0 —
  `{"jsonrpc":"2.0","id":"7","method":"agent.send","params":{…}}`.
  `id` is a string; omit `id` (or null) for a **notification** (no reply;
  errors surface as events).
* **Response** (server → client): `{"jsonrpc":"2.0","id":"7","result":…}` or
  `{"jsonrpc":"2.0","id":"7","error":{"code":…,"message":"…"}}`.
* **Event** (server → client, unsolicited): the *raw bus event* — the exact
  JSON object every local consumer (GUI, headless) renders, carrying a
  `"type"` key and no `"jsonrpc"` key. Payloads keep all keys
  (empty lists are `[]`, never `null`); see the event table in
  [ARCHITECTURE.md](ARCHITECTURE.md) for the catalog. Consumers must
  ignore event types they do not know.

A client distinguishes the three by key: `jsonrpc` present → reply, else
`type` → event.

## Handshake

The first client frame must be:

```json
{"type":"hello","proto":1,"token":"<bearer>","client":"anneal-gui/0.3.0"}
```

* `proto` must equal the server revision (currently `1`).
* `token` is required iff the server has auth enabled (below); otherwise
  ignored.

Success reply (before any event):

```json
{"type":"hello.ack","proto":1,"server":"<version>","idle_timeout_s":900,"drain_grace_s":20}
```

Failure reply, then close: `{"type":"hello.reject","proto":1,"error":"…"}`.

Immediately after the ack the server **replays current state** as events
(`session.opened`, `mode.changed`, `todo.updated`) — the same
`ReplayState()` the local shells rely on. Replay is global (all connected
clients receive it); the operations are idempotent.

## Auth

`server.auth_token_env` names an environment variable; if that variable is
set and non-empty, every hello must carry it (constant-time compare). The
Unix socket is the trusted local transport for one user; for TCP —
especially `0.0.0.0` inside a container — set the token and publish the
port loopback-only (`-p 127.0.0.1:7777:7777`) or front it with TLS.
TLS is not terminated by anneald (v1): use a reverse proxy if needed.

## Lifecycle

* **Idle shutdown**: with `server.idle_timeout_s > 0`, if no client frame
  has arrived and no run/improve activity happened for that long, the
  server emits `server.shutting_down {reason:"idle", eta_s}` and exits
  after `server.drain_grace_s`. A foreground run in flight always defers
  the timer.
* **server.quit**: RPC triggers the same sequence with the given reason.
* During the drain window, non-`server.quit` requests fail with
  `code -32001`; events keep flowing so clients can observe the finish.
* `SIGTERM`/`SIGINT` to anneald: immediate clean exit.

Slow consumers: per-connection queue (1024 events); overflow closes that
connection rather than blocking the bus. Clients reconnect (fresh hello →
replay).

**Keepalive**: the server sends WebSocket pings every 30 s; a connection
that fails to pong within 75 s is closed (clients' libraries answer pings
automatically; pure-WS clients must). The hello read deadline (10 s) ends
at the handshake — afterwards an idle connection is healthy by design.

## Methods

All commands the local GUI has on the core. Params/results are JSON
objects; `result` may be `null` (fire-and-forget commands resolve once
accepted — their effects arrive as events).

| method | params | result |
|--------|--------|--------|
| `server.ping` | – | `{"ok":true}` |
| `server.info` | – | `{version,proto,workdir,mode,session,busy,idle_timeout_s,uptime_s}` |
| `server.quit` | `{reason}` | `{"ok":true}` |
| `agent.send` | `{text}` | `{id,ok}` (run id; `ok:false` if busy) |
| `agent.interrupt` | – | null |
| `run.status` | – | `{busy,mode,session}` |
| `sessions.list` | – | `[{id,title,updated,events}]` |
| `sessions.new` | – | null |
| `sessions.open` | `{id}` | null (replay via `session.opened`) |
| `perm.respond` | `{id,action}` `once\|session\|always\|reject` | null |
| `mode.get` | – | `"<mode>"` |
| `mode.set` | `{mode}` | `{mode}` (effective) |
| `provider.state` | – | health struct |
| `provider.probe` | – | health struct (probe async) |
| `tie.state` | – | index state |
| `index.rebuild` | – | null (progress via `tie.rebuilt`) |
| `config.get` | – | full config document |
| `config.set` | `{doc}` (full document; omitted keys = defaults) | `{"ok":true}` |
| `learn.now` | – | null (result via `review.done`) |
| `memory.view` / `memory.pending` | – | store views |
| `memory.respond` | `{id,action}` `approve\|reject` | null |
| `skills.list` / `skills.pending` | – | store views |
| `skill.respond` | `{id,action}` `approve\|reject` | null |
| `journey.tail` | `{n}` | timeline entries |
| `flags.peek` | `{n}` | reviewer flags |
| `harness.info` / `harness.files` / `harness.lib` | – | harness views |
| `harness.get` / `harness.lib_experience` | `{id}` / `{name}` | content |
| `harness.save` | `{name,content}` | `{error,warns}` |
| `harness.lib_promote` | `{id}` | `{entry,backup,error}` |
| `improve.start` | `{k,mode,resume}` | `{"started":true}` (progress via `improve.*` events) |

Errors: JSON-RPC codes `-32700` parse, `-32600` invalid, `-32601` unknown
method, `-32602` bad params, `-32000` handler error, `-32001` shutting down.

## Concurrency semantics (unchanged from the core)

* One foreground run per server, shared by all clients. `agent.send` while
  busy returns `{ok:false}` (and a `run.busy` event).
* Permission cards are global: any client may answer any `perm.request`
  (watch `perm.resolved` to retire cards answered elsewhere).
* Run-scoped events carry the numeric `run` id assigned by `agent.send`.

## Versioning

`proto` is 1. Additive rule: new methods, new event types, new object keys
are free. Renames, removed keys, changed frame dispatch, changed method
params/results → proto 2 (servers announce their proto in the ack; clients
must reject mismatches). The GUI/headless clients and the server ship from
one repo and move together; external clients pin `proto` explicitly.

## Zero-code alternative (containers)

For one-shot automation without WS: `docker exec <ctr> anneal-headless
-json -p "task"` streams the identical event objects over stdio. Same
events, same permission-policy flags, no port exposed. Prefer WS for
interactive UIs (reconnect, replay, shared permission cards).
