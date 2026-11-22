# Security model

Anneal can read and write project files, run shell commands, fetch URLs, call
MCP servers, and keep durable schedules. Treat the model and every document it
reads as **untrusted input**. This page describes the intended deployment and
the boundaries that matter.

## Never run anneald as root

Do not run `anneald`, the GUI, or the gateway as root. The agent's Bash tool is
an arbitrary command runner for the process user; root would turn any prompt
injection into full host compromise.

Run the backend as a dedicated, unprivileged user:

```sh
useradd --system --create-home --home-dir /var/lib/anneal --shell /usr/sbin/nologin anneal
```

`anneald` refuses to start as root unless `ANNEAL_ALLOW_ROOT=1` is set. That
override exists only for disposable, fully trusted sandboxes; do not use it on
a machine with your projects, keys, or chroots.

Give that user only:

- its `ANNEAL_HOME` state directory;
- the project directories it must edit;
- the specific chroot/build paths described in [GENTOO.md](GENTOO.md);
- read access to the skills/toolchains it needs.

Root requirements (creating a btrfs snapshot, initial ACLs, installing a
service) are **one-time setup** steps, not runtime privileges.

## Filesystem boundaries

The process user's permissions are the real boundary, not the model's prompt or
the permission UI.

- Keep `~/.anneal/config.json`, `jobs.json`, `runs.jsonl`, and the gateway
  config mode `0600`.
- Do not put SSH keys, cloud credentials, or a Docker socket in a project
  directory visible to the agent.
- The built-in `.env` read guard is a prompt-level convenience, not a sandbox.
  Use Unix permissions/ACLs for secrets.
- `external_directory` approval cards exist to make out-of-project access
  visible. Approve `always` sparingly.
- The `script` tool is sandboxed (no exec/net, confined fs), but it can read
  project and `~/.anneal` files. Do not treat it as a secret boundary.

## Permissions

The default mode is `default` with `ask` for Bash/Edit/Write/WebFetch/script.
Unattended jobs default to `dontAsk` so they cannot silently start a shell.
Never use `bypassPermissions` for a scheduled job.

Approval cards show the action and resources. On a phone, the card includes the
exact id; approve with `1` once, `2` this session, `3` always, or `0` reject.
Saved `always` grants are project-scoped; remove them in Settings or
`~/.anneal/permissions.json`.

Plan mode is a hard read-only gate; use it for exploratory sessions.

## Network exposure

`anneald` has one bearer token and no per-client scopes. Treat it as a powerful
remote control.

- Prefer a Unix socket (`unix:/run/anneal.sock`, mode `0600`) for same-host
  clients.
- TCP listeners should be loopback-only, behind a TLS reverse proxy, or on a
  private VPN (WireGuard/Tailscale).
- Without a token, `anneald` refuses browser Origins and non-loopback binds.
- The gateway connects **outbound** to `anneald`; you do not need to expose the
  agent port to the phone network.
- The ntfy inbound topic is a command channel. Protect it with a strong token
  and ACLs; do not use an anonymous public topic.

## Scheduled tasks

- Job prompts are code. Review prompts that can write files or run commands.
- Use `permission` rules to allow only the exact actions a job needs.
- Scheduled jobs run in their own sessions with a private event bus; they are
  not auto-resumed into the desktop conversation.
- The scheduler lock prevents two processes from executing jobs against the
  same state directory. Management is still possible from another client.
- Prefer the validated `gentoo` tool for build automation over `bash`
  allowlists.

## Gentoo builds

The rootless design is described in [GENTOO.md](GENTOO.md). In short:

- `bwrap` user namespaces, uid 0 only inside the namespace;
- read-only source chroot plus a user-owned overlay;
- no host root, no Docker socket, no arbitrary shell;
- validated package atoms and a 0600 audit log.

If a build step truly requires host root, implement a separate fixed helper
with a narrow allowlist and explicit approval. Do not grant the agent a root
shell.

## Gateway

- Store tokens in environment variables referenced by `*_token_env`; config
  files hold only the variable names.
- TLS is terminated by Caddy or another proxy. Neither `anneald` nor the
  gateway terminates TLS.
- The gateway outbound queue is bounded; a failed transport never blocks the
  agent. Durable schedule results remain in the run ledger.
- The Matrix transport is plain text and refuses to start without an
  `allow_users` list. Use a private unencrypted room on your own homeserver;
  do not put it in an encrypted room (E2EE is a separate follow-up). The room
  membership and the allowlist are the authorization boundary.
- Never expose an unauthenticated agent port; the gateway connects outbound
  over a token-protected WS or Unix socket.

## Prompt injection and MCP

Web pages, repository files, issue comments, and MCP tool output can contain
instructions. The permission engine and OS user are the mitigations; the model
cannot be trusted to ignore injected text. Do not connect MCP servers with
broader privileges than the agent user, and do not auto-approve broad Bash
patterns such as `*`.

## Reporting

Security issues in anneal should be reported privately to the maintainer before
public disclosure.
