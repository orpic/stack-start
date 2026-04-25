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
            - log: "https://[a-z0-9-]+\\.trycloudflare\\.com"
        captures:
          - name: url
            log: "(https://[a-z0-9-]+\\.trycloudflare\\.com)"

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
stackstart logs backend    # tail the backend's log
stackstart down            # gracefully stop everything
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
- Prefer tcp: host:port readiness checks for services on a known port
  (more reliable than log matching through a PTY).
- Use log: "pattern" checks when gating on a specific event (like
  "migrations complete") that happens before the port opens.
- Keep log regexes simple. PTY output may contain ANSI escape codes
  and Unicode characters that break complex patterns.
- For captures (like extracting a cloudflared tunnel URL), the regex
  must have exactly one capturing group.
- Project-level stackstart.yaml (committed to repo): paths are relative
  to the file's directory, no project_path field needed.
- User-level ~/stackstart.yaml (personal/company projects): requires an
  explicit project_path field with the absolute path to the project.
```

## Key features

- **Dependency ordering** - processes start in the right order based on a declared dependency graph. Siblings with no dependency between them start in parallel.
- **Readiness gates** - a process isn't "ready" until it matches a log regex or opens a TCP port. Dependents wait for real readiness, not just PID existence.
- **Runtime value propagation** - capture values from a process's log output (e.g. a tunnel URL) and inject them into dependent processes via `${producer.capture_name}` references in env vars or command args.
- **Named profiles** - define `dev`, `minimal`, `tooling`, etc. in one file. Run the one you need: `stackstart up minimal`.
- **Git-style config lookup** - stackstart walks from your current directory upward looking for `stackstart.yaml`, then checks `~/stackstart.yaml`. The most specific match wins. No manual `--config` flag needed.
- **Cross-shell visibility** - `stackstart status`, `stackstart logs`, and `stackstart down` work from any terminal, not just the one running `up`.
- **Clear failure attribution** - when something fails, you get one root-cause line naming the process and the check that failed.
- **Graceful shutdown** - Ctrl-C tears down all processes in reverse dependency order. A second Ctrl-C force-kills everything immediately.
- **Environment composition** - layer env vars from `.envrc` (via direnv), `.env` files, per-process `env:` blocks, and captured runtime values, with clear precedence rules.
- **Share-friendly** - no absolute paths inside process definitions. Commit `stackstart.yaml` to git; it works on every teammate's machine.

## Commands

```text
stackstart up <profile>       Start the named profile
stackstart down                Stop the running stack (from any shell)
stackstart status              Show running sessions and process states
stackstart logs <process>      Tail a process's log (from any shell)
stackstart list                List profiles available from current directory
stackstart validate <profile>  Check config without running anything
stackstart init                Scaffold a starter stackstart.yaml
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
