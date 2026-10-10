package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMain gives every test run its own state directory, so tests never
// write sessions or logs into the developer's real stackstart state.
func TestMain(m *testing.M) {
	state, err := os.MkdirTemp("", "stackstart-e2e-state-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_STATE_HOME", state)
	code := m.Run()
	_ = os.RemoveAll(state)
	os.Exit(code)
}

var (
	fakeprocPath string
	fakeprocOnce sync.Once
	fakeprocErr  error
)

// fakeproc builds the fixture program that reproduces real process behaviors.
func fakeproc(t *testing.T) string {
	t.Helper()
	fakeprocOnce.Do(func() {
		fakeprocPath = filepath.Join(os.TempDir(), "stackstart-e2e-fakeproc")
		out, err := exec.Command("go", "build", "-o", fakeprocPath, "./fakeproc").CombinedOutput()
		if err != nil {
			fakeprocErr = fmt.Errorf("building fakeproc: %s\n%s", err, out)
		}
	})
	require.NoError(t, fakeprocErr)
	return fakeprocPath
}

// syncBuffer is a bytes.Buffer safe to write from a process and read from a test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// running is a `stackstart up` started in the background.
type running struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	done   chan struct{}
	err    error
	exited time.Time
}

func startUp(t *testing.T, dir, profile string, extraArgs ...string) *running {
	t.Helper()
	cmd := runStackstart(t, dir, append([]string{"up", profile}, extraArgs...)...)
	r := &running{cmd: cmd, out: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout = r.out
	cmd.Stderr = r.out
	require.NoError(t, cmd.Start())
	go func() {
		r.err = cmd.Wait()
		r.exited = time.Now()
		close(r.done)
	}()
	t.Cleanup(func() {
		if !r.finished() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-r.done:
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
		if t.Failed() {
			t.Logf("stackstart output:\n%s", r.out.String())
		}
	})
	return r
}

func (r *running) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *running) waitFor(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool { return strings.Contains(r.out.String(), substr) },
		timeout, 50*time.Millisecond, "waiting for %q in output:\n%s", substr, r.out.String())
}

// waitExit waits for stackstart to exit and returns its exit code.
func (r *running) waitExit(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(timeout):
		t.Fatalf("stackstart did not exit within %s; output:\n%s", timeout, r.out.String())
	}
	if r.err == nil {
		return 0
	}
	if ee, ok := r.err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("unexpected wait error: %v", r.err)
	return -1
}

type runInfo struct {
	Slug          string `json:"slug"`
	Profile       string `json:"profile"`
	RunID         string `json:"run_id"`
	Outcome       string `json:"outcome"`
	FailedProcess string `json:"failed_process"`
	Cause         string `json:"cause"`
	LogDir        string `json:"log_dir"`
	Processes     []struct {
		Name    string `json:"name"`
		PID     int    `json:"pid"`
		LogFile string `json:"log_file"`
		RawLog  string `json:"raw_log_file"`
	} `json:"processes"`
}

func sessionsDir() string {
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "stackstart", "sessions")
}

// latestRun reads run.json of the latest run of a profile. Profile names are
// unique per test, so the glob finds exactly one session.
func latestRun(t *testing.T, profile string) runInfo {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(sessionsDir(), "*__"+profile, "latest", "run.json"))
	require.Len(t, matches, 1, "expected exactly one session for profile %q", profile)
	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	var ri runInfo
	require.NoError(t, json.Unmarshal(data, &ri))
	return ri
}

func liveRecord(t *testing.T, profile string) runInfo {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(sessionsDir(), "*__"+profile+".json"))
	require.Len(t, matches, 1, "expected a live session record for profile %q", profile)
	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	var ri runInfo
	require.NoError(t, json.Unmarshal(data, &ri))
	return ri
}

func processLog(t *testing.T, ri runInfo, name string) string {
	t.Helper()
	for _, p := range ri.Processes {
		if p.Name == name {
			data, err := os.ReadFile(p.LogFile)
			require.NoError(t, err)
			return string(data)
		}
	}
	t.Fatalf("process %q not in run %s", name, ri.RunID)
	return ""
}

func rawLog(t *testing.T, ri runInfo, name string) string {
	t.Helper()
	for _, p := range ri.Processes {
		if p.Name == name {
			data, err := os.ReadFile(p.RawLog)
			require.NoError(t, err)
			return string(data)
		}
	}
	t.Fatalf("process %q not in run %s", name, ri.RunID)
	return ""
}

func pidAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// freePort returns a TCP port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
