package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/orpic/stack-start/internal/session"
	"github.com/spf13/cobra"
)

func newCleanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Delete the logs of finished runs",
		Long: fmt.Sprintf(`Delete the log directories of runs that have finished.

Each 'stackstart up' keeps its logs in its own run directory, and the last
%d runs of each profile are kept automatically. clean removes finished runs
for the project in the current directory (or every project with --all).
A running stack's logs are never touched.`, session.DefaultKeepRuns),
		Example: `  stackstart clean --dry-run
  stackstart clean
  stackstart clean --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}

			runs, err := session.ListRuns()
			if err != nil {
				return err
			}

			var removed int
			var freed int64
			for _, r := range runs {
				if r.Outcome == session.OutcomeRunning && session.IsAlive(r.StackstartPID) {
					continue
				}
				if !all && !session.MatchesProject(cwd, r.ProjectPath) {
					continue
				}
				size := dirSize(r.LogDir)
				if dryRun {
					fmt.Printf("would remove  %s  %s  %s  (%s)\n", r.RunID, r.Profile, r.ProjectPath, humanBytes(size))
				} else {
					if err := os.RemoveAll(r.LogDir); err != nil {
						return fmt.Errorf("removing %s: %w", r.LogDir, err)
					}
					removeIfEmpty(session.RunsDir(r.Slug), session.SessionDir(r.Slug))
				}
				removed++
				freed += size
			}

			verb := "Removed"
			if dryRun {
				verb = "Would remove"
			}
			fmt.Printf("%s %d finished run(s), %s.\n", verb, removed, humanBytes(freed))
			return nil
		},
	}
	cmd.Flags().Bool("all", false, "clean finished runs of every project, not just this one")
	cmd.Flags().Bool("dry-run", false, "list what would be removed without removing it")
	return cmd
}

// removeIfEmpty removes a session's directory once it has no runs left.
func removeIfEmpty(runsDir, sessionDir string) {
	entries, err := os.ReadDir(runsDir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(runsDir)
	_ = os.Remove(filepath.Join(sessionDir, "latest"))
	_ = os.Remove(sessionDir)
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
