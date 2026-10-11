<!-- Stackstart - https://github.com/orpic/stack-start -->
<!-- Copyright (c) 2026 Shobhit. All rights reserved. See LICENSE. -->

# Phase 1 design: operate one process at a time

Status: **draft for the owner's review** (written 2026-10-11, work item W9). Nothing here is implemented. Decisions marked **Q** need an answer before implementation starts.

Phase 1 in [ROADMAP.md](../ROADMAP.md): control socket, event log and `up -d` (1.1); `start`/`stop`/`restart <process>` (1.2); more states (1.3); retry policies (1.4); `--keep-going` (1.5); `send`/`attach` (1.6); `status --json` (1.7); `logs --lines N --no-follow` (1.8).

Acceptance (from ROADMAP, measured with conformance cases on `test-projects/`):

- In `fullstack-offline`, `restart phoenix` leaves Postgres, the API, the web app and the tunnel untouched: same PIDs and the same captured URL in `status --json`.
- `bad-migration` with `--keep-going` keeps Postgres up; the case fixes the cause, runs `stackstart start migrate`, and Phoenix then comes up.
- An agent can do all of it without a terminal it has to keep open: `up -d`, `status --json`, `logs --no-follow`, `restart`, `wait`, `down`.

## 1. Process model

The `up` process stays the supervisor. There is no separate daemon: `up -d` is the same program, started detached.

```text
stackstart up dev          foreground: supervisor + console on this terminal (today's behavior)
stackstart up -d dev       starts a detached supervisor, waits until ready or failed, exits
stackstart up -d --no-wait dev    starts it and returns at once
```

`up -d`:

1. The client resolves and validates the profile (errors exit before anything starts).
2. It re-executes itself as `stackstart up <profile> --detached-child` with `setsid`, stdin `/dev/null`, stdout/stderr appended to `runs/<run-id>/console.log` (the same bytes the foreground console would show, colors included).
3. It connects to the new supervisor's control socket and reads the event stream until every process is ready (exit 0, prints the same "all N processes ready · logs: <dir>" notice), a required process fails (exit 1, prints the same FAILED block), or `--timeout` passes (exit 5 once 2.7 lands, 1 before).
4. The child is not tied to the client's terminal: closing it, or the agent harness killing the client, does not stop the stack.

Foreground `up` keeps every current guarantee (Ctrl-C once stops, twice kills, SIGHUP stops).

## 2. Control socket

### 2.1 Location

A Unix socket can't have a path longer than about 104 bytes on macOS. The natural path, `<state>/sessions/<slug>/control.sock`, is about 150 bytes for a typical project. So:

- Use `<state>/sessions/<slug>/control.sock` if it is under 100 bytes.
- Otherwise `/tmp/stackstart-<uid>/<first 16 hex chars of sha256(slug)>.sock`, in a directory created with mode 0700 and owned by the user. Not `$TMPDIR`, which differs between a user's shell and agent sandboxes.
- The chosen path is written to the session record (`control_socket`). Clients never compute it; they read the record. That keeps the rule changeable.
- Socket mode 0600. The supervisor removes it on exit; a stale socket (connect refused, and the PID in the record is dead) is removed by the next `up` or by any client that finds it.

### 2.2 Protocol

Newline-delimited JSON over the socket, one request per connection. Simple to implement in any language, which matters for the "rewrite-proof" rule; debuggable with `nc -U`.

Request:

```json
{"v": 1, "op": "restart", "process": "phoenix", "wait": "ready", "timeout_ms": 120000}
```

Response: one or more JSON lines; the last has `"done": true`.

```json
{"v": 1, "event": {"seq": 412, "type": "process_stopping", "process": "phoenix"}}
{"v": 1, "event": {"seq": 418, "type": "process_ready", "process": "phoenix", "pid": 51234}}
{"v": 1, "done": true, "ok": true}
```

Errors:

```json
{"v": 1, "done": true, "ok": false, "error": {"code": "not_found", "message": "process \"phx\" not found in profile \"dev\"", "process": "phx", "hint": "processes: postgres, migrate, phoenix, api, web", "last_lines": []}}
```

Error codes: `invalid_request`, `not_found`, `conflict` (e.g. start of a running process), `failed` (the process failed; `last_lines` filled), `timeout`, `shutting_down`. These map onto the exit codes of 2.7.

Operations:

| op | Fields | Does |
| --- | --- | --- |
| `status` | | Returns the `status --json` document (section 5). |
| `start` | `process`, `wait` (`none`, `started`, `ready`), `timeout_ms` | Starts a stopped, exited, failed or blocked process. Its dependencies must be ready, else `conflict` listing them. |
| `stop` | `process`, `wait` | Stops one process (grace period, then SIGKILL, escaped descendants included). What happens to its dependents is **Q1**. |
| `restart` | `process`, `wait`, `timeout_ms` | `stop` then `start`. Section 4 says what happens to dependents. |
| `send` | `process`, `data` (string), `echo_ms` | Writes `data` to the process's terminal; returns the output it printed in the next `echo_ms` (default 500). |
| `events` | `after` (seq), `follow` (bool), `types` | Streams events from the event log (section 3), from `after`. |
| `wait` | `process`, `for` (`ready`, `exit`, `event:<type>`), `after` (seq), `timeout_ms` | Returns when the condition holds for an event with seq > `after`. |
| `logs` | `process`, `lines`, `follow`, `raw` | Streams log lines (the CLI can also read the file directly; the socket is for remote/MCP use). |
| `shutdown` | `force` (bool) | What `down` and `down --force` do. Replaces signalling the PID; signals stay as the fallback when the socket is unreachable. |

`down` keeps working against v0.2.x stacks (no socket in the record → signal, as today).

## 3. Event log

Every run writes `runs/<run-id>/events.jsonl`, append-only, one JSON object per line, fsync'd in batches (every 100 ms or on run end). The socket's `events`/`wait` read the in-memory tail; the file is what remains after the run.

```json
{"seq": 17, "ts": "2026-10-11T08:01:02.345Z", "type": "process_ready", "process": "phoenix", "pid": 51234, "after_ms": 4210}
```

- `seq`: per run, starts at 1, strictly increasing, no gaps. Clients pass it back as `after`.
- `ts`: UTC, millisecond precision.
- `type`: one of the list below. New types may be added; clients must ignore unknown ones.

| type | Extra fields |
| --- | --- |
| `run_started` | `profile`, `project`, `run_id`, `stackstart_version`, `detached` |
| `process_pending` | `process`, `waiting_for` (dependency names) |
| `process_starting` | `process`, `pid`, `attempt`, `cmd` (after interpolation, secrets not redacted: **Q4**) |
| `process_ready` | `process`, `pid`, `after_ms` |
| `capture` | `process`, `name`, `value` |
| `screen_cleared` | `process` |
| `fail_on_warning` | `process`, `pattern`, `line` |
| `process_failed` | `process`, `cause`, `last_lines` |
| `process_exited` | `process`, `pid`, `exit_code` or `signal`, `expected` (oneshot or `on_exit: ignore`) |
| `process_stopping` / `process_stopped` | `process`, `by` (`user`, `shutdown`, `restart`) |
| `process_restarting` | `process`, `by` (`user`, `policy`), `attempt` |
| `process_backoff` | `process`, `delay_ms`, `attempt` |
| `process_blocked` | `process`, `blocked_by` |
| `run_stopping` | `reason` (`signal`, `down`, `failure`) |
| `run_finished` | `outcome` (`stopped`, `failed`), `failed_process`, `cause` |

Phase 2 adds `rebuild_started`, `rebuild_finished`, `rebuild_failed` (from `signals:` and screen clears) and health events; the schema already allows them.

## 4. States, restarts and dependents

States (1.3): `pending`, `blocked`, `starting`, `ready`, `restarting`, `backoff`, `crashed`, `failed`, `stopped`, `exited` (oneshot done, or `on_exit: ignore`), plus `degraded` from Phase 2 health checks.

```text
pending ──deps ready──▶ starting ──checks pass──▶ ready
   │                       │                        │
   │ dep failed, keep-going │ fail_on/timeout/exit   │ exit / crash
   ▼                       ▼                        ▼
blocked                  failed ◀── retries spent ── crashed ──policy──▶ backoff ──▶ starting
                                                    stop ▶ stopped ── start ──▶ starting
```

Restart of one process (1.2):

1. The process is stopped (grace period, escaped descendants included) and started again with the same env, re-rendering templates.
2. Its captures are cleared and re-captured. Its readiness checks run again.
3. Dependents: each process declares `on_dep_restart: restart | ignore` (the default is **Q2**). With `ignore` they keep running. With `restart`, they are restarted only if a capture they reference changed value; a dependent whose captured inputs are unchanged is never touched. This is what makes `restart phoenix` leave the tunnel and API alone, and `restart tunnel` restart only what uses `${tunnel.url}` when the URL actually changes.

Retry policies (1.4), per process:

```yaml
restart: never | on-failure | always   # default never (today's behavior)
retries: 3                              # attempts before failed; default 0
backoff: 2s..30s                        # exponential between attempts
```

Applies before ready (a migration that fails on a lock, a tunnel request that times out) and after ready (a crash). A oneshot retries only with `on-failure`.

`--keep-going` (1.5): a failed required process doesn't stop the stack. Its dependents go to `blocked` (the event says by what); everything independent keeps running; `up -d` exits 1 with the failure but the stack stays up. `stackstart start <p>` runs it again; when it becomes ready, blocked dependents start. Without `--keep-going`, behavior is today's: the first required failure stops everything.

## 5. `status --json` (1.7)

`stackstart status --json` prints one document: an array of sessions visible from the current directory (`--all` for every session). Same data over the socket's `status` op.

```json
[
  {
    "profile": "fullstack-offline",
    "project": "/Users/me/app",
    "config_file": "/Users/me/app/stackstart.yaml",
    "run_id": "20261011-080102-4242",
    "stackstart_pid": 4242,
    "detached": true,
    "started_at": "2026-10-11T08:01:02Z",
    "state": "running",
    "log_dir": "/Users/me/.../runs/20261011-080102-4242",
    "events": {"file": ".../events.jsonl", "last_seq": 418},
    "git": {"root": "/Users/me/app", "worktree": false, "branch": "main", "head": "2d5a1c9", "head_moved": false},
    "processes": [
      {
        "name": "phoenix",
        "kind": "long-running",
        "state": "ready",
        "pid": 51234,
        "cwd": "/Users/me/app/phoenix_app",
        "depends_on": ["migrate", "tunnel"],
        "ports": [4110],
        "captures": {},
        "started_at": "2026-10-11T08:01:40Z",
        "ready_after_ms": 4210,
        "restarts": 1,
        "last_exit": {"code": 1, "at": "2026-10-11T08:01:35Z"},
        "log_file": ".../phoenix.log",
        "raw_log_file": ".../phoenix.raw.log"
      },
      {
        "name": "tunnel",
        "state": "ready",
        "pid": 51200,
        "captures": {"url": "https://quiet-river-blue-moon.trycloudflare.com"}
      }
    ]
  }
]
```

- `ports`: from `tcp:` checks now; Phase 4 adds allocated ports.
- `head_moved`: `HEAD` differs from what it was at `up` (an agent switched branches under a running stack).
- Whether the top level is an array or a versioned object is **Q3**.
- The text `status` table stays the default for humans.

## 6. Logs (1.8) and send (1.6)

- `stackstart logs <p> --lines 50 --no-follow`: last 50 plain lines, exit 0, works live or after the run. `--follow` keeps today's live behavior and is the default only on a terminal (**Q5**: or keep follow as the default everywhere?).
- `stackstart send <p> "recompile\n"`: writes to the process's PTY through the socket, prints what the process printed in the next 500 ms. For IEx (`recompile`), Rails console, `y` to a prompt.
- `stackstart attach <p>`: interactive, for humans: shows the process's output and forwards keystrokes; detach with `Ctrl-]`. Not needed by agents.

## 7. CLI summary after Phase 1

```text
stackstart up [-d [--no-wait] [--timeout D]] [--keep-going] <profile>
stackstart start|stop|restart <process> [--wait ready|none] [--timeout D] [--profile P]
stackstart status [--json] [--all]
stackstart logs <process> [--lines N] [--no-follow | --follow] [--profile P]
stackstart events [--after SEQ] [--follow] [--json]          (2.3, cheap once the log exists)
stackstart wait <process> [--for ready|exit|event:<type>] [--after SEQ] --timeout D   (2.4)
stackstart send <process> <text>
stackstart attach <process>
stackstart down [--force] [--no-wait] [--timeout D]
```

Every command keeps the design rules in ROADMAP.md: no prompts, `--json` on read commands, stable exit codes, data on stdout and progress on stderr.

## 8. Testing plan

All black-box, added to `conformance/` with the contract updated first.

- `lifecycle/up-detached`: `up -d` exits 0 once ready; `status --json` shows every PID; the client's exit doesn't stop the stack; `down` stops it; leak check.
- `lifecycle/up-detached-fails`: `up -d` of a failing profile exits 1 with the FAILED block and leaves nothing running.
- `lifecycle/restart-one`: synthetic three-process profile; `restart b` changes only b's PID (via `status --json`).
- `lifecycle/restart-dependents-on-capture-change`: a producer whose captured value changes on restart; `on_dep_restart: restart` restarts the consumer only then.
- `lifecycle/retries`: a process failing twice then succeeding with `retries: 3`; events show the attempts and backoff.
- `lifecycle/keep-going`: a failing oneshot blocks its dependent while an independent process runs; fixing the file and `start` unblocks it.
- `logs/no-follow-lines`, `signals/send-to-prompt` (perl REPL echoing input), `sessions/status-json-schema` (validated against a JSON Schema file in `conformance/`).
- Projects: `projects/composite/restart-phoenix` (the acceptance criterion above), `projects/composite/bad-migration-keep-going`, `projects/phoenix_app/iex-send-recompile`.
- Runner: needs a `json` assertion (`stdout_json: {path: "$.processes[?(@.name=='phoenix')].pid", saves: PHX_PID}`) or a simpler `jq`-style `sh` step. **Q6**: prefer a runner step (portable) over requiring `jq` on CI machines.

## 9. Decisions (owner, 2026-10-11)

- **Q1** `stop <p>` and `restart <p>` **also stop the processes that depend on it** (dependents first). After `start`/`restart`, dependents are started again once the process is ready.
- **Q2** Dependents of a restarted process restart **only if a capture they reference changed value**; `on_dep_restart: ignore` opts out. (When the process is restarted, its dependents were stopped by Q1 and simply come back with the new values.)
- **Q3** `status --json` is an object: `{"schema": 1, "sessions": [...]}`.
- **Q4** Values are shown as-is; files are 0600; `secret: true` on a capture or env var shows `***`.
- **Q5** `logs` follows only when stdout is a terminal; `--follow` / `--no-follow` force either.
- **Q6** JSON assertions are a runner step.
- **Q7** Windows stays out of scope.
- Phase 4: ports are auto-allocated port variables. Phase 5: `init --agents` prints a snippet and never edits files. Phase 6: the TUI is skipped and the licence is left to the owner.

## 10. Original open questions

- **Q1** `stop <p>` while dependents run: leave them running (simplest; they may error) or stop dependents too (safer; `--only` to opt out)? Proposal: leave them, mark them `degraded`, and print which ones depend on it.
- **Q2** `on_dep_restart` default `ignore` (nothing restarts unexpectedly) or `restart`-if-captures-changed (most correct for tunnels)? Proposal: `restart` only when a referenced capture changed; that is never a surprise because without a change nothing happens.
- **Q3** `status --json`: top-level array of sessions, or an object `{"schema": 1, "sessions": [...]}`? Proposal: the object, so the schema version has a place.
- **Q4** Captured values and resolved commands can contain secrets (tokens in URLs). Write them to `events.jsonl` and `status --json` as-is (useful, local-only files with 0600) or redact by pattern? Proposal: as-is, files 0600, and a `secret: true` flag on captures that redacts them.
- **Q5** `logs` default: follow on a terminal and `--no-follow` when stdout is not a terminal (agents get the right behavior automatically), or always explicit?
- **Q6** JSON assertions in the runner (see section 8).
- **Q7** Windows: still out of scope (no PTY parity, no Unix sockets before Windows 10 1803)? Proposal: yes, out of scope.
