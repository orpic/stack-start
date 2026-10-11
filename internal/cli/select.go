package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/interpolate"
)

// selectProcesses applies --only and --skip to a validated profile and
// checks that every captured value a remaining process needs from a
// skipped one was given with --set.
func selectProcesses(p config.Profile, name string, only, skip, set []string) (config.Profile, map[string]map[string]string, error) {
	procs := p.Processes
	known := func(n string) error {
		if _, ok := procs[n]; !ok {
			return usageError(fmt.Errorf("process %q not found in profile %q (processes: %s)", n, name, strings.Join(sortedKeys(procs), ", ")))
		}
		return nil
	}

	keep := map[string]bool{}
	if len(only) > 0 {
		var add func(string)
		add = func(n string) {
			if keep[n] {
				return
			}
			keep[n] = true
			for _, d := range procs[n].DependsOn {
				add(d)
			}
		}
		for _, n := range only {
			if err := known(n); err != nil {
				return p, nil, err
			}
			add(n)
		}
	} else {
		for n := range procs {
			keep[n] = true
		}
	}
	skipped := map[string]bool{}
	for _, n := range skip {
		if err := known(n); err != nil {
			return p, nil, err
		}
		delete(keep, n)
		skipped[n] = true
	}
	// Processes left out by --only count as skipped too.
	for n := range procs {
		if !keep[n] {
			skipped[n] = true
		}
	}

	preset := map[string]map[string]string{}
	for _, kv := range set {
		ref, val, ok := strings.Cut(kv, "=")
		proc, capName, ok2 := strings.Cut(ref, ".")
		if !ok || !ok2 {
			return p, nil, usageError(fmt.Errorf("--set %q: expected <process>.<capture>=<value>", kv))
		}
		if preset[proc] == nil {
			preset[proc] = map[string]string{}
		}
		preset[proc][capName] = val
	}

	out := config.Profile{ProjectPath: p.ProjectPath, ResolvedProjectPath: p.ResolvedProjectPath, Ports: p.Ports, Processes: map[string]config.Process{}}
	for _, n := range sortedKeys(procs) {
		if !keep[n] {
			continue
		}
		pc := procs[n]
		var deps []string
		for _, d := range pc.DependsOn {
			if !skipped[d] {
				deps = append(deps, d)
			}
		}
		pc.DependsOn = deps
		values := []string{pc.Cmd}
		for _, v := range pc.Env {
			values = append(values, v)
		}
		for _, v := range values {
			toks, _ := interpolate.Tokenize(v)
			for _, t := range toks {
				proc, capName, ok := strings.Cut(t.Value, ".")
				if t.Literal || !ok || !skipped[proc] {
					continue
				}
				if _, given := preset[proc][capName]; !given {
					return p, nil, usageError(fmt.Errorf("%s uses ${%s}, but %s is not started; pass --set %s=<value>", n, t.Value, proc, t.Value))
				}
			}
		}
		out.Processes[n] = pc
	}
	if len(out.Processes) == 0 {
		return p, nil, usageError(fmt.Errorf("--skip leaves no process to start"))
	}
	return out, preset, nil
}

func sortedKeys(m map[string]config.Process) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
