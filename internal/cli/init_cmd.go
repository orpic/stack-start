package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const scaffoldYAML = `# stackstart.yaml - local dev orchestrator config
# Docs: https://github.com/orpic/stack-start

profiles:
  dev:
    processes:
      # Example: a database
      # postgres:
      #   cwd: packages/db
      #   cmd: docker compose up postgres
      #   readiness:
      #     timeout: 30s
      #     checks:
      #       - tcp: localhost:5432

      # Example: a backend that depends on the database
      # backend:
      #   cwd: packages/backend
      #   cmd: npm run dev
      #   depends_on: [postgres]
      #   readiness:
      #     timeout: 60s
      #     checks:
      #       - log: "listening on port"

      hello:
        cwd: .
        cmd: echo "Hello from stackstart!"
`

// agentsSnippet is printed by init --agents for AGENTS.md / CLAUDE.md.
const agentsSnippet = `## Local stack (stackstart)

This project's local stack is run with stackstart (see stackstart.yaml).
Use it instead of starting servers by hand; nothing needs a terminal kept open.

- Start: ` + "`stackstart up <profile> -d`" + ` (exit 0 once every process is ready; exit 1
  prints the failing process's cause and last output). ` + "`stackstart list`" + ` shows profiles.
- Look: ` + "`stackstart status --json`" + ` (states, PIDs, ports, captured URLs, restarts),
  ` + "`stackstart logs <process> --lines 100`" + `, ` + "`stackstart events --after <seq>`" + `.
- Change: ` + "`stackstart restart <process>`" + ` restarts it and what depends on it,
  nothing else; ` + "`stackstart stop|start <process>`" + `.
- After editing a file: take events.last_seq from ` + "`status --json`" + ` before the edit,
  then ` + "`stackstart wait <process> --for rebuilt --after <seq> --timeout 60s`" + `.
- Run commands with a process's environment (database URLs, tunnel URLs):
  ` + "`stackstart exec <process> -- <command>`" + `. Never source .envrc by hand; it may
  point at production.
- Type into a REPL in the stack: ` + "`stackstart send <process> \"<text>\"`" + `.
- Stop: ` + "`stackstart down`" + `.
- Exit codes: 0 ok, 1 a process failed, 2 bad usage/config, 3 no stack here,
  4 several stacks (pass --profile), 5 timed out.
- MCP clients can use ` + "`stackstart mcp`" + ` for the same as tools.
`

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a starter stackstart.yaml in the current directory (--agents: print an AGENTS.md section)",
		Long: `Write a starter stackstart.yaml in the current directory.

--agents prints a section for AGENTS.md or CLAUDE.md that tells coding
agents how to run the stack with stackstart. It never edits files: paste
it where your agents read instructions.`,
		Example: `  stackstart init
  stackstart init --agents >> AGENTS.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agents, _ := cmd.Flags().GetBool("agents"); agents {
				fmt.Print(agentsSnippet)
				return nil
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}
			path := filepath.Join(cwd, "stackstart.yaml")
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("stackstart.yaml already exists in %s", cwd)
			}
			if err := os.WriteFile(path, []byte(scaffoldYAML), 0644); err != nil {
				return fmt.Errorf("failed to write stackstart.yaml: %w", err)
			}
			fmt.Printf("Created %s\n", path)
			return nil
		},
	}
	cmd.Flags().Bool("agents", false, "print a stackstart section for AGENTS.md / CLAUDE.md instead")
	return cmd
}
