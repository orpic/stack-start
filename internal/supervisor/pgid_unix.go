//go:build unix

package supervisor

import "syscall"

// signalGroup sends a signal to the entire process group of the given PID.
func signalGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
