package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	captureLib "github.com/orpic/stack-start/internal/capture"
	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/logmux"
	"github.com/orpic/stack-start/internal/output"
	"github.com/orpic/stack-start/internal/readiness"
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
	State   State
	Err     error
	LastLog []string
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

	// settled is set once the startup outcome (ready, or failed before
	// ready) has been decided, so it is decided exactly once.
	settled atomic.Bool
	ready   atomic.Bool
	failed  atomic.Bool

	readinessCancel context.CancelFunc
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

	s.cmd = exec.Command("/bin/sh", "-c", s.Proc.Cmd)
	s.cmd.Dir = cwd
	s.cmd.Env = s.Env

	ptyMaster, err := spawnPTY(s.cmd)
	if err != nil {
		err = fmt.Errorf("failed to spawn: %w", err)
		s.emitEvent(StateFailed, err)
		return err
	}
	s.ptyMaster = ptyMaster
	s.pid.Store(int64(s.cmd.Process.Pid))
	s.done = make(chan struct{})
	s.pumpDone = make(chan struct{})

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
			case chk.TCP != "":
				checks = append(checks, readiness.NewTCPCheck(chk.TCP))
			case chk.Capture != "":
				capture := s.findCapture(chk.Capture)
				if capture == nil {
					return fmt.Errorf("readiness check refers to unknown capture %q", chk.Capture)
				}
				checks = append(checks, readiness.FuncCheck{Desc: "capture " + chk.Capture, Fn: capture.Done})
			}
		}
	}
	s.eval = readiness.NewEvaluator(s.readinessMode(), checks)

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
				break
			}
		}
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

	// The PTY delivers buffered output after the process exits; read it all
	// before deciding the outcome. Grandchildren can hold the PTY open, so
	// do not wait for them forever.
	select {
	case <-s.pumpDone:
	case <-time.After(2 * time.Second):
	}
	_ = s.ptyMaster.Close()

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
		State:   state,
		Err:     err,
		LastLog: s.copyLastLog(),
	}
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
	GracefulStop(s.PID(), grace, s.done)
}

// Kill immediately SIGKILLs the process group.
func (s *Supervisor) Kill() {
	if pid := s.PID(); pid != 0 {
		ForceKill(pid)
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
