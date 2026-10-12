package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	captureLib "github.com/orpic/stack-start/internal/capture"
	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/logmux"
	"github.com/orpic/stack-start/internal/output"
	"github.com/orpic/stack-start/internal/readiness"
	"github.com/orpic/stack-start/internal/signals"
)

// State represents the lifecycle state of a supervised process.
type State string

const (
	StatePending  State = "pending"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateFailed   State = "failed"
	StateExited   State = "exited"
)

// readinessPoll is how often readiness is re-evaluated when no output
// arrives, so background checks (tcp) pass even for silent processes.
const readinessPoll = 100 * time.Millisecond

const lastLogLines = 10

// Event is emitted when a supervised process changes state.
type Event struct {
	Process string
	// Gen identifies which start of the process the event belongs to, so
	// events from a process that was since restarted can be ignored.
	Gen     int
	State   State
	Err     error
	LastLog []string
	// Exit is set on events caused by the process exiting.
	Exit *ExitInfo
}

// ExitInfo describes how a process ended.
type ExitInfo struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

// Supervisor manages a single child process.
type Supervisor struct {
	Name        string
	Proc        config.Process
	ProjectPath string
	Mux         *logmux.Mux
	// Raw, if set, receives the process's output bytes exactly as written.
	Raw      io.Writer
	Captures []*captureLib.Capture
	Registry *captureLib.Registry
	Events   chan<- Event
	Env      []string
	// Gen is copied into every event (see Event.Gen).
	Gen int
	// Notify, if set, receives things worth an event-log entry that are not
	// state changes: "capture", "screen_cleared", "fail_on_warning".
	Notify func(typ string, fields map[string]any)

	cmd       *exec.Cmd
	ptyMaster *os.File
	pid       atomic.Int64
	done      chan struct{}
	pumpDone  chan struct{}

	logMu   sync.Mutex
	lastLog []string

	eval       *readiness.Evaluator
	lineChecks []readiness.LineEvaluator
	failOn     []*regexp.Regexp

	// Rebuild detection (after ready).
	readyRes      []*regexp.Regexp
	rebuilding    *regexp.Regexp
	rebuilt       *regexp.Regexp
	rebuildFailed *regexp.Regexp
	rebuiltAfter  *regexp.Regexp // ends a rebuild only if one started
	inRebuild     bool
	rebuildStart  time.Time

	// settled is set once the startup outcome (ready, or failed before
	// ready) has been decided, so it is decided exactly once.
	settled atomic.Bool
	ready   atomic.Bool
	failed  atomic.Bool

	readinessCancel context.CancelFunc
	exit            *ExitInfo

	daemonPID      atomic.Int64
	daemonLogStart int64
	// stopReq is set when a daemon is asked to stop, possibly before its
	// PID is known; launcherDone closes when its launcher has exited.
	stopReq      atomic.Bool
	stopGrace    atomic.Int64
	launcherDone chan struct{}
}

// Start spawns the child process under a PTY and starts reading its output,
// evaluating readiness, and waiting for it to exit.
func (s *Supervisor) Start(ctx context.Context) error {
	cwd := s.ProjectPath
	if s.Proc.Cwd != "" {
		cwd = s.ProjectPath + "/" + s.Proc.Cwd
	}

	if err := s.buildChecks(); err != nil {
		s.emitEvent(StateFailed, err)
		return err
	}

	if s.isDaemon() && s.Proc.LogFile != "" {
		if st, err := os.Stat(s.path(s.Proc.LogFile)); err == nil {
			s.daemonLogStart = st.Size()
		}
	}

	s.cmd = exec.Command("/bin/sh", "-c", s.Proc.Cmd)
	s.cmd.Dir = cwd
	s.cmd.Env = s.Env

	var ptyMaster *os.File
	var err error
	if s.isDaemon() {
		ptyMaster, err = spawnDetached(s.cmd)
	} else {
		ptyMaster, err = spawnPTY(s.cmd)
	}
	if err != nil {
		err = fmt.Errorf("failed to spawn: %w", err)
		s.emitEvent(StateFailed, err)
		return err
	}
	s.ptyMaster = ptyMaster
	s.pid.Store(int64(s.cmd.Process.Pid))
	s.done = make(chan struct{})
	s.pumpDone = make(chan struct{})
	s.launcherDone = make(chan struct{})

	s.emitEvent(StateStarting, nil)

	var readinessCtx context.Context
	if s.Proc.Readiness != nil && s.Proc.Readiness.Timeout.Duration > 0 {
		readinessCtx, s.readinessCancel = context.WithTimeout(ctx, s.Proc.Readiness.Timeout.Duration)
	} else {
		readinessCtx, s.readinessCancel = context.WithCancel(ctx)
	}

	for _, c := range s.eval.Checks {
		if bc, ok := c.(readiness.Backgrounder); ok {
			go bc.Run(readinessCtx)
		}
	}

	go s.pump()
	go s.readinessLoop(readinessCtx)
	go s.waitLoop()

	return nil
}

func (s *Supervisor) buildChecks() error {
	var checks []readiness.Check
	if s.Proc.Readiness != nil {
		for _, chk := range s.Proc.Readiness.Checks {
			switch {
			case chk.Log != "":
				lc, err := readiness.NewLogRegexCheck(chk.Log)
				if err != nil {
					return fmt.Errorf("invalid log regex: %w", err)
				}
				checks = append(checks, lc)
				s.lineChecks = append(s.lineChecks, lc)
				s.readyRes = append(s.readyRes, lc.Pattern)
			case chk.TCP != "":
				checks = append(checks, readiness.NewTCPCheck(chk.TCP))
			case chk.HTTP != "":
				checks = append(checks, &readiness.ProbeCheck{Desc: "http " + chk.HTTP, Probe: readiness.HTTPProbe(chk.HTTP, chk.Status)})
			case chk.Exec != "":
				checks = append(checks, &readiness.ProbeCheck{Desc: "exec " + chk.Exec, Probe: readiness.ExecProbe(chk.Exec, s.cwd(), s.Env), Interval: 500 * time.Millisecond})
			case chk.File != "":
				path := chk.File
				if !filepath.IsAbs(path) {
					path = filepath.Join(s.cwd(), path)
				}
				checks = append(checks, &readiness.ProbeCheck{Desc: "file " + chk.File, Probe: readiness.FileProbe(path)})
			case chk.Capture != "":
				capture := s.findCapture(chk.Capture)
				if capture == nil {
					return fmt.Errorf("readiness check refers to unknown capture %q", chk.Capture)
				}
				checks = append(checks, readiness.FuncCheck{Desc: "capture " + chk.Capture, Fn: capture.Done})
			}
		}
	}
	if s.isDaemon() {
		// A daemon is not ready before its pidfile names a running process.
		checks = append(checks, readiness.FuncCheck{Desc: "pidfile " + s.Proc.Pidfile, Fn: func() bool { return s.daemonPID.Load() != 0 }})
	}
	s.eval = readiness.NewEvaluator(s.readinessMode(), checks)

	if sig := s.Proc.Signals; sig != nil {
		pat, err := signals.Resolve(sig.Preset, sig.Rebuilding, sig.Rebuilt, sig.RebuildFailed)
		if err != nil {
			return err
		}
		compile := func(re string) *regexp.Regexp {
			if re == "" {
				return nil
			}
			return regexp.MustCompile(re)
		}
		s.rebuilding, s.rebuilt, s.rebuildFailed = compile(pat.Rebuilding), compile(pat.Rebuilt), compile(pat.RebuildFailed)
		s.rebuiltAfter = compile(pat.RebuiltAfterStart)
	}

	for _, p := range s.Proc.FailOn {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid fail_on regex: %w", err)
		}
		s.failOn = append(s.failOn, re)
	}
	return nil
}

func (s *Supervisor) findCapture(name string) *captureLib.Capture {
	for _, c := range s.Captures {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// pump reads the PTY until it closes. It never stops early: a process whose
// output is not drained blocks on its next write.
func (s *Supervisor) pump() {
	defer close(s.pumpDone)
	output.Reader{Raw: s.Raw}.Run(s.ptyMaster, s.handleLine)
}

func (s *Supervisor) handleLine(l output.Line) {
	if l.Cleared {
		s.Mux.Marker("screen cleared")
		s.notify("screen_cleared", nil)
		// Watchers clear the screen when they start a rebuild.
		if s.ready.Load() && !s.inRebuild {
			s.startRebuild("screen_clear", "")
		}
	}
	if l.Cleared && l.Plain == "" {
		return
	}
	s.Mux.Write(l)
	s.appendLastLog(l.Plain)

	line := []byte(l.Plain)

	if s.ready.Load() {
		for _, re := range s.failOn {
			if re.Match(line) {
				s.Mux.Marker(fmt.Sprintf("warning: matched fail_on /%s/ after the process was ready", re))
				s.notify("fail_on_warning", map[string]any{"pattern": re.String(), "line": l.Plain})
				break
			}
		}
		s.detectRebuild(l.Plain)
		return
	}
	if s.settled.Load() {
		return
	}

	for _, lc := range s.lineChecks {
		lc.Evaluate(line)
	}
	for _, c := range s.Captures {
		if matched, val := c.Match(line); matched {
			s.Registry.Store(s.Name, c.Name, val)
			s.notify("capture", map[string]any{"name": c.Name, "value": val})
		}
	}
	for _, re := range s.failOn {
		if re.Match(line) {
			s.fail(fmt.Errorf("matched fail_on /%s/: %s", re, strings.TrimSpace(l.Plain)))
			return
		}
	}
	// Evaluate synchronously so a ready line is reported before an exit
	// that follows it.
	s.tryReady()
}

// readinessLoop re-evaluates readiness on a timer, so background checks pass
// for silent processes, and fails the process when the timeout expires
// whatever it is doing.
func (s *Supervisor) readinessLoop(ctx context.Context) {
	ticker := time.NewTicker(readinessPoll)
	defer ticker.Stop()
	s.tryReady()
	for {
		if s.settled.Load() {
			return
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				s.fail(s.timeoutError())
			}
			return
		case <-ticker.C:
			s.tryReady()
		}
	}
}

func (s *Supervisor) timeoutError() error {
	var waiting []string
	pending := s.eval.Pending()
	if s.Proc.Readiness.CheckMode() == "any" && len(pending) > 0 {
		waiting = append(waiting, "any of: "+strings.Join(pending, ", "))
	} else {
		waiting = append(waiting, pending...)
	}
	for _, c := range s.Captures {
		if c.Required && !c.Done() && !containsString(pending, "capture "+c.Name) {
			waiting = append(waiting, "capture "+c.Name)
		}
	}
	msg := fmt.Sprintf("readiness timeout after %s", s.Proc.Readiness.Timeout.Duration)
	if s.Proc.IsOneshot() && len(waiting) == 0 {
		return fmt.Errorf("%s (oneshot process still running)", msg)
	}
	if len(waiting) > 0 {
		msg += "; still waiting for " + strings.Join(waiting, "; ")
	}
	return fmt.Errorf("%s", msg)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// tryReady marks a long-running process ready once its checks and required
// captures are satisfied. Oneshot processes become ready only on exit.
func (s *Supervisor) tryReady() {
	if s.Proc.IsOneshot() || s.settled.Load() {
		return
	}
	if !s.eval.Satisfied() || !s.allRequiredCapturesDone() {
		return
	}
	if s.settled.CompareAndSwap(false, true) {
		s.ready.Store(true)
		s.readinessCancel()
		s.emitEvent(StateReady, nil)
	}
}

// fail reports a startup failure. The process is left running; the
// orchestrator decides whether to stop it.
func (s *Supervisor) fail(err error) {
	if s.settled.CompareAndSwap(false, true) {
		s.failed.Store(true)
		s.readinessCancel()
		s.emitEvent(StateFailed, err)
	}
}

func (s *Supervisor) waitLoop() {
	err := s.cmd.Wait()
	close(s.launcherDone)

	// The PTY delivers buffered output after the process exits; read it all
	// before deciding the outcome. Grandchildren can hold the PTY open, so
	// do not wait for them forever.
	select {
	case <-s.pumpDone:
	case <-time.After(2 * time.Second):
	}
	_ = s.ptyMaster.Close()

	// Stopped while the launcher still ran: the stop signal can kill the
	// launcher after it forked the daemon, so look for the daemon anyway
	// (watchDaemon stops it, or gives up shortly).
	if s.isDaemon() && err != nil && s.stopReq.Load() {
		_ = s.watchDaemon()
	}

	if s.isDaemon() && err == nil && !s.failed.Load() {
		if derr := s.watchDaemon(); derr != nil {
			s.exit = exitInfo(s.cmd)
			close(s.done)
			s.readinessCancel()
			s.fail(derr)
			return
		}
		s.exit = &ExitInfo{Code: -1}
		close(s.done)
		s.readinessCancel()
		if s.failed.Load() {
			return
		}
		s.settled.Store(true)
		s.emitEvent(StateExited, fmt.Errorf("daemon (pid %d) exited", s.daemonPID.Load()))
		return
	}

	s.exit = exitInfo(s.cmd)
	close(s.done)
	defer s.readinessCancel()

	if s.Proc.IsOneshot() {
		switch {
		case err != nil:
			s.fail(fmt.Errorf("oneshot process failed: %w", err))
		case s.eval.Satisfied() && s.allRequiredCapturesDone():
			if s.settled.CompareAndSwap(false, true) {
				s.ready.Store(true)
				s.emitEvent(StateReady, nil)
			}
		default:
			s.fail(fmt.Errorf("oneshot process exited before readiness checks passed; still waiting for %s",
				strings.Join(s.eval.Pending(), ", ")))
		}
		return
	}

	// A process that already failed startup stays failed.
	if s.failed.Load() {
		return
	}
	s.settled.Store(true)
	if err != nil {
		s.emitEvent(StateExited, err)
	} else {
		s.emitEvent(StateExited, nil)
	}
}

func (s *Supervisor) allRequiredCapturesDone() bool {
	for _, c := range s.Captures {
		if c.Required && !c.Done() {
			return false
		}
	}
	return true
}

func (s *Supervisor) readinessMode() string {
	if s.Proc.Readiness != nil {
		return s.Proc.Readiness.CheckMode()
	}
	return "all"
}

func (s *Supervisor) emitEvent(state State, err error) {
	s.Events <- Event{
		Process: s.Name,
		Gen:     s.Gen,
		State:   state,
		Err:     err,
		LastLog: s.copyLastLog(),
		Exit:    s.Exit(),
	}
}

// detectRebuild turns output lines of a ready process into rebuild events.
func (s *Supervisor) detectRebuild(line string) {
	switch {
	case s.rebuildFailed != nil && s.rebuildFailed.MatchString(line):
		fields := map[string]any{"line": line}
		if s.inRebuild {
			fields["duration_ms"] = time.Since(s.rebuildStart).Milliseconds()
		}
		s.inRebuild = false
		s.notify("rebuild_failed", fields)
	case s.rebuilding != nil && s.rebuilding.MatchString(line):
		if !s.inRebuild {
			s.startRebuild("output", line)
		}
	case s.rebuilt != nil && s.rebuilt.MatchString(line):
		// Tools that only report finished builds (tailwind, Vite HMR)
		// count every one.
		s.finishRebuild(line)
	case s.inRebuild && s.rebuiltAfter != nil && s.rebuiltAfter.MatchString(line):
		s.finishRebuild(line)
	case s.inRebuild && matchesAny(s.readyRes, line):
		// The process printed its ready line again: it restarted.
		s.finishRebuild(line)
	}
}

func (s *Supervisor) startRebuild(via, line string) {
	s.inRebuild = true
	s.rebuildStart = time.Now()
	fields := map[string]any{"via": via}
	if line != "" {
		fields["line"] = line
	}
	s.notify("rebuild_started", fields)
}

func (s *Supervisor) finishRebuild(line string) {
	fields := map[string]any{"line": line}
	if s.inRebuild {
		fields["duration_ms"] = time.Since(s.rebuildStart).Milliseconds()
	}
	s.inRebuild = false
	s.notify("rebuild_finished", fields)
}

func matchesAny(res []*regexp.Regexp, line string) bool {
	for _, re := range res {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func (s *Supervisor) cwd() string {
	if s.Proc.Cwd != "" {
		return filepath.Join(s.ProjectPath, s.Proc.Cwd)
	}
	return s.ProjectPath
}

func (s *Supervisor) notify(typ string, fields map[string]any) {
	if s.Notify != nil {
		s.Notify(typ, fields)
	}
}

func (s *Supervisor) exited() bool {
	if s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// exitInfo reads how cmd ended once Wait has returned.
func exitInfo(cmd *exec.Cmd) *ExitInfo {
	ps := cmd.ProcessState
	if ps == nil {
		return nil
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return &ExitInfo{Code: 128 + int(ws.Signal()), Signal: unix.SignalName(ws.Signal())}
	}
	return &ExitInfo{Code: ps.ExitCode()}
}

// Exit returns how the process ended, or nil while it runs.
func (s *Supervisor) Exit() *ExitInfo {
	if !s.exited() {
		return nil
	}
	return s.exit
}

// Write sends bytes to the process's terminal, as if typed.
func (s *Supervisor) Write(p []byte) (int, error) {
	if !s.Alive() || s.ptyMaster == nil {
		return 0, fmt.Errorf("process %s is not running", s.Name)
	}
	return s.ptyMaster.Write(p)
}

func (s *Supervisor) appendLastLog(line string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.lastLog = append(s.lastLog, line)
	if len(s.lastLog) > lastLogLines {
		s.lastLog = s.lastLog[len(s.lastLog)-lastLogLines:]
	}
}

func (s *Supervisor) copyLastLog() []string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	cp := make([]string, len(s.lastLog))
	copy(cp, s.lastLog)
	return cp
}

// PID returns the child process PID, or 0 if not started.
func (s *Supervisor) PID() int {
	return int(s.pid.Load())
}

// Alive reports whether the process was started and has not exited.
func (s *Supervisor) Alive() bool {
	if s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Stop gracefully stops the supervised process and its process group.
func (s *Supervisor) Stop(grace time.Duration) {
	if !s.Alive() {
		return
	}
	if s.isDaemon() {
		s.stopGrace.Store(int64(grace))
		s.stopReq.Store(true)
		if s.daemonPID.Load() != 0 {
			s.stopDaemon(grace)
			return
		}
		// Still starting: stop the launcher if it runs; the daemon watcher
		// stops the daemon once its pidfile names it.
		select {
		case <-s.launcherDone:
		default:
			GracefulStop(s.PID(), grace, s.launcherDone)
		}
		select {
		case <-s.done:
		case <-time.After(grace + 5*time.Second):
		}
		return
	}
	GracefulStop(s.PID(), grace, s.done)
}

// Kill immediately SIGKILLs the process group.
func (s *Supervisor) Kill() {
	if pid := s.PID(); pid != 0 {
		ForceKill(pid)
	}
	if pid := int(s.daemonPID.Load()); pid != 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// CopyLastLogPublic returns a copy of the last N log lines (for error attribution).
func (s *Supervisor) CopyLastLogPublic() []string {
	return s.copyLastLog()
}

// Done returns a channel that closes when the process exits.
func (s *Supervisor) Done() <-chan struct{} {
	if s.done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.done
}
