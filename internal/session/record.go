package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ProcessRecord struct {
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	LogFile   string `json:"log_file"`
	StateFile string `json:"state_file"`
}

type Record struct {
	Slug              string          `json:"slug"`
	Profile           string          `json:"profile"`
	ProjectPath       string          `json:"project_path"`
	ConfigFile        string          `json:"config_file"`
	StackstartPID     int             `json:"stackstart_pid"`
	StackstartVersion string          `json:"stackstart_version"`
	StartedAt         time.Time       `json:"started_at"`
	LogDir            string          `json:"log_dir"`
	Processes         []ProcessRecord `json:"processes"`
}

// WriteRecord writes the session record JSON to disk.
func WriteRecord(rec Record) error {
	dir := filepath.Dir(SessionRecordPath(rec.Slug))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating session dir: %w", err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session record: %w", err)
	}

	return os.WriteFile(SessionRecordPath(rec.Slug), data, 0644)
}

// ReadRecord reads a session record from the given path.
func ReadRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("parsing session record %s: %w", path, err)
	}
	return rec, nil
}

// RemoveRecord deletes the session record, lock, and per-session directory.
func RemoveRecord(slug string) {
	os.Remove(SessionRecordPath(slug))
	os.Remove(SessionLockPath(slug))
	os.RemoveAll(SessionDir(slug))
}

// ReadProcessState reads the state string from a process state file.
func ReadProcessState(stateFile string) string {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return "unknown"
	}
	var s struct {
		State string `json:"state"`
	}
	if json.Unmarshal(data, &s) != nil {
		return "unknown"
	}
	return s.State
}

// WriteProcessState atomically writes a process state file.
func WriteProcessState(stateFile, state string, pid int) error {
	data := fmt.Sprintf(`{"state":%q,"since":%q,"pid":%d}`,
		state, time.Now().Format(time.RFC3339), pid)
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(data), 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, stateFile)
}
