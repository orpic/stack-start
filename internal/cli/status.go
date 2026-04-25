package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/orpic/stack-start/internal/session"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show all currently running stackstart sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			records, err := session.ListAllSessions()
			if err != nil {
				return err
			}

			if len(records) == 0 {
				fmt.Println("No running stackstart sessions.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PROFILE\tPROJECT\tPID\tSTARTED\tPROCESSES")
			for _, rec := range records {
				procSummary := ""
				for i, p := range rec.Processes {
					state := session.ReadProcessState(p.StateFile)
					if i > 0 {
						procSummary += ", "
					}
					procSummary += fmt.Sprintf("%s(%s)", p.Name, state)
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
					rec.Profile, rec.ProjectPath, rec.StackstartPID,
					rec.StartedAt.Format("15:04:05"), procSummary)
			}
			return w.Flush()
		},
	}
}
