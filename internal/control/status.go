package control

import (
	"time"

	"github.com/orpic/stack-start/internal/env"
)

// StatusSchema is the version of the status document.
const StatusSchema = 1

// StatusDoc is what `status --json` prints.
type StatusDoc struct {
	Schema   int             `json:"schema"`
	Sessions []SessionStatus `json:"sessions"`
}

// SessionStatus is one running (or just finished) stack.
type SessionStatus struct {
	Profile       string    `json:"profile"`
	Project       string    `json:"project"`
	ConfigFile    string    `json:"config_file"`
	RunID         string    `json:"run_id"`
	StackstartPID int       `json:"stackstart_pid"`
	Detached      bool      `json:"detached"`
	KeepGoing     bool      `json:"keep_going"`
	StartedAt     time.Time `json:"started_at"`
	State         string    `json:"state"`
	// Ready: every process is ready (or done, or an optional failure).
	Ready         bool            `json:"ready"`
	LogDir        string          `json:"log_dir"`
	ConsoleLog    string          `json:"console_log,omitempty"`
	Events        EventsInfo      `json:"events"`
	ControlSocket string          `json:"control_socket,omitempty"`
	Git           *GitInfo        `json:"git,omitempty"`
	Ports         map[string]int  `json:"ports,omitempty"`
	Processes     []ProcessStatus `json:"processes"`
}

// EventsInfo locates the event log.
type EventsInfo struct {
	File    string `json:"file"`
	LastSeq int64  `json:"last_seq"`
}

// GitInfo describes the project's checkout.
type GitInfo struct {
	Root      string `json:"root"`
	Worktree  bool   `json:"worktree"`
	Branch    string `json:"branch"`
	Head      string `json:"head"`
	StartHead string `json:"start_head"`
	HeadMoved bool   `json:"head_moved"`
}

// ProcessStatus is one process.
type ProcessStatus struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	State        string            `json:"state"`
	PID          int               `json:"pid,omitempty"`
	Cwd          string            `json:"cwd"`
	DependsOn    []string          `json:"depends_on"`
	BlockedBy    string            `json:"blocked_by,omitempty"`
	Ports        []int             `json:"ports"`
	Captures     map[string]string `json:"captures"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	ReadyAfterMS *int64            `json:"ready_after_ms,omitempty"`
	Restarts     int               `json:"restarts"`
	Attempt      int               `json:"attempt"`
	LastExit     *ExitStatus       `json:"last_exit,omitempty"`
	Cause        string            `json:"cause,omitempty"`
	LogFile      string            `json:"log_file"`
	RawLogFile   string            `json:"raw_log_file"`
	// HealthFailures counts failed health rounds in a row.
	HealthFailures int            `json:"health_failures"`
	Rebuilds       int            `json:"rebuilds"`
	LastRebuild    *RebuildStatus `json:"last_rebuild,omitempty"`
}

// RebuildStatus is the latest rebuild of a process (signals: in the config).
type RebuildStatus struct {
	State      string    `json:"state"` // rebuilding, ok, failed
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// EnvResult is a process's environment, as the running stack resolved it.
type EnvResult struct {
	Process string    `json:"process"`
	Cwd     string    `json:"cwd"`
	Vars    []env.Var `json:"vars"`
	// Secret names variables whose values are shown as ***.
	Secret []string `json:"secret,omitempty"`
}

// ExitStatus is how a process last ended.
type ExitStatus struct {
	Code   int       `json:"code"`
	Signal string    `json:"signal,omitempty"`
	At     time.Time `json:"at"`
}
