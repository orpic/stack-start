package config

import (
	"fmt"
	"strings"
	"time"
)

type File struct {
	Profiles map[string]Profile `yaml:"profiles"`
}

type Profile struct {
	ProjectPath         string             `yaml:"project_path,omitempty"`
	Processes           map[string]Process `yaml:"processes"`
	ResolvedProjectPath string             `yaml:"-"`
}

type Process struct {
	Kind            string            `yaml:"kind,omitempty"`
	Cwd             string            `yaml:"cwd"`
	Cmd             string            `yaml:"cmd"`
	Env             map[string]string `yaml:"env,omitempty"`
	DependsOn       []string          `yaml:"depends_on,omitempty"`
	Readiness       *Readiness        `yaml:"readiness,omitempty"`
	Captures        []Capture         `yaml:"captures,omitempty"`
	FailOn          []string          `yaml:"fail_on,omitempty"`
	Templates       []Template        `yaml:"templates,omitempty"`
	OnExit          string            `yaml:"on_exit,omitempty"`
	Required        *bool             `yaml:"required,omitempty"`
	StopGracePeriod Duration          `yaml:"stop_grace_period,omitempty"`
	// Restart is the retry policy: never (default), on-failure, always.
	Restart string `yaml:"restart,omitempty"`
	// Retries is how many failed attempts in a row are retried before the
	// process counts as failed. 0 with a restart policy means no limit.
	Retries int `yaml:"retries,omitempty"`
	// Backoff is the delay between attempts, "min..max", doubling each time.
	Backoff string `yaml:"backoff,omitempty"`
	// OnDepRestart is what happens to this process when a process it depends
	// on is restarted by its retry policy: restart (default; only when a
	// captured value it uses changed) or ignore.
	OnDepRestart string `yaml:"on_dep_restart,omitempty"`
	// SecretEnv names env variables whose values are shown as *** in status,
	// events and env output.
	SecretEnv []string `yaml:"secret_env,omitempty"`
	// Signals turn output lines into rebuild events.
	Signals *Signals `yaml:"signals,omitempty"`
	// Health re-checks a ready process; failing checks mark it degraded.
	Health *Health `yaml:"health,omitempty"`
	// Inputs (oneshots): globs, relative to cwd. When the command and these
	// files are unchanged since the last successful run, the oneshot is
	// skipped.
	Inputs []string `yaml:"inputs,omitempty"`
	// Daemons (kind: daemon): the command starts the real process in the
	// background and exits. Pidfile (required) names its PID; StopCmd stops
	// it (else SIGTERM); LogFile is its own log, followed into stackstart's.
	Pidfile string `yaml:"pidfile,omitempty"`
	StopCmd string `yaml:"stop_cmd,omitempty"`
	LogFile string `yaml:"log_file,omitempty"`
}

// Signals recognise rebuilds in a process's output. Preset fills in the
// patterns for a known tool; explicit patterns override it.
type Signals struct {
	Preset        string `yaml:"preset,omitempty"`
	Rebuilding    string `yaml:"rebuilding,omitempty"`
	Rebuilt       string `yaml:"rebuilt,omitempty"`
	RebuildFailed string `yaml:"rebuild_failed,omitempty"`
}

// Health checks run every Interval once the process is ready.
type Health struct {
	Interval Duration `yaml:"interval,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty"`
	// Failures is how many failed rounds in a row make it degraded.
	Failures int     `yaml:"failures,omitempty"`
	Checks   []Check `yaml:"checks"`
}

// RestartPolicy returns the retry policy, defaulting to never.
func (p Process) RestartPolicy() string {
	if p.Restart == "" {
		return "never"
	}
	return p.Restart
}

// BackoffRange returns the delay before the first retry and the maximum.
func (p Process) BackoffRange() (min, max time.Duration) {
	min, max = time.Second, 30*time.Second
	if p.Backoff == "" {
		return
	}
	lo, hi, found := strings.Cut(p.Backoff, "..")
	if d, err := time.ParseDuration(strings.TrimSpace(lo)); err == nil {
		min = d
		if !found {
			max = d
		}
	}
	if found {
		if d, err := time.ParseDuration(strings.TrimSpace(hi)); err == nil {
			max = d
		}
	}
	return
}

func (p Process) IsOneshot() bool {
	return p.Kind == "oneshot"
}

func (p Process) IsRequired() bool {
	if p.Required == nil {
		return true
	}
	return *p.Required
}

func (p Process) OnExitPolicy() string {
	if p.OnExit == "" {
		return "fail"
	}
	return p.OnExit
}

func (p Process) GracePeriod() time.Duration {
	if p.StopGracePeriod.Duration == 0 {
		return 10 * time.Second
	}
	return p.StopGracePeriod.Duration
}

type Readiness struct {
	Timeout Duration `yaml:"timeout"`
	Mode    string   `yaml:"mode,omitempty"`
	Checks  []Check  `yaml:"checks"`
}

func (r Readiness) CheckMode() string {
	if r.Mode == "" {
		return "all"
	}
	return r.Mode
}

type Check struct {
	Log string `yaml:"log,omitempty"`
	TCP string `yaml:"tcp,omitempty"`
	// HTTP is a URL that must answer with Status (default any 2xx/3xx).
	HTTP   string `yaml:"http,omitempty"`
	Status int    `yaml:"status,omitempty"`
	// Exec is a shell command, run in the process's cwd and env, that must
	// exit 0 (e.g. pg_isready).
	Exec string `yaml:"exec,omitempty"`
	// File is a path (relative to the process's cwd) that must exist.
	File string `yaml:"file,omitempty"`
	// Capture names one of this process's captures; the check passes once
	// that capture has a value.
	Capture string `yaml:"capture,omitempty"`
}

type Capture struct {
	Name     string `yaml:"name"`
	Log      string `yaml:"log"`
	Required *bool  `yaml:"required,omitempty"`
	// Secret hides the value (***) in status, events and env output.
	Secret bool `yaml:"secret,omitempty"`
}

func (c Capture) IsRequired() bool {
	if c.Required == nil {
		return true
	}
	return *c.Required
}

type Template struct {
	Src string `yaml:"src"`
	Dst string `yaml:"dst"`
}

// Duration wraps time.Duration for YAML unmarshalling of strings like "30s", "1m".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.String(), nil
}

type ProfileEntry struct {
	Name     string
	FilePath string
}
