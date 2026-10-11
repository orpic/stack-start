package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/events"
	"github.com/orpic/stack-start/internal/orchestrator"
	"github.com/orpic/stack-start/internal/session"
)

func newUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up <profile>",
		Short: "Start the named profile (in the foreground, or in the background with -d)",
		Long: `Start every process of a profile in dependency order.

In the foreground (default) the output of every process is shown, and
Ctrl-C stops the stack (twice: kill it now).

With -d the stack runs in the background, independent of this terminal:
up waits until every process is ready (or one fails) and exits 0 (or 1
with the failure). --no-wait returns as soon as it has started. Then use
status, logs, restart, wait and down from any terminal.

--keep-going keeps the stack up when a required process fails: what
depends on it is blocked, everything else keeps running; fix the cause and
run 'stackstart start <process>'.`,
		Example: `  stackstart up dev
  stackstart up dev -d
  stackstart up dev -d --keep-going --timeout 5m
  stackstart up dev -d --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileName := args[0]
			detach, _ := cmd.Flags().GetBool("detach")
			noWait, _ := cmd.Flags().GetBool("no-wait")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			keepGoing, _ := cmd.Flags().GetBool("keep-going")
			child, _ := cmd.Flags().GetBool("detached-child")
			asJSON, _ := cmd.Flags().GetBool("json")
			only, _ := cmd.Flags().GetStringSlice("only")
			skip, _ := cmd.Flags().GetStringSlice("skip")
			set, _ := cmd.Flags().GetStringArray("set")
			noCache, _ := cmd.Flags().GetBool("no-cache")

			profile, filePath, err := resolveProfile(profileName)
			if err != nil {
				return err
			}
			profile, preset, err := selectProcesses(profile, profileName, only, skip, set)
			if err != nil {
				return err
			}
			if detach && !child {
				var extra []string
				for _, o := range only {
					extra = append(extra, "--only", o)
				}
				for _, s := range skip {
					extra = append(extra, "--skip", s)
				}
				for _, s := range set {
					extra = append(extra, "--set", s)
				}
				if noCache {
					extra = append(extra, "--no-cache")
				}
				return upDetached(profileName, profile, keepGoing, noWait, timeout, asJSON, extra)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cfg := orchestrator.Config{
				Profile:     profile,
				ProfileName: profileName,
				ProjectPath: profile.ResolvedProjectPath,
				ConfigFile:  filePath,
				LogFormat:   logFormat,
				Quiet:       quiet,
				KeepGoing:   keepGoing,
				Detached:    child,
				Preset:      preset,
				NoCache:     noCache,
			}
			if child {
				cfg.OpenConsole = func(runDir string) (io.Writer, string, error) {
					path := filepath.Join(runDir, "console.log")
					f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
					return f, path, err
				}
			}
			orc := orchestrator.New(cfg)

			sigCh := make(chan os.Signal, 2)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			go func() {
				<-sigCh
				// Already stopping (down, or a failure): this signal is the
				// second one.
				if orc.Stopping() {
					orc.ForceKill()
					os.Exit(130)
				}
				if !quiet && !child {
					fmt.Fprintln(os.Stderr, "\nstackstart: stopping all processes (Ctrl-C again to kill immediately)")
				}
				cancel()
				<-sigCh
				orc.ForceKill()
				os.Exit(130)
			}()

			if err := orc.Run(ctx); err != nil {
				if errors.Is(err, orchestrator.ErrForceKilled) {
					return err
				}
				if strings.HasPrefix(err.Error(), "cannot start:") {
					return err
				}
				return failedError(err)
			}
			return nil
		},
	}
	cmd.Flags().BoolP("detach", "d", false, "run in the background; return once ready")
	cmd.Flags().Bool("no-wait", false, "with -d: return as soon as the stack has started")
	cmd.Flags().Duration("timeout", 10*time.Minute, "with -d: how long to wait for readiness")
	cmd.Flags().Bool("keep-going", false, "keep the stack up when a required process fails")
	cmd.Flags().Bool("json", false, "with -d: print the stack's status as JSON once ready")
	cmd.Flags().StringSlice("only", nil, "start only these processes (and what they depend on)")
	cmd.Flags().StringSlice("skip", nil, "do not start these processes")
	cmd.Flags().StringArray("set", nil, "a captured value of a skipped process: <process>.<capture>=<value>")
	cmd.Flags().Bool("no-cache", false, "run oneshots even when their inputs have not changed")
	cmd.Flags().Bool("detached-child", false, "internal: run as the background stack")
	_ = cmd.Flags().MarkHidden("detached-child")
	return cmd
}

func resolveProfile(name string) (config.Profile, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return config.Profile{}, "", fmt.Errorf("cannot determine working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Profile{}, "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	profile, filePath, err := config.Resolve(cwd, name, home, cfgFile)
	if err != nil {
		return config.Profile{}, "", usageError(err)
	}
	if err := config.Validate(profile, name); err != nil {
		return config.Profile{}, "", usageError(err)
	}
	if profile.ResolvedProjectPath == "" {
		return config.Profile{}, "", fmt.Errorf("profile %q has no resolved project path", name)
	}
	return profile, filePath, nil
}

// upDetached starts the stack as a background process and reports how its
// start went.
func upDetached(name string, profile config.Profile, keepGoing, noWait bool, timeout time.Duration, asJSON bool, extra []string) error {
	slug := session.Slug(profile.ResolvedProjectPath, name)
	if err := os.MkdirAll(session.SessionsDir(), 0o755); err != nil {
		return err
	}
	// Anything the background process prints before it has a run
	// directory (or if it crashes) lands here.
	bootLog := filepath.Join(session.SessionsDir(), slug+".boot.log")
	boot, err := os.Create(bootLog)
	if err != nil {
		return err
	}
	defer func() { _ = boot.Close() }()

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"up", name, "--detached-child"}
	if keepGoing {
		args = append(args, "--keep-going")
	}
	if cfgFile != "" {
		args = append(args, "--config", cfgFile)
	}
	if logFormat != "" {
		args = append(args, "--log-format", logFormat)
	}
	args = append(args, extra...)
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = devnull.Close() }()
	c := exec.Command(exe, args...)
	c.Stdin, c.Stdout, c.Stderr = devnull, boot, boot
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return fmt.Errorf("starting the stack: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = c.Wait(); close(exited) }()

	bootFailure := func() error {
		// A stack that failed this fast: report the failure from its
		// event log, like the foreground would.
		if e := lastRunFailure(slug); e != nil {
			printFailedEvent(*e)
			return exitError{code: exitFailed}
		}
		data, _ := os.ReadFile(bootLog)
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = "the stack exited while starting"
		}
		return fmt.Errorf("%s", msg)
	}

	// Wait for the session record that names this process and its socket.
	var rec session.Record
	deadline := time.Now().Add(30 * time.Second)
	for {
		if r, err := session.ReadRecord(session.SessionRecordPath(slug)); err == nil && r.StackstartPID == c.Process.Pid && r.ControlSocket != "" {
			rec = r
			break
		}
		select {
		case <-exited:
			return bootFailure()
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the stack did not start within 30s; see %s", bootLog)
		}
	}

	if noWait {
		if asJSON {
			return printJSON(control.StatusDoc{Schema: control.StatusSchema, Sessions: []control.SessionStatus{sessionStatus(rec)}})
		}
		fmt.Printf("Started %q in %s (PID %d). Logs: %s\n", name, rec.ProjectPath, rec.StackstartPID, rec.LogDir)
		return nil
	}

	type outcome struct {
		ready    bool
		finished bool
		failed   *events.Event
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		var failed *events.Event
		_, err := control.Call(rec.ControlSocket, control.Request{Op: "events", Follow: true}, 0, func(e events.Event) {
			switch e.Type {
			case "stack_ready":
				result <- outcome{ready: true}
			case "process_failed":
				if req, _ := e.Fields["required"].(bool); req && failed == nil {
					ev := e
					failed = &ev
					if keepGoing {
						result <- outcome{failed: failed}
					}
				}
			case "run_finished":
				if o, _ := e.Fields["outcome"].(string); o == session.OutcomeStopped && failed == nil {
					result <- outcome{finished: true}
				} else {
					result <- outcome{failed: failed, finished: true}
				}
			}
		})
		result <- outcome{err: err}
	}()

	select {
	case o := <-result:
		switch {
		case o.ready:
			if asJSON {
				return printJSON(control.StatusDoc{Schema: control.StatusSchema, Sessions: []control.SessionStatus{sessionStatus(rec)}})
			}
			n := len(profile.Processes)
			what := fmt.Sprintf("all %d processes ready", n)
			if n == 1 {
				what = "1 process ready"
			}
			if !quiet {
				fmt.Fprintf(os.Stderr, "stackstart: %s · logs: %s\n", what, rec.LogDir)
			}
			fmt.Printf("Started %q in %s (PID %d). Logs: %s\n", name, rec.ProjectPath, rec.StackstartPID, rec.LogDir)
			return nil
		case o.failed != nil:
			printFailedEvent(*o.failed)
			if keepGoing && !o.finished {
				fmt.Fprintf(os.Stderr, "The stack keeps running (--keep-going). Fix the cause, then: stackstart start %s\n", o.failed.Process)
			}
			return exitError{code: 1}
		case o.finished:
			if !quiet {
				fmt.Fprintln(os.Stderr, "stackstart: all processes finished")
			}
			return nil
		default:
			<-exited
			return bootFailure()
		}
	case <-exited:
		return bootFailure()
	case <-time.After(timeout):
		return exitError{code: exitTimeout, msg: fmt.Sprintf("the stack is still starting after %s; it keeps running. Check it with 'stackstart status' or stop it with 'stackstart down'", timeout)}
	}
}

// printFailedEvent prints the FAILED block from a process_failed event.
func printFailedEvent(e events.Event) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n--- FAILED: %s ---\n", e.Process)
	fmt.Fprintf(&b, "Cause: %v\n", e.Fields["cause"])
	if lines, ok := e.Fields["last_lines"].([]any); ok && len(lines) > 0 {
		b.WriteString("Last output:\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "  %v\n", l)
		}
	}
	if lf, ok := e.Fields["log_file"].(string); ok {
		fmt.Fprintf(&b, "Full log: %s\n", lf)
	}
	fmt.Fprintf(&b, "Read it later with: stackstart logs %s\n", e.Process)
	fmt.Fprint(os.Stderr, b.String())
}

// lastRunFailure returns the required-process failure recorded in the
// newest run of a session, if any.
func lastRunFailure(slug string) *events.Event {
	entries, err := os.ReadDir(session.RunsDir(slug))
	if err != nil || len(entries) == 0 {
		return nil
	}
	newest := entries[len(entries)-1].Name()
	evs, err := events.ReadFile(filepath.Join(session.RunsDir(slug), newest, "events.jsonl"))
	if err != nil {
		return nil
	}
	for _, e := range evs {
		if e.Type == "process_failed" {
			if req, _ := e.Fields["required"].(bool); req {
				ev := e
				return &ev
			}
		}
	}
	return nil
}
