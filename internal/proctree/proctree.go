// Package proctree finds every descendant of a process by parent PID.
//
// Signalling a process group is not enough to stop a process tree: children
// can move to their own process group or session. The BEAM does this for
// every port program (Phoenix's esbuild and tailwind watchers), as does
// anything that calls setsid or daemonises. Those children are still
// descendants by parent PID until their parent exits, so a snapshot taken
// before stopping finds them.
package proctree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Descendants returns the PIDs of every descendant of root (not root
// itself), parents before children.
func Descendants(root int) ([]int, error) {
	parents, err := parentMap()
	if err != nil {
		return nil, err
	}
	children := make(map[int][]int, len(parents))
	for pid, ppid := range parents {
		children[ppid] = append(children[ppid], pid)
	}
	var out []int
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out, nil
}

// parentMap returns pid -> ppid for every process on the system.
func parentMap() (map[int]int, error) {
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		return parentMapProc()
	}
	return parentMapPS()
}

// parentMapProc reads /proc/<pid>/stat (Linux).
func parentMapProc() (map[int]int, error) {
	paths, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil, err
	}
	m := make(map[int]int, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue // the process exited
		}
		pid, ppid, ok := parseProcStat(data)
		if ok {
			m[pid] = ppid
		}
	}
	return m, nil
}

// parseProcStat parses "pid (comm) state ppid ...". comm may contain spaces
// and parentheses, so fields are read after the last ')'.
func parseProcStat(data []byte) (pid, ppid int, ok bool) {
	open := bytes.IndexByte(data, '(')
	close := bytes.LastIndexByte(data, ')')
	if open < 1 || close < open {
		return 0, 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data[:open])))
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) < 2 {
		return 0, 0, false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	return pid, ppid, true
}

// parentMapPS runs ps (macOS and other Unixes without /proc).
func parentMapPS() (map[int]int, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	return parsePS(out), nil
}

func parsePS(out []byte) map[int]int {
	m := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			m[pid] = ppid
		}
	}
	return m
}
