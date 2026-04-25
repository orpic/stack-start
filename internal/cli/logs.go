package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/tail"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <process>",
		Short: "Tail the per-process log of a running session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			processName := args[0]
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
				fmt.Fprintln(os.Stderr, "Multiple sessions match. Use --profile to disambiguate:")
				for _, r := range records {
					fmt.Fprintf(os.Stderr, "  profile=%s  project=%s\n", r.Profile, r.ProjectPath)
				}
				return fmt.Errorf("ambiguous session; specify --profile <name>")
			}

			rec := records[0]
			logFile := ""
			for _, p := range rec.Processes {
				if p.Name == processName {
					logFile = p.LogFile
					break
				}
			}
			if logFile == "" {
				return fmt.Errorf("process %q not found in session %q", processName, rec.Profile)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				cancel()
			}()

			return tail.Follow(ctx, logFile, os.Stdout)
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	return cmd
}
