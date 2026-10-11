<!-- Stackstart - https://github.com/orpic/stack-start -->
<!-- Copyright (c) 2026 Shobhit. All rights reserved. See LICENSE. -->

# Stackstart

**Runtime-aware local dev orchestrator.** Start your entire multi-service stack with one command - with dependency ordering, readiness gates, and runtime value propagation between processes.

Stackstart reads a `stackstart.yaml` in your project, starts each process in the right order, waits for each to be ready (by matching a log line or checking a TCP port), captures runtime values from logs (like a tunnel URL), injects them into dependent processes, and shows you everything in one terminal with interleaved colored output. If anything fails, you get one clear root-cause line - not a wall of text.

---

## The problem

You run 4+ local services for development. Today that means:

1. Open a terminal, `cd packages/db`, start Postgres, eyeball the logs until it says "ready".
2. New tab, start `cloudflared`, watch for the tunnel URL, copy it by hand.
3. New tab, paste the URL into an env var, start the backend, wait for "listening on port 4000".
4. New tab, start the frontend.
5. Hope nothing crashed silently in step 1.

With stackstart:

```bash
stackstart up dev
```

That's it. Dependencies are honored. Readiness is machine-checked. The tunnel URL is captured from logs and injected into the backend's environment automatically. Failures are attributed instantly.

## Installation

### Homebrew (macOS / Linux)

```bash
brew tap orpic/tap
brew install stackstart
```

> **macOS note:** If macOS blocks the binary with "cannot verify developer", run:
> ```bash
> xattr -d com.apple.quarantine $(which stackstart)
> ```
> This is a one-time step required for unsigned open source binaries on macOS. Installing from source (`go install`) does not need this.

### From GitHub Releases

Download the binary for your platform from the [releases page](https://github.com/orpic/stack-start/releases), extract it, and put it on your `PATH`. On macOS, run the `xattr` command above after placing the binary.

### From source

Requires Go 1.25+:

```bash
go install github.com/orpic/stack-start/cmd/stackstart@latest
```

## Quickstart

### 1. Create a `stackstart.yaml` in your project root

```bash
stackstart init
```

Or write one by hand:

```yaml
profiles:
  dev:
    processes:
      postgres:
        cwd: packages/db
        cmd: docker compose up postgres
        readiness:
          timeout: 30s
          checks:
            - tcp: localhost:5432

      cloudflared:
        cwd: packages/tunnel
        cmd: cloudflared tunnel --url http://localhost:4000
        readiness:
          timeout: 20s
          checks:
            - capture: url          # ready once the URL below is captured
        captures:
          - name: url
            # Real quick-tunnel hosts are several dash-separated words. This
            # does not match https://api.trycloudflare.com, which appears in
            # cloudflared's own error lines.
            log: "(https://[a-z0-9]+(?:-[a-z0-9]+)+\\.trycloudflare\\.com)"
        fail_on:
          - "failed to request quick Tunnel"   # fail now, not at the timeout

      backend:
        cwd: packages/backend
        cmd: npm run dev
        depends_on: [postgres, cloudflared]
        env:
          TUNNEL_URL: "${cloudflared.url}"
        readiness:
          timeout: 60s
          checks:
            - log: "listening on port 4000"

      web:
        cwd: packages/web
        cmd: npm run dev
        depends_on: [backend]
        readiness:
          timeout: 30s
          checks:
            - tcp: localhost:3000
```

### 2. Start the stack

```bash
stackstart up dev
```

You'll see interleaved colored output from all processes, prefixed by name:

```text
postgres    | LOG:  database system is ready to accept connections
cloudflared | INF +-----------+
cloudflared | INF | Your tunnel: https://random-words.trycloudflare.com |
backend     | listening on port 4000
web         | VITE v5.0.0  ready in 423 ms
```

### 3. From another terminal

```bash
stackstart status          # see what's running
stackstart logs backend    # recent lines, then follow (or the last run's full log if stopped)
stackstart down            # stop everything and wait until it has stopped
stackstart down --force    # kill everything now instead of waiting for grace periods
```

Every run keeps its logs, even after a crash. When something fails, the error ends with the log path and the command to read it:

```text
--- FAILED: backend ---
Cause: process backend exited before becoming ready: exit status 1
Last output:
  ** (Postgrex.Error) column "owner_ref" contains null values
Full log: ~/Library/Application Support/stackstart/sessions/<project>__dev/runs/20261010-221157-123/backend.log
Read it later with: stackstart logs backend
```

### 4. Commit `stackstart.yaml` to git

The file is fully portable - every path inside it is relative to the project root. Any teammate cloning the repo can run `stackstart up dev` and get the same stack, no setup docs required.

## Set up with AI

Any AI coding agent (Cursor, Claude Code, Codex, Copilot, etc.) can generate a `stackstart.yaml` for your project. Copy-paste this prompt to your agent:

```text
Explore my project structure and help me set up stackstart. Read the
stackstart README and TECH.md at https://github.com/orpic/stack-start
for the YAML schema and reference syntax. Identify all the services I
run locally, figure out their start commands, working directories,
dependency order, and readiness signals. Ask me what log lines or ports
indicate each service is ready, and whether any runtime values (like
tunnel URLs) need to flow between services. Then generate a stackstart.yaml.

Tips for generating stackstart configs:
- Prefer tcp: host:port readiness checks for services on a known port.
- Use log: "pattern" checks when gating on a specific event (like
  "migrations complete") that happens before the port opens. Patterns
  match the visible text: colors and terminal codes are removed first.
- Behind Docker's port proxy a tcp check passes before the service
  listens; gate on the service's own "listening" log line instead.
- For captures (like extracting a cloudflared tunnel URL), the regex
  must have exactly one capturing group. Use "- capture: <name>" as the
  readiness check instead of repeating the regex.
- Add fail_on: [patterns] for known fatal lines, so startup fails at
  once instead of at the timeout.
- Use kind: oneshot for steps that run and exit (migrations, builds).
- Project-level stackstart.yaml (committed to repo): paths are relative
  to the file's directory, no project_path field needed.
- User-level ~/stackstart.yaml (personal/company projects): requires an
  explicit project_path field with the absolute path to the project.
```

## Key features

- **Dependency ordering** - processes start in the right order based on a declared dependency graph. Siblings with no dependency between them start in parallel.
- **Readiness gates** - a process isn't "ready" until it matches a log regex, opens a TCP port, or produces a captured value. Prompts without a newline (`iex(1)>`) count. Dependents wait for real readiness, not just PID existence, and every readiness timeout fires on time.
- **Fail fast** - `fail_on:` patterns fail a process the moment a known fatal line appears.
- **Runtime value propagation** - capture values from a process's log output (e.g. a tunnel URL) and inject them into dependent processes via `${producer.capture_name}` references in env vars or command args.
- **Named profiles** - define `dev`, `minimal`, `tooling`, etc. in one file. Run the one you need: `stackstart up minimal`.
- **Git-style config lookup** - stackstart walks from your current directory upward looking for `stackstart.yaml`, then checks `~/stackstart.yaml`. The most specific match wins. No manual `--config` flag needed.
- **Cross-shell visibility** - `stackstart status`, `stackstart logs`, and `stackstart down` work from any terminal, not just the one running `up`.
- **Clear failure attribution** - when something fails, you get one root-cause line naming the process and the check that failed.
- **Graceful shutdown** - Ctrl-C, `stackstart down`, or closing the terminal tears down all processes in reverse dependency order, including children that left the process group (BEAM port programs, `setsid`). A second Ctrl-C force-kills everything immediately.
- **Readable, persistent logs** - one shared console where rebuilds that clear the screen (tsc, vite, bun, nodemon) show a `[stackstart] screen cleared` marker instead of wiping everyone's output. Each run's logs are kept as plain text plus a raw copy, and the last 10 runs are kept.
- **Environment composition** - layer env vars from `.envrc` (via direnv), `.env` files, per-process `env:` blocks, and captured runtime values, with clear precedence rules.
- **Share-friendly** - no absolute paths inside process definitions. Commit `stackstart.yaml` to git; it works on every teammate's machine.

## Commands

```text
stackstart up <profile>          Start a profile in the foreground (Ctrl-C stops it)
stackstart up <profile> -d       Start it in the background; returns once ready (--keep-going, --no-wait)
stackstart status [--json]       Running stacks: every process's state, PID, ports, captured values
stackstart logs <process>        A process's log (follows on a terminal; --lines N, --no-follow)
stackstart restart <process>     Restart one process (and what depends on it); nothing else moves
stackstart stop|start <process>  Stop or start one process in the running stack
stackstart wait [process]        Block until ready, exited, rebuilt or an event happened (--timeout)
stackstart events                What happened: starts, readiness, captures, restarts, exits (--after N)
stackstart send <process> <text> Type into a REPL or prompt running in the stack, print its answer
stackstart attach <process>      Use a running REPL interactively (Ctrl-] detaches)
stackstart down [--force]        Stop the stack and wait until it has stopped (from any shell)
stackstart clean                 Delete the logs of finished runs (--all, --dry-run)
stackstart list                  List profiles available from the current directory
stackstart validate <profile>    Check config without running anything
stackstart init                  Scaffold a starter stackstart.yaml
```

An agent can run a whole stack without holding a terminal:

```bash
stackstart up dev -d                          # exit 0 once everything is ready, 1 with the failure
stackstart status --json                      # PIDs, states, captured URLs, ports, last exits
stackstart restart phoenix                    # only phoenix (and what depends on it) restarts
stackstart logs phoenix --lines 50            # prints and exits when not on a terminal
stackstart wait api --for event:process_ready --after 42 --timeout 60s
stackstart down
```

### With agents (Claude Code, Codex, Cursor, …)

```bash
stackstart init --agents >> AGENTS.md          # tells agents to use stackstart (prints; edits nothing)
claude mcp add stackstart -- stackstart mcp    # or {"command": "stackstart", "args": ["mcp"]} in any MCP client
stackstart describe dev --json                 # the profile as data: processes, start order, checks
stackstart schema > stackstart.schema.json     # JSON Schema for editors and validators
```

The MCP server's tools (status, up, down, restart, start, stop, logs, wait, events, exec, env, send, describe, list_profiles) run the same CLI, so they behave identically.

## Testing

Stackstart is tested black-box: [`conformance/`](conformance/) holds data-driven cases that run the binary and check only what a user or agent can see (exit codes, output, processes, ports, HTTP). Real apps in [`test-projects/`](test-projects/) (Phoenix, Django, FastAPI, Node, React Router, Rails, and a composite stack with cloudflared or ngrok tunnels) are the main test bed.

```bash
go build -o tmp/stackstart ./cmd/stackstart
(cd conformance/runner && go build -o ../../tmp/conformance .)
tmp/conformance --bin tmp/stackstart
```

## Configuration reference

See the full YAML schema, profile lookup algorithm, reference syntax, and environment composition rules in [TECH.md](TECH.md).

## Requirements

- **macOS** (arm64, amd64) or **Linux** (amd64, arm64). Windows is not supported in v1.
- **No runtime dependencies.** Single static binary.
- **Optional:** [direnv](https://direnv.net/) - required only if your project uses `.envrc` files.

## Project status

Stackstart is in active development. Current version: **v0.x** (pre-stable). The API and config format may change between minor versions. See [PRD.md](PRD.md) for the full v1 scope and [TECH.md](TECH.md) for the implementation design.

## License

Proprietary. See [LICENSE](LICENSE) for details. Personal, non-commercial use only.
