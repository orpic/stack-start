// Package orchestrator runs a profile: it starts processes in dependency
// order, reacts to their readiness, failures and exits, and accepts commands
// (start, stop, restart, send, status, ...) on a control socket while the
// stack runs. One goroutine (the loop) owns all process state; everything
// else talks to it through channels.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"

	captureLib "github.com/orpic/stack-start/internal/capture"
	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/events"
	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/supervisor"
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
	// KeepGoing keeps the stack up when a required process fails: its
	// dependents are blocked and everything else keeps running.
	KeepGoing bool
	// Detached marks a stack started with up -d.
	Detached bool
	// Console receives process output and Notices stackstart's own lines.
	// They default to stdout and stderr. OpenConsole, if set, is called once
	// the run directory exists and replaces both (detached stacks write
	// their console to a file in the run directory).
	Console     io.Writer
	Notices     io.Writer
	OpenConsole func(runDir string) (io.Writer, string, error)
}

// ErrForceKilled is returned when the stack was killed on request (a second
// Ctrl-C or down --force); the CLI exits 130.
var ErrForceKilled = errors.New("killed")

// Process states.
const (
	sPending  = "pending"
	sBlocked  = "blocked"
	sStarting = "starting"
	sReady    = "ready"
	sBackoff  = "backoff"
	sStopping = "stopping"
	sStopped  = "stopped"
	sFailed   = "failed"
	sExited   = "exited"
)

// proc is the loop's view of one process.
type proc struct {
	name string
	cfg  config.Process
	sup  *supervisor.Supervisor
	// gen increases every time the process is started or its instance is
	// retired, so events from an old instance are recognised and ignored.
	gen       int
	state     string
	attempt   int // consecutive failed attempts (reset when ready)
	restarts  int
	pid       int
	startedAt time.Time
	readyAt   time.Time
	lastExit  *control.ExitStatus
	cause     string
	lastLines []string
	blockedBy string
	// expectStop marks a stop we asked for, so its exit is not a crash.
	expectStop bool
	// recovering is set while the retry policy brings the process back, so
	// dependents are restarted if a captured value they use changed.
	recovering bool
	// inputs are the captured values the process was started with.
	inputs     map[string]string
	retryTimer *time.Timer

	logPath string
	rawPath string
	logFile *os.File
	rawFile *os.File
}

// Orchestrator manages the lifecycle of all processes in a profile.
type Orchestrator struct {
	cfg      Config
	registry *captureLib.Registry
	logger   *slog.Logger
	lock     *session.Lock
	slug     string
	runDir   string
	rec      session.Record
	ev       *events.Log
	console  io.Writer
	notices  io.Writer
	padTo    int
	startGit *control.GitInfo

	consoleMu sync.Mutex

	// procMu guards the procs map's supervisors for ForceKill, which runs
	// outside the loop.
	procMu sync.Mutex
	procs  map[string]*proc
	order  []string // sorted names

	supEvents chan supervisor.Event
	cmds      chan *command
	retries   chan retryTick
	stopDone  chan int
	plans     map[int]func()
	nextPlan  int

	listener   net.Listener
	socketPath string
	done       chan struct{} // closed when the run is over

	stopping atomic.Bool // set once the stack has started to shut down

	announced bool
	allReady  bool
	finished  bool
	result    error
}

type retryTick struct {
	name string
	gen  int
}

func New(cfg Config) *Orchestrator {
	return &Orchestrator{
		cfg:       cfg,
		registry:  captureLib.NewRegistry(),
		procs:     make(map[string]*proc),
		supEvents: make(chan supervisor.Event, 256),
		cmds:      make(chan *command),
		retries:   make(chan retryTick, 16),
		stopDone:  make(chan int, 16),
		plans:     make(map[int]func()),
	}
}

// Run starts the stack and returns when it has stopped.
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

	o.console, o.notices = o.cfg.Console, o.cfg.Notices
	if o.console == nil {
		o.console = os.Stdout
	}
	if o.notices == nil {
		o.notices = os.Stderr
	}
	consoleLog := ""
	if o.cfg.OpenConsole != nil {
		w, path, err := o.cfg.OpenConsole(runDir)
		if err != nil {
			return err
		}
		o.console, o.notices, consoleLog = w, w, path
	}

	evPath := filepath.Join(runDir, "events.jsonl")
	o.ev, err = events.Open(evPath)
	if err != nil {
		return err
	}
	defer o.ev.Close()

	if err := o.listen(); err != nil {
		return err
	}
	defer o.closeListener()

	o.order = sortedNames(o.cfg.Profile.Processes)
	for _, name := range o.order {
		if len(name) > o.padTo {
			o.padTo = len(name)
		}
	}
	o.startGit = gitInfo(o.cfg.ProjectPath, "")

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
		ControlSocket:     o.socketPath,
		Detached:          o.cfg.Detached,
		ConsoleLog:        consoleLog,
		EventsFile:        evPath,
	}

	for _, name := range o.order {
		p := &proc{name: name, cfg: o.cfg.Profile.Processes[name], state: sPending}
		p.logPath = filepath.Join(runDir, name+".log")
		p.rawPath = filepath.Join(runDir, name+".raw.log")
		if p.logFile, err = os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			return fmt.Errorf("creating log file for %s: %w", name, err)
		}
		if p.rawFile, err = os.OpenFile(p.rawPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			return fmt.Errorf("creating raw log file for %s: %w", name, err)
		}
		o.procs[name] = p
		o.rec.Processes = append(o.rec.Processes, session.ProcessRecord{
			Name:      name,
			LogFile:   p.logPath,
			RawLog:    p.rawPath,
			StateFile: o.stateFile(name),
		})
		_ = session.WriteProcessState(o.stateFile(name), sPending, 0)
	}
	defer func() {
		for _, p := range o.procs {
			_ = p.logFile.Close()
			_ = p.rawFile.Close()
		}
	}()
	o.writeRecord()
	defer session.RemoveRecord(o.slug)

	o.ev.Emit("run_started", "", map[string]any{
		"profile": o.cfg.ProfileName, "project": o.cfg.ProjectPath, "run_id": runID,
		"stackstart_version": version.Version, "detached": o.cfg.Detached, "keep_going": o.cfg.KeepGoing,
	})
	go o.serve()

	o.schedule()
	o.check()

	for !o.finished {
		select {
		case <-ctx.Done():
			o.gracefulShutdown("signal")
		case e := <-o.supEvents:
			o.handleSupEvent(e)
		case c := <-o.cmds:
			o.handleCommand(c)
		case t := <-o.retries:
			o.handleRetry(t)
		case id := <-o.stopDone:
			if then := o.plans[id]; then != nil {
				delete(o.plans, id)
				then()
			}
		}
		if !o.finished {
			o.check()
		}
	}
	return o.result
}

// Stopping reports whether the stack has started to shut down (from a
// signal, down, or a failure).
func (o *Orchestrator) Stopping() bool { return o.stopping.Load() }

// ForceKill immediately SIGKILLs every process group. Safe to call from a
// signal handler while Run is in progress.
func (o *Orchestrator) ForceKill() {
	o.procMu.Lock()
	sups := make([]*supervisor.Supervisor, 0, len(o.procs))
	for _, p := range o.procs {
		if p.sup != nil {
			sups = append(sups, p.sup)
		}
	}
	o.procMu.Unlock()
	for _, s := range sups {
		if s.Alive() {
			s.Kill()
		}
	}
}

// notice prints a line from stackstart itself, keeping stdout for process
// output.
func (o *Orchestrator) notice(c *color.Color, msg string) {
	if o.cfg.Quiet {
		return
	}
	o.consoleMu.Lock()
	defer o.consoleMu.Unlock()
	_, _ = fmt.Fprintln(o.notices, c.Sprint("stackstart: "+msg))
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

func (o *Orchestrator) stateFile(name string) string {
	return filepath.Join(o.runDir, name+".state")
}

// setState changes a process's state and records it.
func (o *Orchestrator) setState(p *proc, state string) {
	p.state = state
	_ = session.WriteProcessState(o.stateFile(p.name), state, p.pid)
}

// writeRecord writes the live session record and the run's run.json.
func (o *Orchestrator) writeRecord() {
	for i := range o.rec.Processes {
		if p := o.procs[o.rec.Processes[i].Name]; p != nil {
			o.rec.Processes[i].PID = p.pid
		}
	}
	if err := session.WriteRecord(o.rec); err != nil {
		o.logger.Error("failed to write session record", "error", err)
	}
	if err := session.WriteRunInfo(o.rec); err != nil {
		o.logger.Error("failed to write run.json", "error", err)
	}
}

func sortedNames(processes map[string]config.Process) []string {
	names := make([]string, 0, len(processes))
	for name := range processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// dependents returns every process that depends on name, directly or not.
func (o *Orchestrator) dependents(name string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		for _, other := range o.order {
			if seen[other] {
				continue
			}
			for _, d := range o.procs[other].cfg.DependsOn {
				if d == n {
					seen[other] = true
					walk(other)
					break
				}
			}
		}
	}
	walk(name)
	var out []string
	for _, n := range o.order {
		if seen[n] {
			out = append(out, n)
		}
	}
	return out
}

// stopOrder sorts names so dependents come before what they depend on.
func (o *Orchestrator) stopOrder(names []string) []string {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var order []string
	for _, n := range reverseDepOrder(o.cfg.Profile.Processes) {
		if want[n] {
			order = append(order, n)
		}
	}
	return order
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
