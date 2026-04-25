package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StateDir returns the base directory for stackstart state files.
func StateDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "stackstart")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "stackstart")
	}
	return filepath.Join(home, ".local", "state", "stackstart")
}

// SessionsDir returns the sessions subdirectory.
func SessionsDir() string {
	return filepath.Join(StateDir(), "sessions")
}

// Slug generates a human-readable slug from project path and profile name.
func Slug(projectPath, profileName string) string {
	cleaned := strings.ReplaceAll(projectPath, "/", "_")
	cleaned = strings.TrimPrefix(cleaned, "_")
	return cleaned + "__" + profileName
}

// SessionDir returns the per-session directory for logs and state files.
func SessionDir(slug string) string {
	return filepath.Join(SessionsDir(), slug)
}

// SessionRecordPath returns the path to the session JSON record.
func SessionRecordPath(slug string) string {
	return filepath.Join(SessionsDir(), slug+".json")
}

// SessionLockPath returns the path to the session lock file.
func SessionLockPath(slug string) string {
	return filepath.Join(SessionsDir(), slug+".lock")
}
