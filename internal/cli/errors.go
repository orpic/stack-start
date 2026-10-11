package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/orchestrator"
)

// Exit codes (ROADMAP.md, "Exit codes"). Stable: scripts and agents rely
// on them.
const (
	exitOK          = 0
	exitFailed      = 1 // a process failed, or anything not listed below
	exitUsage       = 2 // invalid usage or invalid config
	exitNoSession   = 3 // no matching session
	exitAmbiguous   = 4 // more than one session matches
	exitTimeout     = 5 // timed out waiting
	exitInterrupted = 130
)

// codedError carries an exit code and a machine-readable error code.
type codedError struct {
	exit int
	code string
	err  error
}

func (e codedError) Error() string { return e.err.Error() }
func (e codedError) Unwrap() error { return e.err }

func usageError(err error) error {
	if err == nil {
		return nil
	}
	return codedError{exitUsage, "invalid_config", err}
}

func noSessionError(err error) error { return codedError{exitNoSession, "no_session", err} }
func ambiguousError(err error) error { return codedError{exitAmbiguous, "ambiguous_session", err} }
func timeoutError(msg string) error  { return codedError{exitTimeout, "timeout", errors.New(msg)} }
func failedError(err error) error    { return codedError{exitFailed, "process_failed", err} }
func controlError(ce *control.Error) error {
	exit, code := exitFailed, ce.Code
	switch ce.Code {
	case control.CodeTimeout:
		exit = exitTimeout
	case control.CodeNotFound, control.CodeInvalid:
		exit = exitUsage
	}
	return codedError{exit, code, describeError(ce)}
}

// errorJSON is what --json commands print on stdout when they fail.
type errorJSON struct {
	OK    bool           `json:"ok"`
	Error *control.Error `json:"error"`
}

// report prints a failed command's error and returns its exit code.
func report(cmd *cobra.Command, err error) int {
	if errors.Is(err, orchestrator.ErrForceKilled) {
		return exitInterrupted
	}
	exit, code := exitFailed, "error"
	var ce codedError
	var ee exitError
	var ctl *control.Error
	switch {
	case errors.As(err, &ee):
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, ee.msg)
		}
		return ee.code
	case errors.As(err, &ce):
		exit, code = ce.exit, ce.code
	case errors.As(err, &ctl):
		return report(cmd, controlError(ctl))
	case cmd == nil || !ran:
		// Cobra rejected the arguments or flags before the command ran.
		exit, code = exitUsage, "usage"
	}
	if cmd != nil && jsonRequested(cmd) {
		_ = json.NewEncoder(os.Stdout).Encode(errorJSON{Error: &control.Error{Code: code, Message: err.Error()}})
	}
	fmt.Fprintln(os.Stderr, err)
	return exit
}

func jsonRequested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("json")
	return f != nil && f.Value.String() == "true"
}

// ran is set once a command's own code starts, so errors before that are
// usage errors.
var ran bool
