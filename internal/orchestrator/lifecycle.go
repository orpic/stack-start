package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"

	captureLib "github.com/orpic/stack-start/internal/capture"
	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/env"
	"github.com/orpic/stack-start/internal/interpolate"
	"github.com/orpic/stack-start/internal/logmux"
	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/supervisor"
	tmplPkg "github.com/orpic/stack-start/internal/template"
)

// schedule starts every pending process whose dependencies are ready, and
// marks processes whose dependencies failed as blocked.
func (o *Orchestrator) schedule() {
	for changed := true; changed && !o.finished; {
		changed = false
		for _, name := range o.order {
			p := o.procs[name]
			if p.state != sPending && p.state != sBlocked {
				continue
			}
			ready, blockedBy := true, ""
			for _, d := range p.cfg.DependsOn {
				dp := o.procs[d]
				switch dp.state {
				case sReady:
				case sFailed:
					ready, blockedBy = false, d
				case sBlocked:
					ready, blockedBy = false, dp.blockedBy
				default:
					ready = false
				}
			}
			switch {
			case ready:
				o.startProcess(p)
				changed = true
			case blockedBy != "":
				if p.state != sBlocked || p.blockedBy != blockedBy {
					p.blockedBy = blockedBy
					o.setState(p, sBlocked)
					o.ev.Emit("process_blocked", p.name, map[string]any{"blocked_by": blockedBy})
					changed = true
				}
			case p.state == sBlocked:
				p.blockedBy = ""
				o.setState(p, sPending)
				changed = true
			}
		}
	}
}

// startProcess starts a new instance of p.
func (o *Orchestrator) startProcess(p *proc) {
	o.bumpGen(p)
	p.expectStop = false
	p.cause = ""
	p.blockedBy = ""
	o.registry.Clear(p.name)

	captures := o.registry.Snapshot()
	processEnv, err := env.Compose(o.cfg.ProjectPath, p.cfg.Env, captures)
	if err != nil {
		o.onFailure(p, fmt.Errorf("process %s: env composition failed: %w", p.name, err), nil)
		return
	}
	envrcEnv, _ := env.LoadEnvrc(o.cfg.ProjectPath)
	refData := interpolate.BuildRefData(captures, envrcEnv)
	resolvedCmd, err := interpolate.Resolve(p.cfg.Cmd, refData)
	if err != nil {
		o.onFailure(p, fmt.Errorf("process %s: cmd interpolation failed: %w", p.name, err), nil)
		return
	}
	for _, tmpl := range p.cfg.Templates {
		if err := tmplPkg.Render(o.cfg.ProjectPath, tmpl.Src, tmpl.Dst, buildTemplateData(captures, envrcEnv)); err != nil {
			o.onFailure(p, fmt.Errorf("process %s: template render failed: %w", p.name, err), nil)
			return
		}
	}
	p.inputs = usedCaptures(p.cfg, refData)

	var caps []*captureLib.Capture
	for _, cc := range p.cfg.Captures {
		c, err := captureLib.New(cc.Name, cc.Log, cc.IsRequired())
		if err != nil {
			o.onFailure(p, fmt.Errorf("process %s: invalid capture regex: %w", p.name, err), nil)
			return
		}
		caps = append(caps, c)
	}

	runCfg := p.cfg
	runCfg.Cmd = resolvedCmd
	name, gen := p.name, p.gen
	sup := &supervisor.Supervisor{
		Name:        p.name,
		Proc:        runCfg,
		ProjectPath: o.cfg.ProjectPath,
		Mux:         logmux.New(p.name, o.padTo, p.logFile, o.console, &o.consoleMu),
		Raw:         p.rawFile,
		Captures:    caps,
		Registry:    o.registry,
		Events:      o.supEvents,
		Env:         processEnv,
		Gen:         gen,
		Notify: func(typ string, fields map[string]any) {
			if typ == "capture" && o.isSecretCapture(name, fmt.Sprint(fields["name"])) {
				fields["value"] = "***"
			}
			if strings.HasPrefix(typ, "rebuild_") {
				o.recordRebuild(name, typ, fields)
			}
			o.ev.Emit(typ, name, fields)
		},
	}
	o.procMu.Lock()
	p.sup = sup
	o.procMu.Unlock()
	o.setState(p, sStarting)
	p.startedAt = time.Now()
	p.readyAt = time.Time{}
	o.logger.Info("starting process", "process", p.name, "cmd", resolvedCmd, "attempt", p.attempt+1)
	if err := sup.Start(context.Background()); err != nil {
		// Start reports the failure as an event too; nothing more to do.
		o.logger.Error("start failed", "process", p.name, "error", err)
	}
}

// usedCaptures returns the captured values a process's cmd and env refer to.
func usedCaptures(cfg config.Process, refs interpolate.RefData) map[string]string {
	out := map[string]string{}
	values := []string{cfg.Cmd}
	for _, v := range cfg.Env {
		values = append(values, v)
	}
	for _, v := range values {
		toks, _ := interpolate.Tokenize(v)
		for _, t := range toks {
			if !t.Literal && !strings.HasPrefix(t.Value, "envrc.") {
				out[t.Value] = refs[t.Value]
			}
		}
	}
	return out
}

func (o *Orchestrator) isSecretCapture(process, name string) bool {
	for _, c := range o.procs[process].cfg.Captures {
		if c.Name == name {
			return c.Secret
		}
	}
	return false
}

// handleSupEvent reacts to a state change of a process instance.
func (o *Orchestrator) handleSupEvent(e supervisor.Event) {
	p := o.procs[e.Process]
	if p == nil || e.Gen != p.gen {
		return // an instance that was since replaced
	}
	if len(e.LastLog) > 0 {
		p.lastLines = e.LastLog
	}
	if e.Exit != nil {
		p.lastExit = &control.ExitStatus{Code: e.Exit.Code, Signal: e.Exit.Signal, At: time.Now()}
	}

	switch e.State {
	case supervisor.StateStarting:
		p.pid = p.sup.PID()
		o.setState(p, sStarting)
		o.writeRecord()
		o.ev.Emit("process_starting", p.name, map[string]any{
			"pid": p.pid, "attempt": p.attempt + 1, "cmd": p.sup.Proc.Cmd,
		})

	case supervisor.StateReady:
		p.readyAt = time.Now()
		p.attempt = 0
		o.setState(p, sReady)
		o.ev.Emit("process_ready", p.name, map[string]any{
			"pid": p.pid, "after_ms": p.readyAt.Sub(p.startedAt).Milliseconds(),
		})
		if p.recovering {
			p.recovering = false
			o.restartChangedDependents(p)
		}
		p.healthFails = 0
		o.startHealth(p)
		o.schedule()

	case supervisor.StateFailed:
		if p.expectStop {
			return
		}
		o.onFailure(p, e.Err, e.Exit)

	case supervisor.StateExited:
		if p.expectStop {
			o.setState(p, sStopped)
			return
		}
		fields := map[string]any{"pid": p.pid, "expected": p.cfg.OnExitPolicy() == "ignore"}
		if e.Exit != nil {
			fields["exit_code"] = e.Exit.Code
			if e.Exit.Signal != "" {
				fields["signal"] = e.Exit.Signal
			}
		}
		o.ev.Emit("process_exited", p.name, fields)
		wasReady := p.state == sReady
		if p.cfg.OnExitPolicy() == "ignore" {
			o.setState(p, sExited)
			o.schedule()
			return
		}
		what := fmt.Sprintf("process %s exited before becoming ready", p.name)
		if wasReady {
			what = fmt.Sprintf("process %s exited unexpectedly post-ready", p.name)
		}
		cause := fmt.Errorf("%s (exit 0)", what)
		if e.Err != nil {
			cause = fmt.Errorf("%s: %w", what, e.Err)
		}
		o.onFailure(p, cause, e.Exit)
	}
}

// onFailure decides what a failure means: a retry, a failed process the
// stack lives with, or the end of the stack.
func (o *Orchestrator) onFailure(p *proc, cause error, exit *supervisor.ExitInfo) {
	p.cause = cause.Error()
	if o.shouldRetry(p, exit) {
		p.attempt++
		p.restarts++
		delay := o.backoff(p)
		o.setState(p, sBackoff)
		fields := map[string]any{"cause": p.cause, "attempt": p.attempt, "delay_ms": delay.Milliseconds()}
		if exit != nil {
			fields["exit_code"] = exit.Code
		}
		o.ev.Emit("process_backoff", p.name, fields)
		o.notice(color.New(color.FgYellow), fmt.Sprintf("%s failed (%s); retrying in %s (attempt %d%s)",
			p.name, p.cause, delay, p.attempt+1, o.retryLimit(p)))
		p.recovering = true
		o.retire(p, func() {
			gen := p.gen
			p.retryTimer = time.AfterFunc(delay, func() { o.retries <- retryTick{name: p.name, gen: gen} })
		})
		return
	}

	required := p.cfg.IsRequired()
	if required && !o.cfg.KeepGoing {
		o.abort(p, cause)
		return
	}
	o.setState(p, sFailed)
	o.ev.Emit("process_failed", p.name, map[string]any{
		"cause": p.cause, "last_lines": p.lastLines, "log_file": p.logPath, "required": required,
	})
	why := "not required, continuing"
	if required {
		why = "--keep-going; fix it and run 'stackstart start " + p.name + "'"
		o.printFailure(p, cause)
	}
	o.notice(color.New(color.FgYellow), fmt.Sprintf("%s failed (%s): %s", p.name, why, cause))
	o.retire(p, nil)
	o.schedule()
}

func (o *Orchestrator) retryLimit(p *proc) string {
	if p.cfg.Retries > 0 {
		return fmt.Sprintf(" of %d", p.cfg.Retries+1)
	}
	return ""
}

func (o *Orchestrator) shouldRetry(p *proc, exit *supervisor.ExitInfo) bool {
	switch p.cfg.RestartPolicy() {
	case "always":
	case "on-failure":
		if exit != nil && exit.Code == 0 && !p.cfg.IsOneshot() {
			return false
		}
	default:
		return false
	}
	return p.cfg.Retries == 0 || p.attempt < p.cfg.Retries
}

func (o *Orchestrator) backoff(p *proc) time.Duration {
	lo, hi := p.cfg.BackoffRange()
	d := lo
	for i := 1; i < p.attempt && d < hi; i++ {
		d *= 2
	}
	if d > hi {
		d = hi
	}
	return d
}

func (o *Orchestrator) handleRetry(t retryTick) {
	p := o.procs[t.name]
	if p == nil || p.gen != t.gen || p.state != sBackoff {
		return
	}
	p.retryTimer = nil
	o.setState(p, sPending)
	o.schedule()
}

// retire stops p's current instance if it is still running (without
// touching dependents), then runs then. The instance's events are ignored
// from now on.
func (o *Orchestrator) retire(p *proc, then func()) {
	sup := p.sup
	o.bumpGen(p)
	if sup == nil || !sup.Alive() {
		if then != nil {
			then()
		}
		return
	}
	grace := p.cfg.GracePeriod()
	id := o.newPlan(then)
	go func() {
		sup.Stop(grace)
		o.stopDone <- id
	}()
}

func (o *Orchestrator) newPlan(then func()) int {
	o.nextPlan++
	if then == nil {
		then = func() {}
	}
	o.plans[o.nextPlan] = then
	return o.nextPlan
}

// restartChangedDependents restarts processes that use a captured value of
// p that changed when the retry policy brought p back.
func (o *Orchestrator) restartChangedDependents(p *proc) {
	now := o.registry.Snapshot()
	for _, name := range o.order {
		d := o.procs[name]
		if d == p || d.cfg.OnDepRestart == "ignore" || (d.state != sReady && d.state != sDegraded && d.state != sStarting) {
			continue
		}
		for ref, old := range d.inputs {
			proc, capName, _ := strings.Cut(ref, ".")
			if proc != p.name {
				continue
			}
			if now[proc][capName] != old {
				o.notice(color.New(color.FgYellow), fmt.Sprintf("restarting %s: ${%s} changed", d.name, ref))
				o.restart(d, "dependency")
				break
			}
		}
	}
}

// stopTargets stops name and everything that depends on it (dependents
// first), then runs then.
func (o *Orchestrator) stopTargets(name, by string, then func(targets []*proc)) []string {
	names := o.stopOrder(append(o.dependents(name), name))
	var targets []*proc
	type stopJob struct {
		sup   *supervisor.Supervisor
		grace time.Duration
	}
	var jobs []stopJob
	for _, n := range names {
		p := o.procs[n]
		targets = append(targets, p)
		if p.retryTimer != nil {
			p.retryTimer.Stop()
			p.retryTimer = nil
		}
		if p.sup != nil && p.sup.Alive() {
			p.expectStop = true
			o.setState(p, sStopping)
			o.ev.Emit("process_stopping", p.name, map[string]any{"by": by})
			jobs = append(jobs, stopJob{p.sup, p.cfg.GracePeriod()})
		}
	}
	id := o.newPlan(func() {
		for _, p := range targets {
			o.bumpGen(p)
			p.expectStop = false
			p.pid = 0
		}
		then(targets)
		o.writeRecord()
	})
	go func() {
		for _, j := range jobs {
			j.sup.Stop(j.grace)
		}
		o.stopDone <- id
	}()
	return names
}

// stop stops a process and its dependents; the process stays stopped and
// its dependents wait for it.
func (o *Orchestrator) stop(p *proc, by string) []string {
	return o.stopTargets(p.name, by, func(targets []*proc) {
		for _, t := range targets {
			state := sPending
			if t == p {
				state = sStopped
			}
			o.setState(t, state)
			o.ev.Emit("process_stopped", t.name, map[string]any{"by": by})
		}
		o.notice(color.New(color.FgYellow), "stopped "+strings.Join(procNames(targets), ", "))
		o.schedule()
	})
}

// restart stops a process and its dependents, then starts them again.
func (o *Orchestrator) restart(p *proc, by string) []string {
	p.restarts++
	o.ev.Emit("process_restarting", p.name, map[string]any{"by": by})
	return o.stopTargets(p.name, by, func(targets []*proc) {
		for _, t := range targets {
			t.attempt = 0
			o.setState(t, sPending)
			o.ev.Emit("process_stopped", t.name, map[string]any{"by": "restart"})
		}
		o.schedule()
	})
}

func procNames(ps []*proc) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.name)
	}
	return out
}

// printFailure prints the FAILED block for a process.
func (o *Orchestrator) printFailure(p *proc, cause error) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n--- FAILED: %s ---\n", p.name)
	if cause != nil {
		fmt.Fprintf(&b, "Cause: %s\n", cause)
	}
	if len(p.lastLines) > 0 {
		b.WriteString("Last output:\n")
		for _, line := range p.lastLines {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	fmt.Fprintf(&b, "Full log: %s\n", filepath.Join(o.runDir, p.name+".log"))
	fmt.Fprintf(&b, "Read it later with: stackstart logs %s\n", p.name)
	o.consoleMu.Lock()
	_, _ = fmt.Fprint(o.notices, b.String())
	o.consoleMu.Unlock()
}

// abort ends the stack because a required process failed.
func (o *Orchestrator) abort(p *proc, cause error) {
	o.setState(p, sFailed)
	o.ev.Emit("process_failed", p.name, map[string]any{
		"cause": cause.Error(), "last_lines": p.lastLines, "log_file": p.logPath, "required": true,
	})
	o.printFailure(p, cause)
	o.ev.Emit("run_stopping", "", map[string]any{"reason": "failure"})
	o.shutdownAll()
	o.finish(session.OutcomeFailed, p.name, cause)
	o.result = fmt.Errorf("process %s failed: %w", p.name, cause)
}

// gracefulShutdown stops everything and ends the run.
func (o *Orchestrator) gracefulShutdown(reason string) {
	o.logger.Info("shutting down", "reason", reason)
	o.ev.Emit("run_stopping", "", map[string]any{"reason": reason})
	o.shutdownAll()
	o.finish(session.OutcomeStopped, "", nil)
}

// forceShutdown kills everything at once.
func (o *Orchestrator) forceShutdown() {
	o.ev.Emit("run_stopping", "", map[string]any{"reason": "force"})
	o.rec.Stopping = true
	o.writeRecord()
	o.ForceKill()
	o.finish(session.OutcomeStopped, "", nil)
	o.result = ErrForceKilled
}

// shutdownAll stops every process that is still running, dependents first.
// A process's state does not matter: one that failed readiness may still be
// alive and must be stopped too.
func (o *Orchestrator) shutdownAll() {
	o.stopping.Store(true)
	o.rec.Stopping = true
	o.writeRecord()
	for _, p := range o.procs {
		if p.retryTimer != nil {
			p.retryTimer.Stop()
		}
		p.expectStop = true
	}
	for _, name := range reverseDepOrder(o.cfg.Profile.Processes) {
		p := o.procs[name]
		if p.sup == nil || !p.sup.Alive() {
			continue
		}
		o.logger.Info("stopping process", "process", name)
		p.sup.Stop(p.cfg.GracePeriod())
	}
}

// finish records how the run ended and ends the loop.
func (o *Orchestrator) finish(outcome, failedProcess string, cause error) {
	now := time.Now()
	o.rec.EndedAt = &now
	o.rec.Outcome = outcome
	o.rec.FailedProcess = failedProcess
	if cause != nil {
		o.rec.Cause = cause.Error()
	}
	for _, p := range o.procs {
		if p.state != sFailed {
			_ = session.WriteProcessState(o.stateFile(p.name), sExited, p.pid)
		}
	}
	if err := session.WriteRunInfo(o.rec); err != nil {
		o.logger.Error("failed to write run.json", "error", err)
	}
	fields := map[string]any{"outcome": outcome}
	if failedProcess != "" {
		fields["failed_process"] = failedProcess
		fields["cause"] = o.rec.Cause
	}
	o.ev.Emit("run_finished", "", fields)
	o.finished = true
}

// settled reports whether p needs nothing more for the stack to count as
// up: it is ready or done, or it is an optional process that failed (or
// waits for one that did).
func (o *Orchestrator) settled(p *proc) bool {
	switch p.state {
	case sReady, sExited, sDegraded:
		return true
	case sFailed:
		return !p.cfg.IsRequired()
	case sBlocked:
		root := o.procs[p.blockedBy]
		return root != nil && !root.cfg.IsRequired()
	}
	return false
}

// check announces readiness and ends the run when nothing is left to do.
func (o *Orchestrator) check() {
	allReady := true
	for _, p := range o.procs {
		if !o.settled(p) {
			allReady = false
		}
	}
	if allReady && !o.allReady {
		o.ev.Emit("stack_ready", "", nil)
		if !o.announced {
			o.announced = true
			what := fmt.Sprintf("all %d processes ready", len(o.procs))
			if len(o.procs) == 1 {
				what = "1 process ready"
			}
			o.notice(color.New(color.FgGreen), fmt.Sprintf("%s · logs: %s", what, o.runDir))
		}
	}
	o.allReady = allReady

	// Finished: nothing running or able to run on its own.
	var failedRequired *proc
	for _, name := range o.order {
		p := o.procs[name]
		switch p.state {
		case sExited, sBlocked:
		case sFailed:
			if p.cfg.IsRequired() && failedRequired == nil {
				failedRequired = p
			}
		case sReady, sDegraded:
			if !p.cfg.IsOneshot() {
				return
			}
		default: // pending, starting, backoff, stopping, stopped
			return
		}
	}
	if len(o.plans) > 0 {
		return
	}
	if failedRequired != nil {
		cause := fmt.Errorf("%s", failedRequired.cause)
		o.notice(color.New(color.FgRed), "nothing left running; "+failedRequired.name+" failed")
		o.finish(session.OutcomeFailed, failedRequired.name, cause)
		o.result = fmt.Errorf("process %s failed: %w", failedRequired.name, cause)
		return
	}
	o.notice(color.New(color.FgGreen), "all processes finished")
	o.finish(session.OutcomeStopped, "", nil)
}
