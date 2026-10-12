package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/events"
	"github.com/orpic/stack-start/internal/session"
)

// findLive returns the one running session the command is about: the
// sessions for this directory, narrowed by --profile and, when given, by
// which of them has the process.
func findLive(profile, process string) (session.Record, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return session.Record{}, fmt.Errorf("cannot determine working directory: %w", err)
	}
	recs, err := session.FindMatchingSessions(cwd, profile)
	if err != nil {
		return session.Record{}, err
	}
	if process != "" {
		recs = narrowByProcess(recs, process)
	}
	switch len(recs) {
	case 0:
		return session.Record{}, noSessionError(fmt.Errorf("no running stackstart session found for %s", cwd))
	case 1:
		return recs[0], nil
	default:
		return session.Record{}, ambiguousError(ambiguousSessions(recs))
	}
}

// call sends a request to a session's control socket.
func call(rec session.Record, req control.Request, timeout time.Duration, onEvent func(events.Event)) (*control.Response, error) {
	if rec.ControlSocket == "" {
		return nil, fmt.Errorf("the stack %q in %s was started by an older stackstart and has no control socket; restart it with 'stackstart down' and 'stackstart up %s'",
			rec.Profile, rec.ProjectPath, rec.Profile)
	}
	resp, err := control.Call(rec.ControlSocket, req, timeout, onEvent)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return resp, resp.Error
	}
	return resp, nil
}

// describeError turns a control error into the text a person reads.
func describeError(err error) error {
	ce, ok := err.(*control.Error)
	if !ok {
		return err
	}
	var b strings.Builder
	b.WriteString(ce.Message)
	if len(ce.LastLines) > 0 {
		b.WriteString("\nLast output:")
		for _, l := range ce.LastLines {
			b.WriteString("\n  " + l)
		}
	}
	if ce.LogFile != "" {
		b.WriteString("\nFull log: " + ce.LogFile)
	}
	if ce.Process != "" && ce.Code == control.CodeFailed {
		b.WriteString("\nRead it later with: stackstart logs " + ce.Process)
	}
	if ce.Hint != "" {
		b.WriteString("\n" + ce.Hint)
	}
	return fmt.Errorf("%s", b.String())
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// newProcessCmd builds start, stop and restart.
func newProcessCmd(op, short, long, example string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     op + " <process>",
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			noWait, _ := cmd.Flags().GetBool("no-wait")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			asJSON, _ := cmd.Flags().GetBool("json")
			name := args[0]
			rec, err := findLive(profile, name)
			if err != nil {
				return err
			}
			req := control.Request{Op: op, Process: name, Wait: "ready", TimeoutMS: timeout.Milliseconds()}
			if noWait {
				req.Wait = "none"
			}
			if op == "stop" {
				req.Wait = "stopped"
				if noWait {
					req.Wait = "none"
				}
			}
			start := time.Now()
			var seen []events.Event
			resp, err := call(rec, req, timeout+5*time.Second, func(e events.Event) {
				seen = append(seen, e)
				if !asJSON && !quiet {
					if line := progressLine(e); line != "" {
						fmt.Fprintln(os.Stderr, line)
					}
				}
			})
			if err != nil {
				return err
			}
			var result struct {
				Targets []string `json:"targets"`
				Seq     int64    `json:"seq"`
			}
			_ = json.Unmarshal(resp.Result, &result)
			if asJSON {
				return printJSON(map[string]any{"ok": true, "process": name, "targets": result.Targets, "seq": result.Seq})
			}
			verb := map[string]string{"start": "Started", "stop": "Stopped", "restart": "Restarted"}[op]
			what := strings.Join(result.Targets, ", ")
			switch {
			case noWait:
				fmt.Printf("%s %s (not waiting).\n", strings.TrimSuffix(verb, "ed")+"ing", what)
			case op == "stop":
				fmt.Printf("%s %s.\n", verb, what)
			default:
				fmt.Printf("%s %s: ready in %s.\n", verb, what, time.Since(start).Round(100*time.Millisecond))
			}
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Bool("no-wait", false, "return once the request is accepted")
	cmd.Flags().Duration("timeout", 5*time.Minute, "how long to wait")
	cmd.Flags().Bool("json", false, "print the result as JSON")
	return cmd
}

// progressLine is a one-line description of an event for stderr.
func progressLine(e events.Event) string {
	switch e.Type {
	case "process_stopping":
		return "stackstart: stopping " + e.Process
	case "process_stopped":
		return "stackstart: stopped " + e.Process
	case "process_starting":
		return fmt.Sprintf("stackstart: starting %s (pid %v)", e.Process, e.Fields["pid"])
	case "process_ready":
		return "stackstart: " + e.Process + " ready"
	case "process_backoff":
		return fmt.Sprintf("stackstart: %s failed (%v); retrying", e.Process, e.Fields["cause"])
	case "process_blocked":
		return fmt.Sprintf("stackstart: %s blocked by %v", e.Process, e.Fields["blocked_by"])
	}
	return ""
}

func newStartCmd() *cobra.Command {
	return newProcessCmd("start", "Start a stopped or failed process in the running stack",
		`Start one process of the running stack: one that was stopped, that failed,
or that is blocked. Processes waiting for it start once it is ready.
Waits until it is ready unless --no-wait.`,
		`  stackstart start migrate
  stackstart start api --no-wait`)
}

func newStopCmd() *cobra.Command {
	return newProcessCmd("stop", "Stop one process (and what depends on it) in the running stack",
		`Stop one process of the running stack. Processes that depend on it are
stopped first and start again when it is started again.`,
		`  stackstart stop postgres
  stackstart stop tunnel --json`)
}

func newRestartCmd() *cobra.Command {
	return newProcessCmd("restart", "Restart one process (and what depends on it) in the running stack",
		`Restart one process of the running stack. Processes that depend on it are
stopped first and start again once it is ready, with any new captured
values. Nothing else is touched. Waits until all of them are ready.`,
		`  stackstart restart phoenix
  stackstart restart tunnel --timeout 2m --json`)
}
