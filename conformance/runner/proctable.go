package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// procInfo is one row of the process table. A process is identified by
// pid and start time together, so a reused PID is never mistaken for it.
type procInfo struct {
	PID     int
	PPID    int
	Start   string
	Command string
}

func (p procInfo) key() string { return strconv.Itoa(p.PID) + "@" + p.Start }

// readProcTable lists every process with `ps`, which works the same way on
// macOS and Linux.
func readProcTable() (map[int]procInfo, error) {
	cmd := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,lstart=,command=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	table := make(map[int]procInfo)
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	self := os.Getpid()
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// pid ppid Dow Mon DD HH:MM:SS YYYY command...
		if len(f) < 7 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		// Skip our own `ps` invocation.
		if ppid == self && len(f) > 7 && (f[7] == "ps" || strings.HasSuffix(f[7], "/ps")) {
			continue
		}
		table[pid] = procInfo{
			PID:     pid,
			PPID:    ppid,
			Start:   strings.Join(f[2:7], " "),
			Command: strings.Join(f[7:], " "),
		}
	}
	return table, nil
}

// Sampler reads the process table every interval while any tracker is
// registered, and lets every tracker record the descendants of its roots.
type Sampler struct {
	mu       sync.Mutex
	trackers map[*Tracker]struct{}
	interval time.Duration
	wake     chan struct{}
}

func NewSampler(interval time.Duration) *Sampler {
	s := &Sampler{trackers: map[*Tracker]struct{}{}, interval: interval, wake: make(chan struct{}, 1)}
	go s.loop()
	return s
}

func (s *Sampler) loop() {
	for {
		s.mu.Lock()
		n := len(s.trackers)
		s.mu.Unlock()
		if n == 0 {
			<-s.wake
			continue
		}
		s.SampleNow()
		time.Sleep(s.interval)
	}
}

// SampleNow takes one snapshot and feeds it to every tracker.
func (s *Sampler) SampleNow() map[int]procInfo {
	table, err := readProcTable()
	if err != nil {
		return nil
	}
	s.mu.Lock()
	ts := make([]*Tracker, 0, len(s.trackers))
	for t := range s.trackers {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	for _, t := range ts {
		t.observe(table)
	}
	return table
}

func (s *Sampler) Register(t *Tracker) {
	s.mu.Lock()
	s.trackers[t] = struct{}{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Sampler) Unregister(t *Tracker) {
	s.mu.Lock()
	delete(s.trackers, t)
	s.mu.Unlock()
}

// Tracker records every process descended from a case's commands, and,
// separately, from its spawn helpers.
type Tracker struct {
	mu          sync.Mutex
	roots       map[int]bool // pid -> isHelper
	seen        map[string]procInfo
	helperSeen  map[string]procInfo
	helperRoots map[int]bool
}

func NewTracker() *Tracker {
	return &Tracker{
		roots:       map[int]bool{},
		seen:        map[string]procInfo{},
		helperSeen:  map[string]procInfo{},
		helperRoots: map[int]bool{},
	}
}

func (t *Tracker) AddRoot(pid int, helper bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.roots[pid] = helper
	if helper {
		t.helperRoots[pid] = true
	}
}

func (t *Tracker) observe(table map[int]procInfo) {
	children := make(map[int][]int)
	for _, p := range table {
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	walk := func(starts []int, into map[string]procInfo) {
		queue := append([]int(nil), starts...)
		visited := map[int]bool{}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			if visited[pid] {
				continue
			}
			visited[pid] = true
			if p, ok := table[pid]; ok {
				into[p.key()] = p
			}
			queue = append(queue, children[pid]...)
		}
	}

	var caseStarts, helperStarts []int
	for pid, helper := range t.roots {
		if helper {
			helperStarts = append(helperStarts, pid)
		} else {
			caseStarts = append(caseStarts, pid)
		}
	}
	// Continue from every recorded process that is still alive, so a
	// grandchild is found even after its parent has exited.
	for _, p := range t.helperSeen {
		if q, ok := table[p.PID]; ok && q.key() == p.key() {
			helperStarts = append(helperStarts, p.PID)
		}
	}
	walk(helperStarts, t.helperSeen)
	helperPIDs := map[int]bool{}
	for _, p := range t.helperSeen {
		helperPIDs[p.PID] = true
	}
	for _, p := range t.seen {
		if q, ok := table[p.PID]; ok && q.key() == p.key() && !helperPIDs[p.PID] {
			caseStarts = append(caseStarts, p.PID)
		}
	}
	walk(caseStarts, t.seen)
}

// alive returns the recorded case processes (not helpers) still running.
func (t *Tracker) alive(table map[int]procInfo, token string) []procInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	helper := map[string]bool{}
	for k := range t.helperSeen {
		helper[k] = true
	}
	var out []procInfo
	seen := map[string]bool{}
	for k, p := range t.seen {
		if helper[k] {
			continue
		}
		if q, ok := table[p.PID]; ok && q.key() == k {
			out = append(out, q)
			seen[k] = true
		}
	}
	if token != "" {
		for _, q := range table {
			k := q.key()
			if seen[k] || helper[k] || q.PID == os.Getpid() {
				continue
			}
			if strings.Contains(q.Command, token) {
				out = append(out, q)
				seen[k] = true
			}
		}
	}
	return out
}

func (t *Tracker) aliveHelpers(table map[int]procInfo) []procInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []procInfo
	for k, p := range t.helperSeen {
		if q, ok := table[p.PID]; ok && q.key() == k {
			out = append(out, q)
		}
	}
	return out
}
