package env

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LoadEnvrc resolves the .envrc at the given project path.
// If direnv is on PATH, shells out to it. Otherwise falls back to a strict native parser.
func LoadEnvrc(projectPath string) (map[string]string, error) {
	envrcPath := findEnvrc(projectPath)
	if envrcPath == "" {
		return nil, nil
	}

	if direnvPath, err := exec.LookPath("direnv"); err == nil {
		return loadViaDirenv(direnvPath, projectPath)
	}

	return loadNative(envrcPath)
}

func findEnvrc(projectPath string) string {
	dir := projectPath
	for {
		candidate := filepath.Join(dir, ".envrc")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func loadViaDirenv(direnvPath, projectPath string) (map[string]string, error) {
	cmd := exec.Command(direnvPath, "exec", projectPath, "env", "-0")
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("direnv exec failed: %w", err)
	}

	result := make(map[string]string)
	for _, entry := range bytes.Split(out, []byte{0}) {
		s := string(entry)
		if s == "" {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if ok {
			result[k] = v
		}
	}
	return result, nil
}

func loadNative(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !strings.HasPrefix(line, "export ") {
			return nil, fmt.Errorf(
				"%s:%d: unsupported directive %q - "+
					"this .envrc requires direnv to evaluate; "+
					"install direnv via 'brew install direnv' and run 'direnv allow'",
				path, lineNum, line)
		}

		rest := strings.TrimPrefix(line, "export ")
		rest = strings.TrimSpace(rest)

		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: invalid export line: %s", path, lineNum, line)
		}

		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = unquote(v)
		result[k] = v
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return result, nil
}
