package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMatchesProject(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) string {
		full := filepath.Join(root, p)
		require.NoError(t, os.MkdirAll(full, 0755))
		return full
	}
	repo := mk("repo")
	mk("repo/.git")
	pkg := mk("repo/packages/web")
	wt := mk("repo/.worktrees/feature")
	// A git worktree has a .git *file*, not a directory.
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../../.git/worktrees/feature"), 0644))
	wtPkg := mk("repo/.worktrees/feature/packages/web")
	other := mk("repox")

	require.True(t, MatchesProject(repo, repo), "the project root itself")
	require.True(t, MatchesProject(pkg, repo), "a package inside the project")
	require.False(t, MatchesProject(wt, repo), "a worktree must not control the main checkout's stack")
	require.False(t, MatchesProject(wtPkg, repo), "nor a folder inside that worktree")
	require.True(t, MatchesProject(wtPkg, wt), "the worktree's own stack matches")
	require.False(t, MatchesProject(other, repo), "sibling folder with a shared prefix")
	require.False(t, MatchesProject(root, repo), "a parent folder")
}

func TestNewRunAndPrune(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	slug := "proj__dev"

	var ids []string
	for i := 0; i < 4; i++ {
		id, dir, err := NewRun(slug)
		require.NoError(t, err)
		require.DirExists(t, dir)
		// Run IDs carry a second-resolution timestamp; rename to force a stable order.
		forced := filepath.Join(RunsDir(slug), "2026010"+string(rune('1'+i))+"-000000-1")
		require.NoError(t, os.Rename(dir, forced))
		ids = append(ids, filepath.Base(forced))
		_ = id
	}

	require.NoError(t, PruneRuns(slug, 2, ids[0]))
	entries, err := os.ReadDir(RunsDir(slug))
	require.NoError(t, err)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	require.ElementsMatch(t, []string{ids[0], ids[3]}, left, "keeps the protected run plus the newest")
}

func TestLatestLinkPointsAtNewestRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, dir, err := NewRun("proj__dev")
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(filepath.Join(SessionDir("proj__dev"), "latest"))
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(dir)
	require.Equal(t, want, resolved)
}

func TestLatestFinishedRuns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	project := t.TempDir()

	write := func(slug, runID, outcome string, pid int) {
		dir := filepath.Join(RunsDir(slug), runID)
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, WriteRunInfo(Record{
			Slug: slug, Profile: "dev", ProjectPath: project, RunID: runID,
			StackstartPID: pid, Outcome: outcome, LogDir: dir, StartedAt: time.Now(),
		}))
	}
	write("p__dev", "20260101-000000-1", OutcomeStopped, 1)
	write("p__dev", "20260102-000000-1", OutcomeFailed, 1)
	// A run still marked running whose stackstart is alive is not finished.
	write("p__dev", "20260103-000000-1", OutcomeRunning, os.Getpid())

	runs, err := LatestFinishedRuns(project, "")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "20260102-000000-1", runs[0].RunID)
	require.Equal(t, OutcomeFailed, runs[0].Outcome)
}

func TestRemoveRecordKeepsRunLogs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, dir, err := NewRun("proj__dev")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.log"), []byte("hello\n"), 0644))
	require.NoError(t, WriteRecord(Record{Slug: "proj__dev", LogDir: dir}))

	RemoveRecord("proj__dev")

	require.NoFileExists(t, SessionRecordPath("proj__dev"))
	require.FileExists(t, filepath.Join(dir, "app.log"), "logs must survive the end of a session")
}
