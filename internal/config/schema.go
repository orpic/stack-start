package config

import (
	"fmt"
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
	Cwd             string            `yaml:"cwd"`
	Cmd             string            `yaml:"cmd"`
	Env             map[string]string `yaml:"env,omitempty"`
	DependsOn       []string          `yaml:"depends_on,omitempty"`
	Readiness       *Readiness        `yaml:"readiness,omitempty"`
	Captures        []Capture         `yaml:"captures,omitempty"`
	Templates       []Template        `yaml:"templates,omitempty"`
	OnExit          string            `yaml:"on_exit,omitempty"`
	Required        *bool             `yaml:"required,omitempty"`
	StopGracePeriod Duration          `yaml:"stop_grace_period,omitempty"`
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
}

type Capture struct {
	Name     string `yaml:"name"`
	Log      string `yaml:"log"`
	Required *bool  `yaml:"required,omitempty"`
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
