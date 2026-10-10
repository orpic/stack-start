package e2e

// Phase 0 (ROADMAP.md): stackstart must see and report what processes do,
// for every way real dev processes behave. Each test names the roadmap item
// and the real tools whose behavior the fixture reproduces.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func fakeCmd(t *testing.T, mode string, args ...string) string {
	return strings.TrimSpace(fmt.Sprintf("'%s' %s %s", fakeproc(t), mode, strings.Join(args, " ")))
}

// singleProcess writes a profile with one process named "app".
func singleProcess(t *testing.T, dir, profile, cmd, extra string) {
	t.Helper()
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  %s:
    processes:
      app:
        cmd: "%s"
%s
`, profile, strings.ReplaceAll(cmd, `"`, `\"`), extra))
}

// --- 0.1 output is always read, whatever its shape ---

func TestP0_LongLineDoesNotStopOutput(t *testing.T) {
	// Minified stack traces, JSON loggers, webpack stats: one line > 64 KB
	// used to stop all reading and freeze the process.
	dir := t.TempDir()
	singleProcess(t, dir, "p0long", fakeCmd(t, "longline", "200000"), `
        readiness:
          timeout: 10s
          checks:
            - log: "DONE_WRITING"`)
	r := startUp(t, dir, "p0long")
	r.waitFor(t, "1 process ready", 15*time.Second)

	log := processLog(t, latestRun(t, "p0long"), "app")
	require.Contains(t, log, "normal line 3000")
	require.Equal(t, 200_000, strings.Count(log, "x"), "every byte of the long line is logged")
}

func TestP0_BurstOfOutput(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0burst", fakeCmd(t, "burst", "50000"), `
        readiness:
          timeout: 20s
          checks:
            - log: "BURST_DONE"`)
	r := startUp(t, dir, "p0burst")
	r.waitFor(t, "1 process ready", 25*time.Second)

	log := processLog(t, latestRun(t, "p0burst"), "app")
	require.Equal(t, 50_000, strings.Count(log, "burst line "))
}

// --- 0.2 prompts without a newline are seen ---

func TestP0_PromptWithoutNewline(t *testing.T) {
	// IEx, rails console, psql, REPLs: the prompt never ends in a newline.
	dir := t.TempDir()
	singleProcess(t, dir, "p0prompt", fakeCmd(t, "prompt"), `
        readiness:
          timeout: 5s
          checks:
            - log: "iex\\(1\\)>"`)
	r := startUp(t, dir, "p0prompt")
	r.waitFor(t, "1 process ready", 8*time.Second)
	require.Contains(t, processLog(t, latestRun(t, "p0prompt"), "app"), "iex(1)> ")
}

// --- 0.3 readiness does not depend on output arriving ---

func TestP0_SilentProcessBecomesReadyOnTCP(t *testing.T) {
	// Postgres, Redis, many servers: open the port and print nothing more.
	dir := t.TempDir()
	port := freePort(t)
	singleProcess(t, dir, "p0silent", fakeCmd(t, "silentport", strconv.Itoa(port)), fmt.Sprintf(`
        readiness:
          timeout: 5s
          checks:
            - tcp: "127.0.0.1:%d"`, port))
	r := startUp(t, dir, "p0silent")
	r.waitFor(t, "1 process ready", 8*time.Second)
}

func TestP0_NoReadinessBlockIsReadyWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0noready", "sleep 30", "")
	r := startUp(t, dir, "p0noready")
	r.waitFor(t, "1 process ready", 5*time.Second)
}

// --- 0.4 readiness timeouts always fire ---

func TestP0_TimeoutFiresWhileProcessKeepsLogging(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0chatty", fakeCmd(t, "chatty"), `
        readiness:
          timeout: 1s
          checks:
            - log: "will never appear"`)
	start := time.Now()
	r := startUp(t, dir, "p0chatty")
	code := r.waitExit(t, 10*time.Second)

	require.NotEqual(t, 0, code)
	require.Less(t, time.Since(start), 6*time.Second, "the 1s timeout must fire on time")
	require.Contains(t, r.out.String(), "readiness timeout after 1s; still waiting for log /will never appear/")
}

func TestP0_TimeoutFiresWhileProcessIsSilent(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0quiet", fakeCmd(t, "quiet"), `
        readiness:
          timeout: 1s
          checks:
            - tcp: "127.0.0.1:1"`)
	start := time.Now()
	r := startUp(t, dir, "p0quiet")
	code := r.waitExit(t, 10*time.Second)

	require.NotEqual(t, 0, code)
	require.Less(t, time.Since(start), 6*time.Second)
	require.Contains(t, r.out.String(), "still waiting for tcp 127.0.0.1:1")
}

// --- 0.5 screen control never reaches the shared console ---

func TestP0_ScreenClearIsAMarkerNotAWipe(t *testing.T) {
	// tsc --watch, vite, nodemon, next dev clear the screen on rebuild,
	// which used to erase every process's output and the scrollback.
	dir := t.TempDir()
	singleProcess(t, dir, "p0clear", fakeCmd(t, "clear"), `
        readiness:
          timeout: 5s
          checks:
            - log: "build 2 ok"`)
	r := startUp(t, dir, "p0clear")
	r.waitFor(t, "1 process ready", 8*time.Second)

	out := r.out.String()
	require.NotContains(t, out, "\x1b[2J")
	require.NotContains(t, out, "\x1b[3J")
	require.Contains(t, out, "[stackstart] screen cleared")

	log := processLog(t, latestRun(t, "p0clear"), "app")
	require.Equal(t, "build 1 ok\nerror from build 1\n[stackstart] screen cleared\nbuild 2 ok\n", log,
		"output from before the clear stays in the log, with a marker where the clear happened")
}

func TestP0_SelfRestartLeavesAMarkerPerRestart(t *testing.T) {
	// nodemon / node --watch / code reloaders restarting inside one PID.
	dir := t.TempDir()
	singleProcess(t, dir, "p0selfrestart", fakeCmd(t, "selfrestart"), `
        readiness:
          timeout: 5s
          checks:
            - log: "listening on 4000"`)
	r := startUp(t, dir, "p0selfrestart")
	r.waitFor(t, "restarting due to changes... (2)", 8*time.Second)

	log := processLog(t, latestRun(t, "p0selfrestart"), "app")
	require.Equal(t, 2, strings.Count(log, "[stackstart] screen cleared"))
	require.Equal(t, 3, strings.Count(log, "listening on 4000"))
}

func TestP0_SpinnersAndRedrawsBecomeReadableLines(t *testing.T) {
	// npm/docker/cargo progress with \r, docker compose cursor redraws.
	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  p0redraw:
    processes:
      spinner:
        cmd: "%s"
        readiness: {timeout: 5s, checks: [{log: "installed"}]}
      compose:
        cmd: "%s"
        readiness: {timeout: 5s, checks: [{log: "compose ready"}]}
`, fakeCmd(t, "spinner"), fakeCmd(t, "redraw")))
	r := startUp(t, dir, "p0redraw")
	r.waitFor(t, "all 2 processes ready", 8*time.Second)

	ri := latestRun(t, "p0redraw")
	require.Equal(t, "- 100% done\ndownloaded\ninstalled\n", processLog(t, ri, "spinner"))
	require.Equal(t, "[+] Running 0/3\n[+] Running 3/3\n ✔ Container db  Started\ncompose ready\n", processLog(t, ri, "compose"))

	out := r.out.String()
	for _, seq := range []string{"\x1b[1A", "\x1b[2K", "\x1b]0;", "\x1b[?25l", "\r"} {
		require.NotContains(t, out, seq, "control sequence %q must not reach the console", seq)
	}
}

// --- 0.6 plain logs for reading, raw logs for exact bytes ---

func TestP0_PlainAndRawLogs(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0rawlog", fakeCmd(t, "spinner"), `
        readiness: {timeout: 5s, checks: [{log: "installed"}]}`)
	r := startUp(t, dir, "p0rawlog")
	r.waitFor(t, "1 process ready", 8*time.Second)

	ri := latestRun(t, "p0rawlog")
	require.NotContains(t, processLog(t, ri, "app"), "\x1b", "the plain log has no escape codes")
	raw := rawLog(t, ri, "app")
	require.Contains(t, raw, "\x1b[32mdownloading\x1b[0m 30%\r\x1b[Kdownloaded", "the raw log keeps exact bytes")
}

func TestP0_ReadinessMatchesTextNotEscapeCodes(t *testing.T) {
	// "downloaded" is preceded by \r and ESC[K in the raw stream; an anchored
	// regex must still match the visible text.
	dir := t.TempDir()
	singleProcess(t, dir, "p0anchored", fakeCmd(t, "spinner"), `
        readiness: {timeout: 5s, checks: [{log: "^downloaded$"}]}`)
	r := startUp(t, dir, "p0anchored")
	r.waitFor(t, "1 process ready", 8*time.Second)
}

func TestP0_UnicodeAndBinaryOutput(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  p0unicode:
    processes:
      vite:
        cmd: "%s"
        readiness: {timeout: 5s, checks: [{log: "➜  Local:"}]}
      native:
        cmd: "%s"
        readiness: {timeout: 5s, checks: [{log: "after binary"}]}
`, fakeCmd(t, "unicode"), fakeCmd(t, "binary")))
	r := startUp(t, dir, "p0unicode")
	r.waitFor(t, "all 2 processes ready", 8*time.Second)

	ri := latestRun(t, "p0unicode")
	require.Contains(t, processLog(t, ri, "vite"), "✓ Compiled │ 🚀 done")
	native := processLog(t, ri, "native")
	require.True(t, utf8.ValidString(native), "the plain log is always valid UTF-8")
	require.Contains(t, native, "after binary")
}

func TestP0_StderrOnlyProcess(t *testing.T) {
	// Go log, Python logging, Elixir Logger write to stderr.
	dir := t.TempDir()
	singleProcess(t, dir, "p0stderr", fakeCmd(t, "stderr"), `
        readiness: {timeout: 5s, checks: [{log: "stderr ready"}]}`)
	r := startUp(t, dir, "p0stderr")
	r.waitFor(t, "1 process ready", 8*time.Second)
}

// --- 0.7 logs outlive the run ---

func TestP0_LogsSurviveAFailedRun(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0postmortem", fakeCmd(t, "readythencrash"), `
        readiness: {timeout: 5s, checks: [{log: "ready"}]}`)
	r := startUp(t, dir, "p0postmortem")
	require.Equal(t, 1, r.waitExit(t, 15*time.Second))

	out := r.out.String()
	require.Contains(t, out, "Full log: ")
	require.Contains(t, out, "stackstart logs app")

	ri := latestRun(t, "p0postmortem")
	require.Equal(t, "failed", ri.Outcome)
	require.Equal(t, "app", ri.FailedProcess)
	require.Contains(t, ri.Cause, "exited unexpectedly post-ready")

	logs, code := runAndWait(t, dir, "logs", "app")
	require.Equal(t, 0, code, logs)
	require.Contains(t, logs, `column "organization_id" contains null values`)
	require.Contains(t, logs, "failed at")
}

func TestP0_LiveLogsShowRecentHistory(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0livelogs", fakeCmd(t, "spinner"), `
        readiness: {timeout: 5s, checks: [{log: "installed"}]}`)
	r := startUp(t, dir, "p0livelogs")
	r.waitFor(t, "1 process ready", 8*time.Second)

	logs := runStackstart(t, dir, "logs", "app")
	buf := &syncBuffer{}
	logs.Stdout, logs.Stderr = buf, buf
	require.NoError(t, logs.Start())
	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "installed") },
		5*time.Second, 50*time.Millisecond, "live logs should start with recent history")
	_ = logs.Process.Signal(syscall.SIGINT)
	_ = logs.Wait()
	require.Contains(t, buf.String(), "running (Ctrl-C to stop)")
}

func TestP0_LogsPicksTheProfileThatHasTheProcess(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  p0pickone:
    processes:
      onlyhere:
        cmd: "echo from-onlyhere; exit 1"
  p0picktwo:
    processes:
      other:
        cmd: "echo from-other; exit 1"
`)
	_, _ = runAndWait(t, dir, "up", "p0pickone")
	_, _ = runAndWait(t, dir, "up", "p0picktwo")

	out, code := runAndWait(t, dir, "logs", "onlyhere")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "from-onlyhere")
}

func TestP0_OldRunsArePruned(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0prune", "exit 1", "")
	for i := 0; i < 12; i++ {
		_, code := runAndWait(t, dir, "up", "p0prune")
		require.Equal(t, 1, code)
		time.Sleep(20 * time.Millisecond)
	}
	runs, _ := filepath.Glob(filepath.Join(sessionsDir(), "*__p0prune", "runs", "*"))
	require.Len(t, runs, 10)
}

func TestP0_Clean(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0clean", "echo hi; exit 1", "")
	_, _ = runAndWait(t, dir, "up", "p0clean")
	_, _ = runAndWait(t, dir, "up", "p0clean")

	out, code := runAndWait(t, dir, "clean", "--dry-run")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "Would remove 2 finished run(s)")

	out, code = runAndWait(t, dir, "clean")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "Removed 2 finished run(s)")

	out, code = runAndWait(t, dir, "logs", "app")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "no running or previous stackstart session")
}

// --- 0.8 real PIDs in the session record ---

func TestP0_SessionRecordHasRealPIDs(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
profiles:
  p0pids:
    processes:
      a:
        cmd: "sleep 30"
      b:
        cmd: "sleep 30"
        depends_on: [a]
`)
	r := startUp(t, dir, "p0pids")
	r.waitFor(t, "all 2 processes ready", 8*time.Second)

	rec := liveRecord(t, "p0pids")
	require.Len(t, rec.Processes, 2)
	for _, p := range rec.Processes {
		require.True(t, pidAlive(p.PID), "process %s has PID %d, which must be alive", p.Name, p.PID)
	}
}

// --- 0.9 worktrees never control the main checkout's stack ---

func TestP0_WorktreeDoesNotMatchMainCheckout(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0755))
	singleProcess(t, repo, "p0worktree", "sleep 30", "")
	wt := filepath.Join(repo, ".worktrees", "feature")
	require.NoError(t, os.MkdirAll(wt, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../../.git/worktrees/feature\n"), 0644))

	r := startUp(t, repo, "p0worktree")
	r.waitFor(t, "1 process ready", 8*time.Second)

	out, code := runAndWait(t, wt, "down")
	require.NotEqual(t, 0, code, "down from a worktree must not stop the main checkout's stack")
	require.Contains(t, out, "no running stackstart session found for "+wt)
	require.False(t, r.finished())

	sub := filepath.Join(repo, "packages", "web")
	require.NoError(t, os.MkdirAll(sub, 0755))
	out, code = runAndWait(t, sub, "down")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "Stopped")
}

// --- 0.10 fail_on and 0.11 capture checks (the cloudflared case) ---

const tunnelReadiness = `
        readiness:
          timeout: 30s
          checks:
            - capture: url
        captures:
          - name: url
            log: "(https://[a-z0-9]+(?:-[a-z0-9]+)+\\.trycloudflare\\.com)"
        fail_on:
          - "failed to request quick Tunnel"`

func tunnelProfile(t *testing.T, dir, profile, mode string) {
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  %s:
    processes:
      cloudflared:
        cmd: "%s"
%s
      backend:
        cmd: "echo backend got $TUNNEL_URL; sleep 30"
        depends_on: [cloudflared]
        env:
          TUNNEL_URL: "${cloudflared.url}"
        readiness: {timeout: 5s, checks: [{log: "backend got"}]}
`, profile, fakeCmd(t, mode), tunnelReadiness))
}

func TestP0_FailOnFailsFastAndNeverCapturesTheErrorURL(t *testing.T) {
	dir := t.TempDir()
	tunnelProfile(t, dir, "p0tunnelfail", "tunnelfail")
	start := time.Now()
	r := startUp(t, dir, "p0tunnelfail")
	require.Equal(t, 1, r.waitExit(t, 10*time.Second))

	require.Less(t, time.Since(start), 5*time.Second, "fail_on must not wait for the 30s timeout")
	out := r.out.String()
	require.Contains(t, out, "FAILED: cloudflared")
	require.Contains(t, out, "matched fail_on /failed to request quick Tunnel/")
	require.NotContains(t, out, "backend got", "the dependent must never start with the error URL")
}

func TestP0_CaptureCheckPropagatesTheRealURL(t *testing.T) {
	dir := t.TempDir()
	tunnelProfile(t, dir, "p0tunnelok", "tunnelok")
	r := startUp(t, dir, "p0tunnelok")
	r.waitFor(t, "backend got https://quiet-river-blue-moon.trycloudflare.com", 8*time.Second)
}

func TestP0_FailOnAfterReadyIsAWarning(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0latefail", "echo up; sleep 0.3; echo 'password authentication failed for user'; sleep 30", `
        fail_on: ["password authentication failed"]
        readiness: {timeout: 5s, checks: [{log: "up"}]}`)
	r := startUp(t, dir, "p0latefail")
	r.waitFor(t, "[stackstart] warning: matched fail_on /password authentication failed/ after the process was ready", 8*time.Second)
	time.Sleep(300 * time.Millisecond)
	require.False(t, r.finished(), "a late match warns but does not stop the stack")
}

// --- 0.12 teardown stops everything, every time ---

func TestP0_FailedButRunningProcessIsStopped(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0orphan", fakeCmd(t, "chatty"), `
        readiness: {timeout: 1s, checks: [{log: "never"}]}`)
	r := startUp(t, dir, "p0orphan")
	require.Equal(t, 1, r.waitExit(t, 10*time.Second))

	ri := latestRun(t, "p0orphan")
	require.Eventually(t, func() bool { return !pidAlive(ri.Processes[0].PID) }, 3*time.Second, 50*time.Millisecond,
		"a process that failed readiness must not be left running")
}

func TestP0_ChildTreeIsStopped(t *testing.T) {
	// npm run dev -> node -> esbuild; mix phx.server -> watchers.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	singleProcess(t, dir, "p0tree", fakeCmd(t, "tree", pidFile), `
        readiness: {timeout: 5s, checks: [{log: "parent ready"}]}`)
	r := startUp(t, dir, "p0tree")
	r.waitFor(t, "1 process ready", 8*time.Second)

	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	childPID, _ := strconv.Atoi(string(data))
	require.True(t, pidAlive(childPID))

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool { return !pidAlive(childPID) }, 3*time.Second, 50*time.Millisecond,
		"grandchildren must be stopped with their parent")
}

func TestP0_ChildThatLeftTheProcessGroupIsStopped(t *testing.T) {
	// BEAM port programs and setsid'd children are not in the process group.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	singleProcess(t, dir, "p0escape", fakeCmd(t, "escape", pidFile), `
        readiness: {timeout: 5s, checks: [{log: "parent ready"}]}`)
	r := startUp(t, dir, "p0escape")
	r.waitFor(t, "1 process ready", 8*time.Second)

	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	childPID, _ := strconv.Atoi(string(data))
	pgid, err := syscall.Getpgid(childPID)
	require.NoError(t, err)
	require.NotEqual(t, liveRecord(t, "p0escape").Processes[0].PID, pgid, "fixture: child must be in its own group")

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool { return !pidAlive(childPID) }, 3*time.Second, 50*time.Millisecond,
		"a descendant outside the process group must be stopped too")
}

func TestP0_DownWaitsForAStubbornProcess(t *testing.T) {
	// BEAM, Puma, JVM apps with slow shutdown: SIGTERM ignored, SIGKILL after grace.
	dir := t.TempDir()
	singleProcess(t, dir, "p0stubborn", fakeCmd(t, "stubborn"), `
        stop_grace_period: 1s
        readiness: {timeout: 5s, checks: [{log: "stubborn ready"}]}`)
	r := startUp(t, dir, "p0stubborn")
	r.waitFor(t, "1 process ready", 8*time.Second)
	pid := liveRecord(t, "p0stubborn").Processes[0].PID

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.True(t, r.finished() || waitDone(r, time.Second), "down returns only once stackstart has exited")
	require.False(t, pidAlive(pid))
	require.Contains(t, processLog(t, latestRun(t, "p0stubborn"), "app"), "ignoring terminated")
}

func waitDone(r *running, d time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestP0_SecondSignalKillsImmediately(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0forcekill", fakeCmd(t, "stubborn"), `
        stop_grace_period: 60s
        readiness: {timeout: 5s, checks: [{log: "stubborn ready"}]}`)
	r := startUp(t, dir, "p0forcekill")
	r.waitFor(t, "1 process ready", 8*time.Second)
	pid := liveRecord(t, "p0forcekill").Processes[0].PID

	require.NoError(t, r.cmd.Process.Signal(syscall.SIGINT))
	r.waitFor(t, "Ctrl-C again to kill immediately", 3*time.Second)
	require.NoError(t, r.cmd.Process.Signal(syscall.SIGINT))
	require.Equal(t, 130, r.waitExit(t, 5*time.Second))
	require.Eventually(t, func() bool { return !pidAlive(pid) }, 2*time.Second, 50*time.Millisecond)
}

func TestP0_ClosingTheTerminalStopsTheStack(t *testing.T) {
	dir := t.TempDir()
	singleProcess(t, dir, "p0sighup", "sleep 30", "")
	r := startUp(t, dir, "p0sighup")
	r.waitFor(t, "1 process ready", 8*time.Second)
	pid := liveRecord(t, "p0sighup").Processes[0].PID

	require.NoError(t, r.cmd.Process.Signal(syscall.SIGHUP))
	r.waitExit(t, 15*time.Second)
	require.False(t, pidAlive(pid), "SIGHUP (terminal closed) must not orphan the stack")
}

func TestP0_PagerDoesNotHangAOneshot(t *testing.T) {
	// psql/git/mysql page their output when they see a terminal.
	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  p0pager:
    processes:
      query:
        kind: oneshot
        cmd: "%s"
        readiness: {timeout: 5s}
`, fakeCmd(t, "pager")))
	out, code := runAndWaitEnv(t, dir, []string{"PAGER=less"}, "up", "p0pager")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "result row 1")
	require.NotContains(t, out, "(END)")
}

func TestP0_OneshotOnlyProfileExits(t *testing.T) {
	// "migrate then seed" profiles have nothing left to run afterwards.
	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  p0oneshots:
    processes:
      migrate:
        kind: oneshot
        cmd: "%s"
      seed:
        kind: oneshot
        cmd: "echo seeded"
        depends_on: [migrate]
`, fakeCmd(t, "oneshot")))
	out, code := runAndWait(t, dir, "up", "p0oneshots")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "all processes finished")
	require.Equal(t, "stopped", latestRun(t, "p0oneshots").Outcome)
}
