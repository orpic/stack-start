package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mattn/go-isatty"

	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/tail"
	"github.com/spf13/cobra"
)

// liveHistoryLines is how much history `logs` shows before following a live
// process, like `tail -f`.
const liveHistoryLines = 10

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <process>",
		Short: "Show a process's log, from a running stack or the last run",
		Long: `Show a process's log.

If the stack is running and stdout is a terminal, prints the last 10 lines
and then follows new output until Ctrl-C. Otherwise (a stopped or crashed
stack, or output going to a pipe or an agent) prints the log and exits.
--follow and --no-follow choose explicitly; --lines N prints only the last
N lines.

Logs are plain text: colors and terminal control codes are removed. The
exact bytes the process wrote are kept next to it in <process>.raw.log.`,
		Example: `  stackstart logs backend
  stackstart logs backend --lines 50 --no-follow
  stackstart logs backend --profile dev`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			processName := args[0]
			profileFlag, _ := cmd.Flags().GetString("profile")
			lines, _ := cmd.Flags().GetInt("lines")
			follow := isatty.IsTerminal(os.Stdout.Fd())
			if cmd.Flags().Changed("follow") {
				follow, _ = cmd.Flags().GetBool("follow")
			}
			if cmd.Flags().Changed("no-follow") {
				nf, _ := cmd.Flags().GetBool("no-follow")
				follow = !nf
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}

			live, err := session.FindMatchingSessions(cwd, profileFlag)
			if err != nil {
				return err
			}
			live = narrowByProcess(live, processName)
			if len(live) > 1 {
				return ambiguousSessions(live)
			}
			if len(live) == 1 {
				rec := live[0]
				p, err := findProcess(rec, processName)
				if err != nil {
					return err
				}
				if !follow {
					fmt.Fprintf(os.Stderr, "==> %s · %s · %s · running <==\n", processName, rec.Profile, rec.ProjectPath)
					data, err := tail.LastLines(p.LogFile, lines)
					if err != nil {
						return fmt.Errorf("reading %s: %w", p.LogFile, err)
					}
					_, err = os.Stdout.Write(data)
					return err
				}
				if lines <= 0 {
					lines = liveHistoryLines
				}
				fmt.Fprintf(os.Stderr, "==> %s · %s · %s · running (Ctrl-C to stop) <==\n",
					processName, rec.Profile, rec.ProjectPath)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
				go func() {
					<-sigCh
					cancel()
				}()
				return tail.FollowFrom(ctx, p.LogFile, os.Stdout, lines)
			}

			runs, err := session.LatestFinishedRuns(cwd, profileFlag)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				return fmt.Errorf("no running or previous stackstart session found for %s", cwd)
			}
			runs = narrowByProcess(runs, processName)
			if len(runs) > 1 {
				return ambiguousSessions(runs)
			}
			rec := runs[0]
			p, err := findProcess(rec, processName)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "==> %s · %s · %s · %s <==\n",
				processName, rec.Profile, rec.ProjectPath, describeEndedRun(rec))
			data, err := tail.LastLines(p.LogFile, lines)
			if err != nil {
				return fmt.Errorf("reading %s: %w", p.LogFile, err)
			}
			_, err = os.Stdout.Write(data)
			return err
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Int("lines", 0, "print only the last N lines (default: all, or 10 when following)")
	cmd.Flags().Bool("follow", false, "keep printing new lines (default when stdout is a terminal)")
	cmd.Flags().Bool("no-follow", false, "print and exit even on a terminal")
	return cmd
}

func findProcess(rec session.Record, name string) (session.ProcessRecord, error) {
	var names []string
	for _, p := range rec.Processes {
		if p.Name == name {
			return p, nil
		}
		names = append(names, p.Name)
	}
	return session.ProcessRecord{}, fmt.Errorf("process %q not found in profile %q (processes: %s)",
		name, rec.Profile, strings.Join(names, ", "))
}

// narrowByProcess keeps only the sessions that have the named process, when
// that narrows the choice down to exactly one. Otherwise it returns recs
// unchanged, so the caller can report "not found" or "ambiguous" with the
// full list.
func narrowByProcess(recs []session.Record, name string) []session.Record {
	if len(recs) < 2 {
		return recs
	}
	var with []session.Record
	for _, r := range recs {
		if _, err := findProcess(r, name); err == nil {
			with = append(with, r)
		}
	}
	if len(with) == 1 {
		return with
	}
	return recs
}

func ambiguousSessions(recs []session.Record) error {
	width := 0
	for _, r := range recs {
		if len(r.Profile) > width {
			width = len(r.Profile)
		}
	}
	var b strings.Builder
	b.WriteString("more than one stackstart session matches this directory; pass --profile <name>:")
	for _, r := range recs {
		fmt.Fprintf(&b, "\n  --profile %-*s  %s", width, r.Profile, r.ProjectPath)
	}
	return fmt.Errorf("%s", b.String())
}

// describeEndedRun says how and when a run ended, e.g.
// "last run 20261010-221157-123 failed at 22:12:03 (backend)".
func describeEndedRun(rec session.Record) string {
	when := ""
	if rec.EndedAt != nil {
		when = " at " + rec.EndedAt.Format("2006-01-02 15:04:05")
	}
	switch rec.Outcome {
	case session.OutcomeFailed:
		return fmt.Sprintf("last run %s failed%s (%s)", rec.RunID, when, rec.FailedProcess)
	case session.OutcomeStopped:
		return fmt.Sprintf("last run %s stopped%s", rec.RunID, when)
	default:
		// The run never recorded an end: stackstart itself was killed.
		return fmt.Sprintf("last run %s ended unexpectedly", rec.RunID)
	}
}
