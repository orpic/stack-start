package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/orpic/stack-start/internal/orchestrator"

	"github.com/orpic/stack-start/internal/version"
	"github.com/spf13/cobra"
)

var (
	cfgFile   string
	logFormat string
	quiet     bool
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "stackstart",
		Short: "Runtime-aware local dev orchestrator",
		Long: `Stackstart starts and coordinates multi-service local environments
with deterministic execution, readiness-based dependencies, and
runtime value propagation between processes.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "path to stackstart.yaml (overrides cascade lookup)")
	root.PersistentFlags().StringVar(&logFormat, "log-format", "text", "diagnostic log format: text or json")
	root.PersistentFlags().BoolVar(&quiet, "quiet", false, "suppress stackstart's own diagnostic output")

	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newValidateCmd(),
		newListCmd(),
		newUpCmd(),
		newDownCmd(),
		newLogsCmd(),
		newStatusCmd(),
		newCleanCmd(),
		newStartCmd(),
		newStopCmd(),
		newRestartCmd(),
		newSendCmd(),
		newAttachCmd(),
		newEventsCmd(),
		newWaitCmd(),
	)

	return root
}

func Execute() {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		if errors.Is(err, orchestrator.ErrForceKilled) {
			os.Exit(130)
		}
		var ee exitError
		if errors.As(err, &ee) {
			if msg := ee.Error(); msg != "" {
				fmt.Fprintln(os.Stderr, msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print stackstart version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("stackstart %s (commit: %s, built: %s)\n",
				version.Version, version.Commit, version.Date)
		},
	}
}

// exitError makes a command exit with a specific code. An empty message
// prints nothing (the command already reported the problem).
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }
