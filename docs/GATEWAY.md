# anneal-gateway

`anneal-gateway` puts anneal on your phone. It is a small, separate,
CGO-free Go binary that speaks the normal WebSocket protocol to one or more
`anneald` instances and bridges the event bus to messaging transports. The
first transports are **ntfy** and a plain-text **Matrix** room; the interface
is designed for Telegram too.

```text
Phone (ntfy)
   │ HTTPS / push
   ▼
Flint 4 / small always-on host
   ├─ ntfy server
   ├─ Caddy (TLS)
   └─ anneal-gateway ── WS + token / VPN ──► anneald (agent + scheduler)
```

The gateway is intentionally thin: scheduling, permissions, sessions, memory,
skills, and tool execution stay in `anneald`. The gateway only formats
notifications, tracks delivery cursors, and turns chat text into protocol
calls.

## Why a separate binary?

- It can run on a router while the agent runs on a build host.
- A gateway restart cannot disturb a run or the scheduler.
- Matrix/Telegram dependencies never enter the CGO-free `anneald` binary.
- It can connect to several anneal instances (home, VPS, build box) and expose
  several transports (ntfy now, Matrix/Telegram later).

## Configuration

Default path: `$ANNEAL_HOME/gateway.json` (usually `~/.anneal/gateway.json`).
`anneal-gateway -init` writes a template.

```json
{
  "servers": {
    "build": {
      "addr": "unix:/run/anneal.sock",
      "token_env": "ANNEAL_AUTH_TOKEN"
    },
    "home": {
      "addr": "wss://anneal.example.net/ws",
      "token_env": "ANNEAL_HOME_TOKEN"
    }
  },
  "default_server": "build",
  "notify_foreground": false,
  "state_path": "/var/lib/anneal-gateway/state.json",
  "transports": [
    {
      "name": "ntfy",
      "type": "ntfy",
      "base_url": "https://ntfy.example.net",
      "topic": "anneal",
      "in_topic": "anneal-in",
      "token_env": "NTFY_TOKEN",
      "title": "anneal"
    }
  ]
}
```

| key | meaning |
|-----|---------|
| `servers.<name>.addr` | `host:port`, `unix:/path`, `ws://`, or `wss://` |
| `servers.<name>.token_env` | env var holding the hello token (omit if auth is off) |
| `default_server` | used until a chat runs `/use <name>` |
| `notify_foreground` | also notify runs that did not come from a chat (default false) |
| `state_path` | per-chat server selection, ntfy cursor, pending approvals |
| `transports[].name` | unique name used in a job's `deliver` list |
| `transports[].topic` | outbound topic (notifications and replies) |
| `transports[].in_topic` | inbound command topic (defaults to `topic`) |
| `transports[].token_env` | ntfy access token (read + write) |
| `transports[].homeserver` | Matrix homeserver base URL (Matrix transport) |
| `transports[].user_id` | bot MXID; its own messages are ignored |
| `transports[].room_id` | one Matrix room per transport |
| `transports[].allow_users` | required human MXID allowlist |
| `transports[].encrypted` | E2EE via mautrix Olm/Megolm (CGO + `goolm` build) |
| `transports[].pickle_key_env` | env var with the crypto-store pickle secret |
| `transports[].db_path` | crypto SQLite store (default `~/.anneal/matrix/<name>.db`) |

Multiple transports are supported. Multiple instances of the same type are
allowed, for example `ntfy-phone` and `ntfy-office`; a job can target
`"deliver": ["ntfy-phone"]` or `"all"`.

### Run as a service

The gateway is a foreground process; use your init system to keep it running.
On Gentoo/OpenRC:

```sh
#!/sbin/openrc-run
name="anneal-gateway"
description="Anneal phone gateway"
command="/usr/local/bin/anneal-gateway"
command_args="-config /etc/anneal/gateway.json"
command_user="anneal:anneal"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"
output_log="/var/log/anneal-gateway.log"
error_log="/var/log/anneal-gateway.log"
```

A systemd unit is the same idea: `ExecStart=/usr/local/bin/anneal-gateway
-config /etc/anneal/gateway.json`, `User=anneal`, `Restart=always`. Keep the
config and state directories mode `0700`/`0600`.


## ntfy setup

1. Install [ntfy](https://ntfy.sh/docs/install/) on the Flint 4 (single Go
   binary, roughly 10–30 MB RAM) behind Caddy for TLS.
2. Create two topics: `anneal` (outbound) and `anneal-in` (inbound).
3. Create a token for the gateway with read/write on both topics; create a
   token for the phone with read on `anneal` and write on `anneal-in`.
4. Set `NTFY_TOKEN` in the gateway service environment.
5. On the phone, subscribe to `anneal` and publish commands to `anneal-in`.

Example ntfy ACL (exact syntax depends on your ntfy version):

```text
# user keys map to tokens
user anneal-gateway: tk_...
  write anneal, anneal-in
  read  anneal, anneal-in

user phone: tk_...
  read  anneal
  write anneal-in
```

The **write side of `anneal-in` is a remote control for your agent**. Protect
it with a strong token, TLS, and a private network/VPN if possible. Do not make
it an anonymous public topic.

### iOS push

Self-hosted ntfy delivers Android notifications directly (or through
UnifiedPush). For instant iOS notifications, set:

```yaml
upstream-base-url: "ntfy.sh"
```

The self-hosted server forwards only a poll request/message ID through
ntfy.sh → Firebase → APNs; the phone fetches the actual content from your
server. Without the relay, iOS delivery can take hours. This is an Apple
platform restriction, not specific to anneal.

### Android

The ntfy app works directly against the self-hosted server. Disable battery
optimization for reliable delivery. It can also act as a UnifiedPush
distributor for Element X if you add the Matrix transport later.

## Commands

Anything that is not a slash command is sent to the selected server as an
agent message (`agent.send {queue:true, ref:"gateway|<transport>|<chat>"}`).
The gateway replies to the originating chat when `run.finished` arrives.

| command | behavior |
|---------|----------|
| `/status` | server info, session, mode, queue, next scheduled job |
| `/stop` | soft stop; `/stop!` requests a hard stop |
| `/new` | new session |
| `/sessions` | recent sessions |
| `/open <id>` | open a session |
| `/queue` | queued inputs |
| `/cancel <id>` | remove a queued input |
| `/resume` | resume a paused queue |
| `/approve <id> once\|session\|always\|reject` | answer an approval |
| `/schedule list` | jobs and next runs |
| `/schedule get <id>` | job prompt and state |
| `/schedule run <id>` | queue a manual run |
| `/schedule pause\|resume\|rm <id>` | pause/resume/remove |
| `/schedule runs` | recent run ledger |
| `/use <server>` | switch anneald instance |
| `/help` | command reference |

Numeric replies `1`, `2`, `3`, `0` answer the last approval card in that chat
(once / this session / always / reject).

## Notifications

| event | delivery |
|-------|----------|
| `schedule.finished` | job summary to the job's `deliver` targets |
| `schedule.error` | failure alert |
| `schedule.missed` | missed-slot alert |
| `perm.request` | approval card in the home chat |
| `provider.state` | one alert when the model server goes down/recovers |
| `server.shutting_down` | shutdown notice |
| `run.finished` | reply in the chat that sent the message; other foreground runs only with `notify_foreground` |

Scheduled output supports Hermes-style silent tokens: if the final text is
exactly `[SILENT]`, `SILENT`, `NO_REPLY`, or `NO REPLY`, nothing is sent. This
makes monitoring jobs quiet unless they have something to say.

ntfy messages are capped around 4 KB by the push providers, so the gateway
truncates summaries to 3.5 KB and includes the session id. The full transcript
stays on the anneald host.

## Reliability

- One WebSocket per server with exponential-backoff reconnect. The server
  replays session/mode/todo/schedule state after the hello ack.
- The gateway persists the last ntfy message id and resumes the stream with
  `since=<id>`; cached messages are not replayed twice.
- Schedule results are in `~/.anneal/schedules/runs.jsonl`. After a gateway
  restart, `schedule.runs` can be used to catch up on anything missed; the
  gateway never relies on a live socket for durable job history.
- The outbound queue is bounded. If a transport is down, notifications are
  dropped after the queue fills; the run ledger remains authoritative.

## Security

- Keep the gateway process unprivileged and its config mode `0600`.
- Use a token-protected `anneald` listener (never expose an unauthenticated
  port). TLS is terminated by Caddy or another reverse proxy; `anneald` does
  not terminate TLS.
- The ntfy inbound topic is the authorization boundary. A user who can write
  there can run the agent under the server's permission mode.
- Approval cards show the exact command/resource. Do not blanket-allow
  `bash *` for unattended jobs.
- Matrix (next) will require an explicit user-ID allowlist and an encrypted or
  private room; see [MATRIX notes](#matrix-next).

## Matrix

The Matrix transport is a small plain-text client by default: one bot
account, one room, `/sync` long-polling, and `m.room.message` sends. Set
`"encrypted": true` to use mautrix-go's Olm/Megolm machine with an embedded
SQLite crypto store. The encrypted build is optional:

```sh
CGO_ENABLED=1 go build -tags goolm -o bin/anneal-gateway ./cmd/anneal-gateway
# or simply: make gateway
```

Without CGO (or without `-tags goolm`) the gateway still builds and supports
ntfy and unencrypted Matrix rooms; an `encrypted: true` transport refuses to
start with a clear error. Encrypted builds use pure-Go Olm (`goolm`) and the
bundled mattn/go-sqlite3 C source, so a C compiler is required.

```json
{
  "name": "matrix-home",
  "type": "matrix",
  "homeserver": "https://matrix.example.net",
  "user_id": "@anneal:example.net",
  "room_id": "!anneal:example.net",
  "token_env": "MATRIX_TOKEN",
  "allow_users": ["@johan:example.net"],
  "encrypted": true,
  "pickle_key_env": "MATRIX_PICKLE_KEY",
  "db_path": "/var/lib/anneal-gateway/matrix-home.db"
}
```

`MATRIX_PICKLE_KEY` must be a stable secret (the SQLite store is unpickled with
it on every start; losing it means re-establishing the device). The database
holds the Olm account, Megolm sessions, and sync state; keep it mode `0600` and
back it up. The bot's device is not cross-signed/verified by default, so
Element may show an "unverified device" notice. That is expected for a bot:
the room is private and `allow_users` gates commands. If you require verified
devices, complete Matrix device verification once and back up the crypto
database; do not rotate or delete it casually.

The E2EE transport uses mautrix's state store for the sync token. The separate
plain transport keeps a small cursor file; both persist on the gateway host.

1. Create a bot account on your homeserver (registration closed otherwise).
2. Obtain an access token once, e.g.:
   ```sh
   curl -s -X POST https://matrix.example.net/_matrix/client/v3/login \
     -H 'Content-Type: application/json' \
     -d '{"type":"m.login.password","identifier":{"type":"m.id.user","user":"anneal"},"password":"..."}'
   ```
   Store `access_token` in the gateway service environment as `MATRIX_TOKEN`.
3. Create a private room, invite the bot, and have it join. Use encryption
   **on** for `encrypted: true`, or **off** for the default plain transport.
4. Set `room_id` and the human MXIDs in `allow_users`. A Matrix transport
   refuses to start without an allowlist, so a compromised room cannot
   command the agent.
5. Optional: run `tuwunel` or `continuwuity` on the Flint 4 (Rust, single
   binary, roughly 64–256 MB for a small private server) with Caddy in front.
   Keep federation off for a personal setup.

The bot ignores its own messages and any sender not in `allow_users`. Replies
are plain text; the notification title is prefixed to the body. Matrix IDs are
used as chat ids, and `/use <server>` works per room.

Push is the caveat: Android can use ntfy as a UnifiedPush distributor;
Element X on iOS routes notifications through Element's push gateway/matrix.org
(event IDs only). ntfy remains the most direct notification channel on iOS.

