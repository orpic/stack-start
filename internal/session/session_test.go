package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSlug(t *testing.T) {
	s := Slug("/Users/me/code/project", "dev")
	require.Equal(t, "Users_me_code_project__dev", s)
}

func TestSlug_Simple(t *testing.T) {
	s := Slug("/project", "minimal")
	require.Equal(t, "project__minimal", s)
}

func TestSessionPaths(t *testing.T) {
	slug := "test__dev"
	dir := SessionDir(slug)
	require.Contains(t, dir, "sessions")
	require.Contains(t, dir, slug)

	recPath := SessionRecordPath(slug)
	require.True(t, filepath.Ext(recPath) == ".json")

	lockPath := SessionLockPath(slug)
	require.True(t, filepath.Ext(lockPath) == ".lock")
}

func TestWriteAndReadRecord(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	rec := Record{
		Slug:              "test__dev",
		Profile:           "dev",
		ProjectPath:       "/test/project",
		ConfigFile:        "/test/project/stackstart.yaml",
		StackstartPID:     12345,
		StackstartVersion: "0.1.0",
		StartedAt:         time.Now(),
		LogDir:            filepath.Join(tmp, "stackstart", "sessions", "test__dev"),
		Processes: []ProcessRecord{
			{Name: "app", PID: 12346, LogFile: "app.log", StateFile: "app.state"},
		},
	}

	err := WriteRecord(rec)
	require.NoError(t, err)

	read, err := ReadRecord(SessionRecordPath("test__dev"))
	require.NoError(t, err)
	require.Equal(t, "dev", read.Profile)
	require.Equal(t, "/test/project", read.ProjectPath)
	require.Equal(t, 12345, read.StackstartPID)
	require.Len(t, read.Processes, 1)
	require.Equal(t, "app", read.Processes[0].Name)
}

func TestWriteAndReadProcessState(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "app.state")

	err := WriteProcessState(stateFile, "ready", 1234)
	require.NoError(t, err)

	state := ReadProcessState(stateFile)
	require.Equal(t, "ready", state)
}

func TestReadProcessState_Missing(t *testing.T) {
	state := ReadProcessState("/nonexistent/path.state")
	require.Equal(t, "unknown", state)
}

func TestRemoveRecord(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	slug := "cleanup__test"
	sessDir := SessionDir(slug)
	require.NoError(t, os.MkdirAll(sessDir, 0755))
	require.NoError(t, os.WriteFile(SessionRecordPath(slug), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(SessionLockPath(slug), []byte(""), 0644))

	RemoveRecord(slug)

	_, err := os.Stat(SessionRecordPath(slug))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(SessionLockPath(slug))
	require.True(t, os.IsNotExist(err))
}
