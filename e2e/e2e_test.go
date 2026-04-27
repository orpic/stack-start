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

var (
	binaryPath string
	buildOnce  sync.Once
	buildErr   error
)

func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		tmp := os.TempDir()
		binaryPath = filepath.Join(tmp, "stackstart-e2e-test")
		cmd := exec.Command("go", "build", "-o", binaryPath, "../cmd/stackstart")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build failed: %s\n%s", err, out)
		}
	})
	require.NoError(t, buildErr, "binary must build")
	return binaryPath
}

func writeConfig(t *testing.T, dir, yaml string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stackstart.yaml"), []byte(yaml), 0644))
}

func runStackstart(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd
}

func runAndWait(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := runStackstart(t, dir, args...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	return string(out), exitCode
}

// --- CLI commands ---

func TestE2E_Version(t *testing.T) {
	out, code := runAndWait(t, t.TempDir(), "version")
	require.Equal(t, 0, code)
	require.Contains(t, out, "stackstart")
}

func TestE2E_Help(t *testing.T) {
	out, code := runAndWait(t, t.TempDir(), "--help")
	require.Equal(t, 0, code)
	require.Contains(t, out, "up")
	require.Contains(t, out, "down")
	require.Contains(t, out, "status")
	require.Contains(t, out, "logs")
	require.Contains(t, out, "validate")
	require.Contains(t, out, "list")
	require.Contains(t, out, "init")
}

func TestE2E_Init(t *testing.T) {
	dir := t.TempDir()
	out, code := runAndWait(t, dir, "init")
	require.Equal(t, 0, code)
	require.Contains(t, out, "Created")

	_, err := os.Stat(filepath.Join(dir, "stackstart.yaml"))
	require.NoError(t, err)
}

func TestE2E_InitAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "profiles: {}")
	_, code := runAndWait(t, dir, "init")
	require.NotEqual(t, 0, code)
}

// --- Validate ---

func TestE2E_Validate_Valid(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  dev:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)
	out, code := runAndWait(t, dir, "validate", "dev")
	require.Equal(t, 0, code)
	require.Contains(t, out, "valid")
}

func TestE2E_Validate_CycleDetected(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  bad:
    processes:
      a:
        cwd: .
        cmd: echo a
        depends_on: [b]
      b:
        cwd: .
        cmd: echo b
        depends_on: [a]
`)
	out, code := runAndWait(t, dir, "validate", "bad")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "cycle")
}

func TestE2E_Validate_MissingProfile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  dev:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)
	out, code := runAndWait(t, dir, "validate", "nonexistent")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "no profile named")
}

func TestE2E_Validate_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  bad:
    processes:
      app:
        cwd: /absolute/path
        cmd: echo hi
`)
	out, code := runAndWait(t, dir, "validate", "bad")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "absolute")
}

func TestE2E_Validate_BadCaptureRef(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  bad:
    processes:
      app:
        cwd: .
        cmd: echo hi
        env:
          URL: "${ghost.url}"
`)
	out, code := runAndWait(t, dir, "validate", "bad")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "unknown process")
}

// --- List ---

func TestE2E_List(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  dev:
    processes:
      app:
        cwd: .
        cmd: echo hi
  staging:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)
	out, code := runAndWait(t, dir, "list")
	require.Equal(t, 0, code)
	require.Contains(t, out, "dev")
	require.Contains(t, out, "staging")
}

func TestE2E_List_Empty(t *testing.T) {
	out, code := runAndWait(t, t.TempDir(), "list")
	require.Equal(t, 0, code)
	require.Contains(t, out, "No profiles")
}

// --- Up: basic readiness ---

func TestE2E_Up_LogReadiness(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      app:
        cwd: .
        cmd: "sh -c 'echo READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "READY"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "READY")
	}, 10*time.Second, 100*time.Millisecond, "should see READY in output")
}

func TestE2E_Up_TCPReadiness(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  test:
    processes:
      server:
        cwd: .
        cmd: "sh -c 'sleep 1 && nc -l %d'"
        readiness:
          timeout: 10s
          checks:
            - tcp: "localhost:%d"
`, port, port))

	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "server")
	}, 12*time.Second, 200*time.Millisecond)
}

// --- Up: dependency ordering ---

func TestE2E_Up_DependencyOrdering(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  deps:
    processes:
      db:
        cwd: .
        cmd: "sh -c 'echo DB_READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "DB_READY"
      app:
        cwd: .
        cmd: "sh -c 'echo APP_STARTED && sleep 30'"
        depends_on: [db]
        readiness:
          timeout: 5s
          checks:
            - log: "APP_STARTED"
`)
	cmd := runStackstart(t, dir, "up", "deps")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		out := outBuf.String()
		return strings.Contains(out, "DB_READY") && strings.Contains(out, "APP_STARTED")
	}, 10*time.Second, 100*time.Millisecond)

	out := outBuf.String()
	dbIdx := strings.Index(out, "DB_READY")
	appIdx := strings.Index(out, "APP_STARTED")
	require.Less(t, dbIdx, appIdx, "db must be ready before app starts")
}

// --- Up: capture propagation ---

func TestE2E_Up_CapturePropagation(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  capture:
    processes:
      producer:
        cwd: .
        cmd: "sh -c 'echo URL=https://test-tunnel.example.com && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "URL="
        captures:
          - name: url
            log: "URL=(https://[a-z-]+\\.example\\.com)"
            required: true
      consumer:
        cwd: .
        cmd: "sh -c 'echo GOT=$TUNNEL_URL && sleep 30'"
        depends_on: [producer]
        env:
          TUNNEL_URL: "${producer.url}"
        readiness:
          timeout: 5s
          checks:
            - log: "GOT="
`)
	cmd := runStackstart(t, dir, "up", "capture")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "GOT=https://test-tunnel.example.com")
	}, 10*time.Second, 100*time.Millisecond, "consumer should receive captured URL")
}

// --- Up: readiness timeout ---

func TestE2E_Up_ReadinessTimeout(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  slow:
    processes:
      app:
        cwd: .
        cmd: "sh -c 'echo NOT_THE_SIGNAL && sleep 30'"
        readiness:
          timeout: 2s
          checks:
            - log: "WILL_NEVER_APPEAR"
`)
	out, code := runAndWait(t, dir, "up", "slow")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "FAILED")
	require.Contains(t, out, "app")
}

// --- Up: graceful shutdown on SIGINT ---

func TestE2E_Up_GracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      app:
        cwd: .
        cmd: "sh -c 'echo READY && sleep 60'"
        readiness:
          timeout: 5s
          checks:
            - log: "READY"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "READY")
	}, 10*time.Second, 100*time.Millisecond)

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("stackstart did not exit within 15s after SIGTERM")
	}
}

// --- Up: on_exit ignore ---

func TestE2E_Up_OnExitIgnore(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      ephemeral:
        cwd: .
        cmd: "sh -c 'echo EPHEMERAL_READY && sleep 1'"
        on_exit: ignore
        readiness:
          timeout: 5s
          checks:
            - log: "EPHEMERAL_READY"
      keeper:
        cwd: .
        cmd: "sh -c 'echo KEEPER_READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "KEEPER_READY"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		out := outBuf.String()
		return strings.Contains(out, "KEEPER_READY") && strings.Contains(out, "EPHEMERAL_READY")
	}, 10*time.Second, 100*time.Millisecond)

	time.Sleep(3 * time.Second)

	// stackstart should still be running (ephemeral exited but was ignored)
	require.NoError(t, cmd.Process.Signal(syscall.Signal(0)), "stackstart should still be alive")
}

// --- Session: record creation ---

func TestE2E_Up_SessionRecord(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      app:
        cwd: .
        cmd: "sh -c 'echo READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "READY"
`)
	cmd := runStackstart(t, dir, "up", "test")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateDir)
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "READY")
	}, 10*time.Second, 100*time.Millisecond)

	sessDir := filepath.Join(stateDir, "stackstart", "sessions")
	entries, err := os.ReadDir(sessDir)
	require.NoError(t, err)

	var jsonFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	require.NotEmpty(t, jsonFiles, "session record should exist")

	data, err := os.ReadFile(filepath.Join(sessDir, jsonFiles[0]))
	require.NoError(t, err)
	var rec map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &rec))
	require.Equal(t, "test", rec["profile"])
}

// --- Status: no sessions ---

func TestE2E_Status_NoSessions(t *testing.T) {
	dir := t.TempDir()
	cmd := runStackstart(t, dir, "status")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(out), "No running")
}

// --- Up: duplicate session prevention ---

func TestE2E_Up_DuplicateSessionBlocked(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      app:
        cwd: .
        cmd: "sh -c 'echo READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "READY"
`)
	stateDir := t.TempDir()
	env := append(os.Environ(), "XDG_STATE_HOME="+stateDir)

	cmd1 := runStackstart(t, dir, "up", "test")
	cmd1.Env = env
	var out1 bytes.Buffer
	cmd1.Stdout = &out1
	cmd1.Stderr = &out1
	require.NoError(t, cmd1.Start())

	defer func() {
		_ = cmd1.Process.Signal(syscall.SIGTERM)
		_ = cmd1.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(out1.String(), "READY")
	}, 10*time.Second, 100*time.Millisecond)

	cmd2 := runStackstart(t, dir, "up", "test")
	cmd2.Env = env
	out2, err := cmd2.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out2), "already running")
}

// --- Up: multiple checks with mode any ---

func TestE2E_Up_ReadinessModeAny(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      app:
        cwd: .
        cmd: "sh -c 'echo SIGNAL_B && sleep 30'"
        readiness:
          timeout: 5s
          mode: any
          checks:
            - log: "SIGNAL_A"
            - log: "SIGNAL_B"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "SIGNAL_B")
	}, 10*time.Second, 100*time.Millisecond)
}

// --- Up: diamond dependency graph ---

func TestE2E_Up_DiamondDeps(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  diamond:
    processes:
      db:
        cwd: .
        cmd: "sh -c 'echo DB_READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "DB_READY"
      cache:
        cwd: .
        cmd: "sh -c 'echo CACHE_READY && sleep 30'"
        readiness:
          timeout: 5s
          checks:
            - log: "CACHE_READY"
      api:
        cwd: .
        cmd: "sh -c 'echo API_READY && sleep 30'"
        depends_on: [db, cache]
        readiness:
          timeout: 5s
          checks:
            - log: "API_READY"
      web:
        cwd: .
        cmd: "sh -c 'echo WEB_READY && sleep 30'"
        depends_on: [api]
        readiness:
          timeout: 5s
          checks:
            - log: "WEB_READY"
`)
	cmd := runStackstart(t, dir, "up", "diamond")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		out := outBuf.String()
		return strings.Contains(out, "DB_READY") &&
			strings.Contains(out, "CACHE_READY") &&
			strings.Contains(out, "API_READY") &&
			strings.Contains(out, "WEB_READY")
	}, 15*time.Second, 100*time.Millisecond, "all 4 processes should become ready")

	out := outBuf.String()
	apiIdx := strings.Index(out, "API_READY")
	webIdx := strings.Index(out, "WEB_READY")
	require.Less(t, apiIdx, webIdx, "api must be ready before web")
}

// --- Validate: missing readiness timeout ---

func TestE2E_Validate_MissingTimeout(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  bad:
    processes:
      app:
        cwd: .
        cmd: echo hi
        readiness:
          checks:
            - log: "ready"
`)
	out, code := runAndWait(t, dir, "validate", "bad")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "timeout")
}

// --- Up: process with no readiness (immediate ready) ---

func TestE2E_Up_NoReadiness(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      simple:
        cwd: .
        cmd: "sh -c 'echo RUNNING && sleep 30'"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "RUNNING")
	}, 10*time.Second, 100*time.Millisecond)
}

// --- Oneshot processes ---

func TestE2E_Oneshot_ExitZero_UnblocksDependents(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      setup:
        cwd: .
        cmd: "sh -c 'echo SETUP_DONE'"
        kind: oneshot
      app:
        cwd: .
        cmd: "sh -c 'echo APP_STARTED && sleep 30'"
        depends_on: [setup]
        readiness:
          timeout: 5s
          checks:
            - log: "APP_STARTED"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "APP_STARTED")
	}, 10*time.Second, 100*time.Millisecond, "app should start after oneshot completes")
}

func TestE2E_Oneshot_ExitNonZero_FailsStack(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      setup:
        cwd: .
        cmd: "sh -c 'echo FAILING && exit 1'"
        kind: oneshot
      app:
        cwd: .
        cmd: "sh -c 'echo APP_STARTED && sleep 30'"
        depends_on: [setup]
`)
	out, code := runAndWait(t, dir, "up", "test")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "FAILED")
	require.Contains(t, out, "setup")
	require.NotContains(t, out, "APP_STARTED")
}

func TestE2E_Oneshot_WithTimeout(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      setup:
        cwd: .
        cmd: "sh -c 'sleep 30'"
        kind: oneshot
        readiness:
          timeout: 2s
`)
	out, code := runAndWait(t, dir, "up", "test")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "FAILED")
}

func TestE2E_Oneshot_ChainedSetups(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      build:
        cwd: .
        cmd: "sh -c 'echo BUILD_DONE'"
        kind: oneshot
      install:
        cwd: .
        cmd: "sh -c 'echo INSTALL_DONE'"
        kind: oneshot
        depends_on: [build]
      app:
        cwd: .
        cmd: "sh -c 'echo APP_RUNNING && sleep 30'"
        depends_on: [install]
        readiness:
          timeout: 5s
          checks:
            - log: "APP_RUNNING"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "APP_RUNNING")
	}, 10*time.Second, 100*time.Millisecond, "app should start after chained oneshots complete")
}

func TestE2E_Oneshot_WithCapture(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      generator:
        cwd: .
        cmd: "sh -c 'echo TOKEN=abc123secret'"
        kind: oneshot
        captures:
          - name: token
            log: "TOKEN=([a-z0-9]+)"
            required: true
      app:
        cwd: .
        cmd: "sh -c 'echo RECEIVED=$AUTH_TOKEN && sleep 30'"
        depends_on: [generator]
        env:
          AUTH_TOKEN: "${generator.token}"
        readiness:
          timeout: 5s
          checks:
            - log: "RECEIVED="
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(outBuf.String(), "RECEIVED=abc123secret")
	}, 10*time.Second, 100*time.Millisecond, "app should receive captured token from oneshot")
}

func TestE2E_Oneshot_WithReadinessCheck(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  test:
    processes:
      setup:
        cwd: .
        cmd: "sh -c 'echo BUILD_COMPLETE && echo EXTRA_OUTPUT'"
        kind: oneshot
        readiness:
          timeout: 5s
          checks:
            - log: "BUILD_COMPLETE"
      app:
        cwd: .
        cmd: "sh -c 'echo APP_UP && sleep 30'"
        depends_on: [setup]
        readiness:
          timeout: 5s
          checks:
            - log: "APP_UP"
`)
	cmd := runStackstart(t, dir, "up", "test")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	require.NoError(t, cmd.Start())

	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		out := outBuf.String()
		return strings.Contains(out, "BUILD_COMPLETE") && strings.Contains(out, "APP_UP")
	}, 10*time.Second, 100*time.Millisecond)
}
