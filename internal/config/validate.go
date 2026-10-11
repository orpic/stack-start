package config

import (
	"fmt"
	"github.com/orpic/stack-start/internal/interpolate"
	"github.com/orpic/stack-start/internal/signals"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func Validate(profile Profile, profileName string) error {
	if len(profile.Processes) == 0 {
		return fmt.Errorf("profile %q has no processes defined", profileName)
	}

	names := make(map[string]bool, len(profile.Processes))
	for name := range profile.Processes {
		names[name] = true
	}

	for name, proc := range profile.Processes {
		if err := validateProcess(name, proc, names, profile); err != nil {
			return err
		}
	}

	if err := detectCycles(profile.Processes); err != nil {
		return err
	}

	if err := validateCaptureRefs(profile.Processes); err != nil {
		return err
	}

	return nil
}

func validateProcess(name string, proc Process, names map[string]bool, profile Profile) error {
	if proc.Kind != "" && proc.Kind != "oneshot" && proc.Kind != "long-running" {
		return fmt.Errorf("process %q: kind must be 'oneshot' or 'long-running', got %q", name, proc.Kind)
	}

	if proc.Cmd == "" {
		return fmt.Errorf("process %q: 'cmd' is required", name)
	}

	if proc.Cwd != "" && filepath.IsAbs(proc.Cwd) {
		return fmt.Errorf("process %q: 'cwd' must be relative to project_path, got absolute path %q", name, proc.Cwd)
	}

	for _, dep := range proc.DependsOn {
		if !names[dep] {
			return fmt.Errorf("process %q: depends_on references unknown process %q", name, dep)
		}
		if dep == name {
			return fmt.Errorf("process %q: cannot depend on itself", name)
		}
	}

	switch proc.Restart {
	case "", "never", "on-failure", "always":
	default:
		return fmt.Errorf("process %q: restart must be 'never', 'on-failure' or 'always', got %q", name, proc.Restart)
	}
	if proc.Retries < 0 {
		return fmt.Errorf("process %q: retries must not be negative", name)
	}
	if proc.Backoff != "" {
		for _, part := range strings.Split(proc.Backoff, "..") {
			if _, err := time.ParseDuration(strings.TrimSpace(part)); err != nil {
				return fmt.Errorf("process %q: backoff must be '<duration>' or '<min>..<max>', got %q", name, proc.Backoff)
			}
		}
	}
	switch proc.OnDepRestart {
	case "", "restart", "ignore":
	default:
		return fmt.Errorf("process %q: on_dep_restart must be 'restart' or 'ignore', got %q", name, proc.OnDepRestart)
	}

	if s := proc.Signals; s != nil {
		if s.Preset == "" && s.Rebuilding == "" && s.Rebuilt == "" && s.RebuildFailed == "" {
			return fmt.Errorf("process %q: signals needs a preset or at least one of rebuilding, rebuilt, rebuild_failed", name)
		}
		if _, err := signals.Resolve(s.Preset, s.Rebuilding, s.Rebuilt, s.RebuildFailed); err != nil {
			return fmt.Errorf("process %q: signals: %w", name, err)
		}
	}
	if h := proc.Health; h != nil {
		if len(h.Checks) == 0 {
			return fmt.Errorf("process %q: health.checks must contain at least one check", name)
		}
		for i, c := range h.Checks {
			if err := validateCheckKind(name, "health", i, c, false); err != nil {
				return err
			}
		}
		if h.Failures < 0 {
			return fmt.Errorf("process %q: health.failures must not be negative", name)
		}
	}

	if proc.OnExit != "" && proc.OnExit != "fail" && proc.OnExit != "ignore" {
		return fmt.Errorf("process %q: on_exit must be 'fail' or 'ignore', got %q", name, proc.OnExit)
	}

	if proc.Readiness != nil {
		if err := validateReadiness(name, proc.Readiness, proc.IsOneshot(), proc.Captures); err != nil {
			return err
		}
	}

	for i, cap := range proc.Captures {
		if err := validateCapture(name, i, cap); err != nil {
			return err
		}
	}

	for i, pattern := range proc.FailOn {
		if pattern == "" {
			return fmt.Errorf("process %q: fail_on[%d] is empty", name, i)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("process %q: fail_on[%d] invalid regex %q: %w", name, i, pattern, err)
		}
	}

	for i, tmpl := range proc.Templates {
		if tmpl.Src == "" {
			return fmt.Errorf("process %q: template[%d] missing 'src'", name, i)
		}
		if tmpl.Dst == "" {
			return fmt.Errorf("process %q: template[%d] missing 'dst'", name, i)
		}
		if filepath.IsAbs(tmpl.Src) {
			return fmt.Errorf("process %q: template[%d] 'src' must be relative, got %q", name, i, tmpl.Src)
		}
		if filepath.IsAbs(tmpl.Dst) {
			return fmt.Errorf("process %q: template[%d] 'dst' must be relative, got %q", name, i, tmpl.Dst)
		}
	}

	return nil
}

func validateReadiness(name string, r *Readiness, isOneshot bool, captures []Capture) error {
	if r.Timeout.Duration <= 0 {
		return fmt.Errorf("process %q: readiness.timeout is required and must be positive", name)
	}

	if r.Mode != "" && r.Mode != "any" && r.Mode != "all" {
		return fmt.Errorf("process %q: readiness.mode must be 'any' or 'all', got %q", name, r.Mode)
	}

	if len(r.Checks) == 0 && !isOneshot {
		return fmt.Errorf("process %q: readiness.checks must contain at least one check", name)
	}

	for i, check := range r.Checks {
		if err := validateCheckKind(name, "readiness", i, check, true); err != nil {
			return err
		}
		if check.Capture != "" {
			found := false
			for _, c := range captures {
				if c.Name == check.Capture {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("process %q: readiness.checks[%d] capture %q is not one of this process's captures", name, i, check.Capture)
			}
		}
		if check.Log != "" {
			if _, err := regexp.Compile(check.Log); err != nil {
				return fmt.Errorf("process %q: readiness.checks[%d] invalid log regex %q: %w", name, i, check.Log, err)
			}
		}
	}

	return nil
}

// validateCheckKind checks that a readiness or health check has exactly one
// kind and that tcp/http values are well formed. Log and capture checks
// only make sense for readiness.
func validateCheckKind(name, where string, i int, check Check, readiness bool) error {
	kinds := "'log', 'tcp', 'capture', 'http', 'exec' or 'file'"
	values := []string{check.Log, check.TCP, check.Capture, check.HTTP, check.Exec, check.File}
	if !readiness {
		kinds = "'tcp', 'http', 'exec' or 'file'"
		if check.Log != "" || check.Capture != "" {
			return fmt.Errorf("process %q: %s.checks[%d] can only use %s", name, where, i, kinds)
		}
	}
	set := 0
	for _, v := range values {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return fmt.Errorf("process %q: %s.checks[%d] must specify one of %s", name, where, i, kinds)
	}
	if set > 1 {
		return fmt.Errorf("process %q: %s.checks[%d] must specify only one of %s", name, where, i, kinds)
	}
	if check.TCP != "" && !strings.Contains(check.TCP, ":") {
		return fmt.Errorf("process %q: %s.checks[%d] tcp must be 'host:port', got %q", name, where, i, check.TCP)
	}
	if check.HTTP != "" && !strings.HasPrefix(check.HTTP, "http://") && !strings.HasPrefix(check.HTTP, "https://") {
		return fmt.Errorf("process %q: %s.checks[%d] http must be an http:// or https:// URL, got %q", name, where, i, check.HTTP)
	}
	if check.Status != 0 && check.HTTP == "" {
		return fmt.Errorf("process %q: %s.checks[%d] status only applies to http checks", name, where, i)
	}
	return nil
}

func validateCapture(name string, idx int, cap Capture) error {
	if cap.Name == "" {
		return fmt.Errorf("process %q: captures[%d] missing 'name'", name, idx)
	}
	if cap.Log == "" {
		return fmt.Errorf("process %q: captures[%d] missing 'log'", name, idx)
	}

	re, err := regexp.Compile(cap.Log)
	if err != nil {
		return fmt.Errorf("process %q: captures[%d] invalid log regex %q: %w", name, idx, cap.Log, err)
	}

	numGroups := re.NumSubexp()
	if numGroups != 1 {
		return fmt.Errorf("process %q: captures[%d] regex must have exactly 1 capturing group, got %d", name, idx, numGroups)
	}

	return nil
}

// detectCycles uses DFS-based topological sort to find cycles in the dependency graph.
func detectCycles(processes map[string]Process) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int, len(processes))
	var path []string

	var visit func(name string) error
	visit = func(name string) error {
		color[name] = gray
		path = append(path, name)

		proc, ok := processes[name]
		if !ok {
			return nil
		}

		for _, dep := range proc.DependsOn {
			switch color[dep] {
			case gray:
				cycleStart := -1
				for i, n := range path {
					if n == dep {
						cycleStart = i
						break
					}
				}
				cycle := append(path[cycleStart:], dep)
				return fmt.Errorf("dependency cycle detected: %s", strings.Join(cycle, " -> "))
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}

		color[name] = black
		path = path[:len(path)-1]
		return nil
	}

	for name := range processes {
		if color[name] == white {
			if err := visit(name); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateCaptureRefs checks that all ${producer.capture} references in env values
// and cmd strings point to actual captures defined on the named process.
func validateCaptureRefs(processes map[string]Process) error {
	captureIndex := make(map[string]map[string]bool)
	for name, proc := range processes {
		caps := make(map[string]bool, len(proc.Captures))
		for _, c := range proc.Captures {
			caps[c.Name] = true
		}
		captureIndex[name] = caps
	}

	for name, proc := range processes {
		var valuesToCheck []string
		valuesToCheck = append(valuesToCheck, proc.Cmd)
		for _, v := range proc.Env {
			valuesToCheck = append(valuesToCheck, v)
		}

		for _, val := range valuesToCheck {
			// Use the same lexer as interpolation, so $$ escapes are honored.
			tokens, err := interpolate.Tokenize(val)
			if err != nil {
				return fmt.Errorf("process %q: %w", name, err)
			}
			for _, tok := range tokens {
				if tok.Literal {
					continue
				}
				ref := tok.Value
				if strings.HasPrefix(ref, "envrc.") {
					continue
				}
				parts := strings.SplitN(ref, ".", 2)
				if len(parts) != 2 {
					return fmt.Errorf("process %q: invalid reference ${%s} (expected ${process.capture})", name, ref)
				}
				producer, capName := parts[0], parts[1]
				caps, ok := captureIndex[producer]
				if !ok {
					return fmt.Errorf("process %q: reference ${%s} refers to unknown process %q", name, ref, producer)
				}
				if !caps[capName] {
					return fmt.Errorf("process %q: reference ${%s} refers to unknown capture %q on process %q",
						name, ref, capName, producer)
				}
			}
		}
	}

	return nil
}
