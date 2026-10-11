<!-- Stackstart - https://github.com/orpic/stack-start -->
<!-- Copyright (c) 2026 Shobhit. All rights reserved. See LICENSE. -->

# Stackstart Roadmap

Written 2026-10-10 against v0.2.3, last updated 2026-10-10 (late). This replaces the "v1.x candidate priorities" list in [PRD.md](PRD.md) section 12.

**If you are picking this up after a break (human or agent): read [Current state](#current-state), then [Work order](#work-order). They say exactly what is done, what is uncommitted, and what to do next.**

## Goal

Stackstart should be the process manager an agent (Claude Code, Codex, Cursor, or any other harness and model) uses to run a real local stack without help: start it, restart one process, read what happened, wait for a rebuild, run a one-off command with the right env, and stop it. And it should still be pleasant for a human at a terminal.

Before Phase 0 an agent could start a stack, watch it and stop it, and nothing else. In practice it couldn't even do that, because `up` blocks the terminal. In one production project (Phoenix + Node tool-server + cloudflared/tailscale + Postgres) the agent never ran stackstart once in ~80 sessions. A human started and restarted everything and pasted logs back into chat. See [Evidence](#evidence).

## Design rules for every feature

Every command and config field has to satisfy both columns. A feature that only works for one of them isn't finished.

| For agents | For humans |
| --- | --- |
| Never blocks unless asked to (`wait`, `logs --follow`, `attach`). Every blocking command takes `--timeout`. | Defaults stay readable: colored, prefixed, aligned console output. |
| `--json` on every read command, with a stable, documented schema. | Tables and sentences by default; `--json` is opt-in. |
| Stable exit codes (see [Exit codes](#exit-codes)) and structured errors: `{code, process, cause, hint, last_lines}`. | Every error says what failed, why, and the next command to run. |
| Never prompts on stdin. No confirmation dialogs; destructive commands take `--dry-run`. | Same. Safe by default instead of asking. |
| Every log line, state change and event is on disk, readable after the stack is gone. | `stackstart logs <p>` just works, live or after a crash. |
| Output on stdout is data; headers and progress go to stderr. | Headers and progress are still shown. |
| One way to do each thing, discoverable from `--help` and `describe --json`. | `--help` has examples for each command. |

---

## Testing: black-box only

Decided 2026-10-10 by the project owner. This governs every phase.

### Principles

1. **Tests are data, not code.** A test case is a folder containing a `case.yaml` (steps and expectations), a `stackstart.yaml` (the input), and any files the case needs. Nothing in a case is written in the language stackstart is implemented in.
2. **Only the public contract is tested.** Cases drive the `stackstart` binary through its CLI and observe only what a user or agent can observe: exit codes, stdout/stderr text, signals, processes, ports, HTTP responses, and files the user's own project contains. **No case reads stackstart's internal state files** (`run.json`, `<slug>.json`, `.state`, the run directory layout). Where a case needs information the CLI doesn't expose yet, the missing CLI feature is the bug, and the case waits for it (see `status --json`, moved to Phase 1).
3. **No shared fixture programs.** No `fakeproc`, no helper binaries, no test-only scripts that every case depends on. A rewrite of stackstart must never require rewriting or rebuilding test infrastructure other than the runner.
4. **Real projects first.** Behavior is tested against real frameworks in [`test-projects/`](#test-projects): Phoenix, Django, FastAPI, Node, React Router, Rails, cloudflared, Postgres. Those are the source of truth for "does it work in real life".
5. **Synthetic cases only where a real tool can't be deterministic**, e.g. a process that ignores SIGTERM, a 200 KB line, a prompt with no newline at an exact moment, a failure at an exact time. These are written **inline in the case's `stackstart.yaml`** using only POSIX `sh` and tools present on every macOS and Linux machine (`printf`, `head`, `tr`, `seq`, `sleep`, `trap`, `perl`). They are part of the case's input, like any other config, not shared fixtures.
6. **The runner is the only code.** One small program reads case folders and executes them against whatever binary `STACKSTART_BIN` points at. It must not import or depend on stackstart's code. If stackstart is rewritten in another language, the cases and the runner stay as they are; the runner can also be reimplemented from the [case format spec](#case-format) alone.
7. **Every case proves it can fail.** A new case is only accepted after it has been run against a binary without the feature (an older commit or release) and failed there for the right reason. Record which binary was used in the case's `notes`.
8. **Every case checks for leaks.** After each case the runner verifies that no process carrying the case's unique token is still alive and that every port the case allocated is free. A leak fails the case even if all steps passed.

### What counts as the public contract

Documented in [`conformance/CONTRACT.md`](conformance/CONTRACT.md), which is authoritative, and changed only deliberately. Summary:

- CLI commands, flags and arguments.
- Exit codes (today: 0 success, 1 any failure, 130 second Ctrl-C; the full table is item 2.7).
- Stable output lines:
  - `stackstart: all N processes ready · logs: <dir>` and `stackstart: 1 process ready · logs: <dir>` (stderr)
  - `stackstart: all processes finished` (stderr)
  - `--- FAILED: <process> ---`, `Cause: <text>`, `Last output:`, `Full log: <path>`, `Read it later with: stackstart logs <process>` (stderr)
  - `stackstart: stopping all processes (Ctrl-C again to kill immediately)` (stderr)
  - `[stackstart] screen cleared` and `[stackstart] warning: matched fail_on /<re>/ after the process was ready` (in a process's console stream and log)
  - Cause texts: `readiness timeout after <d>; still waiting for <checks>`, `matched fail_on /<re>/: <line>`, `process <p> exited before becoming ready ...`, `process <p> exited unexpectedly post-ready ...`
  - `logs` header on stderr: `==> <process> · <profile> · <project> · ... <==`
  - `down`: `Stopping "<profile>" in <path>...` (stderr), `Stopped "<profile>" in <path>. Logs: <dir>` (stdout)
- Console format: `<name padded> | <line>` on stdout; stackstart's own lines on stderr.
- `stackstart logs <p>` output is plain text: no escape codes, valid UTF-8.
- Process lifecycle guarantees: nothing a profile started is alive after `up` exits or `down` returns.
- Environment guarantees: `PAGER`, `GIT_PAGER`, `PSQL_PAGER`, `MANPAGER` are `cat` unless the project sets them.

Not contract (cases must not rely on them): the state directory layout, `run.json`, `.state` files, slug format, `stackstart.log` contents, timing beyond documented timeouts.

### Case format, steps and runner

Specified in [`conformance/README.md`](conformance/README.md) (the single source; this file no longer repeats it). The runner is in [`conformance/runner/`](conformance/runner/), its own Go module. Summary: a case is `conformance/cases/<area>/<name>/case.yaml` plus its `stackstart.yaml` and files; steps are `run`, `start`, `wait_output`, `expect_output`, `mark`, `wait_exit`, `alive`, `signal`, `sh`, `spawn`, `write_file`, `edit_file`, `http`, `port`, `process`, `sleep`; every case gets its own state dir, home, work dir and ports, and a leak check.

### Behavior catalogue

Every row needs at least one synthetic case and, where a real tool can run offline, one real-tool case. "Real case" names the test project or tool.

| Behavior | Who does it | Synthetic (inline sh) | Real case |
| --- | --- | --- | --- |
| Prompt with no trailing newline | IEx `iex(1)>`, `rails console`, `psql`, REPLs, "continue? [y/N]" | `printf 'iex(1)> '; sleep 300` | `iex`; `iex -S mix phx.server` (phoenix_app) |
| Opens a pager because it sees a terminal | `psql`, `git log`, `mysql`, `man`, `systemctl` | `[ "$PAGER" = cat ] && echo row \|\| { printf '(END)'; sleep 300; }` | `psql -c` oneshot (phoenix_app) |
| Clears the screen on rebuild/restart | `bun --watch` (twice per restart), `tsc --watch`, vite (restart), nodemon, Next.js, `cargo watch -c`, `air`. `node --watch` (v24) does not clear; it prints `Restarting '<file>'`. | `printf '\033[2J\033[3J\033[H'` | `bun --watch`, react_router_app (vite), node_api (`tsx watch`) |
| `\r` progress/spinner lines | npm/pnpm install, docker pull, esbuild, webpack, cargo, `mix deps.get` | `printf '10%%\r50%%\r100%% done\n'` | phoenix_app setup, `npm ci` |
| Cursor movement / redraw | docker compose, Next.js, Turborepo, ink CLIs | `printf '\033[1A\033[2K...'` | — |
| One very long line (>64 KB) | minified traces, JSON loggers, webpack stats, Elixir `inspect`/`IO` (Elixir's Logger truncates at 8 KB) | `head -c 200000 /dev/zero \| tr '\0' x; echo` | `node -e`, `elixir -e` |
| Opens a port, then goes silent | Postgres, Redis, many servers | — (needs a listener; use real tools) | Postgres (every project with a DB), Redis |
| Port accepts TCP before the service is ready | Docker's port proxy; postgres image init prints "ready to accept connections" for a temporary server | — | Postgres in Docker |
| Keeps logging but never becomes ready | wrong ready regex | `while :; do echo booting; sleep 0.1; done` | — |
| Spawns a child tree | `npm run dev` → node → esbuild, `go run` → binary, foreman | `sleep 300 & wait` | node_api, phoenix_app watchers |
| Child leaves the process group | BEAM port programs (Phoenix esbuild/tailwind watchers) via `erl_child_setup`; `setsid` | `perl -MPOSIX -e 'fork or do { POSIX::setsid(); exec @ARGV }' sleep 300` | phoenix_app (watchers checked after `down`) |
| Ignores or delays SIGTERM | BEAM, Puma, JVM, long shutdown hooks | `trap '' TERM; while :; do sleep 1; done` | phoenix_app (graceful BEAM shutdown) |
| Restarts itself inside the same PID | Phoenix code reloader, nodemon, `node --watch`, Zeitwerk, Django runserver, uvicorn `--reload` | `printf '\033cready\n'` twice | django_app, fastapi_app, phoenix_app, node_api |
| Prints the ready line, then crashes | bad migration, port taken after boot, tunnel failure | `echo ready; sleep 0.3; exit 1` | scenarios: bad-migration, port-in-use |
| Error line contains a URL that looks like success | cloudflared (`https://api.trycloudflare.com` in its error) | `echo 'ERR failed to request quick Tunnel: Post "https://api.trycloudflare.com/tunnel"'` | real cloudflared (network-only) |
| Oneshot | `mix ecto.migrate`, `npm ci && npm run build`, `rails db:migrate`, `prisma generate` | `echo migrated` | every project's `setup` profile and migrations |
| Reads stdin / prompts | REPLs, `mix ecto.reset`, `npx` "Ok to proceed?" | Phase 1.6 (`send`) | `iex` |
| stderr only | Go `log`, Python `logging`, Elixir Logger | `echo ready >&2` | fastapi_app (uvicorn logs to stderr) |
| UTF-8, wide characters | vite/next banners, cloudflared boxes | `printf '➜ ✓ │ 🚀\n'` | react_router_app (vite banner) |
| Invalid UTF-8 | native crashes, `cat` of a binary | `printf 'bad \377\376 bytes\n'` | — |
| Output burst | webpack verbose, test runners, SQL debug | `seq 1 50000` | — |
| Daemonises; parent exits | `docker compose up -d`, `pg_ctl start`, `redis-server --daemonize yes` | Phase 3 design needed | `pg_ctl` |
| Build tools sharing a build dir must not run in parallel | two `mix` commands into one `_build` (found 2026-10-10) | — | phoenix_app `setup` (chained on purpose) |
| Locale-dependent behavior | `initdb` picks SQL_ASCII without `LANG` (found 2026-10-10) | — | `test-projects/tools/pg-init` passes `-E UTF8 --locale=C` |

---

## Test projects

`test-projects/` holds real, minimal apps generated by each framework's own generator and committed to git. They are the main test bed: every feature is checked against them through conformance cases (`conformance/cases/projects/<project>/`).

### Rules

- **Commit source and lockfiles only.** Never `deps/`, `_build/`, `node_modules/`, `.venv/`, `.data/`, build output. Each project's `.gitignore` must cover these; verify with `git status --ignored` before finishing a project.
- **Each project has a `stackstart.yaml` with at least `setup` and `dev` profiles.** `setup` installs dependencies with oneshots and must exit 0 on its own. `dev` starts the app and everything it needs (database included), project-local.
- **Unique fixed ports per project** (table below), so every project can run at the same time. This also tests several stacks side by side (Phase 4).
- **Project-local databases.** Postgres runs from `.data/pg` via `test-projects/tools/pg-init` (idempotent `initdb -E UTF8 --locale=C`), never the user's own Postgres. SQLite where the framework defaults to it.
- **Pin runtime versions** with `.tool-versions` (asdf) where the machine needs it (Python has no global asdf version on the dev machine).
- **No network at test time except `setup`.** `dev` profiles must work offline once `setup` has run, except profiles that are explicitly about a network service (real cloudflared).
- `test-projects/tools/` holds only project infrastructure that real projects would also have (like `pg-init`). It must not contain fake versions of real tools.

### Projects

| Project | Status | Ports | What it exercises |
| --- | --- | --- | --- |
| `phoenix_app` | **Done.** Cases: `dev` (setup, ready, `GET /`, `.ex` edit → "Compiling 1 file (.ex)", CSS edit → tailwind `Done in`, down leaves no BEAM/watchers/postgres, data reused), `iex`, `bad-port`. | phoenix 4100, postgres 5544 | Phoenix 1.8 + LiveView + Ecto. Watchers as BEAM ports (process-group escape), code reloader, tailwind/esbuild rebuilds, graceful BEAM shutdown, `iex -S mix phx.server` prompt, migrations as oneshots, project-local Postgres |
| `django_app` | **Done.** Case `dev`: setup (venv + pip), migrate, runserver, edit → `<file> changed, reloading.`, down. | runserver 8100 | Django 5.2 LTS, StatReloader, `migrate` oneshot on SQLite, venv setup |
| `fastapi_app` | **Done.** Case `dev`: setup, uvicorn ready (stderr logging), edit → `WatchFiles detected changes … Reloading...` with a new worker PID, down stops reloader and worker. | uvicorn 8200 | FastAPI + `uvicorn --reload` |
| `node_api` | **Done.** Case `dev`: `npm ci` + `tsc` (spinner leaves no stray characters), `tsx watch` restart with a screen clear, down stops npm → tsx → node. | 3100 | Express 5 in TypeScript with `tsx watch` (mirrors the production tool-server) |
| `react_router_app` | **Done.** Case `dev`: Unicode Vite banner as ready signal, route edit → `(ssr) page reload`, `vite.config.ts` edit → `server restarted.`, down. | vite 5300 | React Router 8 framework mode on Vite 8 |
| `rails_app` | **Done** (Ruby 3.4.11 installed with asdf). Cases `dev` (foreman → puma + tailwind watcher, view reload, CSS rebuild, graceful foreman shutdown) and `console` (`rails-app(dev):001>` prompt). | puma 3200 | Rails 8.1, foreman, puma, tailwind, `db:prepare` oneshot, console prompt |
| Composite (`test-projects/stackstart.yaml`) | **Done.** Cases: `fullstack-offline`, `fullstack` (real cloudflared; passed 2026-10-11), `fullstack-ngrok` (passed 2026-10-11; skips without an authtoken), `tunnel-fails`, `bad-migration`, `port-in-use`, `cascade`. | postgres 5545, phoenix 4110, node_api 3110, react_router 5310 | The production stack: Postgres → Phoenix + Node API + React Router + tunnel, URL captured and injected into Phoenix (`/tunnel`) and the API (`publicUrl`) |

Composite profiles:

- `fullstack`: real `cloudflared tunnel --url http://127.0.0.1:4110` (network-only; cases `requires` network).
- `fullstack-offline`: same stack, tunnel replaced by an inline command printing cloudflared's real success banner (copied from real output) and sleeping. Used for every offline case.
- Scenario profiles reproducing the production incidents: `tunnel-fails` (cloudflared error line with the `api.trycloudflare.com` decoy; must fail fast via `fail_on`, dependents never start), `bad-migration` (a migration that raises when `BROKEN_MIGRATION=1`, on its own database recreated each run), and the `port-in-use` case (occupies Phoenix's port 4110 before `up fullstack-offline`). Phase 1 (`--keep-going`, retries, `restart`) is measured against these.
- `setup-all` installs every project's dependencies. Composite profile names must not clash with the projects' own (`setup`, `dev`, `iex`, `console`), because from inside a project both files are visible.

---

## Work order

Do these in order. Each item lists what "done" means. Verify every item with the full suite before moving on, and update [Current state](#current-state) as you go.

**Rules while working unattended:** commit on branch `agent-native-blackbox` at regular intervals (at least once per work item; never push; no attribution lines); Ruby may be installed (decision 2), nothing else system-wide without asking; network only for package registries during `setup`; scratch files go in `tmp/` (see AGENTS.md); never touch the developer's real stackstart state from tests; kill every process you start, and check for leftovers (`pgrep -fl "stackstart|esbuild|tailwind|beam.smp|postgres -D"`, ignoring VS Code and ElixirLS) after each run; do not touch processes you didn't start.

| # | Work | Done when |
| --- | --- | --- |
| W1 | Write `conformance/CONTRACT.md` (the public contract, from the section above, with every stable string) and `conformance/README.md` (case format and step vocabulary, from the section above, plus how to run). | Both files exist; every string currently asserted by `e2e/` tests is either in CONTRACT.md or the test is marked for rewrite. |
| W2 | Build the runner in `conformance/runner/` as its own Go module. Implement every step in the vocabulary, variables, isolation, parallelism, `exclusive`, leak check, `--junit`. | `go vet` and `golangci-lint` clean; runs a hello-world case; a case with a deliberate leak fails; a case with a timeout fails with the captured output. |
| W3 | Port every existing test to cases: the 31 `TestE2E_*` in `e2e/e2e_test.go`, the 32 `TestP0_*` in `e2e/phase0_test.go`, the 13 `TestRW_*` in `e2e/realworld_test.go`. Synthetic behaviors become inline `sh`; real-tool tests become cases with `requires`. Replace every read of `run.json`/state files with CLI observation (`logs`, process and port checks). Keep a mapping table (old test → new case) in `conformance/MIGRATION.md`. | Every old test has a case or a written reason it was dropped. Full suite passes on the current code. |
| W3b | Port the **unit-test** coverage to black-box cases, so nothing is lost when the Go unit tests are deleted. For each package, list what its `_test.go` checks and add cases observing the same behavior through the CLI: `config` (every validation error message, profile lookup cascade, `--config`), `interpolate` (`${p.c}`, `${envrc.X}`, `$$` escaping, errors), `env` (layer precedence shell < defaults < `.envrc` < `.env` < `env:`; `.env` parsing; native `.envrc` parser and its "requires direnv" error), `capture` (first match wins, exactly one group), `readiness` (`mode: any/all`), `output` (every sanitize rule: CRLF, `\r` collapse, SGR kept on console, cursor/OSC/charset stripped, clears, invalid UTF-8, chunking of long lines, partial-line flush), `session` (worktree matching, run pruning to 10, `clean`), `tail` (last lines, follow), `logmux` (prefix padding, markers), `proctree` (escaped descendants stopped). Record the mapping in `conformance/MIGRATION.md`. | Every unit test has a covering case listed in MIGRATION.md. |
| W4 | Prove the cases: build v0.2.3 (`git worktree add tmp/old v0.2.3` or commit `2d5a1c9`), run the suite with `--bin` pointing at it, and record in each Phase 0 case's `notes` whether it fails there and why. Then delete `e2e/` (Go e2e tests, `fakeproc`, helpers) **and every `internal/**/*_test.go` plus `internal/config/testdata/`** (decision 3), drop `testify` from `go.mod` if unused, and update CI: synthetic cases on every PR (macOS + Linux); project and real-tool cases in a separate job (nightly + manual dispatch) with toolchains installed (setup-beam, setup-node, setup-python, Postgres). | Old binary fails the Phase 0 cases; `e2e/` is gone; `.github/workflows/ci.yaml` runs the runner; TECH.md §14 and AGENTS.md describe the new suite. |
| W5 | Phoenix cases (`conformance/cases/projects/phoenix_app/`): setup exits 0; dev ready and `GET /` 200 on 4100; editing a `.ex` file then requesting `/` shows "Compiling" in `stackstart logs phoenix`; editing `assets/css/app.css` shows a tailwind rebuild; `down` leaves no BEAM, esbuild, tailwind or postgres process and frees 4100 and 5544; second `up dev` reuses `.data/pg`; `iex -S mix phx.server` profile becomes ready on the prompt. | All pass, leak check clean. |
| W6 | Generate and wire `django_app`, `fastapi_app`, `node_api`, `react_router_app`, then `rails_app` after installing Ruby (decision 2) (in that order), each with `setup`/`dev` profiles, `.gitignore` checked, and cases: setup, ready + HTTP 200, edit → reload visible in `logs`, `down` leaves nothing. Record each framework's exact reload/ready lines in the behavior catalogue. | Four projects committed-ready (sources + lockfiles only), cases pass. |
| W7 | Composite stack and scenarios: `test-projects/stackstart.yaml` with `fullstack` (cloudflared), `fullstack-ngrok` (ngrok; capture its `https://….ngrok-free.app` URL, skip when ngrok isn't configured), `fullstack-offline`, `tunnel-fails`, `bad-migration`, `port-in-use`; cases for each; cascade case (`stackstart list` from `test-projects/phoenix_app` shows both files' profiles). | All offline cases pass; `fullstack` case passes when network is available (skips otherwise). |
| W8 | Docs: README test section, TECH.md §14 rewritten for the black-box suite, `test-projects/README.md` (projects, ports, how to run), this file's Current state. | Docs match the code. |
| W9 | Only after W1–W8 are green: write a design note for Phase 1 (`docs/phase1-design.md`: control socket protocol, event log schema, `up -d` lifecycle, `status --json` schema) and stop for the owner's review. Don't implement Phase 1 unattended. | Design note written. |

### Owner decisions (2026-10-10, before the overnight run)

1. **Commits:** work on the branch `agent-native-blackbox`, created from `main`. Commit at regular intervals (at least once per work item, and at every green sub-milestone) so the morning shows one branch with many commits. **Never push**; the owner pushes and opens the PR. No attribution lines in commit messages.
2. **Ruby:** approved. Install Ruby ≥ 3.2 by whatever works (asdf `asdf install ruby 3.3.x`; if compiling fails, Homebrew `brew install ruby` and use its path), then build `rails_app`.
3. **Go unit tests: delete them all.** Every unit of behavior must be testable black-box, end to end included. Before deleting a package's `_test.go` files, make sure every behavior they check has a conformance case (W3b). After W4 the repo has exactly one test suite: `conformance/`.
4. **Tunnels only** (no Cloudflare Workers): cloudflared, and **ngrok** as a second tunnel provider. ngrok needs an authtoken; its cases `requires: ["ngrok config check"]` and skip when it isn't configured. Never ask for or write an authtoken.

---

## Architecture shift (for Phase 1 onwards)

Restarting one process, `up -d`, `wait`, sending stdin and `exec` all need the same thing: **the running stack must accept commands from other terminals.** Today the only channel is "send SIGTERM to the stackstart PID".

- **Control socket.** The running stack listens on a Unix socket in its session directory. Every CLI command becomes a short client call. The MCP server is another client.
- **`up -d`.** The same program started in the background, with its console output going to a file in the run directory.
- **Event log.** Each run gets an append-only `events.jsonl` with a sequence number: state changes, readiness, captures, restarts, rebuilds, screen clears, `fail_on` matches, exits. `wait`, `events`, the TUI and the MCP server all read from it.
- **Run directories.** Each `up` writes to its own `runs/<run-id>/` directory, kept after exit (done in Phase 0).

This reverses two v1 non-goals in the PRD: "not a daemon" and "no auto-restart". The evidence below shows agents need both.

## Phase 0 — make stackstart see and report correctly (done)

| # | Problem (reproduced against v0.2.3) | Fix |
| --- | --- | --- |
| 0.1 | One output line over 64 KB stops all output reading. The process then blocks writing to its terminal and hangs, while `status` still says `ready`. | Read output with no line-length limit; split very long lines into chunks; never stop reading. |
| 0.2 | A prompt without a newline (`iex(1)>`) is never seen, so it can't be used as a ready signal. | When output pauses with an unfinished line, flush it as a line (after 250 ms) so readiness, captures and logs see it. |
| 0.3 | Readiness is only re-checked when a new line arrives. A `tcp:` check on a process that goes quiet after opening its port never passes. | Re-evaluate readiness every 100 ms, not only on output. |
| 0.4 | Readiness timeouts only fire after the process stops producing output. A process that keeps logging, or goes silent, waits forever in `starting`. | The timeout fires on time, whatever the process is doing. Ready/failed is decided exactly once. The cause names the checks still pending. |
| 0.5 | A rebuild that clears the screen (`ESC[2J ESC[3J`) erases the whole terminal, including every other process's output and the scrollback. | Remove screen-control sequences from the console and keep colors. Each clear becomes a `[stackstart] screen cleared` line. `\r` progress updates collapse to the final text. |
| 0.6 | Log files contain raw escape codes, and readiness/capture regexes run against them. | `<p>.log` is plain text; readiness and captures match against it. `<p>.raw.log` keeps the exact bytes. |
| 0.7 | `up` truncates every log, exit deletes the whole log folder, and `logs` starts at end of file. No history is ever readable. | One directory per run, kept after exit; the last 10 runs are kept. `stackstart clean` removes finished runs. `logs` on a stopped stack prints the last run's log and exits; on a live one it shows the last 10 lines, then follows. The failure message prints the log path. |
| 0.8 | The session record lists `pid: 0` for every process. | Real PIDs, updated whenever a process starts. |
| 0.9 | Sessions are matched by folder prefix, so a git worktree inside the main checkout (`.worktrees/x`) finds the main checkout's stack. | Don't match from inside a nested git repo or worktree. Every `down`/`logs` message names the project path. |
| 0.10 | No way to fail fast on a known error line. A failed cloudflared tunnel waits out the whole timeout or, worse, is marked ready. | `fail_on: [regex]`. During startup a match fails the process immediately; after ready it is a warning. |
| 0.11 | Readiness and capture need two copies of nearly the same regex. The README's cloudflared example matched `https://api.trycloudflare.com` from cloudflared's error line, so a failed tunnel was marked ready with that URL. | `capture: <name>` readiness check. README example fixed. |
| 0.12 | A failed-but-running process is left alive at shutdown. A second Ctrl-C kills nothing. Closing the terminal (SIGHUP) orphans the stack. `down` returns before the stack has stopped. | Teardown stops everything alive. Second Ctrl-C SIGKILLs every group. SIGHUP stops the stack. `down` waits (`--no-wait`, `--timeout`). |
| 0.13 | `psql` in a oneshot opened `less` and waited at `(END)` forever. Same for `git log`, `mysql`, `man`. | `PAGER`, `GIT_PAGER`, `PSQL_PAGER`, `MANPAGER` default to `cat`; projects can override. |
| 0.14 | Children that leave the process group survive `down` (every BEAM port program, e.g. Phoenix watchers). | Snapshot descendants by parent PID before signalling; stop them with the group (`internal/proctree`). Verified on a real Phoenix app 2026-10-10. |
| 0.15 | A profile made only of oneshots never exits. | `up` exits 0 once every process has finished. Verified with phoenix_app's `setup`. |
| 0.16 | The e2e harness wrote into the developer's real state directory, and one hung `up` froze the suite. | Isolated state per run and a timeout on every command (carried into the runner design). |

Phase 0 is tested by the conformance suite; [`conformance/MIGRATION.md`](conformance/MIGRATION.md) maps each fix to its cases.

## Phase 1 — control plane: operate one process at a time (done 2026-10-11)

Changed 2026-10-10: `status --json` and non-following `logs` moved here from Phase 2, because agents need them first and the black-box suite needs them to observe state without reading internal files.

| # | Feature |
| --- | --- |
| 1.1 | Control socket, event log, and `up -d`. `up -d` returns once the stack is ready, or exits non-zero with the failure as JSON. `--no-wait` returns straight away. |
| 1.2 | `start`, `stop`, `restart <process>`. Restart touches only that process. Dependents are restarted only if a captured value they use changes (`on_dep_restart: restart \| ignore`). |
| 1.3 | More process states: `stopped`, `restarting`, `crashed`, `backoff`, `blocked`, `degraded`, plus restart count and last exit code. |
| 1.4 | Retry policies: `restart: on-failure \| always \| never`, `retries: N`, `backoff: 2s..30s`. Oneshots retry too. |
| 1.5 | `--keep-going`. A failed required process holds its dependents in `blocked` and leaves everything else running. Fix the cause, run `stackstart start <p>`, and dependents continue. |
| 1.6 | `send <process> "recompile\n"` writes to a process's input and prints what it wrote back. `attach <process>` is the interactive version for humans. |
| 1.7 | `status --json` (was 2.1). Per process: state, PID, ports, captures, cwd, uptime, restarts, last exit. Per session: project root, worktree, git branch, whether `HEAD` moved since start, log directory. |
| 1.8 | `logs <p> --lines N --no-follow` (part of 2.2). |

Acceptance for Phase 1 is measured on test projects: in `fullstack-offline`, `restart phoenix` must leave Postgres, the Node API and the tunnel untouched (same PIDs and same captured URL in `status --json`); `bad-migration` with `--keep-going` must keep Postgres up, let the case fix the data and `start migrate`, and then bring Phoenix up.

## Phase 2 — observation: no more pasting logs into chat (done 2026-10-11)

| # | Feature |
| --- | --- |
| 2.2 | The rest of `logs`: `--since 5m --grep X --run prev`. |
| 2.3 | `events [--since <seq>] [--follow] [--json]`: what changed since I last looked. |
| 2.4 | `wait <p> [--for ready\|rebuilt\|event:<name>] [--after <seq>] --timeout`. Blocks, then exits 0, or non-zero with the cause and last lines. |
| 2.5 | Watch signals per process (`signals: {rebuilding: ..., rebuilt: ..., rebuild_failed: ...}`) that turn rebuilds into events, with built-in presets for each test project's framework (lines recorded in the behavior catalogue during W5–W6). Screen clears count as a rebuild hint. |
| 2.6 | Health checks after startup (re-run HTTP/TCP every N seconds) that mark a process `degraded`. |
| 2.7 | Structured errors and stable exit codes on every command (see [Exit codes](#exit-codes)). |

## Phase 3 — execution: never step outside stackstart (done 2026-10-11)

| # | Feature |
| --- | --- |
| 3.1 | `exec <process> -- <cmd>` runs a command with that process's exact env, cwd and captured values. This removes the "source `.envrc` by hand and hit the prod database" failure. |
| 3.2 | `env <process> [--explain]` prints the final env and which layer set each variable (shell, defaults, `.envrc`, `.env`, `env:`, capture). |
| 3.3 | `up --only <p>` (pulls in its dependencies) and `--skip <p> --set <p>.<capture>=<value>`. |
| 3.4 | Build caching for oneshots: `inputs: [package-lock.json, "src/**"]`. If the hash hasn't changed, skip the step (phoenix_app `setup` and node_api builds are the test bed). |
| 3.5 | `http:`, `exec:` and `file:` readiness checks. |
| 3.6 | Daemonising processes (`pg_ctl start`, `docker compose up -d`): design how stackstart tracks a process whose parent exits immediately. |

## Phase 4 — several stacks at once (done 2026-10-11)

| # | Feature |
| --- | --- |
| 4.1 | Port variables (`${port.phoenix}`) allocated per session, or a fixed `port_offset` per worktree. Ports appear in `status --json`. |
| 4.2 | `status --all` across worktrees, and a port-clash check before `up` that names who holds the port. |

## Phase 5 — agent integration (done 2026-10-11)

| # | Feature |
| --- | --- |
| 5.1 | `describe --json` returns the resolved profile as data: processes, the dependency graph, captures, ports, signals, and available commands. |
| 5.2 | A published JSON Schema for `stackstart.yaml`. |
| 5.3 | `stackstart mcp`: an MCP server exposing status, restart, wait, logs, exec, send and events as typed tools. Works with any MCP client. |
| 5.4 | `init --agents` writes a stackstart section into `AGENTS.md`/`CLAUDE.md`: which commands are safe to run, and that env comes from `stackstart exec`, never from sourcing `.envrc`. |

## Phase 6 — adoption (owner decisions 2026-10-11: the TUI is skipped; the licence is left to the owner)

- An open-source licence. LICENSE is proprietary while the Homebrew cask declares `license: "MIT"`.
- A TUI built on the event log.

## Exit codes

To be fixed in 2.7 and kept stable after that. Today every error exits 1.

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | A process failed (readiness, crash, `fail_on`) |
| 2 | Invalid usage or invalid config |
| 3 | No matching session |
| 4 | Ambiguous session (more than one matches) |
| 5 | Timed out waiting |
| 130 | Interrupted |

## Evidence

From one production project (Elixir/Phoenix + Node tool-server + cloudflared/tailscale + Postgres) across roughly 80 agent sessions on v0.2.2:

- **The agent never ran stackstart itself.** `up` holds the terminal, so the agent asked the human to start, restart and stop it.
- **Four Phoenix restarts in an hour,** each one a full stack restart by hand. Fixed by 1.2.
- **Logs pasted into chat** because `logs` only showed new lines and never exited. Fixed by 0.7 and 1.8.
- **Three `up` attempts and about 30 minutes to get a stack running.** A bad migration and then a slow trycloudflare request each tore down the whole stack and threw away a finished tool-server build. Fixed by 1.4, 1.5, 0.10 and 3.4.
- **The agent read captured values with `epmd`, `rpc` and `ps eww`,** because `status` had no captures, ports or PIDs. Fixed by 1.7.
- **An agent ran a script outside stackstart** with `source .envrc`, which points at production, and reached the production database (database permissions stopped the write). Fixed by 3.1, 3.2 and 5.4.
- **Port clashes between worktrees, hand-written `until curl` loops, and `pkill -f` to stop one process.** Fixed by 4.1, 2.4, 3.5 and 1.2.
- **Logs disappear after a rebuild.** That was 0.5 (screen clears) and 0.7 (log deletion), not a config issue.

For comparison, on the same project's docker compose e2e stack the agent restarted Phoenix 15 times on its own with `docker compose up -d --no-deps phoenix` and waited on a log line. Phase 0 plus 1.1, 1.2, 1.7, 1.8 and 2.4 give stackstart the same capabilities.

## Findings log

Things learned by running real tools. Add to this whenever a test project teaches something.

- 2026-10-10 · `bun --watch` clears the screen twice per restart (`ESC[2J ESC[3J ESC[H` ×2). `node --watch` (v24) doesn't clear; it prints `Restarting '<file>'`.
- 2026-10-10 · `psql` pages output through `less` when it sees a terminal; under stackstart it hung at `(END)` (fixed by 0.13).
- 2026-10-10 · The BEAM starts port programs through `erl_child_setup` in new process groups; group signals miss them (fixed by 0.14, verified on phoenix_app's esbuild and tailwind watchers).
- 2026-10-10 · Elixir's Logger truncates messages at 8 KB; `IO.puts` doesn't.
- 2026-10-10 · Docker's port proxy accepts TCP before the container's service listens; the postgres image prints "ready to accept connections" for a temporary init server first. Gate on `listening on IPv4 address "0.0.0.0", port 5432`.
- 2026-10-11 · Correction to the line above: the final `listening on IPv4` line comes ~50 ms *before* `database system is ready to accept connections`, and a query in between fails with `FATAL: the database system is starting up` (seen under parallel load). No single log line is a safe gate; use a retry policy on the dependent (`restart: on-failure`) or an `exec: pg_isready` readiness check (3.5).
- 2026-10-10 · `mix compile` and `mix assets.setup` in parallel corrupt `_build` (`.beam` write errors); chain them.
- 2026-10-10 · `initdb` without `LANG` creates a SQL_ASCII cluster and Ecto refuses to create a UTF-8 database; pass `-E UTF8 --locale=C`.
- 2026-10-11 · `iex -S mix phx.server`: the `iex(1)> ` prompt has no newline, and the watchers (esbuild, tailwind) print right after it, so the terminal line reads `iex(1)> [watch] build finished, …`. A ready regex must be `^iex\(1\)> `, not `iex\(1\)> $`. Agents reading logs should expect REPL prompts glued to the next process output.
- 2026-10-11 · The tailwind watcher prints `Done in <n>ms` on the first build only if it finishes after Phoenix has attached the port; whether a startup `Done in` appears is timing-dependent. Waiting for a rebuild has to mean "a `Done in` after my edit" (the runner's `mark`/`since`; Phase 2's `wait --after`).
- 2026-10-11 · Tailwind/daisyUI sometimes print non-ASCII as Perl-style escapes (`\x{2248} tailwindcss`, `\x{1F33C} daisyUI`) and sometimes as the characters; the raw bytes really contain the escape text. Not a stackstart issue, but log greps for `≈`/`🌼` are unreliable.
- 2026-10-11 · Two processes without dependencies: when one became ready before the other's "starting" event was handled, the other was started a second time (both copies ran, one untracked). Present since v0.x; fixed.
- 2026-10-11 · With direnv installed, `direnv exec <dir> env` returns the whole environment, so applying it as the `.envrc` layer re-applied the shell's `PAGER`. Only the variables the `.envrc` changes may be applied.
- 2026-10-11 · Phoenix on Bandit reports a taken port as `** (EXIT) :eaddrinuse` under `** (Mix) Could not start application`; it never prints "address already in use". `fail_on` patterns for Phoenix must use `eaddrinuse` (or the Mix line).
- 2026-10-11 · npm (and ora-style spinners) draw a frame, then `ESC[1G ESC[0K` to erase it. Treating only `\r` as a line reset left a lone `\` line after every npm command; cursor-to-column-1 and erase-line are now resets too.
- 2026-10-11 · `tsx watch` clears the screen on every restart and prints `change in ./src/server.ts Restarting...`; on SIGTERM it prints `Previous process hasn't exited yet. Force killing...`.
- 2026-10-11 · Vite 8 (React Router 8) prints `(ssr) page reload <file>` and `program reload` for a route edit even with no browser attached, and `vite.config.ts changed, restarting server...` / `server restarted.` for a config edit, without a screen clear. `react-router dev` relaunches itself once at startup (`[restart] Relaunching with --conditions=development`).
- 2026-10-11 · uvicorn `--reload` restarts its worker as a new PID (`Started server process [<pid>]`) under the same reloader process; logs go to stderr.
- 2026-10-11 · Django runserver prints `<abs path> changed, reloading.` then `Watching for file changes with StatReloader`, then the whole startup banner again.
- 2026-10-11 · Rails 8.1's console prompt is `rails-app(dev):001> ` (hyphenated app name, line counter), not `rails_app(dev)>`.
- 2026-10-11 · Rails' `bin/dev` runs `gem install foreman` when missing, but under asdf the new executable has no shim until `asdf reshim`, so `exec foreman` fails with "not found". Putting foreman in the Gemfile and using `bundle exec foreman` avoids it.
- 2026-10-11 · Tailwind's standalone CLI (used by Phoenix and Rails) prints `Done in <n>ms` per build; colors split the line into several SGR runs, so console matching needs colors ignored.
- 2026-10-11 · Phoenix on Linux needs `inotify-tools` for its file watching (code reloader, LiveReload); without it a CSS or .ex edit is never noticed (`[error] inotify-tools is needed to run file_system`). macOS needs nothing. On Linux, uvicorn may not print "Stopping reloader process" when stopped.
- 2026-10-10 · Phoenix 1.8's generator writes an `AGENTS.md`; the repo's root `.gitignore` ignores `AGENTS.md` everywhere, so it is not committed. Harmless, but don't be surprised.

## Current state

As of 2026-10-11, evening. Everything is on branch `agent-native-blackbox` (PR orpic/stack-start#2), pushed.

**Phases 0–5 are implemented and tested; Phase 6 is closed by owner decision** (no TUI; the licence is the owner's call). [`conformance/CONTRACT.md`](conformance/CONTRACT.md) documents every command and behavior; sections 11–15 cover Phases 1–5.

| Phase | What an agent (or a person) can now do |
| --- | --- |
| 0 | See what processes really do: long lines, prompts, screen clears, spinners, logs that survive the run, `fail_on`, `capture:` checks, clean teardown of every descendant. |
| 1 | `up -d`; `start`/`stop`/`restart <process>` (dependents follow, nothing else moves); retries with backoff; `--keep-going`; `send`/`attach`; `status --json`; `logs --lines/--no-follow`; a control socket and an `events.jsonl` per run. |
| 2 | Stable exit codes and `--json` errors; `logs --since/--grep/--run`; `events --after`; `wait --for ready/exit/rebuilt/event:<type>`; rebuild signals with presets for Phoenix, Vite, tsx, Django, uvicorn, Rails, tailwind, esbuild, Next, webpack, tsc, nodemon, node --watch; health checks and the `degraded` state. |
| 3 | `exec <p> -- <cmd>` and `env <p> --explain` (the process's exact environment, layer by layer); `up --only/--skip/--set`; oneshot `inputs` caching; `http`/`exec`/`file` readiness checks; `kind: daemon` (pidfile, log_file, stop_cmd). |
| 4 | Port variables (`ports:` + `${port.x}`) so worktrees run side by side; port-clash warnings naming the holder. |
| 5 | `describe --json`; `schema` (JSON Schema); `stackstart mcp` (MCP server whose tools run the CLI); `init --agents` (prints an AGENTS.md section). |

Verification on 2026-10-11: the full suite on this Mac **132 passed, 0 failed, 0 skipped** (about 2.5 minutes, every test project and real tool including cloudflared and ngrok tunnels); the synthetic suite on Linux arm64 (Docker, non-root) **98 passed, 0 failed, 15 skipped** (tools absent). CI runs lint and the non-project suite on Ubuntu and macOS on every push; the full suite with every toolchain runs only when started by hand.

Phase 1 acceptance criteria, as cases on the real composite stack: `projects/composite/restart-phoenix` (Postgres, API, web and tunnel keep their PIDs and URL) and `projects/composite/bad-migration-keep-going` (fix the data, `start migrate`, Phoenix comes up).

Bugs found while building Phases 1–5 (each has a case): a socket-initiated stop made a later `down --force` count as the first signal; `wait --for event:` without `--after` matched old events; a stack that failed within milliseconds was reported without its FAILED block by `up -d`; `--only/--skip` dropped the profile's `ports`; the `.env` files of two cases were gitignored and missing in CI; Postgres in Docker logs "listening" ~50 ms before it accepts queries (case now retries; `exec: pg_isready` is the robust gate).

Not done / next:

- A TUI (skipped by the owner).
- Windows (out of scope).
- Prefix colors are not covered by tests (the runner has no terminal).
- The `full` CI job (all toolchains) is manual only; it has not been run on GitHub yet.
