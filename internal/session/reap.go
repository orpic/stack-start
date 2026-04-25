package session

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ReapStaleSessions removes session records whose stackstart process is no longer alive.
func ReapStaleSessions() {
	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		rec, err := ReadRecord(path)
		if err != nil {
			continue
		}

		if !isProcessAlive(rec.StackstartPID) {
			slug := strings.TrimSuffix(entry.Name(), ".json")
			RemoveRecord(slug)
		}
	}
}

func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// ListAllSessions returns all live session records after reaping stale ones.
func ListAllSessions() ([]Record, error) {
	ReapStaleSessions()

	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var records []Record
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		rec, err := ReadRecord(path)
		if err != nil {
			continue
		}
		records = append(records, rec)
	}

	return records, nil
}

// FindMatchingSessions finds sessions whose project path contains the given cwd.
func FindMatchingSessions(cwd, profileFilter string) ([]Record, error) {
	all, err := ListAllSessions()
	if err != nil {
		return nil, err
	}

	var matches []Record
	for _, rec := range all {
		if profileFilter != "" && rec.Profile != profileFilter {
			continue
		}
		// Check if cwd is within-or-equal to the session's project path
		cleaned := filepath.Clean(cwd)
		recPath := filepath.Clean(rec.ProjectPath)
		if cleaned == recPath || strings.HasPrefix(cleaned, recPath+string(os.PathSeparator)) {
			matches = append(matches, rec)
		}
	}

	return matches, nil
}
