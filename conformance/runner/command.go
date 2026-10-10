package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// output captures a command's stdout and stderr separately and together.
type output struct {
	mu       sync.Mutex
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	combined bytes.Buffer
}

type streamWriter struct {
	o      *output
	stderr bool
}

func (w streamWriter) Write(p []byte) (int, error) {
	w.o.mu.Lock()
	defer w.o.mu.Unlock()
	if w.stderr {
		w.o.stderr.Write(p)
	} else {
		w.o.stdout.Write(p)
	}
	w.o.combined.Write(p)
	return len(p), nil
}

func (o *output) get(stream string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch stream {
	case "stdout":
		return o.stdout.String()
	case "stderr":
		return o.stderr.String()
	default:
		return o.combined.String()
	}
}

// command is a process the case started: stackstart (run/start), or a
// shell script (sh/spawn).
type command struct {
	id      string
	label   string
	cmd     *exec.Cmd
	out     *output
	started time.Time
	done    chan struct{}
	exited  time.Time
	code    int
	waitErr error
}

func startCommand(id, label string, cmd *exec.Cmd) (*command, error) {
	c := &command{id: id, label: label, cmd: cmd, out: &output{}, done: make(chan struct{})}
	cmd.Stdout = streamWriter{o: c.out}
	cmd.Stderr = streamWriter{o: c.out, stderr: true}
	// Own process group: a Ctrl-C on the runner's terminal must not reach
	// the commands directly; the runner stops them itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.started = time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		c.exited = time.Now()
		c.code = exitCode(cmd, err)
		c.waitErr = err
		close(c.done)
	}()
	return c, nil
}

// exitCode is the process's exit code, or 128+signal if a signal killed it,
// as a shell would report it.
func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return -1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}

func (c *command) finished() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *command) describe() string {
	var b strings.Builder
	state := "still running"
	if c.finished() {
		state = fmt.Sprintf("exit %d after %s", c.code, c.exited.Sub(c.started).Round(time.Millisecond))
	}
	fmt.Fprintf(&b, "--- %s: %s (%s)\n", c.id, c.label, state)
	stdout, stderr := c.out.get("stdout"), c.out.get("stderr")
	fmt.Fprintf(&b, "stdout:\n%s", indent(visible(stdout)))
	fmt.Fprintf(&b, "stderr:\n%s", indent(visible(stderr)))
	return b.String()
}

// visible makes control characters readable in failure reports.
func visible(s string) string {
	const max = 20000
	if len(s) > max {
		s = s[:max/2] + fmt.Sprintf("\n… %d bytes omitted …\n", len(s)-max) + s[len(s)-max/2:]
	}
	r := strings.NewReplacer("\x1b", `\e`, "\r", `\r`, "\x07", `\a`)
	return r.Replace(s)
}

func indent(s string) string {
	if s == "" {
		return "    (empty)\n"
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return "    " + strings.Join(lines, "\n    ") + "\n"
}

// splitArgs splits a command line the way a shell would, with quotes and
// backslashes but no other shell features.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped = true
			inArg = true
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in %q", s)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
