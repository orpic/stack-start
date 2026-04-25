package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
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

	if proc.OnExit != "" && proc.OnExit != "fail" && proc.OnExit != "ignore" {
		return fmt.Errorf("process %q: on_exit must be 'fail' or 'ignore', got %q", name, proc.OnExit)
	}

	if proc.Readiness != nil {
		if err := validateReadiness(name, proc.Readiness); err != nil {
			return err
		}
	}

	for i, cap := range proc.Captures {
		if err := validateCapture(name, i, cap); err != nil {
			return err
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

func validateReadiness(name string, r *Readiness) error {
	if r.Timeout.Duration <= 0 {
		return fmt.Errorf("process %q: readiness.timeout is required and must be positive", name)
	}

	if r.Mode != "" && r.Mode != "any" && r.Mode != "all" {
		return fmt.Errorf("process %q: readiness.mode must be 'any' or 'all', got %q", name, r.Mode)
	}

	if len(r.Checks) == 0 {
		return fmt.Errorf("process %q: readiness.checks must contain at least one check", name)
	}

	for i, check := range r.Checks {
		if check.Log == "" && check.TCP == "" {
			return fmt.Errorf("process %q: readiness.checks[%d] must specify either 'log' or 'tcp'", name, i)
		}
		if check.Log != "" && check.TCP != "" {
			return fmt.Errorf("process %q: readiness.checks[%d] must specify only one of 'log' or 'tcp'", name, i)
		}
		if check.Log != "" {
			if _, err := regexp.Compile(check.Log); err != nil {
				return fmt.Errorf("process %q: readiness.checks[%d] invalid log regex %q: %w", name, i, check.Log, err)
			}
		}
		if check.TCP != "" {
			if !strings.Contains(check.TCP, ":") {
				return fmt.Errorf("process %q: readiness.checks[%d] tcp must be 'host:port', got %q", name, i, check.TCP)
			}
		}
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

	refPattern := regexp.MustCompile(`\$\{([^}]+)\}`)

	for name, proc := range processes {
		var valuesToCheck []string
		valuesToCheck = append(valuesToCheck, proc.Cmd)
		for _, v := range proc.Env {
			valuesToCheck = append(valuesToCheck, v)
		}

		for _, val := range valuesToCheck {
			matches := refPattern.FindAllStringSubmatch(val, -1)
			for _, m := range matches {
				ref := m[1]
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
