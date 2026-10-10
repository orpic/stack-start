package env

import (
	"fmt"
	"os"
	"path/filepath"
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
	envMap := make(map[string]string)

	// Layer 1: inherited shell env
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			envMap[k] = v
		}
	}
	for k, v := range NonInteractiveDefaults {
		envMap[k] = v
	}

	// Layer 2: .envrc
	envrcEnv, err := LoadEnvrc(projectPath)
	if err != nil {
		return nil, fmt.Errorf("loading .envrc: %w", err)
	}
	for k, v := range envrcEnv {
		envMap[k] = v
	}

	// Layer 3: .env file
	dotenvPath := filepath.Join(projectPath, ".env")
	if dotenvEnv, err := LoadDotenv(dotenvPath); err == nil {
		for k, v := range dotenvEnv {
			envMap[k] = v
		}
	}

	// Layer 4+5: per-process env with reference resolution
	refData := interpolate.BuildRefData(captures, envrcEnv)
	for k, v := range processEnv {
		resolved, err := interpolate.Resolve(v, refData)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		envMap[k] = resolved
	}

	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result, nil
}
