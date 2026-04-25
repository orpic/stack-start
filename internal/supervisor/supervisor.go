package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	captureLib "github.com/orpic/stack-start/internal/capture"
	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/logmux"
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

// Event is emitted when a supervised process changes state.
type Event struct {
	Process string
	State   State
	Err     error
	LastLog []string
}

// Supervisor manages a single child process.
type Supervisor struct {
	Name         string
	Proc         config.Process
	ProjectPath  string
	Mux          *logmux.Mux
	Captures     []*captureLib.Capture
	Registry     *captureLib.Registry
	Events       chan<- Event
	Env          []string
	cmd          *exec.Cmd
	ptyMaster    *os.File
	pid          int
	done         chan struct{}
	lastLogLines []string
}

// Start spawns the child process under a PTY, starts scanning its output,
// and evaluates readiness checks and captures.
func (s *Supervisor) Start(ctx context.Context) error {
	cwd := s.ProjectPath
	if s.Proc.Cwd != "" {
		cwd = s.ProjectPath + "/" + s.Proc.Cwd
	}

	s.cmd = exec.CommandContext(ctx, "/bin/sh", "-c", s.Proc.Cmd)
	s.cmd.Dir = cwd
	s.cmd.Env = s.Env

	ptyMaster, err := spawnPTY(s.cmd)
	if err != nil {
		s.emitEvent(StateFailed, fmt.Errorf("failed to spawn: %w", err))
		return err
	}
	s.ptyMaster = ptyMaster
	s.pid = s.cmd.Process.Pid
	s.done = make(chan struct{})

	s.emitEvent(StateStarting, nil)

	var checks []readiness.Check
	var lineChecks []readiness.LineEvaluator
	var bgChecks []readiness.Backgrounder

	if s.Proc.Readiness != nil {
		for _, chk := range s.Proc.Readiness.Checks {
			if chk.Log != "" {
				lc, err := readiness.NewLogRegexCheck(chk.Log)
				if err != nil {
					s.emitEvent(StateFailed, fmt.Errorf("invalid log regex: %w", err))
					return err
				}
				checks = append(checks, lc)
				lineChecks = append(lineChecks, lc)
			}
			if chk.TCP != "" {
				tc := readiness.NewTCPCheck(chk.TCP)
				checks = append(checks, tc)
				bgChecks = append(bgChecks, tc)
			}
		}
	}

	eval := readiness.NewEvaluator(s.readinessMode(), checks)

	var readinessCtx context.Context
	var readinessCancel context.CancelFunc
	if s.Proc.Readiness != nil {
		readinessCtx, readinessCancel = context.WithTimeout(ctx, s.Proc.Readiness.Timeout.Duration)
	} else {
		readinessCtx, readinessCancel = context.WithCancel(ctx)
	}

	for _, bc := range bgChecks {
		go bc.Run(readinessCtx)
	}

	// Log scanning + readiness + capture evaluation
	go s.scanLoop(readinessCtx, readinessCancel, eval, lineChecks)

	// Wait for process exit
	go s.waitLoop(readinessCancel)

	return nil
}

func (s *Supervisor) scanLoop(
	readinessCtx context.Context,
	readinessCancel context.CancelFunc,
	eval *readiness.Evaluator,
	lineChecks []readiness.LineEvaluator,
) {
	defer readinessCancel()

	ready := false
	scanner := bufio.NewScanner(s.ptyMaster)
	for scanner.Scan() {
		line := scanner.Bytes()
		s.Mux.WriteLine(line)

		// Keep a rolling buffer of last 10 lines for error attribution
		s.appendLastLog(string(line))

		if ready {
			continue
		}

		for _, lc := range lineChecks {
			lc.Evaluate(line)
		}

		for _, cap := range s.Captures {
			if matched, val := cap.Match(line); matched {
				s.Registry.Store(s.Name, cap.Name, val)
			}
		}

		if s.allRequiredCapturesDone() && eval.Satisfied() {
			ready = true
			s.emitEvent(StateReady, nil)
		}
	}

	// If we exited the scan without becoming ready, check if context timed out
	if !ready {
		select {
		case <-readinessCtx.Done():
			if readinessCtx.Err() == context.DeadlineExceeded {
				s.emitEvent(StateFailed, fmt.Errorf("readiness timeout after %s", s.Proc.Readiness.Timeout.Duration))
			}
		default:
		}
	}
}

func (s *Supervisor) waitLoop(readinessCancel context.CancelFunc) {
	err := s.cmd.Wait()
	s.ptyMaster.Close()
	close(s.done)
	readinessCancel()

	if err != nil {
		s.emitEvent(StateExited, fmt.Errorf("process exited: %w", err))
	} else {
		s.emitEvent(StateExited, nil)
	}
}

func (s *Supervisor) allRequiredCapturesDone() bool {
	for _, cap := range s.Captures {
		if cap.Required && !cap.Done() {
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
	s.lastLogLines = append(s.lastLogLines, line)
	if len(s.lastLogLines) > 10 {
		s.lastLogLines = s.lastLogLines[len(s.lastLogLines)-10:]
	}
}

func (s *Supervisor) copyLastLog() []string {
	cp := make([]string, len(s.lastLogLines))
	copy(cp, s.lastLogLines)
	return cp
}

// PID returns the child process PID, or 0 if not started.
func (s *Supervisor) PID() int {
	return s.pid
}

// Stop gracefully stops the supervised process.
func (s *Supervisor) Stop(grace time.Duration) {
	if s.pid == 0 {
		return
	}
	GracefulStop(s.pid, grace, s.done)
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
