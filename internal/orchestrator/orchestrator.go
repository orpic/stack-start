package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"

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
	cfg      Config
	registry *captureLib.Registry
	events   chan supervisor.Event
	logger   *slog.Logger
	lock     *session.Lock
	slug     string
	runDir   string
	rec      session.Record

	consoleMu sync.Mutex

	supMu       sync.Mutex
	supervisors map[string]*supervisor.Supervisor
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

	lock, err := session.Acquire(o.slug)
	if err != nil {
		return fmt.Errorf("cannot start: %s", err)
	}
	o.lock = lock
	defer o.lock.Release()

	runID, runDir, err := session.NewRun(o.slug)
	if err != nil {
		return err
	}
	o.runDir = runDir
	_ = session.PruneRuns(o.slug, session.DefaultKeepRuns, runID)

	logClose := o.setupLogger()
	defer logClose()

	padTo := 0
	for name := range o.cfg.Profile.Processes {
		if len(name) > padTo {
			padTo = len(name)
		}
	}

	depGraph := buildDepGraph(o.cfg.Profile.Processes)

	o.rec = session.Record{
		Slug:              o.slug,
		Profile:           o.cfg.ProfileName,
		ProjectPath:       o.cfg.ProjectPath,
		ConfigFile:        o.cfg.ConfigFile,
		StackstartPID:     os.Getpid(),
		StackstartVersion: version.Version,
		RunID:             runID,
		StartedAt:         time.Now(),
		Outcome:           session.OutcomeRunning,
		LogDir:            runDir,
	}

	o.supMu.Lock()
	for _, name := range sortedNames(o.cfg.Profile.Processes) {
		proc := o.cfg.Profile.Processes[name]
		logPath := filepath.Join(runDir, name+".log")
		rawPath := filepath.Join(runDir, name+".raw.log")

		logFile, err := os.Create(logPath)
		if err != nil {
			o.supMu.Unlock()
			return fmt.Errorf("creating log file for %s: %w", name, err)
		}
		defer func() { _ = logFile.Close() }()
		rawFile, err := os.Create(rawPath)
		if err != nil {
			o.supMu.Unlock()
			return fmt.Errorf("creating raw log file for %s: %w", name, err)
		}
		defer func() { _ = rawFile.Close() }()

		var captures []*captureLib.Capture
		for _, capCfg := range proc.Captures {
			c, err := captureLib.New(capCfg.Name, capCfg.Log, capCfg.IsRequired())
			if err != nil {
				o.supMu.Unlock()
				return fmt.Errorf("process %s: invalid capture regex: %w", name, err)
			}
			captures = append(captures, c)
		}

		o.supervisors[name] = &supervisor.Supervisor{
			Name:        name,
			Proc:        proc,
			ProjectPath: o.cfg.ProjectPath,
			Mux:         logmux.New(name, padTo, logFile, os.Stdout, &o.consoleMu),
			Raw:         rawFile,
			Captures:    captures,
			Registry:    o.registry,
			Events:      o.events,
		}
		o.rec.Processes = append(o.rec.Processes, session.ProcessRecord{
			Name:      name,
			LogFile:   logPath,
			RawLog:    rawPath,
			StateFile: filepath.Join(runDir, name+".state"),
		})
	}
	o.supMu.Unlock()

	states := make(map[string]supervisor.State, len(o.cfg.Profile.Processes))
	for name := range o.cfg.Profile.Processes {
		states[name] = supervisor.StatePending
		_ = session.WriteProcessState(o.stateFile(name), string(supervisor.StatePending), 0)
	}
	o.writeRecord()
	defer session.RemoveRecord(o.slug)

	for _, name := range sortedNames(o.cfg.Profile.Processes) {
		if len(depGraph[name]) == 0 {
			if err := o.startProcess(name); err != nil {
				return o.abort(name, err, states)
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			o.logger.Info("shutting down (context cancelled)")
			o.markStopping()
			o.shutdownAll()
			o.finish(session.OutcomeStopped, "", nil, states)
			return nil

		case evt := <-o.events:
			prevState := states[evt.Process]
			states[evt.Process] = evt.State
			pid := o.supervisor(evt.Process).PID()
			_ = session.WriteProcessState(o.stateFile(evt.Process), string(evt.State), pid)
			if evt.State == supervisor.StateStarting {
				o.setPID(evt.Process, pid)
			}

			switch evt.State {
			case supervisor.StateReady:
				o.logger.Info("process ready", "process", evt.Process)
				for _, depName := range sortedNames(o.cfg.Profile.Processes) {
					if states[depName] != supervisor.StatePending {
						continue
					}
					allReady := true
					for _, dep := range depGraph[depName] {
						if states[dep] != supervisor.StateReady {
							allReady = false
							break
						}
					}
					if allReady {
						if err := o.startProcess(depName); err != nil {
							return o.abort(depName, err, states)
						}
					}
				}

				if allSettled(states) {
					o.logger.Info("all processes ready")
					what := fmt.Sprintf("all %d processes ready", len(states))
					if len(states) == 1 {
						what = "1 process ready"
					}
					o.notice(color.New(color.FgGreen), fmt.Sprintf("%s · logs: %s", what, o.runDir))
				}
				if o.allFinished(states) {
					o.logger.Info("all processes have finished")
					o.notice(color.New(color.FgGreen), "all processes finished")
					o.finish(session.OutcomeStopped, "", nil, states)
					return nil
				}

			case supervisor.StateFailed:
				proc := o.cfg.Profile.Processes[evt.Process]
				if proc.IsRequired() {
					return o.abort(evt.Process, evt.Err, states)
				}
				o.logger.Warn("non-required process failed", "process", evt.Process, "error", evt.Err)
				o.notice(color.New(color.FgYellow), fmt.Sprintf("%s failed (not required, continuing): %s",
					evt.Process, evt.Err))

			case supervisor.StateExited:
				proc := o.cfg.Profile.Processes[evt.Process]
				wasReady := prevState == supervisor.StateReady
				if proc.OnExitPolicy() == "fail" && proc.IsRequired() {
					what := fmt.Sprintf("process %s exited before becoming ready", evt.Process)
					if wasReady {
						what = fmt.Sprintf("process %s exited unexpectedly post-ready", evt.Process)
					}
					errMsg := fmt.Errorf("%s (exit 0)", what)
					if evt.Err != nil {
						errMsg = fmt.Errorf("%s: %w", what, evt.Err)
					}
					return o.abort(evt.Process, errMsg, states)
				}
				o.logger.Info("process exited (ignored)", "process", evt.Process)

				if o.allFinished(states) {
					o.logger.Info("all processes have finished")
					o.notice(color.New(color.FgGreen), "all processes finished")
					o.finish(session.OutcomeStopped, "", nil, states)
					return nil
				}
			}
		}
	}
}

// allFinished reports whether nothing is left running or waiting to run: every
// process has exited, failed, or is a oneshot that completed. A profile made
// only of oneshots (migrate, seed, build) then ends instead of waiting forever.
func (o *Orchestrator) allFinished(states map[string]supervisor.State) bool {
	for name, s := range states {
		switch {
		case s == supervisor.StateExited, s == supervisor.StateFailed:
		case s == supervisor.StateReady && o.cfg.Profile.Processes[name].IsOneshot():
		default:
			return false
		}
	}
	return true
}

func allSettled(states map[string]supervisor.State) bool {
	for _, s := range states {
		if s != supervisor.StateReady && s != supervisor.StateExited {
			return false
		}
	}
	return true
}

func (o *Orchestrator) supervisor(name string) *supervisor.Supervisor {
	o.supMu.Lock()
	defer o.supMu.Unlock()
	return o.supervisors[name]
}

func (o *Orchestrator) stateFile(name string) string {
	return filepath.Join(o.runDir, name+".state")
}

func (o *Orchestrator) setPID(name string, pid int) {
	for i := range o.rec.Processes {
		if o.rec.Processes[i].Name == name {
			o.rec.Processes[i].PID = pid
		}
	}
	o.writeRecord()
}

// markStopping records that the stack is shutting down.
func (o *Orchestrator) markStopping() {
	o.rec.Stopping = true
	o.writeRecord()
}

// writeRecord writes the live session record and the run's run.json.
func (o *Orchestrator) writeRecord() {
	if err := session.WriteRecord(o.rec); err != nil {
		o.logger.Error("failed to write session record", "error", err)
	}
	if err := session.WriteRunInfo(o.rec); err != nil {
		o.logger.Error("failed to write run.json", "error", err)
	}
}

// finish records how the run ended in run.json, which outlives the session.
func (o *Orchestrator) finish(outcome, failedProcess string, cause error, states map[string]supervisor.State) {
	now := time.Now()
	o.rec.EndedAt = &now
	o.rec.Outcome = outcome
	o.rec.FailedProcess = failedProcess
	if cause != nil {
		o.rec.Cause = cause.Error()
	}
	for name, st := range states {
		if st != supervisor.StateFailed {
			_ = session.WriteProcessState(o.stateFile(name), string(supervisor.StateExited), o.supervisor(name).PID())
		}
	}
	if err := session.WriteRunInfo(o.rec); err != nil {
		o.logger.Error("failed to write run.json", "error", err)
	}
}

func (o *Orchestrator) startProcess(name string) error {
	sup := o.supervisor(name)

	captures := o.registry.Snapshot()
	processEnv, err := env.Compose(o.cfg.ProjectPath, sup.Proc.Env, captures)
	if err != nil {
		return fmt.Errorf("process %s: env composition failed: %w", name, err)
	}

	envrcEnv, _ := env.LoadEnvrc(o.cfg.ProjectPath)
	refData := interpolate.BuildRefData(captures, envrcEnv)
	resolvedCmd, err := interpolate.Resolve(sup.Proc.Cmd, refData)
	if err != nil {
		return fmt.Errorf("process %s: cmd interpolation failed: %w", name, err)
	}
	sup.Proc.Cmd = resolvedCmd
	sup.Env = processEnv

	for _, tmpl := range sup.Proc.Templates {
		tmplData := buildTemplateData(captures, envrcEnv)
		if err := tmplPkg.Render(o.cfg.ProjectPath, tmpl.Src, tmpl.Dst, tmplData); err != nil {
			return fmt.Errorf("process %s: template render failed: %w", name, err)
		}
	}

	o.logger.Info("starting process", "process", name, "cmd", resolvedCmd)
	return sup.Start(context.Background())
}

func (o *Orchestrator) abort(failedProcess string, err error, states map[string]supervisor.State) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\n--- FAILED: %s ---\n", failedProcess)
	if err != nil {
		fmt.Fprintf(&b, "Cause: %s\n", err)
	}
	if sup := o.supervisor(failedProcess); sup != nil {
		lastLog := sup.CopyLastLogPublic()
		if len(lastLog) > 0 {
			b.WriteString("Last output:\n")
			for _, line := range lastLog {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	fmt.Fprintf(&b, "Full log: %s\n", filepath.Join(o.runDir, failedProcess+".log"))
	fmt.Fprintf(&b, "Read it later with: stackstart logs %s\n", failedProcess)
	fmt.Fprint(os.Stderr, b.String())

	o.markStopping()
	o.shutdownAll()
	o.finish(session.OutcomeFailed, failedProcess, err, states)
	if err != nil {
		return fmt.Errorf("process %s failed: %w", failedProcess, err)
	}
	return fmt.Errorf("process %s failed", failedProcess)
}

// shutdownAll stops every process that is still running, dependents first.
// A process's state does not matter: one that failed readiness may still be
// alive and must be stopped too.
func (o *Orchestrator) shutdownAll() {
	order := reverseDepOrder(o.cfg.Profile.Processes)
	o.logger.Info("shutting down all processes", "order", order)

	for _, name := range order {
		sup := o.supervisor(name)
		if sup == nil || !sup.Alive() {
			continue
		}
		o.logger.Info("stopping process", "process", name)
		sup.Stop(o.cfg.Profile.Processes[name].GracePeriod())
	}
}

// ForceKill immediately SIGKILLs every process group. Safe to call from a
// signal handler while Run is in progress.
func (o *Orchestrator) ForceKill() {
	o.supMu.Lock()
	sups := make([]*supervisor.Supervisor, 0, len(o.supervisors))
	for _, s := range o.supervisors {
		sups = append(sups, s)
	}
	o.supMu.Unlock()
	for _, s := range sups {
		if s.Alive() {
			s.Kill()
		}
	}
}

// notice prints a line from stackstart itself to stderr, keeping stdout for
// process output.
func (o *Orchestrator) notice(c *color.Color, msg string) {
	if o.cfg.Quiet {
		return
	}
	o.consoleMu.Lock()
	defer o.consoleMu.Unlock()
	fmt.Fprintln(os.Stderr, c.Sprint("stackstart: "+msg))
}

func (o *Orchestrator) setupLogger() func() {
	logFile, err := os.Create(filepath.Join(o.runDir, "stackstart.log"))
	if err != nil {
		o.logger = slog.Default()
		return func() {}
	}

	var handler slog.Handler
	if o.cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(logFile, nil)
	} else {
		handler = slog.NewTextHandler(logFile, nil)
	}
	o.logger = slog.New(handler)
	return func() { _ = logFile.Close() }
}

func sortedNames(processes map[string]config.Process) []string {
	names := make([]string, 0, len(processes))
	for name := range processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	var order []string
	visited := make(map[string]bool)

	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		if proc, ok := processes[name]; ok {
			for _, dep := range proc.DependsOn {
				visit(dep)
			}
		}
		order = append(order, name)
	}

	for _, name := range sortedNames(processes) {
		visit(name)
	}

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
