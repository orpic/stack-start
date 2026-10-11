package orchestrator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orpic/stack-start/internal/config"
	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/env"
	"github.com/orpic/stack-start/internal/events"
	"github.com/orpic/stack-start/internal/interpolate"
	"github.com/orpic/stack-start/internal/session"
	"github.com/orpic/stack-start/internal/supervisor"
)

// command is a request handed from a connection to the loop.
type command struct {
	req   control.Request
	reply chan cmdReply
}

type cmdReply struct {
	result    any
	err       *control.Error
	seq       int64
	targets   []string
	logPath   string
	logOffset int64
	sup       *supervisor.Supervisor
}

func (o *Orchestrator) listen() error {
	path, err := control.SocketPath(session.SessionDir(o.slug), o.slug)
	if err != nil {
		return err
	}
	// A socket left by a stack that died without cleaning up.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	o.listener, o.socketPath = ln, path
	o.done = make(chan struct{})
	return nil
}

func (o *Orchestrator) closeListener() {
	close(o.done)
	_ = o.listener.Close()
	_ = os.Remove(o.socketPath)
}

func (o *Orchestrator) serve() {
	for {
		conn, err := o.listener.Accept()
		if err != nil {
			return
		}
		go o.handleConn(conn)
	}
}

// ask hands a request to the loop and waits for its answer.
func (o *Orchestrator) ask(req control.Request) cmdReply {
	c := &command{req: req, reply: make(chan cmdReply, 1)}
	select {
	case o.cmds <- c:
	case <-o.done:
		return cmdReply{err: &control.Error{Code: control.CodeShuttingDown, Message: "the stack is stopping"}}
	}
	select {
	case r := <-c.reply:
		return r
	case <-o.done:
		return cmdReply{err: &control.Error{Code: control.CodeShuttingDown, Message: "the stack is stopping"}}
	}
}

// handleCommand runs in the loop.
func (o *Orchestrator) handleCommand(c *command) {
	req := c.req
	reply := func(r cmdReply) { c.reply <- r }

	if req.Op == "status" {
		reply(cmdReply{result: o.snapshot()})
		return
	}
	if req.Op == "shutdown" {
		reply(cmdReply{})
		if req.Force {
			o.forceShutdown()
		} else {
			o.gracefulShutdown("down")
		}
		return
	}

	p := o.procs[req.Process]
	if p == nil {
		reply(cmdReply{err: &control.Error{
			Code:    control.CodeNotFound,
			Message: fmt.Sprintf("process %q not found in profile %q", req.Process, o.cfg.ProfileName),
			Process: req.Process,
			Hint:    "processes: " + strings.Join(o.order, ", "),
		}})
		return
	}
	seq := o.ev.LastSeq()
	switch req.Op {
	case "start":
		switch p.state {
		case sStarting, sReady, sStopping, sDegraded:
			reply(cmdReply{err: &control.Error{Code: control.CodeConflict, Process: p.name,
				Message: fmt.Sprintf("process %s is already %s", p.name, p.state),
				Hint:    "use 'stackstart restart " + p.name + "' to restart it"}})
			return
		}
		if p.retiring {
			reply(cmdReply{err: &control.Error{Code: control.CodeConflict, Process: p.name,
				Message: fmt.Sprintf("process %s is still stopping its previous instance", p.name),
				Hint:    "try again in a moment, or 'stackstart wait " + p.name + " --for exit'"}})
			return
		}
		if p.state == sBlocked {
			root := p.blockedBy
			reply(cmdReply{err: &control.Error{Code: control.CodeConflict, Process: p.name,
				Message: fmt.Sprintf("process %s is blocked: %s failed", p.name, root),
				Hint:    fmt.Sprintf("fix it, then run 'stackstart start %s'; %s starts once it is ready", root, p.name)}})
			return
		}
		if p.retryTimer != nil {
			p.retryTimer.Stop()
			p.retryTimer = nil
		}
		p.attempt = 0
		o.bumpGen(p)
		o.setState(p, sPending)
		o.ev.Emit("start_requested", p.name, nil)
		o.schedule()
		reply(cmdReply{seq: seq, targets: []string{p.name}})
	case "stop":
		reply(cmdReply{seq: seq, targets: o.stop(p, "user")})
	case "restart":
		reply(cmdReply{seq: seq, targets: o.restart(p, "user")})
	case "env":
		vars, err := env.ComposeExplained(o.cfg.ProjectPath, p.cfg.Env, o.registry.Snapshot(), os.Environ())
		if err != nil {
			reply(cmdReply{err: &control.Error{Code: control.CodeFailed, Process: p.name, Message: err.Error()}})
			return
		}
		cwd := o.cfg.ProjectPath
		if p.cfg.Cwd != "" {
			cwd = filepath.Join(o.cfg.ProjectPath, p.cfg.Cwd)
		}
		reply(cmdReply{result: control.EnvResult{Process: p.name, Cwd: cwd, Vars: vars, Secret: o.secretEnv(p)}})
	case "send":
		if p.sup == nil || !p.sup.Alive() {
			reply(cmdReply{err: &control.Error{Code: control.CodeConflict, Process: p.name,
				Message: fmt.Sprintf("process %s is not running (%s)", p.name, p.state)}})
			return
		}
		var off int64
		if st, err := os.Stat(p.logPath); err == nil {
			off = st.Size()
		}
		o.ev.Emit("input_sent", p.name, map[string]any{"bytes": len(req.Data)})
		// The write happens in the connection's goroutine: a process that
		// doesn't read its terminal must not block the loop.
		reply(cmdReply{logPath: p.logPath, logOffset: off, sup: p.sup})
	default:
		reply(cmdReply{err: &control.Error{Code: control.CodeInvalid, Message: "unknown op " + strconv.Quote(req.Op)}})
	}
}

func (o *Orchestrator) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	w := control.NewWriter(conn)
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req control.Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = w.Fail(&control.Error{Code: control.CodeInvalid, Message: "bad request: " + err.Error()})
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond

	switch req.Op {
	case "status":
		r := o.ask(req)
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		st := r.result.(control.SessionStatus)
		st.Git = gitInfo(st.Project, startHead(o.startGit))
		_ = w.OK(st)

	case "start", "stop", "restart":
		r := o.ask(req)
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		if req.Wait == "" || req.Wait == "none" {
			_ = w.OK(map[string]any{"targets": r.targets, "seq": r.seq})
			return
		}
		o.awaitTargets(w, req, r, timeout)

	case "send":
		r := o.ask(req)
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		written := make(chan error, 1)
		go func() {
			_, err := r.sup.Write([]byte(req.Data))
			written <- err
		}()
		select {
		case err := <-written:
			if err != nil {
				_ = w.Fail(&control.Error{Code: control.CodeFailed, Process: req.Process, Message: err.Error()})
				return
			}
		case <-time.After(5 * time.Second):
			_ = w.Fail(&control.Error{Code: control.CodeTimeout, Process: req.Process,
				Message: fmt.Sprintf("process %s is not reading its input", req.Process)})
			return
		}
		echo := time.Duration(req.EchoMS) * time.Millisecond
		if echo <= 0 {
			echo = 500 * time.Millisecond
		}
		time.Sleep(echo)
		_ = w.OK(map[string]any{"output": readFrom(r.logPath, r.logOffset)})

	case "env":
		r := o.ask(req)
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		_ = w.OK(r.result)

	case "events":
		o.streamEvents(w, req)

	case "wait":
		o.waitFor(w, req, timeout)

	case "shutdown":
		r := o.ask(req)
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		_ = w.OK(nil)

	default:
		_ = w.Fail(&control.Error{Code: control.CodeInvalid, Message: "unknown op " + strconv.Quote(req.Op)})
	}
}

func readFrom(path string, off int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	return string(data)
}

// awaitTargets streams the events of a start/stop/restart until every
// target reached the requested state, one failed, or time ran out.
func (o *Orchestrator) awaitTargets(w *control.Writer, req control.Request, r cmdReply, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	want := "process_ready"
	switch {
	case req.Op == "stop":
		want = "process_stopped"
	case req.Wait == "started":
		want = "process_starting"
	}
	pending := map[string]bool{}
	for _, t := range r.targets {
		pending[t] = true
	}
	if req.Op == "restart" && want == "process_stopped" {
		want = "process_ready"
	}
	deadline := time.After(timeout)
	after := r.seq
	for {
		evs, changed := o.ev.After(after)
		for _, e := range evs {
			after = e.Seq
			if !pending[e.Process] && e.Type != "run_finished" {
				continue
			}
			if err := w.Event(e); err != nil {
				return
			}
			switch {
			case e.Type == want:
				delete(pending, e.Process)
			case e.Type == "process_failed" && want != "process_stopped":
				_ = w.Fail(failureError(e))
				return
			case e.Type == "run_finished":
				_ = w.Fail(&control.Error{Code: control.CodeShuttingDown, Message: "the stack stopped"})
				return
			}
			if len(pending) == 0 {
				_ = w.OK(map[string]any{"targets": r.targets, "seq": e.Seq})
				return
			}
		}
		select {
		case <-changed:
		case <-deadline:
			var left []string
			for t := range pending {
				left = append(left, t)
			}
			_ = w.Fail(&control.Error{Code: control.CodeTimeout,
				Message: fmt.Sprintf("timed out after %s waiting for %s", timeout, strings.Join(left, ", "))})
			return
		case <-o.done:
			_ = w.Fail(&control.Error{Code: control.CodeShuttingDown, Message: "the stack stopped"})
			return
		}
	}
}

func failureError(e events.Event) *control.Error {
	ce := &control.Error{Code: control.CodeFailed, Process: e.Process}
	ce.Message = fmt.Sprintf("process %s failed: %v", e.Process, e.Fields["cause"])
	if lines, ok := e.Fields["last_lines"].([]string); ok {
		ce.LastLines = lines
	} else if lines, ok := e.Fields["last_lines"].([]any); ok {
		for _, l := range lines {
			ce.LastLines = append(ce.LastLines, fmt.Sprint(l))
		}
	}
	if lf, ok := e.Fields["log_file"].(string); ok {
		ce.LogFile = lf
	}
	return ce
}

// streamEvents sends events after req.After; with Follow it keeps going
// until the run ends or the client goes away.
func (o *Orchestrator) streamEvents(w *control.Writer, req control.Request) {
	types := map[string]bool{}
	for _, t := range req.Types {
		types[t] = true
	}
	after := req.After
	for {
		evs, changed := o.ev.After(after)
		for _, e := range evs {
			after = e.Seq
			if len(types) > 0 && !types[e.Type] {
				continue
			}
			if req.Process != "" && e.Process != req.Process && e.Process != "" {
				continue
			}
			if err := w.Event(e); err != nil {
				return
			}
			if e.Type == "run_finished" {
				_ = w.OK(map[string]any{"last_seq": e.Seq})
				return
			}
		}
		if !req.Follow {
			_ = w.OK(map[string]any{"last_seq": after})
			return
		}
		select {
		case <-changed:
		case <-o.done:
			_ = w.OK(map[string]any{"last_seq": after})
			return
		}
	}
}

// waitFor blocks until a condition holds: "ready", "exit", "rebuilt" or
// "event:<type>", for events after req.After.
func (o *Orchestrator) waitFor(w *control.Writer, req control.Request, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cond := req.For
	if cond == "" {
		cond = "ready"
	}
	after := req.After
	if req.Process != "" {
		r := o.ask(control.Request{Op: "status"})
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		st := r.result.(control.SessionStatus)
		if after == 0 {
			// "From now on" is the moment of this snapshot, so an event
			// that comes right after it is not missed.
			after = st.Events.LastSeq
		}
		found := false
		for _, ps := range st.Processes {
			if ps.Name != req.Process {
				continue
			}
			found = true
			// Without --after, a condition that already holds is met.
			if req.After == 0 && cond == "ready" && ps.State == sReady {
				_ = w.OK(map[string]any{"state": ps.State, "seq": st.Events.LastSeq})
				return
			}
		}
		if !found {
			_ = w.Fail(&control.Error{Code: control.CodeNotFound, Process: req.Process,
				Message: fmt.Sprintf("process %q not found in profile %q", req.Process, o.cfg.ProfileName)})
			return
		}
	}
	if req.Process == "" && cond == "ready" {
		r := o.ask(control.Request{Op: "status"})
		if r.err != nil {
			_ = w.Fail(r.err)
			return
		}
		st := r.result.(control.SessionStatus)
		if st.Ready && req.After == 0 {
			_ = w.OK(map[string]any{"state": "ready", "seq": st.Events.LastSeq})
			return
		}
		if after == 0 {
			after = st.Events.LastSeq
		}
	}
	match := func(e events.Event) bool {
		if req.Process != "" && e.Process != req.Process {
			return false
		}
		switch {
		case cond == "ready" && req.Process == "":
			return e.Type == "stack_ready"
		case cond == "ready":
			return e.Type == "process_ready"
		case cond == "exit":
			return e.Type == "process_exited" || e.Type == "process_stopped" || e.Type == "process_failed"
		case cond == "rebuilt":
			return e.Type == "rebuild_finished" || e.Type == "process_ready"
		case strings.HasPrefix(cond, "event:"):
			return e.Type == strings.TrimPrefix(cond, "event:")
		}
		return false
	}
	deadline := time.After(timeout)
	if after == 0 {
		// Without --after, only what happens from now on counts (a process
		// that is already ready was handled above).
		after = o.ev.LastSeq()
	}
	for {
		evs, changed := o.ev.After(after)
		for _, e := range evs {
			after = e.Seq
			if match(e) {
				_ = w.Event(e)
				_ = w.OK(map[string]any{"seq": e.Seq})
				return
			}
			failed := e.Type == "process_failed" || (e.Type == "rebuild_failed" && cond == "rebuilt")
			if failed && (req.Process == "" || e.Process == req.Process) && cond != "exit" {
				_ = w.Event(e)
				_ = w.Fail(failureError(e))
				return
			}
			if e.Type == "run_finished" {
				_ = w.Fail(&control.Error{Code: control.CodeShuttingDown, Message: "the stack stopped"})
				return
			}
		}
		select {
		case <-changed:
		case <-deadline:
			_ = w.Fail(&control.Error{Code: control.CodeTimeout, Process: req.Process,
				Message: fmt.Sprintf("timed out after %s waiting for %s", timeout, cond)})
			return
		case <-o.done:
			_ = w.Fail(&control.Error{Code: control.CodeShuttingDown, Message: "the stack stopped"})
			return
		}
	}
}

// snapshot builds the status document for this session (in the loop).
func (o *Orchestrator) snapshot() control.SessionStatus {
	st := control.SessionStatus{
		Profile:       o.cfg.ProfileName,
		Project:       o.cfg.ProjectPath,
		ConfigFile:    o.cfg.ConfigFile,
		RunID:         o.rec.RunID,
		StackstartPID: os.Getpid(),
		Detached:      o.cfg.Detached,
		KeepGoing:     o.cfg.KeepGoing,
		StartedAt:     o.rec.StartedAt,
		State:         "running",
		LogDir:        o.runDir,
		ConsoleLog:    o.rec.ConsoleLog,
		Events:        control.EventsInfo{File: o.rec.EventsFile, LastSeq: o.ev.LastSeq()},
		ControlSocket: o.socketPath,
		Ports:         o.cfg.Ports,
	}
	if o.rec.Stopping {
		st.State = "stopping"
	}
	st.Ready = o.allReady
	caps := o.registry.Snapshot()
	for _, name := range o.order {
		p := o.procs[name]
		kind := "long-running"
		if p.cfg.Kind != "" {
			kind = p.cfg.Kind
		}
		cwd := o.cfg.ProjectPath
		if p.cfg.Cwd != "" {
			cwd = o.cfg.ProjectPath + "/" + p.cfg.Cwd
		}
		ps := control.ProcessStatus{
			Name: name, Kind: kind, State: p.state, PID: p.pid, Cwd: cwd,
			DependsOn: append([]string{}, p.cfg.DependsOn...), BlockedBy: p.blockedBy,
			Ports: tcpPorts(p.cfg), Captures: map[string]string{},
			Restarts: p.restarts, Attempt: p.attempt, LastExit: p.lastExit, Cause: p.cause,
			LogFile: p.logPath, RawLogFile: p.rawPath,
		}
		for k, v := range caps[name] {
			if o.isSecretCapture(name, k) {
				v = "***"
			}
			ps.Captures[k] = v
		}
		if p.state != sPending && p.state != sBlocked && !p.startedAt.IsZero() {
			t := p.startedAt
			ps.StartedAt = &t
		}
		if !p.readyAt.IsZero() {
			ms := p.readyAt.Sub(p.startedAt).Milliseconds()
			ps.ReadyAfterMS = &ms
		}
		ps.HealthFailures = p.healthFails
		if rs := o.rebuilds[name]; rs != nil {
			rs.mu.Lock()
			ps.Rebuilds = rs.count
			if rs.state != "" {
				ps.LastRebuild = &control.RebuildStatus{State: rs.state, At: rs.at, DurationMS: rs.duration, Error: rs.lastError}
			}
			rs.mu.Unlock()
		}
		if p.sup != nil && p.sup.Alive() {
			// The supervisor knows a daemon's real PID once it has started.
			ps.PID = p.sup.PID()
		}
		if p.state == sStopped || p.state == sPending || p.state == sBlocked || p.state == sBackoff || p.state == sFailed {
			ps.PID = 0
		}
		st.Processes = append(st.Processes, ps)
	}
	return st
}

// secretEnv lists the env variables of p whose values are secret: named in
// secret_env, or set from a capture marked secret.
func (o *Orchestrator) secretEnv(p *proc) []string {
	names := append([]string{}, p.cfg.SecretEnv...)
	for k, v := range p.cfg.Env {
		toks, _ := interpolate.Tokenize(v)
		for _, t := range toks {
			if t.Literal {
				continue
			}
			if proc, capName, ok := strings.Cut(t.Value, "."); ok && o.procs[proc] != nil && o.isSecretCapture(proc, capName) {
				names = append(names, k)
			}
		}
	}
	return names
}

func tcpPorts(cfg config.Process) []int {
	ports := []int{}
	if cfg.Readiness == nil {
		return ports
	}
	for _, c := range cfg.Readiness.Checks {
		if c.TCP == "" {
			continue
		}
		if i := strings.LastIndex(c.TCP, ":"); i >= 0 {
			if n, err := strconv.Atoi(c.TCP[i+1:]); err == nil {
				ports = append(ports, n)
			}
		}
	}
	return ports
}

func startHead(g *control.GitInfo) string {
	if g == nil {
		return ""
	}
	return g.Head
}

// gitInfo describes the checkout at dir, or nil if it isn't one.
func gitInfo(dir, startHead string) *control.GitInfo {
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	root := run("rev-parse", "--show-toplevel")
	if root == "" {
		return nil
	}
	g := &control.GitInfo{
		Root:   root,
		Branch: run("rev-parse", "--abbrev-ref", "HEAD"),
		Head:   run("rev-parse", "--short", "HEAD"),
	}
	gitDir, common := run("rev-parse", "--absolute-git-dir"), run("rev-parse", "--path-format=absolute", "--git-common-dir")
	g.Worktree = gitDir != "" && common != "" && gitDir != common
	if startHead == "" {
		startHead = g.Head
	}
	g.StartHead = startHead
	g.HeadMoved = g.Head != startHead
	return g
}
