package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"

	"github.com/orpic/stack-start/internal/readiness"
)

type healthResult struct {
	name string
	gen  int
	err  error
}

// rebuildStats are updated from output goroutines, so they have their own
// lock.
type rebuildStats struct {
	mu        sync.Mutex
	count     int
	state     string // rebuilding, ok, failed
	at        time.Time
	duration  int64
	lastError string
}

// bumpGen retires the process's current instance: its events are ignored
// from now on and its health checks stop.
func (o *Orchestrator) bumpGen(p *proc) {
	p.gen++
	if p.healthCancel != nil {
		p.healthCancel()
		p.healthCancel = nil
	}
}

// startHealth runs a ready process's health checks until its instance is
// retired.
func (o *Orchestrator) startHealth(p *proc) {
	h := p.cfg.Health
	if h == nil || p.sup == nil {
		return
	}
	interval := h.Interval.Duration
	if interval <= 0 {
		interval = 10 * time.Second
	}
	timeout := h.Timeout.Duration
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	cwd := o.cfg.ProjectPath
	if p.cfg.Cwd != "" {
		cwd = filepath.Join(o.cfg.ProjectPath, p.cfg.Cwd)
	}
	var probes []readiness.Probe
	var descs []string
	for _, c := range h.Checks {
		switch {
		case c.TCP != "":
			probes, descs = append(probes, readiness.TCPProbe(c.TCP)), append(descs, "tcp "+c.TCP)
		case c.HTTP != "":
			probes, descs = append(probes, readiness.HTTPProbe(c.HTTP, c.Status)), append(descs, "http "+c.HTTP)
		case c.Exec != "":
			probes, descs = append(probes, readiness.ExecProbe(c.Exec, cwd, p.sup.Env)), append(descs, "exec "+c.Exec)
		case c.File != "":
			path := c.File
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			probes, descs = append(probes, readiness.FileProbe(path)), append(descs, "file "+c.File)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.healthCancel = cancel
	name, gen := p.name, p.gen
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-o.done:
				return
			case <-t.C:
			}
			var err error
			for i, probe := range probes {
				pctx, pcancel := context.WithTimeout(ctx, timeout)
				e := probe(pctx)
				pcancel()
				if e != nil {
					err = fmt.Errorf("%s: %v", descs[i], e)
					break
				}
			}
			select {
			case o.health <- healthResult{name: name, gen: gen, err: err}:
			case <-ctx.Done():
				return
			case <-o.done:
				return
			}
		}
	}()
}

// handleHealth marks a process degraded after enough failed rounds, and
// ready again when a round passes.
func (o *Orchestrator) handleHealth(r healthResult) {
	p := o.procs[r.name]
	if p == nil || p.gen != r.gen {
		return
	}
	if r.err == nil {
		p.healthFails = 0
		if p.state == sDegraded {
			p.cause = ""
			o.setState(p, sReady)
			o.ev.Emit("process_healthy", p.name, nil)
			o.notice(color.New(color.FgGreen), p.name+" is healthy again")
		}
		return
	}
	p.healthFails++
	limit := p.cfg.Health.Failures
	if limit <= 0 {
		limit = 3
	}
	if p.healthFails >= limit && p.state == sReady {
		p.cause = r.err.Error()
		o.setState(p, sDegraded)
		o.ev.Emit("process_degraded", p.name, map[string]any{"cause": p.cause, "failures": p.healthFails})
		o.notice(color.New(color.FgYellow), fmt.Sprintf("%s is degraded: %s", p.name, strings.TrimSpace(p.cause)))
	}
}

// recordRebuild keeps per-process rebuild statistics for status.
func (o *Orchestrator) recordRebuild(name, typ string, fields map[string]any) {
	st := o.rebuilds[name]
	st.mu.Lock()
	defer st.mu.Unlock()
	st.at = time.Now()
	switch typ {
	case "rebuild_started":
		st.state = "rebuilding"
	case "rebuild_finished":
		st.state, st.lastError = "ok", ""
		st.count++
	case "rebuild_failed":
		st.state = "failed"
		st.lastError, _ = fields["line"].(string)
	}
	if d, ok := fields["duration_ms"].(int64); ok {
		st.duration = d
	}
}
