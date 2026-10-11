package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/events"
	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/tail"
)

func newSendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send <process> <text>",
		Short: "Type text into a running process (a REPL, a prompt) and print what it answers",
		Long: `Write text to a process's terminal, as if typed, followed by Enter, and
print what the process printed during the next half second (--echo).
For IEx (recompile), a Rails console, or answering a "continue? [y/N]" prompt.`,
		Example: `  stackstart send phoenix "recompile"
  stackstart send console "User.count"
  stackstart send migrate y --echo 2s`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			noNewline, _ := cmd.Flags().GetBool("no-newline")
			echo, _ := cmd.Flags().GetDuration("echo")
			asJSON, _ := cmd.Flags().GetBool("json")
			rec, err := findLive(profile, args[0])
			if err != nil {
				return err
			}
			data := args[1]
			if !noNewline {
				data += "\r"
			}
			resp, err := call(rec, control.Request{Op: "send", Process: args[0], Data: data, EchoMS: echo.Milliseconds()}, echo+10*time.Second, nil)
			if err != nil {
				return err
			}
			var result struct {
				Output string `json:"output"`
			}
			_ = json.Unmarshal(resp.Result, &result)
			if asJSON {
				return printJSON(map[string]any{"ok": true, "process": args[0], "output": result.Output})
			}
			fmt.Print(result.Output)
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Bool("no-newline", false, "do not press Enter after the text")
	cmd.Flags().Duration("echo", 500*time.Millisecond, "how long to collect the process's answer")
	cmd.Flags().Bool("json", false, "print the result as JSON")
	return cmd
}

func newAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <process>",
		Short: "Use a running process interactively (keystrokes go to it); Ctrl-] detaches",
		Long: `Show a process's output and send your keystrokes to it, for REPLs and
consoles running inside the stack. Ctrl-] detaches and leaves it running.
Needs a terminal; agents use 'stackstart send'.`,
		Example: `  stackstart attach phoenix`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("attach needs a terminal; use 'stackstart send %s <text>' instead", args[0])
			}
			rec, err := findLive(profile, args[0])
			if err != nil {
				return err
			}
			var logFile string
			for _, p := range rec.Processes {
				if p.Name == args[0] {
					logFile = p.LogFile
				}
			}
			fmt.Fprintf(os.Stderr, "==> attached to %s · Ctrl-] to detach <==\r\n", args[0])
			old, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				return err
			}
			defer func() { _ = term.Restore(int(os.Stdin.Fd()), old) }()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = tail.FollowFrom(ctx, logFile, crlfWriter{os.Stdout}, 20) }()
			buf := make([]byte, 256)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					return nil
				}
				chunk := buf[:n]
				if i := strings.IndexByte(string(chunk), 0x1d); i >= 0 { // Ctrl-]
					if i > 0 {
						_, _ = call(rec, control.Request{Op: "send", Process: args[0], Data: string(chunk[:i]), EchoMS: 1}, 5*time.Second, nil)
					}
					fmt.Fprint(os.Stderr, "\r\n==> detached <==\r\n")
					return nil
				}
				if _, err := call(rec, control.Request{Op: "send", Process: args[0], Data: string(chunk), EchoMS: 1}, 5*time.Second, nil); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	return cmd
}

// crlfWriter turns \n into \r\n for a terminal in raw mode.
type crlfWriter struct{ f *os.File }

func (w crlfWriter) Write(p []byte) (int, error) {
	_, err := w.f.Write([]byte(strings.ReplaceAll(string(p), "\n", "\r\n")))
	return len(p), err
}

func newEventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show what happened in the stack: starts, readiness, captures, restarts, exits",
		Long: `Print the stack's event log, one event per line (JSON with --json).
--after N shows only events after sequence number N, so a script can ask
"what changed since I last looked". --follow keeps printing new events.
Works on a running stack and on the last finished run.`,
		Example: `  stackstart events
  stackstart events --after 42 --json
  stackstart events --follow --type process_ready --type process_failed`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			after, _ := cmd.Flags().GetInt64("after")
			follow, _ := cmd.Flags().GetBool("follow")
			asJSON, _ := cmd.Flags().GetBool("json")
			types, _ := cmd.Flags().GetStringSlice("type")
			process, _ := cmd.Flags().GetString("process")
			print := func(e events.Event) {
				if asJSON {
					data, _ := json.Marshal(e)
					fmt.Println(string(data))
					return
				}
				fmt.Println(formatEvent(e))
			}
			rec, err := findLive(profile, "")
			if err != nil {
				// No live stack: read the last run's event log.
				cwd, _ := os.Getwd()
				runs, rerr := session.LatestFinishedRuns(cwd, profile)
				if rerr != nil || len(runs) == 0 {
					return err
				}
				if len(runs) > 1 {
					return ambiguousError(ambiguousSessions(runs))
				}
				evs, rerr := events.ReadFile(eventsPath(runs[0]))
				if rerr != nil {
					return rerr
				}
				want := typeSet(types)
				for _, e := range evs {
					if e.Seq > after && (len(want) == 0 || want[e.Type]) && (process == "" || e.Process == process) {
						print(e)
					}
				}
				return nil
			}
			ctx, cancel := signalContext()
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := call(rec, control.Request{Op: "events", After: after, Follow: follow, Types: types, Process: process}, 0, print)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil && err != control.ErrStackStopped {
					return err
				}
			case <-ctx.Done():
			}
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Int64("after", 0, "only events after this sequence number")
	cmd.Flags().Bool("follow", false, "keep printing new events until the stack stops")
	cmd.Flags().Bool("json", false, "one JSON object per line")
	cmd.Flags().StringSlice("type", nil, "only events of this type (repeatable)")
	cmd.Flags().String("process", "", "only events of this process")
	return cmd
}

func eventsPath(rec session.Record) string {
	if rec.EventsFile != "" {
		return rec.EventsFile
	}
	return rec.LogDir + "/events.jsonl"
}

func typeSet(types []string) map[string]bool {
	m := map[string]bool{}
	for _, t := range types {
		m[t] = true
	}
	return m
}

// formatEvent renders an event as one readable line.
func formatEvent(e events.Event) string {
	var parts []string
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		if k == "last_lines" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, e.Fields[k]))
	}
	proc := e.Process
	if proc == "" {
		proc = "-"
	}
	return strings.TrimSpace(fmt.Sprintf("%d %s %s %s %s", e.Seq, e.TS.Local().Format("15:04:05.000"), e.Type, proc, strings.Join(parts, " ")))
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}

func newWaitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait [process]",
		Short: "Block until a process is ready, exits, rebuilds or an event happens",
		Long: `Wait for a condition and exit 0, or exit non-zero with the cause and the
process's last output.

  --for ready         the process is ready (or, without a process, the whole stack)
  --for exit          the process exited or was stopped
  --for rebuilt       the process finished a rebuild (signals: in the config)
  --for event:<type>  any event of that type (see 'stackstart events')

Without --after, a process that is already ready satisfies --for ready.
With --after N, only events after sequence number N count: take N from
'stackstart status --json' (events.last_seq) before editing a file, then
wait for the rebuild that edit causes.`,
		Example: `  stackstart wait phoenix --timeout 2m
  stackstart wait --timeout 5m
  stackstart wait api --for rebuilt --after 118 --timeout 60s`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			forWhat, _ := cmd.Flags().GetString("for")
			after, _ := cmd.Flags().GetInt64("after")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			asJSON, _ := cmd.Flags().GetBool("json")
			process := ""
			if len(args) == 1 {
				process = args[0]
			}
			rec, err := findLive(profile, process)
			if err != nil {
				return err
			}
			var last events.Event
			resp, err := call(rec, control.Request{Op: "wait", Process: process, For: forWhat, After: after, TimeoutMS: timeout.Milliseconds()},
				timeout+5*time.Second, func(e events.Event) { last = e })
			if err != nil {
				// Show the event that ended the wait (e.g. rebuild_failed).
				if last.Seq != 0 && !asJSON {
					fmt.Println(formatEvent(last))
				}
				return err
			}
			var result struct {
				Seq   int64  `json:"seq"`
				State string `json:"state"`
			}
			_ = json.Unmarshal(resp.Result, &result)
			if asJSON {
				out := map[string]any{"ok": true, "seq": result.Seq}
				if last.Seq != 0 {
					out["event"] = last
				}
				return printJSON(out)
			}
			if last.Seq != 0 {
				fmt.Println(formatEvent(last))
			} else {
				fmt.Printf("%s is %s (seq %d)\n", process, result.State, result.Seq)
			}
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().String("for", "ready", "ready, exit, rebuilt or event:<type>")
	cmd.Flags().Int64("after", 0, "only count events after this sequence number")
	cmd.Flags().Duration("timeout", 5*time.Minute, "give up after this long")
	cmd.Flags().Bool("json", false, "print the result as JSON")
	return cmd
}
