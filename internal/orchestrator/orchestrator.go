package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	captureLib "github.com/orpic/stack-start/internal/capture"
	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/env"
	"github.com/orpic/stack-start/internal/interpolate"
	"github.com/orpic/stack-start/internal/logmux"
	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/supervisor"
	tmplPkg "github.com/orpic/stack-start/internal/template"
	"github.com/orpic/stack-start/internal/version"
)

// Config holds all the parameters needed to run the orchestrator.
type Config struct {
	Profile     config.Profile
	ProfileName string
	ProjectPath string
	ConfigFile  string
	LogFormat   string
	Quiet       bool
}

// Orchestrator manages the lifecycle of all processes in a profile.
type Orchestrator struct {
	cfg         Config
	supervisors map[string]*supervisor.Supervisor
	registry    *captureLib.Registry
	events      chan supervisor.Event
	consoleMu   sync.Mutex
	logger      *slog.Logger
	lock        *session.Lock
	slug        string
}

func New(cfg Config) *Orchestrator {
	return &Orchestrator{
		cfg:         cfg,
		supervisors: make(map[string]*supervisor.Supervisor),
		registry:    captureLib.NewRegistry(),
		events:      make(chan supervisor.Event, 64),
	}
}

// Run is the main orchestration loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.slug = session.Slug(o.cfg.ProjectPath, o.cfg.ProfileName)

	// Setup logger
	o.setupLogger()

	// Acquire session lock
	lock, err := session.Acquire(o.slug)
	if err != nil {
		return fmt.Errorf("cannot start: %s", err)
	}
	o.lock = lock
	defer o.cleanup()

	// Create session directory for logs
	sessDir := session.SessionDir(o.slug)
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}

	// Compute longest process name for padding
	padTo := 0
	for name := range o.cfg.Profile.Processes {
		if len(name) > padTo {
			padTo = len(name)
		}
	}

	// Build dependency graph
	depGraph := buildDepGraph(o.cfg.Profile.Processes)

	// Build supervisors
	for name, proc := range o.cfg.Profile.Processes {
		logFile, err := os.Create(filepath.Join(sessDir, name+".log"))
		if err != nil {
			return fmt.Errorf("creating log file for %s: %w", name, err)
		}
		defer logFile.Close()

		mux := logmux.New(name, padTo, logFile, os.Stdout, &o.consoleMu)

		var captures []*captureLib.Capture
		for _, capCfg := range proc.Captures {
			cap, err := captureLib.New(capCfg.Name, capCfg.Log, capCfg.IsRequired())
			if err != nil {
				return fmt.Errorf("process %s: invalid capture regex: %w", name, err)
			}
			captures = append(captures, cap)
		}

		o.supervisors[name] = &supervisor.Supervisor{
			Name:        name,
			Proc:        proc,
			ProjectPath: o.cfg.ProjectPath,
			Mux:         mux,
			Captures:    captures,
			Registry:    o.registry,
			Events:      o.events,
		}
	}

	// Write session record
	rec := o.buildSessionRecord(sessDir)
	if err := session.WriteRecord(rec); err != nil {
		o.logger.Error("failed to write session record", "error", err)
	}

	// Track process states
	states := make(map[string]supervisor.State, len(o.cfg.Profile.Processes))
	for name := range o.cfg.Profile.Processes {
		states[name] = supervisor.StatePending
		stateFile := filepath.Join(sessDir, name+".state")
		_ = session.WriteProcessState(stateFile, string(supervisor.StatePending), 0)
	}

	// Start root processes (those with no dependencies)
	for name := range o.cfg.Profile.Processes {
		if len(depGraph[name]) == 0 {
			if err := o.startProcess(ctx, name, sessDir); err != nil {
				return o.abort(ctx, name, err, sessDir, states)
			}
		}
	}

	// Event loop
	for {
		select {
		case <-ctx.Done():
			o.logger.Info("shutting down (context cancelled)")
			o.shutdownAll(sessDir, states)
			return nil

		case evt := <-o.events:
			prevState := states[evt.Process]
			states[evt.Process] = evt.State
			stateFile := filepath.Join(sessDir, evt.Process+".state")
			pid := 0
			if sup, ok := o.supervisors[evt.Process]; ok {
				pid = sup.PID()
			}
			_ = session.WriteProcessState(stateFile, string(evt.State), pid)

			switch evt.State {
			case supervisor.StateReady:
				o.logger.Info("process ready", "process", evt.Process)
				for depName, deps := range depGraph {
					if states[depName] != supervisor.StatePending {
						continue
					}
					allReady := true
					for _, dep := range deps {
						if states[dep] != supervisor.StateReady {
							allReady = false
							break
						}
					}
					if allReady {
						if err := o.startProcess(ctx, depName, sessDir); err != nil {
							return o.abort(ctx, depName, err, sessDir, states)
						}
					}
				}

				allReady := true
				for _, s := range states {
					if s != supervisor.StateReady && s != supervisor.StateExited {
						allReady = false
						break
					}
				}
				if allReady {
					o.logger.Info("all processes ready")
				}

			case supervisor.StateFailed:
				proc := o.cfg.Profile.Processes[evt.Process]
				if proc.IsRequired() {
					return o.abort(ctx, evt.Process, evt.Err, sessDir, states)
				}
				o.logger.Warn("non-required process failed", "process", evt.Process, "error", evt.Err)

			case supervisor.StateExited:
				proc := o.cfg.Profile.Processes[evt.Process]
				wasReady := prevState == supervisor.StateReady
				if proc.OnExitPolicy() == "fail" && proc.IsRequired() {
					errMsg := evt.Err
					if wasReady {
						msg := fmt.Sprintf("process %s exited unexpectedly post-ready", evt.Process)
						if evt.Err != nil {
							errMsg = fmt.Errorf("%s: %w", msg, evt.Err)
						} else {
							errMsg = fmt.Errorf("%s (exit 0)", msg)
						}
					} else {
						if errMsg == nil {
							errMsg = fmt.Errorf("process %s exited before becoming ready", evt.Process)
						}
					}
					return o.abort(ctx, evt.Process, errMsg, sessDir, states)
				}
				o.logger.Info("process exited (ignored)", "process", evt.Process)

				allExited := true
				for _, s := range states {
					if s != supervisor.StateExited && s != supervisor.StateFailed {
						allExited = false
						break
					}
				}
				if allExited {
					o.logger.Info("all processes have exited")
					return nil
				}
			}
		}
	}
}

func (o *Orchestrator) startProcess(ctx context.Context, name, sessDir string) error {
	sup := o.supervisors[name]

	// Compose environment
	captures := o.registry.Snapshot()
	processEnv, err := env.Compose(o.cfg.ProjectPath, sup.Proc.Env, captures)
	if err != nil {
		return fmt.Errorf("process %s: env composition failed: %w", name, err)
	}

	// Resolve cmd interpolation
	envrcEnv, _ := env.LoadEnvrc(o.cfg.ProjectPath)
	refData := interpolate.BuildRefData(captures, envrcEnv)
	resolvedCmd, err := interpolate.Resolve(sup.Proc.Cmd, refData)
	if err != nil {
		return fmt.Errorf("process %s: cmd interpolation failed: %w", name, err)
	}
	sup.Proc = config.Process{
		Cwd:             sup.Proc.Cwd,
		Cmd:             resolvedCmd,
		Env:             sup.Proc.Env,
		DependsOn:       sup.Proc.DependsOn,
		Readiness:       sup.Proc.Readiness,
		Captures:        sup.Proc.Captures,
		Templates:       sup.Proc.Templates,
		OnExit:          sup.Proc.OnExit,
		Required:        sup.Proc.Required,
		StopGracePeriod: sup.Proc.StopGracePeriod,
	}
	sup.Env = processEnv

	// Render templates before starting
	for _, tmpl := range sup.Proc.Templates {
		tmplData := buildTemplateData(captures, envrcEnv)
		if err := tmplPkg.Render(o.cfg.ProjectPath, tmpl.Src, tmpl.Dst, tmplData); err != nil {
			return fmt.Errorf("process %s: template render failed: %w", name, err)
		}
	}

	o.logger.Info("starting process", "process", name, "cmd", resolvedCmd)
	return sup.Start(ctx)
}

func (o *Orchestrator) abort(ctx context.Context, failedProcess string, err error, sessDir string, states map[string]supervisor.State) error {
	// Print root cause
	msg := fmt.Sprintf("\n--- FAILED: %s ---\n", failedProcess)
	if err != nil {
		msg += fmt.Sprintf("Cause: %s\n", err)
	}
	if sup, ok := o.supervisors[failedProcess]; ok {
		lastLog := sup.CopyLastLogPublic()
		if len(lastLog) > 0 {
			msg += "Last output:\n"
			for _, line := range lastLog {
				msg += fmt.Sprintf("  %s\n", line)
			}
		}
	}
	fmt.Fprint(os.Stderr, msg)

	o.shutdownAll(sessDir, states)
	if err != nil {
		return fmt.Errorf("process %s failed: %w", failedProcess, err)
	}
	return fmt.Errorf("process %s failed", failedProcess)
}

func (o *Orchestrator) shutdownAll(sessDir string, states map[string]supervisor.State) {
	// Compute reverse dependency order
	order := reverseDepOrder(o.cfg.Profile.Processes)

	o.logger.Info("shutting down all processes", "order", order)

	for _, name := range order {
		sup, ok := o.supervisors[name]
		if !ok {
			continue
		}
		state := states[name]
		if state == supervisor.StateExited || state == supervisor.StateFailed || state == supervisor.StatePending {
			continue
		}

		proc := o.cfg.Profile.Processes[name]
		o.logger.Info("stopping process", "process", name)
		sup.Stop(proc.GracePeriod())

		stateFile := filepath.Join(sessDir, name+".state")
		_ = session.WriteProcessState(stateFile, string(supervisor.StateExited), sup.PID())
	}
}

func (o *Orchestrator) cleanup() {
	if o.lock != nil {
		o.lock.Release()
	}
	session.RemoveRecord(o.slug)
}

func (o *Orchestrator) setupLogger() {
	sessDir := session.SessionDir(o.slug)
	os.MkdirAll(sessDir, 0755)

	logPath := filepath.Join(sessDir, "stackstart.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		o.logger = slog.Default()
		return
	}

	var handler slog.Handler
	if o.cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(logFile, nil)
	} else {
		handler = slog.NewTextHandler(logFile, nil)
	}
	o.logger = slog.New(handler)
}

func (o *Orchestrator) buildSessionRecord(sessDir string) session.Record {
	var procs []session.ProcessRecord
	for name := range o.cfg.Profile.Processes {
		procs = append(procs, session.ProcessRecord{
			Name:      name,
			LogFile:   filepath.Join(sessDir, name+".log"),
			StateFile: filepath.Join(sessDir, name+".state"),
		})
	}

	return session.Record{
		Slug:              o.slug,
		Profile:           o.cfg.ProfileName,
		ProjectPath:       o.cfg.ProjectPath,
		ConfigFile:        o.cfg.ConfigFile,
		StackstartPID:     os.Getpid(),
		StackstartVersion: version.Version,
		StartedAt:         time.Now(),
		LogDir:            sessDir,
		Processes:         procs,
	}
}

// buildDepGraph returns a map of process -> list of dependencies.
func buildDepGraph(processes map[string]config.Process) map[string][]string {
	graph := make(map[string][]string, len(processes))
	for name, proc := range processes {
		graph[name] = proc.DependsOn
	}
	return graph
}

// reverseDepOrder computes a shutdown order (reverse topological).
func reverseDepOrder(processes map[string]config.Process) []string {
	// Topological sort
	var order []string
	visited := make(map[string]bool)

	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		proc, ok := processes[name]
		if ok {
			for _, dep := range proc.DependsOn {
				visit(dep)
			}
		}
		order = append(order, name)
	}

	for name := range processes {
		visit(name)
	}

	// Reverse for shutdown order (dependents first)
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}

	return order
}

func buildTemplateData(captures map[string]map[string]string, envrcEnv map[string]string) map[string]any {
	data := make(map[string]any)
	for proc, caps := range captures {
		procData := make(map[string]any)
		for k, v := range caps {
			procData[k] = v
		}
		data[proc] = procData
	}
	if len(envrcEnv) > 0 {
		envrcData := make(map[string]any)
		for k, v := range envrcEnv {
			envrcData[k] = v
		}
		data["envrc"] = envrcData
	}
	return data
}
