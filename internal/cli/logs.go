package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/orpic/stack-start/internal/logmux"
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
		Short: "Show a process's log, from a running stack or an earlier run",
		Long: `Show a process's log.

If the stack is running and stdout is a terminal, prints the last 10 lines
and then follows new output until Ctrl-C. Otherwise (a stopped or crashed
stack, or output going to a pipe or an agent) prints the log and exits.
--follow and --no-follow choose explicitly.

  --lines N     only the last N lines
  --since 5m    only lines written in the last 5 minutes (or since 14:05,
                or an RFC 3339 time)
  --grep RE     only lines matching a regular expression
  --run prev    an earlier run: latest, prev, or a run id from 'stackstart events'

Logs are plain text: colors and terminal control codes are removed. The
exact bytes the process wrote are kept next to it in <process>.raw.log.`,
		Example: `  stackstart logs backend
  stackstart logs backend --lines 50 --no-follow
  stackstart logs backend --since 2m --grep -i error
  stackstart logs backend --run prev`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			processName := args[0]
			profileFlag, _ := cmd.Flags().GetString("profile")
			lines, _ := cmd.Flags().GetInt("lines")
			sinceFlag, _ := cmd.Flags().GetString("since")
			grepFlag, _ := cmd.Flags().GetString("grep")
			ignoreCase, _ := cmd.Flags().GetBool("ignore-case")
			runFlag, _ := cmd.Flags().GetString("run")
			follow := isatty.IsTerminal(os.Stdout.Fd())
			if cmd.Flags().Changed("follow") {
				follow, _ = cmd.Flags().GetBool("follow")
			}
			if cmd.Flags().Changed("no-follow") {
				nf, _ := cmd.Flags().GetBool("no-follow")
				follow = !nf
			}
			f := logFilter{lines: lines}
			if sinceFlag != "" {
				t, err := parseSince(sinceFlag, time.Now())
				if err != nil {
					return usageError(err)
				}
				f.since = &t
			}
			if grepFlag != "" {
				pattern := grepFlag
				if ignoreCase {
					pattern = "(?i)" + pattern
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return usageError(fmt.Errorf("--grep: %w", err))
				}
				f.grep = re
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}

			if runFlag != "" {
				rec, err := pickRun(cwd, profileFlag, processName, runFlag)
				if err != nil {
					return err
				}
				p, err := findProcess(rec, processName)
				if err != nil {
					return usageError(err)
				}
				fmt.Fprintf(os.Stderr, "==> %s · %s · %s · run %s <==\n", processName, rec.Profile, rec.ProjectPath, rec.RunID)
				return f.print(p.LogFile, os.Stdout)
			}

			live, err := session.FindMatchingSessions(cwd, profileFlag)
			if err != nil {
				return err
			}
			live = narrowByProcess(live, processName)
			if len(live) > 1 {
				return ambiguousError(ambiguousSessions(live))
			}
			if len(live) == 1 {
				rec := live[0]
				p, err := findProcess(rec, processName)
				if err != nil {
					return usageError(err)
				}
				if !follow {
					fmt.Fprintf(os.Stderr, "==> %s · %s · %s · running <==\n", processName, rec.Profile, rec.ProjectPath)
					return f.print(p.LogFile, os.Stdout)
				}
				if f.lines <= 0 && f.since == nil {
					f.lines = liveHistoryLines
				}
				fmt.Fprintf(os.Stderr, "==> %s · %s · %s · running (Ctrl-C to stop) <==\n",
					processName, rec.Profile, rec.ProjectPath)
				if err := f.print(p.LogFile, os.Stdout); err != nil {
					return err
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
				go func() {
					<-sigCh
					cancel()
				}()
				return tail.FollowFrom(ctx, p.LogFile, &grepWriter{re: f.grep, w: os.Stdout}, 0)
			}

			runs, err := session.LatestFinishedRuns(cwd, profileFlag)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				return noSessionError(fmt.Errorf("no running or previous stackstart session found for %s", cwd))
			}
			runs = narrowByProcess(runs, processName)
			if len(runs) > 1 {
				return ambiguousError(ambiguousSessions(runs))
			}
			rec := runs[0]
			p, err := findProcess(rec, processName)
			if err != nil {
				return usageError(err)
			}
			fmt.Fprintf(os.Stderr, "==> %s · %s · %s · %s <==\n",
				processName, rec.Profile, rec.ProjectPath, describeEndedRun(rec))
			return f.print(p.LogFile, os.Stdout)
		},
	}
	cmd.Flags().String("profile", "", "profile name (required when multiple sessions match)")
	cmd.Flags().Int("lines", 0, "print only the last N lines (default: all, or 10 when following)")
	cmd.Flags().Bool("follow", false, "keep printing new lines (default when stdout is a terminal)")
	cmd.Flags().Bool("no-follow", false, "print and exit even on a terminal")
	cmd.Flags().String("since", "", "only lines written since a duration ago (5m) or a time (14:05, RFC 3339)")
	cmd.Flags().String("grep", "", "only lines matching this regular expression")
	cmd.Flags().BoolP("ignore-case", "i", false, "with --grep: ignore case")
	cmd.Flags().String("run", "", "an earlier run: latest, prev, or a run id")
	return cmd
}

// logFilter selects lines of a log.
type logFilter struct {
	since *time.Time
	grep  *regexp.Regexp
	lines int
}

func (f logFilter) print(path string, w io.Writer) error {
	var start int64
	if f.since != nil {
		off, err := logmux.OffsetAt(path, *f.since)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if off < 0 {
			return nil
		}
		start = off
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	text := string(data)
	if f.grep == nil && f.lines <= 0 {
		_, err = io.WriteString(w, text)
		return err
	}
	all := strings.SplitAfter(text, "\n")
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	var kept []string
	for _, l := range all {
		if f.grep == nil || f.grep.MatchString(strings.TrimSuffix(l, "\n")) {
			kept = append(kept, l)
		}
	}
	if f.lines > 0 && len(kept) > f.lines {
		kept = kept[len(kept)-f.lines:]
	}
	_, err = io.WriteString(w, strings.Join(kept, ""))
	return err
}

// grepWriter passes through only the lines matching re (all if nil).
type grepWriter struct {
	re      *regexp.Regexp
	w       io.Writer
	pending []byte
}

func (g *grepWriter) Write(p []byte) (int, error) {
	if g.re == nil {
		return g.w.Write(p)
	}
	g.pending = append(g.pending, p...)
	for {
		i := bytes.IndexByte(g.pending, '\n')
		if i < 0 {
			break
		}
		line := g.pending[:i+1]
		if g.re.Match(line[:i]) {
			if _, err := g.w.Write(line); err != nil {
				return 0, err
			}
		}
		g.pending = g.pending[i+1:]
	}
	return len(p), nil
}

// parseSince reads "5m", "1h30m", "14:05", "14:05:30" or an RFC 3339 time.
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location()), nil
		}
	}
	return time.Time{}, fmt.Errorf("--since: %q is not a duration (5m), a time (14:05) or an RFC 3339 time", s)
}

// pickRun finds a run of this project by name: latest, prev, or an id.
func pickRun(cwd, profile, process, which string) (session.Record, error) {
	all, err := session.ListRuns()
	if err != nil {
		return session.Record{}, err
	}
	var runs []session.Record
	profiles := map[string]bool{}
	for _, r := range all {
		if profile != "" && r.Profile != profile {
			continue
		}
		if !session.MatchesProject(cwd, r.ProjectPath) {
			continue
		}
		if _, err := findProcess(r, process); err != nil {
			continue
		}
		runs = append(runs, r)
		profiles[r.Profile] = true
	}
	if len(runs) == 0 {
		return session.Record{}, noSessionError(fmt.Errorf("no run of %s found for %s", process, cwd))
	}
	if len(profiles) > 1 {
		var names []string
		for p := range profiles {
			names = append(names, p)
		}
		sort.Strings(names)
		return session.Record{}, ambiguousError(fmt.Errorf("runs of more than one profile have %s; pass --profile (%s)", process, strings.Join(names, ", ")))
	}
	switch which {
	case "latest", "last":
		return runs[0], nil
	case "prev", "previous":
		if len(runs) < 2 {
			return session.Record{}, noSessionError(fmt.Errorf("there is no run before %s", runs[0].RunID))
		}
		return runs[1], nil
	}
	for _, r := range runs {
		if r.RunID == which || strings.HasPrefix(r.RunID, which) {
			return r, nil
		}
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.RunID)
	}
	return session.Record{}, usageError(fmt.Errorf("no run %q; runs: %s", which, strings.Join(ids, ", ")))
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
