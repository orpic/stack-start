package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/orchestrator"
	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up <profile>",
		Short: "Resolve, validate, and run the named profile",
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

			projectPath := profile.ResolvedProjectPath
			if projectPath == "" {
				return fmt.Errorf("profile %q has no resolved project path", profileName)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			orc := orchestrator.New(orchestrator.Config{
				Profile:     profile,
				ProfileName: profileName,
				ProjectPath: projectPath,
				ConfigFile:  filePath,
				LogFormat:   logFormat,
				Quiet:       quiet,
			})

			// Children run in their own sessions, so signals sent to the
			// terminal (Ctrl-C, closing the window) reach only stackstart.
			// It must pass them on, or the stack is orphaned.
			sigCh := make(chan os.Signal, 2)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

			go func() {
				<-sigCh
				if !quiet {
					fmt.Fprintln(os.Stderr, "\nstackstart: stopping all processes (Ctrl-C again to kill immediately)")
				}
				cancel()
				<-sigCh
				orc.ForceKill()
				os.Exit(130)
			}()

			return orc.Run(ctx)
		},
	}
}
