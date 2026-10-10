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
process has stopped, so it is safe to run 'stackstart up' straight after.

Running down again while the stack is stopping just waits again.
--force kills every process at once instead of waiting for grace periods.`,
		Example: `  stackstart down
  stackstart down --profile dev
  stackstart down --no-wait
  stackstart down --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			profileFlag, _ := cmd.Flags().GetString("profile")
			noWait, _ := cmd.Flags().GetBool("no-wait")
			force, _ := cmd.Flags().GetBool("force")
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
			pid := rec.StackstartPID
			// A stack that is already stopping must not get another SIGTERM
			// unless the caller wants it killed: stackstart treats a second
			// signal as "kill everything now".
			if !rec.Stopping {
				if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
					return fmt.Errorf("failed to signal stackstart (PID %d): %w", pid, err)
				}
			}
			if force {
				if !rec.Stopping {
					waitUntilStopping(cwd, rec.Profile, pid, 2*time.Second)
				}
				if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && session.IsAlive(pid) {
					return fmt.Errorf("failed to signal stackstart (PID %d): %w", pid, err)
				}
			}

			if noWait {
				fmt.Printf("Stopping %q in %s (PID %d).\n", rec.Profile, rec.ProjectPath, pid)
				return nil
			}

			fmt.Fprintf(os.Stderr, "Stopping %q in %s...\n", rec.Profile, rec.ProjectPath)
			deadline := time.Now().Add(timeout)
			for session.IsAlive(pid) {
				if time.Now().After(deadline) {
					return fmt.Errorf("stack %q is still stopping after %s (stackstart PID %d); "+
						"run 'stackstart down' again to keep waiting, or 'stackstart down --force' to kill it now",
						rec.Profile, timeout, pid)
				}
				time.Sleep(100 * time.Millisecond)
			}
			verb := "Stopped"
			if force {
				verb = "Killed"
			}
			fmt.Printf("%s %q in %s. Logs: %s\n", verb, rec.Profile, rec.ProjectPath, rec.LogDir)
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Bool("no-wait", false, "return as soon as the stop signal is sent")
	cmd.Flags().Bool("force", false, "kill every process now instead of waiting for grace periods")
	cmd.Flags().Duration("timeout", 2*time.Minute, "how long to wait for the stack to stop")
	return cmd
}

// waitUntilStopping waits until the session record says the stack has
// started shutting down, so a follow-up signal is seen as a second one.
func waitUntilStopping(cwd, profile string, pid int, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) && session.IsAlive(pid) {
		recs, err := session.FindMatchingSessions(cwd, profile)
		if err != nil || len(recs) == 0 {
			return
		}
		for _, r := range recs {
			if r.StackstartPID == pid && r.Stopping {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}
