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
	RawLog    string `json:"raw_log_file,omitempty"`
	StateFile string `json:"state_file"`
}

// Record describes one run of a profile. While the run is live it is the
// session record (<slug>.json); a copy is kept in the run directory as
// run.json and outlives the session.
type Record struct {
	Slug              string     `json:"slug"`
	Profile           string     `json:"profile"`
	ProjectPath       string     `json:"project_path"`
	ConfigFile        string     `json:"config_file"`
	StackstartPID     int        `json:"stackstart_pid"`
	StackstartVersion string     `json:"stackstart_version"`
	RunID             string     `json:"run_id,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
	FailedProcess     string     `json:"failed_process,omitempty"`
	Cause             string     `json:"cause,omitempty"`
	// Stopping is set once the stack has started to shut down, so a second
	// 'down' waits instead of signalling again (which would force-kill).
	Stopping bool `json:"stopping,omitempty"`
	// ControlSocket is where the running stack accepts commands.
	ControlSocket string `json:"control_socket,omitempty"`
	// Detached is set for stacks started with up -d.
	Detached bool `json:"detached,omitempty"`
	// ConsoleLog is the console output of a detached stack.
	ConsoleLog string `json:"console_log,omitempty"`
	// EventsFile is the run's events.jsonl.
	EventsFile string          `json:"events_file,omitempty"`
	LogDir     string          `json:"log_dir"`
	Processes  []ProcessRecord `json:"processes"`
}

// WriteRecord writes the session record JSON to disk atomically, so readers
// in other shells never see a half-written file.
func WriteRecord(rec Record) error {
	dir := filepath.Dir(SessionRecordPath(rec.Slug))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating session dir: %w", err)
	}
	if err := writeJSONAtomic(SessionRecordPath(rec.Slug), rec); err != nil {
		return fmt.Errorf("writing session record: %w", err)
	}
	return nil
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

// RemoveRecord deletes the live session record and lock. Run directories,
// with their logs, are kept; see PruneRuns and `stackstart clean`.
func RemoveRecord(slug string) {
	_ = os.Remove(SessionRecordPath(slug))
	_ = os.Remove(SessionLockPath(slug))
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
