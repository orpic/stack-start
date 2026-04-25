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

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold a starter stackstart.yaml in the current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
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
}
