package cli

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/orpic/stack-start/internal/session"
	"github.com/spf13/cobra"
)

func newDownCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop the running stack for this directory and wait until it has stopped",
		Long: `Stop the running stack for this directory, from any terminal.

Processes are stopped in reverse dependency order (SIGTERM, then SIGKILL
after each process's stop_grace_period). By default down waits until every
process has stopped, so it is safe to run 'stackstart up' straight after.`,
		Example: `  stackstart down
  stackstart down --profile dev
  stackstart down --no-wait`,
		RunE: func(cmd *cobra.Command, args []string) error {
			profileFlag, _ := cmd.Flags().GetString("profile")
			noWait, _ := cmd.Flags().GetBool("no-wait")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}

			records, err := session.FindMatchingSessions(cwd, profileFlag)
			if err != nil {
				return err
			}
			if len(records) == 0 {
				return fmt.Errorf("no running stackstart session found for %s", cwd)
			}
			if len(records) > 1 {
				return ambiguousSessions(records)
			}

			rec := records[0]
			if err := syscall.Kill(rec.StackstartPID, syscall.SIGTERM); err != nil {
				return fmt.Errorf("failed to signal stackstart (PID %d): %w", rec.StackstartPID, err)
			}

			if noWait {
				fmt.Printf("Stopping %q in %s (PID %d).\n", rec.Profile, rec.ProjectPath, rec.StackstartPID)
				return nil
			}

			fmt.Fprintf(os.Stderr, "Stopping %q in %s...\n", rec.Profile, rec.ProjectPath)
			deadline := time.Now().Add(timeout)
			for session.IsAlive(rec.StackstartPID) {
				if time.Now().After(deadline) {
					return fmt.Errorf("stack %q is still stopping after %s (stackstart PID %d); "+
						"run 'stackstart down' again to wait longer, or 'kill -9 %d' to force it",
						rec.Profile, timeout, rec.StackstartPID, rec.StackstartPID)
				}
				time.Sleep(100 * time.Millisecond)
			}
			fmt.Printf("Stopped %q in %s. Logs: %s\n", rec.Profile, rec.ProjectPath, rec.LogDir)
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Bool("no-wait", false, "return as soon as the stop signal is sent")
	cmd.Flags().Duration("timeout", 2*time.Minute, "how long to wait for the stack to stop")
	return cmd
}
