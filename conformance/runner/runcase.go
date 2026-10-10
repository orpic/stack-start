package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type Result struct {
	Case     *Case
	Status   string // PASS, FAIL, SKIP
	Reason   string // skip reason or failure summary
	Detail   string // failure details: step, expectation, outputs
	Duration time.Duration
	WorkDir  string
}

type Options struct {
	Bin      string
	Root     string
	WorkRoot string
	KeepWork bool
	Verbose  bool
	Sampler  *Sampler
	Log      func(string)
}

// caseRun is the state of one case while it runs.
type caseRun struct {
	c        *Case
	opts     Options
	vars     map[string]string
	env      []string
	work     string
	base     string
	token    string
	ports    map[string]int
	cmds     map[string]*command
	saved    map[string]string
	order    []*command
	tracker  *Tracker
	restores map[string]*[]byte // project files to restore: path -> original (nil = delete)
	stepNum  int
}

func runCase(ctx context.Context, c *Case, opts Options) (res Result) {
	start := time.Now()
	res = Result{Case: c}
	defer func() { res.Duration = time.Since(start) }()

	if !platformOK(c, runtime.GOOS) {
		res.Status, res.Reason = "SKIP", "not on "+runtime.GOOS
		return res
	}

	r := &caseRun{c: c, opts: opts, cmds: map[string]*command{}, saved: map[string]string{}, restores: map[string]*[]byte{}, tracker: NewTracker()}
	r.token = "ssc" + randomHex(6)

	base, err := os.MkdirTemp(opts.WorkRoot, "ssc-")
	if err != nil {
		return fail(res, "setup", err.Error(), "")
	}
	base, _ = filepath.EvalSymlinks(base)
	r.base = base
	res.WorkDir = base
	defer func() {
		if !opts.KeepWork && res.Status != "FAIL" {
			_ = os.RemoveAll(base)
		}
	}()

	if err := r.setup(); err != nil {
		return fail(res, "setup", err.Error(), "")
	}
	defer r.restoreFiles()

	timeout, _ := c.TimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, req := range c.Requires {
		req = substitute(req, r.vars)
		rctx, rcancel := context.WithTimeout(ctx, 60*time.Second)
		cmd := exec.CommandContext(rctx, "/bin/sh", "-c", req)
		cmd.Dir, cmd.Env = r.work, r.env
		out, err := cmd.CombinedOutput()
		rcancel()
		if err != nil {
			res.Status = "SKIP"
			res.Reason = fmt.Sprintf("requires %q: %v: %s", req, err, strings.TrimSpace(truncate(string(out), 200)))
			return res
		}
	}

	steps, caseEnv, err := c.parseSteps(r.vars)
	if err != nil {
		return fail(res, "setup", err.Error(), "")
	}
	for k, v := range caseEnv {
		r.env = setEnv(r.env, k, v)
	}

	opts.Sampler.Register(r.tracker)
	defer opts.Sampler.Unregister(r.tracker)

	var stepErr error
	var failedStep string
	for i, step := range steps {
		r.stepNum = i + 1
		desc := describeStep(step)
		if opts.Verbose {
			opts.Log(fmt.Sprintf("  %s step %d: %s", c.ID, i+1, desc))
		}
		if err := r.runStep(ctx, step); err != nil {
			if ctx.Err() != nil && !errors.Is(err, errCaseTimeout) {
				err = fmt.Errorf("%v (case timeout %s reached)", err, timeout)
			}
			stepErr = err
			failedStep = fmt.Sprintf("step %d: %s", i+1, desc)
			break
		}
	}

	leakErr := r.cleanup(stepErr == nil)
	switch {
	case stepErr != nil:
		res = fail(res, failedStep, stepErr.Error(), r.report(leakErr))
	case leakErr != nil:
		res = fail(res, "leak check", leakErr.Error(), r.report(nil))
	default:
		res.Status = "PASS"
	}
	return res
}

var errCaseTimeout = errors.New("case timeout")

func fail(res Result, where, why, detail string) Result {
	res.Status = "FAIL"
	res.Reason = where + ": " + why
	res.Detail = detail
	return res
}

func (r *caseRun) report(leakErr error) string {
	var b strings.Builder
	if leakErr != nil {
		fmt.Fprintf(&b, "also failed the leak check: %v\n", leakErr)
	}
	for _, c := range r.order {
		b.WriteString(c.describe())
	}
	return b.String()
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// setup creates the work, state and home directories, allocates ports and
// copies the case folder.
func (r *caseRun) setup() error {
	c := r.c
	state := filepath.Join(r.base, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	home := os.Getenv("HOME")
	if c.HomeMode() == "isolated" {
		home = filepath.Join(r.base, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			return err
		}
	}

	r.ports = map[string]int{}
	var listeners []net.Listener
	for _, name := range c.Ports {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("allocating port %s: %w", name, err)
		}
		listeners = append(listeners, ln)
		r.ports[name] = ln.Addr().(*net.TCPAddr).Port
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}

	if c.Project != "" {
		r.work = filepath.Join(r.opts.Root, c.Project)
		if st, err := os.Stat(r.work); err != nil || !st.IsDir() {
			return fmt.Errorf("project %s not found", r.work)
		}
	} else {
		r.work = filepath.Join(r.base, "work")
	}

	r.vars = map[string]string{
		"BIN":   r.opts.Bin,
		"WORK":  r.work,
		"CASE":  c.Dir,
		"ROOT":  r.opts.Root,
		"TOKEN": r.token,
		"STATE": state,
		"HOME":  home,
	}
	for name, port := range r.ports {
		r.vars["PORT_"+name] = fmt.Sprint(port)
	}

	if c.Project == "" {
		if err := copyCase(c.Dir, r.work, r.vars); err != nil {
			return fmt.Errorf("copying case: %w", err)
		}
	}

	env := os.Environ()
	env = unsetEnv(env, "NO_COLOR")
	env = setEnv(env, "XDG_STATE_HOME", state)
	env = setEnv(env, "HOME", home)
	env = setEnv(env, "STACKSTART_BIN", r.opts.Bin)
	r.env = env
	return nil
}

// copyCase copies the case folder (except case.yaml) into the work dir,
// substituting variables in text files.
func copyCase(src, dst string, vars map[string]string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if rel == "case.yaml" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if utf8.Valid(data) {
			data = []byte(substitute(string(data), vars))
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func setEnv(env []string, k, v string) []string {
	env = unsetEnv(env, k)
	return append(env, k+"="+v)
}

func unsetEnv(env []string, k string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, k+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// cleanup stops everything still running and runs the leak check. When the
// steps passed, a command still running is itself a failure.
func (r *caseRun) cleanup(stepsPassed bool) error {
	var problems []string

	// Commands still running.
	var running []*command
	for _, c := range r.order {
		if c.finished() || c.label == "spawn" {
			continue
		}
		running = append(running, c)
	}
	if len(running) > 0 {
		var ids []string
		for _, c := range running {
			ids = append(ids, c.id)
			_ = c.cmd.Process.Signal(syscall.SIGTERM)
		}
		if stepsPassed {
			problems = append(problems, "still running at the end of the case: "+strings.Join(ids, ", "))
		}
		deadline := time.After(15 * time.Second)
		for _, c := range running {
			select {
			case <-c.done:
			case <-deadline:
				_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
				_ = c.cmd.Process.Kill()
				<-c.done
			}
		}
	}

	// Leaks: wait up to 2s for everything the case started to be gone.
	var leaked []procInfo
	deadline := time.Now().Add(2 * time.Second)
	for {
		table := r.opts.Sampler.SampleNow()
		if table == nil {
			break
		}
		leaked = r.tracker.alive(table, r.token)
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(leaked) > 0 {
		var lines []string
		for _, p := range leaked {
			lines = append(lines, fmt.Sprintf("  pid %d: %s", p.PID, truncate(p.Command, 200)))
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
		}
		sort.Strings(lines)
		problems = append(problems, "processes left running (killed):\n"+strings.Join(lines, "\n"))
	}

	// Helpers: kill them and their children.
	for _, c := range r.order {
		if c.label == "spawn" && !c.finished() {
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	if table := r.opts.Sampler.SampleNow(); table != nil {
		for _, p := range r.tracker.aliveHelpers(table) {
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
		}
	}
	for _, c := range r.order {
		if c.label == "spawn" {
			<-c.done
		}
	}

	// Ports must be free again.
	names := make([]string, 0, len(r.ports))
	for n := range r.ports {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		addr := fmt.Sprintf("127.0.0.1:%d", r.ports[n])
		if waitPort(addr, false, 2*time.Second) != nil {
			problems = append(problems, fmt.Sprintf("port %s (%s) is still open", n, addr))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "\n"))
}

// restoreFiles puts back project files the case changed.
func (r *caseRun) restoreFiles() {
	for path, orig := range r.restores {
		if orig == nil {
			_ = os.Remove(path)
		} else {
			_ = os.WriteFile(path, *orig, 0o644)
		}
	}
}

func waitPort(addr string, open bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		isOpen := err == nil
		if conn != nil {
			_ = conn.Close()
		}
		if isOpen == open {
			return nil
		}
		if time.Now().After(deadline) {
			if open {
				return fmt.Errorf("%s did not open within %s", addr, timeout)
			}
			return fmt.Errorf("%s still open after %s", addr, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
