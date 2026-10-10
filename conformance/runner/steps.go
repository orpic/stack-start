package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

var stepKinds = []string{
	"run", "start", "wait_output", "expect_output", "wait_exit", "alive", "signal",
	"sh", "spawn", "write_file", "edit_file", "http", "port", "process", "sleep",
}

// stepFields lists the fields each step accepts, so typos fail loudly.
var stepFields = map[string][]string{
	"run":           {"id", "cwd", "env", "timeout", "expect"},
	"start":         {"id", "cwd", "env"},
	"wait_output":   {},
	"expect_output": {},
	"wait_exit":     {},
	"alive":         {},
	"signal":        {},
	"sh":            {"cwd", "env", "timeout", "expect"},
	"spawn":         {"id", "cwd", "env"},
	"write_file":    {},
	"edit_file":     {},
	"http":          {},
	"port":          {},
	"process":       {},
	"sleep":         {},
}

// mapFields lists the fields of steps whose value is a mapping.
var mapFields = map[string][]string{
	"wait_output":   {"id", "stream", "contains", "matches", "count", "timeout", "save"},
	"expect_output": append([]string{"id", "stream"}, keys(assertionNames)...),
	"wait_exit":     {"id", "exit", "timeout", "within"},
	"alive":         {"id", "for"},
	"signal":        {"id", "sig"},
	"write_file":    {"path", "content"},
	"edit_file":     {"path", "replace"},
	"http":          {"url", "status", "body_contains", "timeout"},
	"port":          {"addr", "open", "timeout"},
	"process":       {"matches", "count", "absent", "timeout"},
	"sleep":         {"duration", "reason"},
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stepKind(step map[string]any) (string, error) {
	var found []string
	for _, k := range stepKinds {
		if _, ok := step[k]; ok {
			found = append(found, k)
		}
	}
	if len(found) != 1 {
		var ks []string
		for k := range step {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return "", fmt.Errorf("a step needs exactly one of %s; got keys %v", strings.Join(stepKinds, ", "), ks)
	}
	return found[0], nil
}

func describeStep(step map[string]any) string {
	kind, err := stepKind(step)
	if err != nil {
		return "invalid step"
	}
	v := step[kind]
	if s, ok := v.(string); ok {
		return kind + ": " + truncate(strings.ReplaceAll(s, "\n", "; "), 100)
	}
	return kind + ": " + truncate(fmt.Sprint(v), 100)
}

// params is a step's mapping with typed getters.
type params map[string]any

func (p params) str(k string) string {
	if v, ok := p[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func (p params) has(k string) bool { _, ok := p[k]; return ok }

func (p params) dur(k string, def time.Duration, required bool) (time.Duration, error) {
	s := p.str(k)
	if s == "" {
		if required {
			return 0, fmt.Errorf("'%s' is required", k)
		}
		return def, nil
	}
	return time.ParseDuration(s)
}

func (p params) strMap(k string) map[string]string {
	out := map[string]string{}
	if m, ok := p[k].(map[string]any); ok {
		for kk, v := range m {
			out[kk] = fmt.Sprint(v)
		}
	}
	return out
}

func checkFields(kind string, p params, allowed []string) error {
	ok := map[string]bool{}
	for _, f := range allowed {
		ok[f] = true
	}
	for k := range p {
		if !ok[k] {
			return fmt.Errorf("%s: unknown field %q (allowed: %s)", kind, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func (r *caseRun) runStep(ctx context.Context, step map[string]any) error {
	kind, err := stepKind(step)
	if err != nil {
		return err
	}
	step = substituteSaved(step, r.saved).(map[string]any)
	var p params
	if fields, isMap := mapFields[kind]; isMap {
		m, ok := step[kind].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected a mapping", kind)
		}
		if len(step) != 1 {
			return fmt.Errorf("%s: put every field inside the mapping", kind)
		}
		p = params(m)
		if err := checkFields(kind, p, fields); err != nil {
			return err
		}
	} else {
		p = params(step)
		if err := checkFields(kind, p, append([]string{kind}, stepFields[kind]...)); err != nil {
			return err
		}
	}

	switch kind {
	case "run":
		return r.stepRun(ctx, p)
	case "start":
		return r.stepStart(p)
	case "sh":
		return r.stepSh(ctx, p)
	case "spawn":
		return r.stepSpawn(p)
	case "wait_output":
		return r.stepWaitOutput(ctx, p)
	case "expect_output":
		return r.stepExpectOutput(p)
	case "wait_exit":
		return r.stepWaitExit(ctx, p)
	case "alive":
		return r.stepAlive(ctx, p)
	case "signal":
		return r.stepSignal(p)
	case "write_file":
		return r.stepWriteFile(p)
	case "edit_file":
		return r.stepEditFile(p)
	case "http":
		return r.stepHTTP(ctx, p)
	case "port":
		return r.stepPort(ctx, p)
	case "process":
		return r.stepProcess(ctx, p)
	case "sleep":
		return r.stepSleep(ctx, p)
	}
	return fmt.Errorf("unknown step %q", kind)
}

func (r *caseRun) dir(p params) string {
	if cwd := p.str("cwd"); cwd != "" {
		if filepath.IsAbs(cwd) {
			return cwd
		}
		return filepath.Join(r.work, cwd)
	}
	return r.work
}

func (r *caseRun) cmdEnv(p params) []string {
	env := append([]string(nil), r.env...)
	for k, v := range p.strMap("env") {
		env = setEnv(env, k, v)
	}
	return env
}

func (r *caseRun) newID(p params, def string) (string, error) {
	id := p.str("id")
	if id == "" {
		id = def
	}
	if _, exists := r.cmds[id]; exists {
		if p.str("id") == "" {
			id = fmt.Sprintf("%s%d", def, r.stepNum)
		} else {
			return "", fmt.Errorf("id %q is already used in this case", id)
		}
	}
	return id, nil
}

func (r *caseRun) launch(p params, kind, defID string, cmd *exec.Cmd, helper bool) (*command, error) {
	id, err := r.newID(p, defID)
	if err != nil {
		return nil, err
	}
	cmd.Dir = r.dir(p)
	cmd.Env = r.cmdEnv(p)
	c, err := startCommand(id, kind, cmd)
	if err != nil {
		return nil, err
	}
	r.tracker.AddRoot(c.cmd.Process.Pid, helper)
	r.cmds[id] = c
	r.order = append(r.order, c)
	return c, nil
}

func (r *caseRun) stackstartCmd(args string) (*exec.Cmd, string, error) {
	argv, err := splitArgs(args)
	if err != nil {
		return nil, "", err
	}
	first := "stackstart"
	if len(argv) > 0 {
		first = argv[0]
	}
	return exec.Command(r.opts.Bin, argv...), first, nil
}

// waitDone waits for a command to finish, killing it at the timeout.
func waitDone(ctx context.Context, c *command, timeout time.Duration) error {
	select {
	case <-c.done:
		return nil
	case <-time.After(timeout):
	case <-ctx.Done():
	}
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	_ = c.cmd.Process.Kill()
	<-c.done
	return fmt.Errorf("%s did not exit within %s (killed)", c.id, timeout)
}

func (r *caseRun) stepRun(ctx context.Context, p params) error {
	cmd, first, err := r.stackstartCmd(p.str("run"))
	if err != nil {
		return err
	}
	timeout, err := p.dur("timeout", 30*time.Second, false)
	if err != nil {
		return err
	}
	c, err := r.launch(p, "stackstart "+p.str("run"), first, cmd, false)
	if err != nil {
		return err
	}
	if err := waitDone(ctx, c, timeout); err != nil {
		return err
	}
	expect, _ := p["expect"].(map[string]any)
	return checkExpect(expect, c)
}

func (r *caseRun) stepStart(p params) error {
	cmd, first, err := r.stackstartCmd(p.str("start"))
	if err != nil {
		return err
	}
	_, err = r.launch(p, "stackstart "+p.str("start"), first, cmd, false)
	return err
}

func (r *caseRun) stepSh(ctx context.Context, p params) error {
	timeout, err := p.dur("timeout", 30*time.Second, false)
	if err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", "-c", p.str("sh"))
	c, err := r.launch(p, "sh", "sh", cmd, false)
	if err != nil {
		return err
	}
	c.label = "sh: " + truncate(p.str("sh"), 80)
	if err := waitDone(ctx, c, timeout); err != nil {
		return err
	}
	expect, _ := p["expect"].(map[string]any)
	return checkExpect(expect, c)
}

func (r *caseRun) stepSpawn(p params) error {
	if p.str("id") == "" {
		return fmt.Errorf("spawn: 'id' is required")
	}
	cmd := exec.Command("/bin/sh", "-c", p.str("spawn"))
	_, err := r.launch(p, "spawn", p.str("id"), cmd, true)
	return err
}

func (r *caseRun) get(p params) (*command, error) {
	id := p.str("id")
	if id == "" {
		return nil, fmt.Errorf("'id' is required")
	}
	c, ok := r.cmds[id]
	if !ok {
		return nil, fmt.Errorf("no command with id %q", id)
	}
	return c, nil
}

func stream(p params) (string, error) {
	s := p.str("stream")
	switch s {
	case "", "any":
		return "any", nil
	case "stdout", "stderr":
		return s, nil
	}
	return "", fmt.Errorf("stream must be stdout, stderr or any, got %q", s)
}

func (r *caseRun) stepWaitOutput(ctx context.Context, p params) error {
	c, err := r.get(p)
	if err != nil {
		return err
	}
	st, err := stream(p)
	if err != nil {
		return err
	}
	timeout, err := p.dur("timeout", 0, true)
	if err != nil {
		return err
	}
	want := 1
	if p.has("count") {
		if want, err = toInt(p["count"]); err != nil {
			return err
		}
	}
	var match func(string) bool
	var what string
	var re *regexp.Regexp
	switch {
	case p.has("contains"):
		s := p.str("contains")
		what = fmt.Sprintf("%q", s)
		match = func(t string) bool { return strings.Count(t, s) >= want }
	case p.has("matches"):
		if re, err = regexp.Compile(p.str("matches")); err != nil {
			return err
		}
		what = "/" + p.str("matches") + "/"
		match = func(t string) bool { return len(re.FindAllStringIndex(t, -1)) >= want }
	default:
		return fmt.Errorf("wait_output needs 'contains' or 'matches'")
	}
	if want > 1 {
		what = fmt.Sprintf("%s %d times", what, want)
	}
	save := p.str("save")
	if save != "" && (re == nil || re.NumSubexp() < 1) {
		return fmt.Errorf("save needs 'matches' with a capturing group")
	}
	done := func() bool {
		text := c.out.get(st)
		if !match(text) {
			return false
		}
		if save != "" {
			r.saved[save] = re.FindStringSubmatch(text)[1]
		}
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return nil
		}
		if c.finished() {
			// One last look: output may have arrived just before exit.
			if done() {
				return nil
			}
			return fmt.Errorf("%s exited (code %d) without printing %s on %s", c.id, c.code, what, st)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not print %s on %s within %s", c.id, what, st, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, errCaseTimeout)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (r *caseRun) stepExpectOutput(p params) error {
	c, err := r.get(p)
	if err != nil {
		return err
	}
	st, err := stream(p)
	if err != nil {
		return err
	}
	text := c.out.get(st)
	for _, name := range keys(assertionNames) {
		if v, ok := p[name]; ok {
			if err := checkAssertion(name, v, text); err != nil {
				return fmt.Errorf("%s output of %s: %v", st, c.id, err)
			}
		}
	}
	return nil
}

func (r *caseRun) stepWaitExit(ctx context.Context, p params) error {
	c, err := r.get(p)
	if err != nil {
		return err
	}
	timeout, err := p.dur("timeout", 0, true)
	if err != nil {
		return err
	}
	select {
	case <-c.done:
	case <-time.After(timeout):
		return fmt.Errorf("%s did not exit within %s", c.id, timeout)
	case <-ctx.Done():
		return fmt.Errorf("waiting for %s to exit: %w", c.id, errCaseTimeout)
	}
	if p.has("exit") {
		want, err := toInt(p["exit"])
		if err != nil {
			return err
		}
		if c.code != want {
			return fmt.Errorf("%s exited %d, expected %d", c.id, c.code, want)
		}
	}
	if p.has("within") {
		within, err := p.dur("within", 0, true)
		if err != nil {
			return err
		}
		if took := c.exited.Sub(c.started); took > within {
			return fmt.Errorf("%s exited after %s, expected within %s", c.id, took.Round(time.Millisecond), within)
		}
	}
	return nil
}

func (r *caseRun) stepAlive(ctx context.Context, p params) error {
	c, err := r.get(p)
	if err != nil {
		return err
	}
	d, err := p.dur("for", 0, true)
	if err != nil {
		return err
	}
	select {
	case <-c.done:
		return fmt.Errorf("%s exited (code %d) but should still be running", c.id, c.code)
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return errCaseTimeout
	}
}

func (r *caseRun) stepSignal(p params) error {
	c, err := r.get(p)
	if err != nil {
		return err
	}
	sigs := map[string]syscall.Signal{
		"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "HUP": syscall.SIGHUP,
		"KILL": syscall.SIGKILL, "QUIT": syscall.SIGQUIT,
	}
	sig, ok := sigs[strings.TrimPrefix(strings.ToUpper(p.str("sig")), "SIG")]
	if !ok {
		return fmt.Errorf("unknown signal %q", p.str("sig"))
	}
	if c.finished() {
		return fmt.Errorf("%s has already exited (code %d)", c.id, c.code)
	}
	return c.cmd.Process.Signal(sig)
}

func (r *caseRun) path(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("'path' is required")
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	return filepath.Join(r.work, rel), nil
}

// remember saves a project file's original content before the case changes it.
func (r *caseRun) remember(path string) {
	if r.c.Project == "" {
		return
	}
	if _, done := r.restores[path]; done {
		return
	}
	if data, err := os.ReadFile(path); err == nil {
		r.restores[path] = &data
	} else {
		r.restores[path] = nil
	}
}

func (r *caseRun) stepWriteFile(p params) error {
	path, err := r.path(p.str("path"))
	if err != nil {
		return err
	}
	r.remember(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(p.str("content")), 0o644)
}

func (r *caseRun) stepEditFile(p params) error {
	path, err := r.path(p.str("path"))
	if err != nil {
		return err
	}
	rep, ok := p["replace"].(map[string]any)
	if !ok {
		return fmt.Errorf("edit_file needs replace: {from, to}")
	}
	from, to := fmt.Sprint(rep["from"]), fmt.Sprint(rep["to"])
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), from) {
		return fmt.Errorf("%s does not contain %q", path, from)
	}
	r.remember(path)
	return os.WriteFile(path, []byte(strings.Replace(string(data), from, to, 1)), 0o644)
}

func (r *caseRun) stepHTTP(ctx context.Context, p params) error {
	timeout, err := p.dur("timeout", 0, true)
	if err != nil {
		return err
	}
	want := 200
	if p.has("status") {
		if want, err = toInt(p["status"]); err != nil {
			return err
		}
	}
	url, body := p.str("url"), p.str("body_contains")
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			if resp.StatusCode == want && (body == "" || strings.Contains(string(data), body)) {
				return nil
			}
			last = fmt.Sprintf("status %d, body %q", resp.StatusCode, truncate(string(data), 300))
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("GET %s: wanted status %d%s within %s; last: %s", url, want,
				map[bool]string{true: fmt.Sprintf(" with body containing %q", body), false: ""}[body != ""], timeout, last)
		}
		select {
		case <-ctx.Done():
			return errCaseTimeout
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *caseRun) stepPort(ctx context.Context, p params) error {
	timeout, err := p.dur("timeout", 0, true)
	if err != nil {
		return err
	}
	open := true
	if p.has("open") {
		b, ok := p["open"].(bool)
		if !ok {
			return fmt.Errorf("open must be true or false")
		}
		open = b
	}
	return waitPort(p.str("addr"), open, timeout)
}

func (r *caseRun) stepProcess(ctx context.Context, p params) error {
	timeout, err := p.dur("timeout", 0, true)
	if err != nil {
		return err
	}
	re, err := regexp.Compile(p.str("matches"))
	if err != nil {
		return err
	}
	want := 0
	absent, _ := p["absent"].(bool)
	if !absent {
		if !p.has("count") {
			return fmt.Errorf("process needs 'count' or 'absent: true'")
		}
		if want, err = toInt(p["count"]); err != nil {
			return err
		}
	}
	self := os.Getpid()
	deadline := time.Now().Add(timeout)
	for {
		table, err := readProcTable()
		if err != nil {
			return err
		}
		var found []string
		for _, q := range table {
			if q.PID == self {
				continue
			}
			if re.MatchString(q.Command) {
				found = append(found, fmt.Sprintf("pid %d: %s", q.PID, truncate(q.Command, 160)))
			}
		}
		if len(found) == want {
			return nil
		}
		if time.Now().After(deadline) {
			sort.Strings(found)
			return fmt.Errorf("expected %d processes matching /%s/, found %d:\n  %s",
				want, re, len(found), strings.Join(found, "\n  "))
		}
		select {
		case <-ctx.Done():
			return errCaseTimeout
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (r *caseRun) stepSleep(ctx context.Context, p params) error {
	if p.str("reason") == "" {
		return fmt.Errorf("sleep needs a 'reason'")
	}
	d, err := p.dur("duration", 0, true)
	if err != nil {
		return err
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return errCaseTimeout
	}
}

var savedPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substituteSaved replaces ${NAME} with values saved by earlier steps.
func substituteSaved(v any, saved map[string]string) any {
	switch x := v.(type) {
	case string:
		return savedPattern.ReplaceAllStringFunc(x, func(m string) string {
			if val, ok := saved[m[2:len(m)-1]]; ok {
				return val
			}
			return m
		})
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = substituteSaved(e, saved)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = substituteSaved(e, saved)
		}
		return out
	}
	return v
}
