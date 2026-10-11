package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orpic/stack-start/internal/output"
)

// A daemon's command starts the real process in the background and exits
// (pg_ctl start, redis-server --daemonize yes). The supervisor then reads
// the real PID from the pidfile and watches that process instead.

func (s *Supervisor) isDaemon() bool { return s.Proc.Kind == "daemon" }

func (s *Supervisor) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.cwd(), p)
}

// watchDaemon runs after the launcher exited 0: it finds the daemon's PID,
// follows its log file, and returns when the daemon is gone.
func (s *Supervisor) watchDaemon() error {
	deadline := time.Now().Add(30 * time.Second)
	if s.Proc.Readiness != nil && s.Proc.Readiness.Timeout.Duration > 0 {
		deadline = time.Now().Add(s.Proc.Readiness.Timeout.Duration)
	}
	pidfile := s.path(s.Proc.Pidfile)
	var pid int
	for {
		if data, err := os.ReadFile(pidfile); err == nil {
			first := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
			if n, err := strconv.Atoi(first); err == nil && n > 0 && syscall.Kill(n, 0) == nil {
				pid = n
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon started but %s did not name a running process", s.Proc.Pidfile)
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.pid.Store(int64(pid))
	s.daemonPID.Store(int64(pid))
	s.notify("daemon_started", map[string]any{"pid": pid})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if s.Proc.LogFile != "" {
		go s.followDaemonLog(ctx, s.path(s.Proc.LogFile))
	}
	for syscall.Kill(pid, 0) == nil {
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// followDaemonLog feeds new lines of the daemon's own log file through the
// normal output path (console, log, readiness, captures, signals).
func (s *Supervisor) followDaemonLog(ctx context.Context, path string) {
	var f *os.File
	for f == nil {
		if ff, err := os.Open(path); err == nil {
			f = ff
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer func() { _ = f.Close() }()
	if s.daemonLogStart > 0 {
		_, _ = f.Seek(s.daemonLogStart, io.SeekStart)
	}
	r := bufio.NewReader(f)
	var pending []byte
	for {
		line, err := r.ReadBytes('\n')
		pending = append(pending, line...)
		if err == nil {
			s.handleLine(output.Sanitize(pending[:len(pending)-1]))
			pending = pending[:0]
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// stopDaemon stops a daemon: its stop_cmd if it has one, then SIGTERM and
// SIGKILL to whatever is left.
func (s *Supervisor) stopDaemon(grace time.Duration) {
	pid := int(s.daemonPID.Load())
	deadline := time.Now().Add(grace)
	if s.Proc.StopCmd != "" {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.Proc.StopCmd)
		cmd.Dir, cmd.Env = s.cwd(), s.Env
		_ = cmd.Run()
		cancel()
	}
	if pid == 0 {
		return
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	for time.Now().Before(deadline) && syscall.Kill(pid, 0) == nil {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		ForceKill(pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}
