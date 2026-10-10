package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Case is one parsed case.yaml.
type Case struct {
	ID        string
	Dir       string
	Name      string            `yaml:"name"`
	Roadmap   string            `yaml:"roadmap"`
	Covers    []string          `yaml:"covers"`
	Requires  []string          `yaml:"requires"`
	Platforms []string          `yaml:"platforms"`
	Project   string            `yaml:"project"`
	Exclusive *bool             `yaml:"exclusive"`
	Home      string            `yaml:"home"`
	Timeout   string            `yaml:"timeout"`
	Ports     []string          `yaml:"ports"`
	Env       map[string]string `yaml:"env"`
	Notes     string            `yaml:"notes"`
	Steps     []map[string]any  `yaml:"steps"`

	raw []byte
}

func (c *Case) IsExclusive() bool {
	if c.Exclusive != nil {
		return *c.Exclusive
	}
	return c.Project != ""
}

func (c *Case) HomeMode() string {
	if c.Home != "" {
		return c.Home
	}
	if c.Project != "" {
		return "real"
	}
	return "isolated"
}

func (c *Case) TimeoutDuration() (time.Duration, error) {
	if c.Timeout == "" {
		return 60 * time.Second, nil
	}
	return time.ParseDuration(c.Timeout)
}

// loadCase reads a case header. Steps are parsed again after variables are
// substituted (see parseSteps).
func loadCase(casesDir, dir string) (*Case, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "case.yaml"))
	if err != nil {
		return nil, err
	}
	c := &Case{raw: raw, Dir: dir}
	if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("parsing case.yaml: %w", err)
	}
	rel, _ := filepath.Rel(casesDir, dir)
	c.ID = filepath.ToSlash(rel)
	if c.Name == "" {
		return nil, fmt.Errorf("case.yaml: 'name' is required")
	}
	if len(c.Steps) == 0 {
		return nil, fmt.Errorf("case.yaml: 'steps' is required")
	}
	if _, err := c.TimeoutDuration(); err != nil {
		return nil, fmt.Errorf("case.yaml: timeout: %w", err)
	}
	switch c.HomeMode() {
	case "isolated", "real":
	default:
		return nil, fmt.Errorf("case.yaml: home must be 'isolated' or 'real', got %q", c.Home)
	}
	for _, p := range c.Ports {
		if !portName.MatchString(p) {
			return nil, fmt.Errorf("case.yaml: invalid port name %q", p)
		}
	}
	return c, nil
}

var portName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// discover finds every directory under casesDir that holds a case.yaml.
func discover(casesDir string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(casesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "case.yaml" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	return dirs, err
}

// varPattern matches only the runner's own variable names, so stackstart's
// ${process.capture} references pass through untouched.
var varPattern = regexp.MustCompile(`\$\{(BIN|WORK|CASE|ROOT|TOKEN|STATE|HOME|PORT_[A-Za-z0-9_]+)\}`)

func substitute(s string, vars map[string]string) string {
	return varPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

// parseSteps re-parses case.yaml after substituting variables in its text.
func (c *Case) parseSteps(vars map[string]string) ([]map[string]any, map[string]string, error) {
	var again struct {
		Env   map[string]string `yaml:"env"`
		Steps []map[string]any  `yaml:"steps"`
	}
	if err := yaml.Unmarshal([]byte(substitute(string(c.raw), vars)), &again); err != nil {
		return nil, nil, fmt.Errorf("parsing case.yaml after substitution: %w", err)
	}
	return again.Steps, again.Env, nil
}

func platformOK(c *Case, goos string) bool {
	if len(c.Platforms) == 0 {
		return true
	}
	for _, p := range c.Platforms {
		if strings.EqualFold(p, goos) {
			return true
		}
	}
	return false
}
