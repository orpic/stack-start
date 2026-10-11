<!-- Stackstart - https://github.com/orpic/stack-start -->
<!-- Copyright (c) 2026 Shobhit. All rights reserved. See LICENSE. -->

# Stackstart public contract

This is everything a user or an agent can rely on, and the only thing the conformance cases in [`cases/`](cases/) are allowed to observe. Any implementation of stackstart, in any language, that passes the suite satisfies it.

Change this file deliberately. If you change a string here, change the cases that assert it in the same commit, and say so in the commit message.

**Not contract** (cases must never read or rely on them): the state directory layout, `run.json`, `<slug>.json`, `.state` files, the slug format, the run-id format, `stackstart.log` contents, the order of lines from *different* processes on the console, and timing beyond the documented timeouts.

Notation: `<x>` is a variable part. `…` is free text. Strings are exact unless marked "contains".

## 1. Commands

| Command | Arguments and flags | Blocks? |
| --- | --- | --- |
| `stackstart version` | | no |
| `stackstart --help`, `<cmd> --help` | | no |
| `stackstart init` | | no |
| `stackstart validate <profile>` | | no |
| `stackstart list` | | no |
| `stackstart up <profile>` | `-d`/`--detach`, `--no-wait`, `--timeout <d>` (10m), `--keep-going`, `--json` | foreground: until the stack stops; `-d`: until ready, failed or timed out |
| `stackstart down` | `--profile <name>`, `--no-wait`, `--force`, `--timeout <duration>` (default `2m`) | until the stack has stopped, unless `--no-wait` |
| `stackstart status` | `--json`, `--all`, `--profile` | no |
| `stackstart start\|stop\|restart <process>` | `--profile`, `--no-wait`, `--timeout <d>` (5m), `--json` | until ready (stop: stopped), unless `--no-wait` |
| `stackstart send <process> <text>` | `--profile`, `--no-newline`, `--echo <d>` (500ms), `--json` | for `--echo` |
| `stackstart attach <process>` | `--profile` | until Ctrl-] (needs a terminal) |
| `stackstart events` | `--after <seq>`, `--follow`, `--json`, `--type <t>` (repeatable), `--process <p>`, `--profile` | with `--follow` |
| `stackstart wait [process]` | `--for ready\|exit\|rebuilt\|event:<type>`, `--after <seq>`, `--timeout <d>` (5m), `--json`, `--profile` | until the condition or the timeout |
| `stackstart logs <process>` | `--profile <name>`, `--lines <n>`, `--follow`, `--no-follow` | follows only on a live stack with stdout on a terminal, or with `--follow` |
| `stackstart clean` | `--all`, `--dry-run` | no |

Global flags on every command: `--config <path>` (use this file instead of the cascade lookup), `--quiet` (no `stackstart:` notices on stderr), `--log-format text|json` (diagnostic log format).

`stdin` is never read by any command. No command prompts.

## 2. Exit codes

| Code | When |
| --- | --- |
| 0 | Success. For `up`: every process finished (oneshot-only profile), or the stack was stopped by one SIGINT/SIGTERM/SIGHUP or by `down`. |
| 1 | Any failure: invalid config, unknown profile, a process failed, no matching session, etc. |
| 5 | `wait` timed out; `up -d` is still starting after `--timeout`. |
| 130 | `up` received a second SIGINT/SIGTERM while stopping (or `down --force`), and killed everything. |

The finer table in ROADMAP.md ("Exit codes") is planned, not contract yet.

## 3. Output streams

- `up` writes process output to **stdout** as `<name padded to the longest process name> | <line>`. A process's own stdout and stderr are both shown (processes run on a terminal, so they are one stream).
- `up` writes its own lines to **stderr**, each starting with `stackstart: `, except the failure report (section 5).
- Console lines may contain SGR color codes (`ESC[…m`). They never contain any other escape sequence or a carriage return (section 7).
- `logs` writes the log to **stdout** and its header to **stderr**.
- Errors from any command go to **stderr** as one message (possibly several lines), and the exit code is non-zero.

## 4. Configuration

`stackstart.yaml`:

```yaml
profiles:
  <profile>:
    project_path: <abs path>          # optional; default: the directory holding the file
    processes:
      <name>:
        cmd: <shell command>          # required; run with /bin/sh -c
        cwd: <relative path>          # optional; relative to the project path
        kind: oneshot | long-running  # default long-running
        depends_on: [<name>, …]
        env: {KEY: value}             # values may use ${<process>.<capture>} and ${envrc.KEY}
        readiness:
          timeout: <duration>         # required when readiness is present
          mode: all | any             # default all
          checks:
            - log: <regex>            # a line of the process's plain output matches
            - tcp: <host:port>        # a TCP connect succeeds
            - capture: <name>         # that capture of this process has a value
        captures:
          - name: <name>
            log: <regex with exactly one group>
            required: true|false
        fail_on: [<regex>, …]
        templates:
          - {src: <relative path>, dst: <relative path>}
        on_exit: fail | ignore        # default fail
        required: true|false          # default true
        stop_grace_period: <duration> # default 10s
```

Behavior:

- **Lookup cascade.** Without `--config`, `stackstart.yaml` files are searched from the current directory upwards (and in the home directory). Every profile visible from the current directory is listed by `list`. When several files define the same profile name, the one whose project path is deepest wins.
- **Interpolation** in `cmd` and `env` values: `${<process>.<capture>}` is the captured value, `${envrc.KEY}` a value from the project's `.envrc`, `$$` a literal `$`. Any other `${…}` is an error, so shell variables in `cmd` are written `$VAR` or `$${VAR}`. A single `$` not followed by `{` or `$` is literal.
- **Templates** are Go `text/template` files rendered before the process starts, with `.<process>.<capture>` and `.envrc.<KEY>` available.
- **Readiness.** A process without `readiness` is ready as soon as it has started. A process is never ready before every one of its required captures (`required` defaults to `true`) has a value; a pending one shows as `capture <name>` in a timeout cause. The first line matching a capture sets it; later matches are ignored. A reference to an optional capture that never matched fails the process that uses it (`unresolved reference ${<p>.<c>}`). Readiness is checked continuously (not only when output arrives). Ready or failed is decided exactly once. `log` and `capture` checks and `captures` match the **plain** text of a line (section 7), so `^` and `$` anchor to the visible text.
- **Dependencies.** A process starts only after every process in its `depends_on` is ready (a oneshot is ready when it exits 0).
- **Oneshot.** Ready when it exits 0. Non-zero exit fails it. With `readiness.checks`, it is ready when the checks pass; with only `readiness.timeout`, the timeout bounds its run time.
- **`fail_on`.** Before the process is ready, a matching line fails it immediately. After it is ready, a matching line only adds the warning line in section 6.
- **`on_exit: ignore`.** The process may exit (any code, any time) without stopping the stack.
- **`required: false`.** A failure of this process is reported and the rest of the stack keeps running.
- **Stopping.** Processes are stopped in reverse dependency order: SIGTERM to the process group and to every descendant that left the group, then SIGKILL after `stop_grace_period`.

### Validation errors

`validate <profile>` and `up <profile>` reject invalid profiles with exit 1 and one of these messages (stderr, "contains"):

| Problem | Message |
| --- | --- |
| Unparsable YAML (also from the cascade: `up`, `validate`, `list`) | `parsing <path>: …` |
| No profiles key | `<path>: no 'profiles' key found` |
| Unknown profile (cascade) | `no profile named "<p>" applies to directory <cwd>; run 'stackstart list' to see available profiles, or 'stackstart init' to create one` |
| Unknown profile (`--config`) | `profile "<p>" not found in <path> (available: …)` |
| No processes | `profile "<p>" has no processes defined` |
| Bad kind | `process "<n>": kind must be 'oneshot' or 'long-running', got "<k>"` |
| No cmd | `process "<n>": 'cmd' is required` |
| Absolute cwd | `process "<n>": 'cwd' must be relative to project_path, got absolute path "<cwd>"` |
| Unknown dependency | `process "<n>": depends_on references unknown process "<d>"` |
| Self dependency | `process "<n>": cannot depend on itself` |
| Cycle | `dependency cycle detected: <a> -> <b> -> <a>` |
| Bad on_exit | `process "<n>": on_exit must be 'fail' or 'ignore', got "<v>"` |
| Empty fail_on | `process "<n>": fail_on[<i>] is empty` |
| Bad fail_on regex | `process "<n>": fail_on[<i>] invalid regex "<re>"` |
| Template without src/dst | `process "<n>": template[<i>] missing 'src'` / `missing 'dst'` |
| Absolute template path | `process "<n>": template[<i>] 'src' must be relative, got "<p>"` (same for `dst`) |
| No readiness timeout | `process "<n>": readiness.timeout is required and must be positive` |
| Bad mode | `process "<n>": readiness.mode must be 'any' or 'all', got "<m>"` |
| No checks | `process "<n>": readiness.checks must contain at least one check` |
| Check with no kind | `process "<n>": readiness.checks[<i>] must specify one of 'log', 'tcp' or 'capture'` |
| Check with two kinds | `process "<n>": readiness.checks[<i>] must specify only one of 'log', 'tcp' or 'capture'` |
| Capture check of unknown capture | `process "<n>": readiness.checks[<i>] capture "<c>" is not one of this process's captures` |
| Bad log regex | `process "<n>": readiness.checks[<i>] invalid log regex "<re>"` |
| Bad tcp | `process "<n>": readiness.checks[<i>] tcp must be 'host:port', got "<v>"` |
| Capture without name/log | `process "<n>": captures[<i>] missing 'name'` / `missing 'log'` |
| Bad capture regex | `process "<n>": captures[<i>] invalid log regex "<re>"` |
| Capture group count | `process "<n>": captures[<i>] regex must have exactly 1 capturing group, got <k>` |
| Malformed reference | `process "<n>": invalid reference ${<ref>} (expected ${process.capture})` |
| Reference to unknown process | `process "<n>": reference ${<ref>} refers to unknown process "<p>"` |
| Reference to unknown capture | `process "<n>": reference ${<ref>} refers to unknown capture "<c>" on process "<p>"` |
| Bad duration | `invalid duration "<s>"` |

Success: `Profile "<p>" from <path> is valid.` (stdout).

## 5. `up`

stderr notices:

- `stackstart: 1 process ready · logs: <dir>` or `stackstart: all <N> processes ready · logs: <dir>` (N counts every process, oneshots included), once, when every process is ready. `<dir>` holds `<process>.log` (plain) and `<process>.raw.log` (exact bytes) for every process.
- `stackstart: all processes finished`, when nothing is left running (then exit 0).
- `stackstart: <p> failed (not required, continuing): <cause>`
- `stackstart: stopping all processes (Ctrl-C again to kill immediately)`, on the first SIGINT/SIGTERM/SIGHUP.

Failure report (stderr), then exit 1 after every process is stopped:

```text
--- FAILED: <process> ---
Cause: <cause>
Last output:
  <last lines of the process, indented two spaces>
Full log: <path to the process's plain log>
Read it later with: stackstart logs <process>
```

`Last output:` is omitted when the process printed nothing. Causes (contains):

| Cause | Text |
| --- | --- |
| Readiness timeout | `readiness timeout after <d>; still waiting for <checks>`, where checks are `log /<re>/`, `tcp <addr>`, `capture <name>`, joined by `, ` |
| `fail_on` match | `matched fail_on /<re>/: <line>` |
| Exit before ready | `process <p> exited before becoming ready: exit status <n>` (or `signal: <name>`), or `… (exit 0)` |
| Exit after ready | `process <p> exited unexpectedly post-ready: exit status <n>` (or `signal: <name>`), or `… (exit 0)` |
| Oneshot exit non-zero | `oneshot process failed: exit status <n>` |
| Oneshot exit 0 with checks pending | `oneshot process exited before readiness checks passed; still waiting for <checks>` |
| Already running | `profile is already running for this project; see 'stackstart status'` (no FAILED block) |

Guarantees:

- A readiness timeout fires on time whatever the process is doing (logging, silent).
- A `fail_on` match fails the process without waiting for the timeout, and its dependents never start.
- After `up` exits, or after `down` returns, **no process the profile started is alive**: not the process, not its children, not descendants that left its process group.
- A second SIGINT/SIGTERM kills everything immediately and exits 130.
- SIGHUP (terminal closed) stops the stack like SIGINT.

## 6. Console and log markers

These lines appear in a process's console stream (prefixed with its name) and in its log:

- `[stackstart] screen cleared` — where the process cleared the screen (`ESC[2J`, `ESC[3J`, `ESC c`).
- `[stackstart] warning: matched fail_on /<re>/ after the process was ready`

## 7. Output handling

What a process writes is turned into lines as follows. "Console" is `up`'s stdout; "log" is what `stackstart logs` prints.

- Lines end at `\n`. `\r\n` is a line end.
- `\r` inside a line keeps only the text after the last `\r` (progress bars and spinners collapse to their final text).
- Moving the cursor to column 1 (`ESC[G`, `ESC[1G`) or erasing the whole line (`ESC[2K`) works like `\r`. A spinner frame erased this way, with nothing after it, leaves no line at all.
- A line that hasn't ended is still shown, logged and matched once output pauses for 250 ms (prompts such as `iex(1)> `).
- A line longer than 64 KB is split into 64 KB pieces; no byte is lost and reading never stops.
- Screen clears become the marker line above. All other escape sequences (cursor movement, erase line, OSC titles, charset switches, cursor visibility) are removed.
- SGR color codes are kept on the console and removed from the log.
- Invalid UTF-8 is replaced; logs are always valid UTF-8.
- Other control characters except tab are removed.
- Every byte the process wrote is also kept unchanged in a raw log next to the plain log (`<process>.raw.log` beside the path printed as `Full log:`).

## 8. Environment

Each process's environment is layered, later wins:

1. The environment `up` was started with.
2. `PAGER=cat`, `GIT_PAGER=cat`, `PSQL_PAGER=cat`, `MANPAGER=cat`, `SYSTEMD_PAGER=` (empty), over the shell's own values.
3. The project's `.envrc`: evaluated with `direnv` when it is installed; otherwise only `export KEY=value` lines (and comments, blank lines) are supported, and anything else fails with `… unsupported directive "<line>" - this .envrc requires direnv to evaluate; install direnv via 'brew install direnv' and run 'direnv allow'`.
4. The project's `.env` (`KEY=value` lines, `#` comments).
5. The process's `env:` block, with interpolation.

## 9. Sessions

- A running `up` is a session for (project path, profile). A second `up` of the same profile in the same project fails with the "already running" message.
- `down`, `logs` find sessions whose project path is the current directory or one of its parents, **but never across a nested git repository or worktree**: from a directory inside the project that contains its own `.git` (directory or file), the outer project's sessions are not found.
- More than one match: `more than one stackstart session matches this directory; pass --profile <name>:` followed by one `  --profile <name>  <project path>` line per session. `logs <p>` first narrows to the sessions that have process `<p>`.
- `down`:
  - success (stdout): `Stopped "<profile>" in <path>. Logs: <dir>`; progress (stderr): `Stopping "<profile>" in <path>...`
  - `--no-wait` (stdout): `Stopping "<profile>" in <path> (PID <pid>).`
  - `--force` (stdout): `Killed "<profile>" in <path>. Logs: <dir>`; every process is killed at once and `up` exits 130.
  - running `down` again while the stack is stopping waits again; it never force-kills.
  - nothing running: `no running stackstart session found for <cwd>`
  - timeout: `stack "<profile>" is still stopping after <d> (stackstart PID <pid>); run 'stackstart down' again to keep waiting, or 'stackstart down --force' to kill it now`
- `status`: `No running stackstart sessions.` or a table with header `PROFILE  PROJECT  PID  STARTED  PROCESSES` and one row per session, processes as `<name>(<state>)` joined by `, `.
- `list`: `Available profiles:` then `  <name padded>  (from <path>)` per profile; or `No profiles found for the current directory context.` and `Run 'stackstart init' to create a stackstart.yaml.`
- `init`: `Created <path>`; if the file exists, fails with `stackstart.yaml already exists in <dir>`.
- `version`: `stackstart <version> (commit: <c>, built: <date>)`.

## 10. Logs and runs

- Every `up` is a run with its own logs. Logs are kept after the run ends, whatever the outcome. The last **10** runs of each (project, profile) are kept; older ones are deleted when a new run starts.
- `logs <p>` on a live session, following (stdout is a terminal, or `--follow`): header `==> <p> · <profile> · <project> · running (Ctrl-C to stop) <==` on stderr, then the last 10 lines (or `--lines`), then new lines as they come, until SIGINT/SIGTERM (exit 0). Not following: header `==> <p> · <profile> · <project> · running <==`, then the whole log (or the last `--lines`), exit 0.
- `logs <p>` with no live session: the whole log of the most recent finished run that matches, then exit 0. Header: `==> <p> · <profile> · <project> · last run <id> stopped at <time> <==`, or `… last run <id> failed at <time> (<failed process>) <==`.
- Errors: `no running or previous stackstart session found for <cwd>`; `process "<p>" not found in profile "<profile>" (processes: …)`.
- `clean` removes finished runs of the project in the current directory (`--all`: every project); never a running session's logs. Output: `Removed <N> finished run(s), <size>.`; with `--dry-run`, one `would remove  <run>  <profile>  <project>  (<size>)` line per run and `Would remove <N> finished run(s), <size>.`

## 11. Controlling a running stack

Every running stack accepts commands on a control socket (its path is in `status --json` as `control_socket`). The CLI commands below use it; anything else may too (newline-delimited JSON, see docs/phase1-design.md).

**`up -d`** starts the stack in the background, detached from the terminal, and waits:

- ready: `stackstart: all <N> processes ready · logs: <dir>` (stderr) and `Started "<profile>" in <path> (PID <pid>). Logs: <dir>` (stdout), exit 0. With `--json`, the status document instead.
- a required process failed: the FAILED block (section 5), exit 1. Without `--keep-going` the stack has stopped; with it, the stack keeps running and stderr says `The stack keeps running (--keep-going). Fix the cause, then: stackstart start <p>`.
- a profile of oneshots finished: `stackstart: all processes finished`, exit 0.
- `--timeout` passed: exit 5; the stack keeps running.
- `--no-wait`: returns once the stack has started (`Started …`).

The background stack's console output goes to `console.log` in its logs directory.

**Process states** (`status`, `status --json`): `pending` (waiting for dependencies), `blocked` (a dependency failed; `blocked_by` names it), `starting`, `ready`, `backoff` (waiting to retry), `stopping`, `stopped` (by `stop`), `failed`, `exited` (a finished oneshot or `on_exit: ignore`).

**`stop <p>`** stops `p` and every process that depends on it, dependents first. `p` becomes `stopped`; its dependents `pending`. Output: `Stopped <dependents…>, <p>.` Processes that don't depend on `p` are not touched.

**`start <p>`** starts a `stopped`, `failed`, `exited` or `pending` process; processes waiting for it start once it is ready. Output: `Started <p>: ready in <d>.` A process that is already starting, ready or stopping: exit 1, `process <p> is already <state>` and `use 'stackstart restart <p>' to restart it`. A `blocked` process: exit 1, `process <p> is blocked: <q> failed` and `fix it, then run 'stackstart start <q>'; <p> starts once it is ready`.

**`restart <p>`** stops `p` and its dependents, then starts them again with fresh captured values. Nothing else is touched: other processes keep their PIDs and captured values. Output: `Restarted <targets>: ready in <d>.` (`--json`: `{"ok": true, "process": …, "targets": [dependents…, p], "seq": …}`).

Unknown process for any of these: exit 1, `process "<p>" not found in profile "<profile>"` and `processes: <list>`.

**Retry policy** (config): `restart: never | on-failure | always` (default `never`), `retries: N` (failed attempts in a row before the process counts as failed; 0 = no limit), `backoff: <min>..<max>` (default `1s..30s`, doubling). `on-failure` does not retry a long-running process that exited with code 0. While waiting, the state is `backoff` and stderr says `stackstart: <p> failed (<cause>); retrying in <d> (attempt <n>[ of <max>])`. When a retried process is ready again, processes that use one of its captured values are restarted **only if that value changed** (`on_dep_restart: ignore` opts out).

**`--keep-going`**: a required process that fails (after its retries) becomes `failed`, its dependents `blocked`, everything else keeps running; stderr says `<p> failed (--keep-going; fix it and run 'stackstart start <p>')`. The stack ends on its own only when nothing is left running.

**`send <p> <text>`** types `text` and Enter into `p`'s terminal and prints what `p` printed during `--echo` (the terminal echoes the typed text too). `--no-newline` omits Enter.

**`status --json`** prints `{"schema": 1, "sessions": [...]}`. Per session: `profile`, `project`, `config_file`, `run_id`, `stackstart_pid`, `detached`, `keep_going`, `started_at`, `state` (`running`, `stopping`), `log_dir`, `console_log`, `events.file`, `events.last_seq`, `control_socket`, `git` (`root`, `worktree`, `branch`, `head`, `start_head`, `head_moved`). Per process (in name order): `name`, `kind`, `state`, `pid` (0 unless running), `cwd`, `depends_on`, `blocked_by`, `ports` (from `tcp:` checks), `captures` (`***` for `secret: true`), `started_at`, `ready_after_ms`, `restarts`, `attempt`, `last_exit` (`code`, `signal`, `at`), `cause`, `log_file`, `raw_log_file`. Without a running stack: `{"schema": 1, "sessions": []}`.

**Events.** Every run keeps `events.jsonl` (path in `status --json`): one JSON object per line with `seq` (1, 2, 3… per run), `ts`, `type`, `process` and type-specific fields. Types: `run_started`, `process_starting` (`pid`, `attempt`, `cmd`), `process_ready` (`pid`, `after_ms`), `capture` (`name`, `value`), `screen_cleared`, `fail_on_warning` (`pattern`, `line`), `process_backoff` (`cause`, `attempt`, `delay_ms`), `process_failed` (`cause`, `last_lines`, `log_file`, `required`), `process_exited` (`pid`, `exit_code`, `signal`, `expected`), `process_blocked` (`blocked_by`), `start_requested`, `process_restarting` (`by`), `process_stopping` / `process_stopped` (`by`: `user`, `restart`, `dependency`), `input_sent`, `stack_ready`, `run_stopping` (`reason`), `run_finished` (`outcome`, `failed_process`, `cause`). Clients must ignore types they don't know.

`events` prints them as `<seq> <time> <type> <process or -> <key=value …>` (or JSON lines with `--json`); on a stopped stack it reads the last run's file.

**`wait`** exits 0 when the condition holds, printing the matching event (or `<p> is ready (seq <n>)` when it already was). `--for ready` is met at once if the process is ready and no `--after` is given; every other condition counts only events after `--after <seq>` (default: now). A `process_failed` of the process (or `rebuild_failed` for `rebuilt`) ends the wait with exit 1 and the failure. Timeout: exit 5, `timed out after <d> waiting for <condition>`.
