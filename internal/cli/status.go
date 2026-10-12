package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/session"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show running stacks and their processes",
		Long: `Show running stacks: every session when run outside a project, or the
sessions of this project. --json prints a stable document for scripts and
agents: per process its state, PID, ports, captured values, restarts and
last exit; per session the log directory, event log and git state.`,
		Example: `  stackstart status
  stackstart status --json
  stackstart status --all --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			all, _ := cmd.Flags().GetBool("all")
			profile, _ := cmd.Flags().GetString("profile")

			records, err := session.ListAllSessions()
			if err != nil {
				return err
			}
			cwd, _ := os.Getwd()
			// Inside a project with sessions, show those; otherwise all.
			var mine []session.Record
			for _, r := range records {
				if profile != "" && r.Profile != profile {
					continue
				}
				if session.MatchesProject(cwd, r.ProjectPath) {
					mine = append(mine, r)
				}
			}
			if !all && len(mine) > 0 {
				records = mine
			} else if profile != "" {
				var filtered []session.Record
				for _, r := range records {
					if r.Profile == profile {
						filtered = append(filtered, r)
					}
				}
				records = filtered
			}

			doc := control.StatusDoc{Schema: control.StatusSchema, Sessions: []control.SessionStatus{}}
			for _, rec := range records {
				doc.Sessions = append(doc.Sessions, sessionStatus(rec))
			}
			if asJSON {
				return printJSON(doc)
			}
			if len(doc.Sessions) == 0 {
				fmt.Println("No running stackstart sessions.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PROFILE\tPROJECT\tPID\tSTARTED\tPROCESSES")
			for _, s := range doc.Sessions {
				var procs []string
				for _, p := range s.Processes {
					procs = append(procs, fmt.Sprintf("%s(%s)", p.Name, p.State))
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", s.Profile, s.Project, s.StackstartPID,
					s.StartedAt.Local().Format("15:04:05"), strings.Join(procs, ", "))
			}
			return w.Flush()
		},
	}
	cmd.Flags().Bool("json", false, "print a JSON document")
	cmd.Flags().Bool("all", false, "show every running session, not just this project's")
	cmd.Flags().String("profile", "", "only sessions of this profile")
	return cmd
}

// sessionStatus asks the stack for its status, or builds what it can from
// the session record if the stack doesn't answer.
func sessionStatus(rec session.Record) control.SessionStatus {
	if rec.ControlSocket != "" {
		if resp, err := control.Call(rec.ControlSocket, control.Request{Op: "status"}, 5*time.Second, nil); err == nil && resp.Error == nil {
			var st control.SessionStatus
			if json.Unmarshal(resp.Result, &st) == nil {
				return st
			}
		}
	}
	st := control.SessionStatus{
		Profile: rec.Profile, Project: rec.ProjectPath, ConfigFile: rec.ConfigFile, RunID: rec.RunID,
		StackstartPID: rec.StackstartPID, Detached: rec.Detached, StartedAt: rec.StartedAt,
		State: "unknown", LogDir: rec.LogDir, ConsoleLog: rec.ConsoleLog,
		Events: control.EventsInfo{File: rec.EventsFile}, ControlSocket: rec.ControlSocket,
	}
	for _, p := range rec.Processes {
		st.Processes = append(st.Processes, control.ProcessStatus{
			Name: p.Name, State: session.ReadProcessState(p.StateFile), PID: p.PID,
			LogFile: p.LogFile, RawLogFile: p.RawLog, Ports: []int{}, Captures: map[string]string{},
		})
	}
	return st
}
