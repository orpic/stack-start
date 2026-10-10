<!-- Stackstart - https://github.com/orpic/stack-start -->
<!-- Copyright (c) 2026 Shobhit. All rights reserved. See LICENSE. -->

# Conformance suite

This is stackstart's only test suite. It is black-box: every case drives a `stackstart` binary through its command line and checks only what a user or an agent could see (exit codes, output, processes, ports, HTTP, files in the project). It never imports stackstart's code or reads its internal files, so it keeps working, unchanged, if stackstart is rewritten in another language.

- [`CONTRACT.md`](CONTRACT.md): the public contract. Cases may only rely on what it describes.
- [`cases/`](cases/): the cases, one folder each.
- [`runner/`](runner/): the program that runs them. It is its own Go module and imports nothing from stackstart.
- [`MIGRATION.md`](MIGRATION.md): which case replaced which old Go test.

## Running

```bash
go build -o tmp/stackstart ./cmd/stackstart          # the binary under test
(cd conformance/runner && go build -o ../../tmp/conformance .)
tmp/conformance --bin tmp/stackstart                  # every case
tmp/conformance --bin tmp/stackstart --filter 'output/*'
tmp/conformance --bin tmp/stackstart --filter 'projects/phoenix_app/*' -v
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bin PATH` | `$STACKSTART_BIN` | The stackstart binary under test. Required. |
| `--cases DIR` | `conformance/cases` next to the runner's module | Where the cases are. |
| `--root DIR` | the parent of the `conformance` directory | Repository root; `project:` paths are relative to it. |
| `--filter GLOB` | all | Run cases whose id (path under `cases/`, e.g. `output/screen-clear`) matches. Repeatable; also matches a prefix with `/*`. |
| `--skip-projects` | off | Skip cases that set `project:` (the fast, synthetic-only run used on every PR). |
| `-j N` | number of CPUs | Cases run in parallel; `exclusive` cases run alone. |
| `--junit FILE` | none | Also write a JUnit XML report. |
| `--keep-work` | off | Keep each case's work directory and print its path. |
| `--work-root DIR` | system temp dir | Where work directories are created. Keep it outside any directory with a `stackstart.yaml` above it. |
| `-v` | off | Print every step as it runs. |

Output: one line per case, `PASS`, `FAIL` or `SKIP <reason>`, with its duration. A failure prints the step that failed, what was expected, and the full stdout and stderr of every command the case ran. Exit code 1 if any case failed, 0 otherwise.

## Writing a case

A case is a folder under `cases/<area>/<case-name>/`:

```text
cases/output/screen-clear-is-a-marker/
  case.yaml          # required: what to do and what to expect
  stackstart.yaml    # the config under test (unless the case uses a test project)
  …                  # any other files the case needs; copied into the work dir
```

Areas: `cli`, `config`, `lifecycle`, `readiness`, `output`, `logs`, `sessions`, `env`, `signals`, `realtools`, `projects/<project>`, `scenarios`.

Rules:

1. **Only the contract.** Assert only what [CONTRACT.md](CONTRACT.md) promises. If you need something it doesn't offer (a PID, a state), that is a missing feature: add it to stackstart and the contract first.
2. **No shared helpers.** Synthetic behavior is written inline in the case's own `stackstart.yaml` with POSIX `sh` and tools found on every macOS and Linux machine (`printf`, `head`, `tr`, `seq`, `sleep`, `trap`, `perl`). No helper scripts shared between cases, no compiled fixtures.
3. **Real tools first.** If a real tool shows the behavior, add a case using it (with `requires:`), even when a synthetic case exists.
4. **Prove it fails.** Run a new case against a binary without the feature and record the result in `notes`.
5. **Stop what you start.** Every `start` must have exited by the end of the case (stop it with `signal` or `run: down`), and nothing it started may still be alive. The runner checks this (see [Leak check](#leak-check)).
6. **No sleeping to wait.** Use `wait_output`, `wait_exit`, `http`, `port` or `process`, which poll until a timeout. `sleep` is allowed only to make time pass on purpose, with a `reason`.

### `case.yaml`

```yaml
name: screen clear becomes a marker instead of wiping the console   # required
roadmap: "0.5"                 # roadmap item(s) covered
covers: [clear]                # behavior catalogue rows (ROADMAP.md)
requires: ["perl -v"]          # shell commands that must exit 0, or the case is skipped
platforms: [darwin, linux]     # default: both
project: null                  # e.g. test-projects/phoenix_app: run there instead of in a copy
exclusive: false               # true: never run alongside another case (default true with project)
home: isolated                 # isolated (default) | real (default with project); see Environment
timeout: 60s                   # whole case; default 60s
ports: [web, db]               # free TCP ports, available as ${PORT_web}, ${PORT_db}
env: {}                        # extra environment for every command in the case
notes: "fails on v0.2.3: console shows ESC[2J"
steps: [...]                   # required
```

### Variables

Substituted in every step value, in `env`, and in every text file copied into the work dir (including `stackstart.yaml`). Only these names are replaced; anything else, such as stackstart's own `${process.capture}`, is left alone.

| Variable | Value |
| --- | --- |
| `${BIN}` | Absolute path of the stackstart binary. |
| `${WORK}` | The directory the case runs in: a fresh copy of the case folder, or the project folder. |
| `${CASE}` | The case's source folder (read-only). |
| `${ROOT}` | The repository root. |
| `${TOKEN}` | A unique string for this run of the case, e.g. `ssc7f3a91`. Put it in long-running synthetic commands so they can be found (`perl -e 'sleep 300' ${TOKEN}` shows the token in `ps`). |
| `${STATE}` | The case's own `XDG_STATE_HOME`. |
| `${HOME}` | The `HOME` the case's commands see. |
| `${PORT_<name>}` | A TCP port that was free when the case started. |

### Environment

Every command a case runs gets:

- the runner's environment, plus `XDG_STATE_HOME=${STATE}` (a fresh directory per case, so cases never see each other's or the developer's sessions), `NO_COLOR` unset, and the case's `env`;
- `HOME`: with `home: isolated`, a fresh empty directory, so a developer's `~/stackstart.yaml` can't leak into the case. With `home: real`, the developer's own home, which toolchains managed by asdf, mise, rbenv and similar need. Project cases default to `real`.

`requires` commands run with the same environment, in the work dir.

### Steps

Steps run in order. The first failing step fails the case; the remaining steps are skipped, but cleanup and the leak check still run.

Command arguments (`run`, `start`) are split like a shell would (spaces, single and double quotes, backslash), with no other shell features. `stackstart` itself is never written: `run: up dev` runs `${BIN} up dev`.

| Step | Fields | Meaning |
| --- | --- | --- |
| `run: <args>` | `id`, `cwd`, `env`, `timeout` (30s), `expect` | Run stackstart in the foreground and wait for it to exit. Hitting the timeout fails the step (and kills the command). |
| `start: <args>` | `id` (default: first word of args; must be unique), `cwd`, `env` | Run stackstart in the background. |
| `wait_output` | `id`, `stream` (`stdout`, `stderr`, `any`; default `any`), `contains` or `matches`, `count` (default 1: wait for at least this many occurrences), `timeout` (required), `save` | Wait until the output of `id` so far satisfies the condition. Fails early if `id` exits without it. `save: NAME` (with `matches`) stores the regex's first group as `${NAME}` for later steps, e.g. a printed log directory or tunnel URL. |
| `expect_output` | `id`, `stream`, plus any [output assertion](#output-assertions) | Assert on the output of `id` so far. |
| `wait_exit` | `id`, `exit` (expected code; omit to accept any), `timeout` (required), `within` | Wait for `id` to exit. `within: 5s` fails if it exited later than 5s after it was started. |
| `alive` | `id`, `for` | Fail if `id` exits during the next `for` (e.g. `3s`). |
| `signal` | `id`, `sig` (`INT`, `TERM`, `HUP`, `KILL`) | Send a signal to the stackstart process `id`. |
| `sh: <script>` | `cwd`, `env`, `timeout` (30s), `expect` | Run a POSIX `sh` script and wait for it. For setup (create `.git`, write files) and for observing (`grep`, `curl`). It must not leave processes behind. |
| `spawn: <script>` | `id` (required), `cwd`, `env` | Start a POSIX `sh` helper in the background, e.g. a listener that occupies a port. Helpers and their children are killed when the case ends and are exempt from the leak check. Its output can be awaited with `wait_output`. |
| `write_file` | `path`, `content` | Write a file (relative to `${WORK}`), creating directories. |
| `edit_file` | `path`, `replace: {from, to}` | Replace the first occurrence of `from` (must exist) with `to`. |
| `http` | `url`, `status` (200), `body_contains`, `timeout` (required) | Request `url` until it answers with `status` (and body), or fail at the timeout. |
| `port` | `addr` (`host:port`), `open` (`true`/`false`), `timeout` (required) | Wait until a TCP connect to `addr` succeeds (`open: true`) or fails (`open: false`). |
| `process` | `matches` (regex on the full command line), `count` or `absent: true`, `timeout` (required) | Wait until exactly `count` processes match (or none). The runner's own processes never match. |
| `sleep` | `duration`, `reason` (required) | Let time pass on purpose. |

In project cases, files changed with `write_file` and `edit_file` are restored (or deleted, if they didn't exist) when the case ends, so the committed project is never left modified.

#### Output assertions

Used by `expect_output`, and inside `expect` of `run` and `sh`. In `expect`, prefix each with the stream: `stdout_`, `stderr_` or `output_` (both streams, in the order written). `expect.exit` is the expected exit code (default 0).

| Assertion | Meaning |
| --- | --- |
| `contains: <s>` or `[<s>, …]` | Every string occurs. |
| `not_contains: <s>` or `[<s>, …]` | No string occurs. |
| `equals: <s>` | The whole output equals `<s>`. |
| `matches: <re>` | The regex matches somewhere (RE2 syntax; `(?s)` makes `.` match newlines, `(?m)` makes `^`/`$` match at lines). |
| `count: {<s>: N, …}` | `<s>` occurs exactly `N` times (non-overlapping). |
| `in_order: [<s>, …]` | The strings occur in this order. |
| `valid_utf8: true` | The output is valid UTF-8. |

YAML double-quoted strings can contain escapes: `"\e[2J"` (or `"\x1b[2J"`), `"\r"`, `"\t"`.

### Durations

`500ms`, `10s`, `2m`, `1m30s`.

### Leak check

After the last step the runner checks that the case left nothing running:

1. Every `start` must have exited. One still running fails the case.
2. While the case runs, the runner samples the process table every 50 ms and records every process descended from a command it started (`run`, `start`, `sh`), including ones that later left their process group or were re-parented. Any recorded process still alive 2 s after the last step is a leak. So is any process whose command line contains `${TOKEN}` and isn't a `spawn` helper.
3. Every `${PORT_<name>}` must be free again, unless a `spawn` helper holds it.

Leaked processes are killed (`SIGKILL`) and listed in the failure. A leak fails the case even if every step passed.

### Example

```yaml
name: screen clear becomes a marker instead of wiping the console
roadmap: "0.5"
covers: [clear]
notes: "fails on v0.2.3: console contains ESC[2J"
steps:
  - start: up p
  - wait_output: {id: up, contains: "1 process ready", timeout: 10s}
  - expect_output: {id: up, stream: stdout, not_contains: ["\e[2J", "\e[3J"], contains: "[stackstart] screen cleared"}
  - signal: {id: up, sig: TERM}
  - wait_exit: {id: up, exit: 0, timeout: 15s}
  - run: logs app
    expect:
      stdout_equals: "build 1 ok\nerror from build 1\n[stackstart] screen cleared\nbuild 2 ok\n"
```

with `stackstart.yaml`:

```yaml
profiles:
  p:
    processes:
      app:
        cmd: "printf 'build 1 ok\\nerror from build 1\\n'; sleep 0.3; printf '\\033[2J\\033[3J\\033[H'; echo 'build 2 ok'; exec perl -e 'sleep 300' ${TOKEN}"
        readiness: {timeout: 5s, checks: [{log: "build 2 ok"}]}
```
