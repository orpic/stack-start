package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/orpic/stack-start/internal/session"
	"github.com/spf13/cobra"
)

func newDownCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Gracefully stop the running session matching the current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			profileFlag, _ := cmd.Flags().GetString("profile")
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}

			records, err := session.FindMatchingSessions(cwd, profileFlag)
			if err != nil {
				return err
			}

			if len(records) == 0 {
				return fmt.Errorf("no running stackstart session found for this directory")
			}
			if len(records) > 1 {
				fmt.Fprintln(os.Stderr, "Multiple sessions match this directory. Use --profile to disambiguate:")
				for _, r := range records {
					fmt.Fprintf(os.Stderr, "  profile=%s  project=%s\n", r.Profile, r.ProjectPath)
				}
				return fmt.Errorf("ambiguous session; specify --profile <name>")
			}

			rec := records[0]
			if err := syscall.Kill(rec.StackstartPID, syscall.SIGTERM); err != nil {
				return fmt.Errorf("failed to send SIGTERM to stackstart (PID %d): %w", rec.StackstartPID, err)
			}

			fmt.Printf("Sent SIGTERM to stackstart session %q (PID %d). Shutting down...\n",
				rec.Profile, rec.StackstartPID)
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	return cmd
}
