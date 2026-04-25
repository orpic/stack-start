package cli

import (
	"fmt"
	"os"

	"github.com/orpic/stack-start/internal/config"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all profiles available from the current directory context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot determine home directory: %w", err)
			}

			profiles, err := config.ListProfiles(cwd, home, cfgFile)
			if err != nil {
				return err
			}

			if len(profiles) == 0 {
				fmt.Println("No profiles found for the current directory context.")
				fmt.Println("Run 'stackstart init' to create a stackstart.yaml.")
				return nil
			}

			fmt.Println("Available profiles:")
			for _, p := range profiles {
				fmt.Printf("  %-20s  (from %s)\n", p.Name, p.FilePath)
			}
			return nil
		},
	}
}
