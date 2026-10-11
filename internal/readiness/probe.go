package readiness

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Probe tests something once: nil means it passed.
type Probe func(ctx context.Context) error

// ProbeCheck polls a probe until it passes. Its description includes the
// last failure, so a readiness timeout says why it never passed.
type ProbeCheck struct {
	Desc     string
	Probe    Probe
	Interval time.Duration
	matched  atomic.Bool
	mu       sync.Mutex
	lastErr  error
}

func (c *ProbeCheck) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	for {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := c.Probe(pctx)
		cancel()
		if err == nil {
			c.matched.Store(true)
			return
		}
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (c *ProbeCheck) Satisfied() bool { return c.matched.Load() }

func (c *ProbeCheck) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastErr != nil && !c.matched.Load() {
		return fmt.Sprintf("%s (last: %v)", c.Desc, c.lastErr)
	}
	return c.Desc
}

// TCPProbe passes when a TCP connection to addr succeeds.
func TCPProbe(addr string) Probe {
	return func(ctx context.Context) error {
		d := net.Dialer{Timeout: time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return shortErr(err)
		}
		return conn.Close()
	}
}

// HTTPProbe passes when GET url answers with status (any 2xx or 3xx when
// status is 0).
func HTTPProbe(url string, status int) Probe {
	client := &http.Client{
		Timeout: 5 * time.Second,
		// A redirect answer is an answer; don't follow it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return shortErr(err)
		}
		_ = resp.Body.Close()
		ok := resp.StatusCode >= 200 && resp.StatusCode < 400
		if status != 0 {
			ok = resp.StatusCode == status
		}
		if !ok {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}
}

// ExecProbe passes when the shell command exits 0.
func ExecProbe(command, dir string, env []string) Probe {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
				msg = msg[i+1:]
			}
			if len(msg) > 200 {
				msg = msg[:200]
			}
			if msg != "" {
				return fmt.Errorf("%v: %s", err, msg)
			}
			return err
		}
		return nil
	}
}

// FileProbe passes when path exists.
func FileProbe(path string) Probe {
	return func(context.Context) error {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s does not exist", path)
		}
		return nil
	}
}

// shortErr drops the noisy prefix of network errors ("dial tcp ...: ").
func shortErr(err error) error {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return fmt.Errorf("%s", msg)
}
