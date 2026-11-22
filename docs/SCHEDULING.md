# Scheduled tasks

Anneal can run durable jobs at specific times from a continuously running
`anneald`. Jobs are **not** shell cron entries: each job starts a normal agent
run with the configured tools, permissions, memory, skills, and search index,
in an isolated session. Results are written to a durable ledger and can be
delivered to a phone by `anneal-gateway`.

The scheduler is opt-in:

```json
{
  "schedule": {
    "enabled": true,
    "timezone": "Europe/Amsterdam",
    "tick_s": 15,
    "default_timeout_s": 1800,
    "grace_s": 900,
    "max_jobs": 50
  }
}
```

| key | default | meaning |
|-----|---------|---------|
| `enabled` | false | start the scheduler in `anneald` |
| `timezone` | system local | IANA zone used when a job has no `timezone` |
| `tick_s` | 15 | due-scan interval |
| `default_timeout_s` | 1800 | per-run limit when the job has no `timeout_s` |
| `grace_s` | 900 | catch-up window for `missed: "run_once"` |
| `max_jobs` | 50 | cap on stored jobs |

State lives under `~/.anneal/schedules/`:

| path | contents |
|------|----------|
| `jobs.json` | durable job definitions (`{"version":1,"jobs":[...]}`) |
| `runs.jsonl` | append-only execution ledger (one JSON object per line) |
| `scheduler.lock` | single-owner advisory lock |

`anneald` owns execution. A second `anneald` (or the GUI) can manage jobs but
cannot run them while the lock is held. Idle shutdown is disabled while
schedules are enabled, because a sleeping server cannot fire jobs.

## Schedule formats

| form | example | behavior |
|------|---------|----------|
| cron | `0 7 * * 1-5` | recurring, five fields, names accepted (`MON-FRI`) |
| interval | `every 2h` | recurring from the previous scheduled instant |
| delay | `30m`, `in 2h` | one-shot after the delay |
| timestamp | `2026-12-01T09:30:00` | one-shot at that instant (job timezone) |

Bare durations are one-shots. Use `every <duration>` for a recurring job.
Durations accept `d` and `w` (`every 1d12h`, `2w`). Cron handles DST through
the job's IANA timezone; a slot that does not exist in a spring-forward day is
skipped by the cron library.

## Job document

```json
{
  "id": "daily-brief",
  "name": "Daily brief",
  "prompt": "Summarize my unread GitHub notifications and today's calendar. Reply with [SILENT] if there is nothing useful.",
  "schedule": "0 7 * * 1-5",
  "timezone": "Europe/Amsterdam",
  "work": "projects/anneal",
  "session": "fresh",
  "permission_mode": "dontAsk",
  "permission": {
    "bash": {"git status *": "allow"}
  },
  "deliver": ["ntfy"],
  "overlap": "skip",
  "missed": "run_once",
  "grace_s": 900,
  "timeout_s": 1800,
  "enabled": true
}
```

| field | meaning |
|-------|---------|
| `id` | slug, unique; derived from the name/prompt when omitted |
| `prompt` | required; **must be self-contained** for fresh sessions |
| `schedule` | one of the forms above |
| `timezone` | optional IANA zone |
| `work` | work-root-relative directory (same rules as `agent.send work`) |
| `session` | `fresh` (default) or `sticky` |
| `session_id` | set by the scheduler for sticky jobs |
| `permission_mode` | defaults to `dontAsk` |
| `permission` | OpenCode-style rules for this job only |
| `deliver` | transport names (`ntfy`, `matrix`, ...), `all` (default) |
| `overlap` | `skip` (default) or `queue` |
| `missed` | `skip` (default) or `run_once` within `grace_s` |
| `timeout_s` | hard run limit, `0` = `schedule.default_timeout_s` |
| `approval_timeout_s` | reserved for ask-mode phone approvals |
| `enabled` | omitted means true |

The scheduler stores `next_run`, `last_run_id`, `last_status`, `last_error`,
`last_session_id`, `run_count`, and `fail_count` on the job. Those are
read-only state; create/update requests may omit them.

## Execution semantics

- **One run slot per host.** Scheduled runs share a process-wide `RunGate` with
  interactive runs because both use `os.Chdir` for `work`. A job due while you
  are chatting waits; a chat message sent while a job runs is queued (with
  `queue: true`) and starts after the job releases the gate.
- **Fresh sessions by default.** Every run gets a new session titled from the
  prompt, tagged `origin: "schedule"` and `job_id`. The foreground boot resume
  skips those sessions, so a job can never become your next desktop
  conversation. `sticky` reuses the job's `session_id` and keeps context.
- **Isolation.** The job runs in a separate worker `agent.Runner` with a
  private event bus. Job tool traffic never appears in the interactive
  transcript. The main bus receives `schedule.*` summaries only.
- **Permissions.** `dontAsk` allows reads/search/todo and denies Bash/Write/Edit
  unless the job's `permission` rules explicitly allow them. Never use
  `bypassPermissions` for an unattended job. `ask` cards are forwarded to the
  notification gateway; if nobody answers before the job timeout, the card is
  denied and the run stops.
- **No learning loop recursion.** Scheduled sessions are not enqueued for the
  background reviewer, and the auto-improve pass is not armed by their finish.
- **Timeouts.** The manager cancels the run context at `timeout_s`; the worker
  gets a soft interrupt, then a hard interrupt after 10 seconds.
- **Missed runs.** On startup, a recurring job whose `next_run` is in the past
  is advanced to the next future slot. With `missed: "run_once"` and a slot
  inside `grace_s`, it runs once first. One-shot jobs that passed while the
  server was down are disabled.
- **Overlap.** `skip` jumps over occurrences missed while the previous run was
  active. `queue` keeps one missed occurrence pending, which can produce a
  backlog if the run takes longer than the interval.

Events emitted on the main bus:

| event | payload |
|-------|---------|
| `schedule.state` | jobs, running flag, current job |
| `schedule.created` / `updated` / `removed` / `paused` / `resumed` | job |
| `schedule.started` | job, record (status `running`) |
| `schedule.finished` | job, record (`status`, `ok`, `text`, `error`, `usage`, `session_id`, `next_run`) |
| `schedule.missed` | job, `scheduled_for`, `next_run` |
| `schedule.error` | job/job_id, error |

## Managing jobs

### From chat (agent tool)

The interactive agent has a `schedule_manage` tool:

```text
Every weekday at 07:00, summarize my unread GitHub notifications and send
the result to my phone. Use ntfy.
```

The model creates the job with a self-contained prompt and reports the
computed `next_run`. Actions: `list`, `get`, `create`, `update`, `pause`,
`resume`, `run`, `remove`, `runs`. Scheduling is a state-changing capability;
the default permission is `ask`. To skip the card in interactive sessions:

```json
{"permission": {"schedule": "allow"}}
```

Scheduled jobs themselves should normally **not** be allowed to create more
jobs (`dontAsk` already denies it).

### From the terminal

```sh
annealctl -server 127.0.0.1:7777 schedule list
annealctl schedule add \
  -id daily-brief -name "Daily brief" \
  -schedule "0 7 * * 1-5" -timezone Europe/Amsterdam \
  -prompt "Summarize my unread GitHub notifications. Reply [SILENT] if nothing changed." \
  -permission-mode dontAsk -deliver ntfy
annealctl schedule run daily-brief
annealctl schedule pause daily-brief
annealctl schedule runs
```

`annealctl` uses the same WebSocket protocol as the GUI and gateway. Set
`ANNEAL_SERVER` and `ANNEAL_AUTH_TOKEN` to avoid repeating flags. Manual
`schedule run` requires the scheduler to be running (it needs the job loop and
the shared run gate).

### Protocol

All methods are additive (protocol revision stays 1):

| method | params | result |
|--------|--------|--------|
| `schedule.list` | – | `[job]` |
| `schedule.state` | – | `{jobs, running, job, now}` |
| `schedule.get` | `{id}` | job |
| `schedule.create` | job document | job |
| `schedule.update` | job document | job |
| `schedule.remove` | `{id}` | `{ok:true}` |
| `schedule.pause` / `schedule.resume` | `{id}` | job |
| `schedule.run` | `{id}` | `{ok:true, id:<run-record-id>}` |
| `schedule.runs` | `{limit, since}` (`since` unix seconds) | `[record]` |

## Notifications

`anneal-gateway` consumes `schedule.finished` and related events and sends a
short summary to the job's `deliver` targets. The final assistant text is the
notification body; `[SILENT]`, `SILENT`, `NO_REPLY`, and `NO REPLY` suppress
delivery. Long output is truncated (the full transcript remains in the
session). The gateway keeps a delivery cursor and catches up on
`schedule.runs` after a restart.

See [GATEWAY.md](GATEWAY.md) and [GENTOO.md](GENTOO.md) for a complete
scheduled binhost example.

## Security notes

- Keep `anneald` running as a dedicated unprivileged user. The scheduler does
  not require root, and root would give every tool (including WebFetch and MCP)
  full host privileges.
- A job prompt is code: review recurring jobs that can write files or use
  `bash`. Prefer explicit `allow` rules over `bypassPermissions`.
- Job definitions can contain secrets in prompts; `jobs.json` and
  `runs.jsonl` are mode `0600`.
- Use the gateway's authenticated transport. The write side of an ntfy topic
  is the command channel; protect it with ACLs.
