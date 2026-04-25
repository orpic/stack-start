package supervisor

import (
	"syscall"
	"time"
)

// GracefulStop sends SIGTERM to the process group, waits for the grace period,
// then sends SIGKILL if the process is still alive.
func GracefulStop(pid int, grace time.Duration, done <-chan struct{}) {
	_ = signalGroup(pid, syscall.SIGTERM)

	select {
	case <-done:
		return
	case <-time.After(grace):
	}

	_ = signalGroup(pid, syscall.SIGKILL)
}

// ForceKill sends SIGKILL to the process group immediately.
func ForceKill(pid int) {
	_ = signalGroup(pid, syscall.SIGKILL)
}
