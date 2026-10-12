package cli

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/interpolate"
	"github.com/orpic/stack-start/internal/signals"
)

// describeDoc is the resolved profile as data (describe --json).
type describeDoc struct {
	Schema      int                `json:"schema"`
	Profile     string             `json:"profile"`
	Project     string             `json:"project"`
	ConfigFile  string             `json:"config_file"`
	Ports       map[string]int     `json:"ports"`
	StartOrder  [][]string         `json:"start_order"`
	Processes   []describedProcess `json:"processes"`
	Commands    []string           `json:"commands"`
	Running     bool               `json:"running"`
	Description string             `json:"description"`
}

type describedProcess struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Cmd          string            `json:"cmd"`
	Cwd          string            `json:"cwd"`
	DependsOn    []string          `json:"depends_on"`
	Dependents   []string          `json:"dependents"`
	Required     bool              `json:"required"`
	OnExit       string            `json:"on_exit"`
	Readiness    *describedReady   `json:"readiness,omitempty"`
	Captures     []describedCap    `json:"captures"`
	Uses         []string          `json:"uses"`
	FailOn       []string          `json:"fail_on"`
	Restart      describedRestart  `json:"restart"`
	Signals      *signals.Patterns `json:"signals,omitempty"`
	SignalPreset string            `json:"signal_preset,omitempty"`
	Health       *config.Health    `json:"health,omitempty"`
	Inputs       []string          `json:"inputs,omitempty"`
	Ports        []int             `json:"ports"`
	Env          []string          `json:"env"`
}

type describedReady struct {
	Timeout string   `json:"timeout"`
	Mode    string   `json:"mode"`
	Checks  []string `json:"checks"`
}

type describedCap struct {
	Name     string `json:"name"`
	Log      string `json:"log"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
}

type describedRestart struct {
	Policy       string `json:"policy"`
	Retries      int    `json:"retries"`
	Backoff      string `json:"backoff"`
	OnDepRestart string `json:"on_dep_restart"`
}

func newDescribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe <profile>",
		Short: "Explain a profile: its processes, start order, checks, captures and policies",
		Long: `Describe the resolved profile without running anything: every process with
its command, working directory, dependencies (both ways), readiness checks,
captured values and who uses them, fail_on patterns, retry policy, rebuild
signals, health checks and ports, plus the order processes start in.
--json prints it as a stable document for agents.`,
		Example: `  stackstart describe dev
  stackstart describe dev --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			name := args[0]
			profile, file, err := resolveProfile(name)
			if err != nil {
				return err
			}
			doc := describeProfile(name, file, profile)
			if _, err := findLive(name, ""); err == nil {
				doc.Running = true
			}
			if asJSON {
				return printJSON(doc)
			}
			fmt.Printf("Profile %q from %s\nProject: %s\n", name, file, doc.Project)
			if len(doc.Ports) > 0 {
				var ps []string
				for _, k := range sortedPortKeys(doc.Ports) {
					ps = append(ps, fmt.Sprintf("%s=%d", k, doc.Ports[k]))
				}
				fmt.Printf("Ports: %s\n", strings.Join(ps, ", "))
			}
			fmt.Println("Start order:")
			for i, wave := range doc.StartOrder {
				fmt.Printf("  %d. %s\n", i+1, strings.Join(wave, ", "))
			}
			for _, p := range doc.Processes {
				fmt.Printf("\n%s (%s)\n  cmd: %s\n", p.Name, p.Kind, p.Cmd)
				if p.Cwd != "" {
					fmt.Printf("  cwd: %s\n", p.Cwd)
				}
				if len(p.DependsOn) > 0 {
					fmt.Printf("  after: %s\n", strings.Join(p.DependsOn, ", "))
				}
				if len(p.Dependents) > 0 {
					fmt.Printf("  needed by: %s\n", strings.Join(p.Dependents, ", "))
				}
				switch {
				case p.Readiness != nil && len(p.Readiness.Checks) > 0:
					fmt.Printf("  ready when (%s, within %s): %s\n", p.Readiness.Mode, p.Readiness.Timeout, strings.Join(p.Readiness.Checks, "; "))
				case p.Readiness != nil:
					fmt.Printf("  done when it exits 0, within %s\n", p.Readiness.Timeout)
				case p.Kind == "oneshot":
					fmt.Println("  done when it exits 0")
				default:
					fmt.Println("  ready as soon as it starts")
				}
				for _, c := range p.Captures {
					fmt.Printf("  captures %s: /%s/\n", c.Name, c.Log)
				}
				if len(p.Uses) > 0 {
					fmt.Printf("  uses: %s\n", strings.Join(p.Uses, ", "))
				}
				if len(p.FailOn) > 0 {
					fmt.Printf("  fails on: %s\n", strings.Join(p.FailOn, "; "))
				}
				if p.Restart.Policy != "never" {
					fmt.Printf("  restart: %s (retries %d, backoff %s)\n", p.Restart.Policy, p.Restart.Retries, p.Restart.Backoff)
				}
				if p.SignalPreset != "" {
					fmt.Printf("  rebuild signals: %s\n", p.SignalPreset)
				} else if p.Signals != nil {
					fmt.Println("  rebuild signals: custom")
				}
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "print JSON")
	return cmd
}

func sortedPortKeys(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func describeProfile(name, file string, profile config.Profile) describeDoc {
	doc := describeDoc{
		Schema: 1, Profile: name, Project: profile.ResolvedProjectPath, ConfigFile: file,
		Ports: profile.Ports, Processes: []describedProcess{},
		Commands: []string{"up", "up -d", "status --json", "logs", "restart", "stop", "start", "wait", "events", "exec", "env", "send", "down"},
	}
	if doc.Ports == nil {
		doc.Ports = map[string]int{}
	}
	names := sortedKeys(profile.Processes)
	dependents := map[string][]string{}
	for _, n := range names {
		for _, d := range profile.Processes[n].DependsOn {
			dependents[d] = append(dependents[d], n)
		}
	}
	// Start order in waves: a process starts once everything before it is ready.
	placed := map[string]bool{}
	for len(placed) < len(names) {
		var wave []string
		for _, n := range names {
			if placed[n] {
				continue
			}
			ok := true
			for _, d := range profile.Processes[n].DependsOn {
				if !placed[d] {
					ok = false
				}
			}
			if ok {
				wave = append(wave, n)
			}
		}
		if len(wave) == 0 {
			break // a cycle; validation reports it
		}
		for _, n := range wave {
			placed[n] = true
		}
		doc.StartOrder = append(doc.StartOrder, wave)
	}
	subst, _ := config.SubstitutePorts(profile, profile.Ports)
	for _, n := range names {
		pc := subst.Processes[n]
		kind := pc.Kind
		if kind == "" {
			kind = "long-running"
		}
		minB, maxB := pc.BackoffRange()
		dp := describedProcess{
			Name: n, Kind: kind, Cmd: pc.Cmd, Cwd: pc.Cwd,
			DependsOn: nonNil(pc.DependsOn), Dependents: nonNil(dependents[n]),
			Required: pc.IsRequired(), OnExit: pc.OnExitPolicy(),
			Captures: []describedCap{}, Uses: []string{}, FailOn: nonNil(pc.FailOn),
			Restart: describedRestart{Policy: pc.RestartPolicy(), Retries: pc.Retries, Backoff: fmt.Sprintf("%s..%s", minB, maxB), OnDepRestart: orDefault(pc.OnDepRestart, "restart")},
			Health:  pc.Health, Inputs: pc.Inputs, Ports: []int{}, Env: []string{},
		}
		if pc.Readiness != nil {
			r := &describedReady{Timeout: pc.Readiness.Timeout.String(), Mode: pc.Readiness.CheckMode(), Checks: []string{}}
			for _, c := range pc.Readiness.Checks {
				r.Checks = append(r.Checks, checkString(c))
			}
			dp.Readiness = r
		}
		for _, c := range pc.Captures {
			dp.Captures = append(dp.Captures, describedCap{Name: c.Name, Log: c.Log, Required: c.IsRequired(), Secret: c.Secret})
		}
		for _, m := range refPattern(pc) {
			dp.Uses = append(dp.Uses, "${"+m+"}")
		}
		if s := pc.Signals; s != nil {
			if pat, err := signals.Resolve(s.Preset, s.Rebuilding, s.Rebuilt, s.RebuildFailed); err == nil {
				dp.Signals = &pat
			}
			dp.SignalPreset = s.Preset
		}
		for k := range pc.Env {
			dp.Env = append(dp.Env, k)
		}
		sort.Strings(dp.Env)
		dp.Ports = localCheckPorts(pc)
		doc.Processes = append(doc.Processes, dp)
	}
	doc.Description = fmt.Sprintf("%d processes starting in %d steps", len(names), len(doc.StartOrder))
	return doc
}

func checkString(c config.Check) string {
	switch {
	case c.Log != "":
		return "log /" + c.Log + "/"
	case c.TCP != "":
		return "tcp " + c.TCP
	case c.Capture != "":
		return "capture " + c.Capture
	case c.HTTP != "":
		if c.Status != 0 {
			return fmt.Sprintf("http %s (status %d)", c.HTTP, c.Status)
		}
		return "http " + c.HTTP
	case c.Exec != "":
		return "exec " + c.Exec
	case c.File != "":
		return "file " + c.File
	}
	return ""
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// refPattern lists the captured values a process uses (process.capture).
func refPattern(pc config.Process) []string {
	seen := map[string]bool{}
	var out []string
	values := []string{pc.Cmd}
	for _, v := range pc.Env {
		values = append(values, v)
	}
	for _, v := range values {
		toks, _ := interpolate.Tokenize(v)
		for _, t := range toks {
			if t.Literal || strings.HasPrefix(t.Value, "envrc.") || seen[t.Value] {
				continue
			}
			seen[t.Value] = true
			out = append(out, t.Value)
		}
	}
	sort.Strings(out)
	return out
}

// localCheckPorts are the ports of a process's tcp and http readiness checks.
func localCheckPorts(pc config.Process) []int {
	out := []int{}
	if pc.Readiness == nil {
		return out
	}
	for _, c := range pc.Readiness.Checks {
		addr := c.TCP
		if c.HTTP != "" {
			if u, err := url.Parse(c.HTTP); err == nil && u.Port() != "" {
				addr = u.Host
			}
		}
		if _, p, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}
