package cli

import (
	"fmt"
	"os"

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
		newEnvCmd(),
		newExecCmd(),
		newDescribeCmd(),
		newSchemaCmd(),
		newMCPCmd(),
	)

	return root
}

func Execute() {
	root := NewRootCmd()
	root.PersistentPreRun = func(*cobra.Command, []string) { ran = true }
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return codedError{exitUsage, "usage", fmt.Errorf("%w\nSee '%s --help'", err, c.CommandPath())}
	})
	if cmd, err := root.ExecuteC(); err != nil {
		os.Exit(report(cmd, err))
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
