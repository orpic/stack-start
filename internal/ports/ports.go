// Package ports allocates a profile's port variables (`ports:` in the
// config, used as ${port.<name>}) so several stacks of one project, e.g.
// one per git worktree, can run at once: the first gets the defaults, the
// next ones the nearest free ports that no other stack has claimed.
package ports

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// reservation is <sessions>/<slug>.ports.json: the ports a running stack
// claimed, so a stack starting at the same time doesn't take them.
type reservation struct {
	PID   int            `json:"pid"`
	Ports map[string]int `json:"ports"`
}

// Allocate picks a port for every name in defaults and records them for
// slug in sessionsDir. Call Release when the stack ends.
func Allocate(sessionsDir, slug string, defaults map[string]int) (map[string]int, error) {
	if len(defaults) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(sessionsDir, ".ports.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	taken := claimed(sessionsDir, slug)
	names := make([]string, 0, len(defaults))
	for n := range defaults {
		names = append(names, n)
	}
	sort.Strings(names)
	out := map[string]int{}
	for _, n := range names {
		p := defaults[n]
		for tries := 0; ; tries++ {
			if tries > 200 {
				return nil, fmt.Errorf("no free port near %d for %s", defaults[n], n)
			}
			if !taken[p] && Free(p) {
				break
			}
			p++
		}
		out[n] = p
		taken[p] = true
	}
	data, _ := json.Marshal(reservation{PID: os.Getpid(), Ports: out})
	if err := os.WriteFile(filepath.Join(sessionsDir, slug+".ports.json"), data, 0o644); err != nil {
		return nil, err
	}
	return out, nil
}

// Release forgets slug's reservation.
func Release(sessionsDir, slug string) {
	_ = os.Remove(filepath.Join(sessionsDir, slug+".ports.json"))
}

// claimed returns the ports reserved by other live stacks.
func claimed(sessionsDir, self string) map[int]bool {
	taken := map[int]bool{}
	files, _ := filepath.Glob(filepath.Join(sessionsDir, "*.ports.json"))
	for _, f := range files {
		if strings.TrimSuffix(filepath.Base(f), ".ports.json") == self {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r reservation
		if json.Unmarshal(data, &r) != nil || r.PID <= 0 || syscall.Kill(r.PID, 0) != nil {
			_ = os.Remove(f) // left by a stack that died
			continue
		}
		for _, p := range r.Ports {
			taken[p] = true
		}
	}
	return taken
}

// Free reports whether nothing listens on port (on any local address).
func Free(port int) bool {
	for _, addr := range []string{"127.0.0.1", "0.0.0.0"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

// Holder describes the process listening on a local TCP port, using lsof
// when it is installed ("pid 123 (beam.smp)"), or "" if unknown.
func Holder(port int) string {
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil {
		return ""
	}
	var pid, cmd string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p") && pid == "":
			pid = line[1:]
		case strings.HasPrefix(line, "c") && cmd == "":
			cmd = line[1:]
		}
	}
	if pid == "" {
		return ""
	}
	return fmt.Sprintf("pid %s (%s)", pid, cmd)
}
