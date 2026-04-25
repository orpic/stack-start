package cli

import (
	"fmt"
	"os"

	"github.com/orpic/stack-start/internal/config"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <profile>",
		Short: "Parse and validate the named profile without running anything",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileName := args[0]
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot determine home directory: %w", err)
			}

			profile, filePath, err := config.Resolve(cwd, profileName, home, cfgFile)
			if err != nil {
				return err
			}

			if err := config.Validate(profile, profileName); err != nil {
				return err
			}

			fmt.Printf("Profile %q from %s is valid.\n", profileName, filePath)
			return nil
		},
	}
}
