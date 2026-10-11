package env

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orpic/stack-start/internal/interpolate"
)

// NonInteractiveDefaults are set for every process, over the inherited shell
// env. Processes run on a terminal nobody is typing into, so a tool that
// opens a pager because it sees a terminal (psql, git, mysql, man,
// systemctl) would wait for a keypress forever. .envrc, .env and the env:
// block can still override them.
var NonInteractiveDefaults = map[string]string{
	"PAGER":         "cat",
	"GIT_PAGER":     "cat",
	"PSQL_PAGER":    "cat",
	"MANPAGER":      "cat",
	"SYSTEMD_PAGER": "",
}

// Var is one variable of a composed environment and where it came from.
type Var struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"` // shell, default, .envrc, .env, env
	// Overrode lists the earlier layers whose value this one replaced.
	Overrode []string `json:"overrode,omitempty"`
}

// Compose builds the final environment for a process by layering:
// 1. Inherited shell env, then NonInteractiveDefaults
// 2. .envrc resolved env
// 3. .env file
// 4. Per-process env: block (with ${...} interpolation)
func Compose(
	projectPath string,
	processEnv map[string]string,
	captures map[string]map[string]string,
) ([]string, error) {
	vars, err := ComposeExplained(projectPath, processEnv, captures, os.Environ())
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(vars))
	for _, v := range vars {
		result = append(result, v.Name+"="+v.Value)
	}
	return result, nil
}

// ComposeExplained is Compose starting from base (KEY=value entries), and
// says which layer set each variable. The result is sorted by name.
func ComposeExplained(
	projectPath string,
	processEnv map[string]string,
	captures map[string]map[string]string,
	base []string,
) ([]Var, error) {
	vars := make(map[string]*Var)
	set := func(k, v, source string) {
		if cur, ok := vars[k]; ok {
			if cur.Value != v || cur.Source != source {
				cur.Overrode = append(cur.Overrode, cur.Source)
			}
			cur.Value, cur.Source = v, source
			return
		}
		vars[k] = &Var{Name: k, Value: v, Source: source}
	}

	// Layer 1: inherited shell env
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v, "shell")
		}
	}
	for k, v := range NonInteractiveDefaults {
		set(k, v, "default")
	}

	// Layer 2: .envrc
	envrcEnv, err := LoadEnvrc(projectPath)
	if err != nil {
		return nil, fmt.Errorf("loading .envrc: %w", err)
	}
	for k, v := range envrcEnv {
		set(k, v, ".envrc")
	}

	// Layer 3: .env file
	dotenvPath := filepath.Join(projectPath, ".env")
	dotenvEnv, err := LoadDotenv(dotenvPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("loading .env: %w", err)
	}
	for k, v := range dotenvEnv {
		set(k, v, ".env")
	}

	// Layer 4+5: per-process env with reference resolution
	refData := interpolate.BuildRefData(captures, envrcEnv)
	for k, v := range processEnv {
		resolved, err := interpolate.Resolve(v, refData)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		set(k, resolved, "env")
	}

	out := make([]Var, 0, len(vars))
	for _, v := range vars {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
