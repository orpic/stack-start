package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultKeepRuns is how many runs per session are kept on disk. Older runs
// are pruned when a new one starts.
const DefaultKeepRuns = 10

// Run outcomes recorded in run.json.
const (
	OutcomeRunning = "running"
	OutcomeStopped = "stopped"
	OutcomeFailed  = "failed"
)

// RunsDir returns the directory holding every run of a session.
func RunsDir(slug string) string {
	return filepath.Join(SessionDir(slug), "runs")
}

// RunInfoPath returns the path of a run's run.json.
func RunInfoPath(runDir string) string {
	return filepath.Join(runDir, "run.json")
}

// NewRun creates a run directory for a new `up` of the session and points
// the session's `latest` link at it. Run IDs sort by start time.
func NewRun(slug string) (runID, runDir string, err error) {
	runID = fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), os.Getpid())
	runDir = filepath.Join(RunsDir(slug), runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return "", "", fmt.Errorf("creating run directory: %w", err)
	}

	latest := filepath.Join(SessionDir(slug), "latest")
	tmp := latest + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("runs", runID), tmp); err == nil {
		_ = os.Rename(tmp, latest)
	}
	return runID, runDir, nil
}

// PruneRuns deletes the oldest runs of a session so at most keep remain.
// The run named keepRunID is never deleted.
func PruneRuns(slug string, keep int, keepRunID string) error {
	entries, err := os.ReadDir(RunsDir(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var ids []string
	allowed := keep
	for _, e := range entries {
		switch {
		case !e.IsDir():
		case e.Name() == keepRunID:
			allowed--
		default:
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	for len(ids) > allowed && len(ids) > 0 {
		_ = os.RemoveAll(filepath.Join(RunsDir(slug), ids[0]))
		ids = ids[1:]
	}
	return nil
}

// WriteRunInfo writes the run's record to run.json inside its directory.
// Unlike the session record it outlives the session.
func WriteRunInfo(rec Record) error {
	return writeJSONAtomic(RunInfoPath(rec.LogDir), rec)
}

// ReadRunInfo reads a run.json.
func ReadRunInfo(runDir string) (Record, error) {
	return ReadRecord(RunInfoPath(runDir))
}

// ListRuns returns every run on disk for every session, newest first.
func ListRuns() ([]Record, error) {
	slugs, err := os.ReadDir(SessionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var runs []Record
	for _, s := range slugs {
		if !s.IsDir() {
			continue
		}
		entries, err := os.ReadDir(RunsDir(s.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			rec, err := ReadRunInfo(filepath.Join(RunsDir(s.Name()), e.Name()))
			if err != nil {
				continue
			}
			runs = append(runs, rec)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RunID > runs[j].RunID })
	return runs, nil
}

// LatestFinishedRuns returns, for each session matching cwd (and profile, if
// set), its most recent run that is no longer running.
func LatestFinishedRuns(cwd, profileFilter string) ([]Record, error) {
	runs, err := ListRuns()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []Record
	for _, r := range runs {
		if seen[r.Slug] || r.Outcome == OutcomeRunning && isProcessAlive(r.StackstartPID) {
			continue
		}
		if profileFilter != "" && r.Profile != profileFilter {
			continue
		}
		if !MatchesProject(cwd, r.ProjectPath) {
			continue
		}
		seen[r.Slug] = true
		out = append(out, r)
	}
	return out, nil
}

// MatchesProject reports whether a command run from cwd refers to a stack
// whose project root is projectPath: cwd is inside projectPath and no nested
// git repository or worktree lies between them. Without the second rule, a
// worktree at <repo>/.worktrees/x would control the main checkout's stack.
func MatchesProject(cwd, projectPath string) bool {
	cwd = filepath.Clean(cwd)
	projectPath = filepath.Clean(projectPath)
	if cwd != projectPath && !strings.HasPrefix(cwd, projectPath+string(os.PathSeparator)) {
		return false
	}
	for dir := cwd; dir != projectPath; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return false
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return true
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
