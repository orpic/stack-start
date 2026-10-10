package supervisor

import (
	"syscall"
	"time"

	"github.com/orpic/stack-start/internal/proctree"
)

// GracefulStop stops a process and everything it started: SIGTERM, then
// SIGKILL to whatever is still alive after the grace period.
//
// The process group is signalled as a whole. Descendants that left the group
// (BEAM port programs such as Phoenix's watchers, anything that called
// setsid) are found by parent PID before any signal is sent, because once
// their parent exits they are re-parented and can no longer be traced.
func GracefulStop(pid int, grace time.Duration, done <-chan struct{}) {
	escaped := escapedDescendants(pid)

	_ = signalGroup(pid, syscall.SIGTERM)
	for _, p := range escaped {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}

	deadline := time.After(grace)
	select {
	case <-done:
	case <-deadline:
		_ = signalGroup(pid, syscall.SIGKILL)
		killAll(escaped)
		return
	}

	// The process exited; give anything it left behind the rest of the
	// grace period, then kill it.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for anyAlive(escaped) {
		select {
		case <-deadline:
			killAll(escaped)
			return
		case <-ticker.C:
		}
	}
	// Grandchildren still in the group (rare once the leader is gone).
	_ = signalGroup(pid, syscall.SIGKILL)
}

// ForceKill SIGKILLs a process's group and every descendant immediately.
func ForceKill(pid int) {
	escaped := escapedDescendants(pid)
	_ = signalGroup(pid, syscall.SIGKILL)
	killAll(escaped)
}

// escapedDescendants returns descendants of pid that are not in its process
// group (pgid == pid, since every child starts its own session).
func escapedDescendants(pid int) []int {
	desc, err := proctree.Descendants(pid)
	if err != nil {
		return nil
	}
	var out []int
	for _, p := range desc {
		if pgid, err := syscall.Getpgid(p); err == nil && pgid != pid {
			out = append(out, p)
		}
	}
	return out
}

func killAll(pids []int) {
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

func anyAlive(pids []int) bool {
	for _, p := range pids {
		if syscall.Kill(p, 0) == nil {
			return true
		}
	}
	return false
}
