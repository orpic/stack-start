//go:build unix

package session

import (
	"fmt"
	"os"
	"syscall"
)

// Lock holds a file lock for session exclusivity.
type Lock struct {
	file *os.File
}

// Acquire attempts to get an exclusive non-blocking lock on the session lock file.
func Acquire(slug string) (*Lock, error) {
	path := SessionLockPath(slug)
	if err := os.MkdirAll(SessionsDir(), 0755); err != nil {
		return nil, fmt.Errorf("creating sessions dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf(
			"profile is already running for this project; see 'stackstart status'")
	}

	return &Lock{file: f}, nil
}

// Release releases the lock and closes the file.
func (l *Lock) Release() {
	if l.file != nil {
		syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		l.file.Close()
	}
}
