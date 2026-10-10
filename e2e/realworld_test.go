//go:build realworld

package e2e

// Real-tool tests: the same Phase 0 guarantees, checked against the actual
// tools developers run. Each test skips (with the reason) when its tool is
// missing. Run with:
//
//	go test -tags realworld -timeout 900s ./e2e/ -run TestRW_ -v

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// requireTool skips the test unless `name args...` succeeds in dir. Running
// it (not just finding it on PATH) catches version-manager shims that exist
// but have no version selected for that directory.
func requireTool(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("%s %s does not work here: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

// sleepCount counts `sleep <secs>` processes by exact command line, so the
// commands that mention it (sh -c, elixir -e) are not counted.
func sleepCount(secs string) int {
	out, _ := exec.Command("pgrep", "-f", "^sleep "+secs+"$").Output()
	return len(strings.Fields(string(out)))
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func uniqueSleep() string {
	return fmt.Sprintf("%d", 30000+rand.Intn(9999))
}

// --- Elixir / Erlang ---

func TestRW_Elixir_IExPromptIsAReadySignal(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "iex", "--version")
	singleProcess(t, dir, "rwiex", "iex", `
        readiness: {timeout: 30s, checks: [{log: "iex\\(1\\)>"}]}`)
	r := startUp(t, dir, "rwiex")
	r.waitFor(t, "1 process ready", 40*time.Second)

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	log := processLog(t, latestRun(t, "rwiex"), "app")
	require.Contains(t, log, "iex(1)> ")
	require.NotContains(t, log, "\x1b")
}

func TestRW_Elixir_PortChildIsStopped(t *testing.T) {
	// Phoenix watchers (esbuild, tailwind) run as BEAM ports: OS children of the VM.
	dir := t.TempDir()
	requireTool(t, dir, "elixir", "--version")
	secs := uniqueSleep()
	singleProcess(t, dir, "rwport", fmt.Sprintf(
		`elixir -e 'Port.open({:spawn, "sleep %s"}, []); IO.puts("elixir ready"); Process.sleep(:infinity)'`, secs), `
        readiness: {timeout: 30s, checks: [{log: "elixir ready"}]}`)
	r := startUp(t, dir, "rwport")
	r.waitFor(t, "1 process ready", 40*time.Second)
	require.Eventually(t, func() bool { return sleepCount(secs) == 1 }, 5*time.Second, 100*time.Millisecond)

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool { return sleepCount(secs) == 0 }, 5*time.Second, 100*time.Millisecond,
		"the BEAM's port child must not outlive the stack")
}

func TestRW_Elixir_HugeLinesOnStderr(t *testing.T) {
	// Logger truncates messages at 8 KB by default, but inspect/IO output of
	// large terms does not, and both go to stderr.
	dir := t.TempDir()
	requireTool(t, dir, "elixir", "--version")
	singleProcess(t, dir, "rwhugeline", `elixir -e 'require Logger; Logger.error(String.duplicate("y", 20_000)); IO.puts(:stderr, String.duplicate("z", 150_000)); IO.puts("after long line"); Process.sleep(:infinity)'`, `
        readiness: {timeout: 30s, checks: [{log: "after long line"}]}`)
	r := startUp(t, dir, "rwhugeline")
	r.waitFor(t, "1 process ready", 40*time.Second)

	log := processLog(t, latestRun(t, "rwhugeline"), "app")
	require.Equal(t, 150_000, strings.Count(log, "z"), "the whole 150 KB line is logged")
	require.Contains(t, log, "(truncated)", "Logger's own truncation marker is kept as-is")
}

func TestRW_Elixir_MixCompileThenRun(t *testing.T) {
	// A real mix project: compile as a oneshot, then run the app.
	root := t.TempDir()
	requireTool(t, root, "mix", "--version")
	mixNew := exec.Command("mix", "new", "demo")
	mixNew.Dir = root
	out, err := mixNew.CombinedOutput()
	require.NoError(t, err, string(out))
	dir := filepath.Join(root, "demo")
	writeConfig(t, dir, `
profiles:
  rwmix:
    processes:
      compile:
        kind: oneshot
        cmd: "mix compile --force"
        readiness: {timeout: 120s}
      app:
        cmd: "mix run --no-halt -e 'IO.puts(\"demo app ready\")'"
        depends_on: [compile]
        readiness: {timeout: 120s, checks: [{log: "demo app ready"}]}
`)
	r := startUp(t, dir, "rwmix")
	r.waitFor(t, "all 2 processes ready", 150*time.Second)

	compileLog := processLog(t, latestRun(t, "rwmix"), "compile")
	require.Contains(t, compileLog, "Generated demo app")
	require.NotContains(t, compileLog, "\x1b")
}

// --- Node / Bun ---

func TestRW_Node_WatchRestartKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "node", "--version")
	writeFile(t, filepath.Join(dir, "server.js"), `console.log("listening v1"); setInterval(() => {}, 1000)`)
	singleProcess(t, dir, "rwnodewatch", "node --watch server.js", `
        readiness: {timeout: 15s, checks: [{log: "listening v1"}]}`)
	r := startUp(t, dir, "rwnodewatch")
	r.waitFor(t, "1 process ready", 20*time.Second)

	writeFile(t, filepath.Join(dir, "server.js"), `console.log("listening v2"); setInterval(() => {}, 1000)`)
	r.waitFor(t, "listening v2", 15*time.Second)

	log := processLog(t, latestRun(t, "rwnodewatch"), "app")
	require.Contains(t, log, "listening v1\n")
	require.Contains(t, log, "Restarting 'server.js'")
	require.Contains(t, log, "listening v2\n")
}

func TestRW_Bun_WatchClearsScreenOnRestart(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "bun", "--version")
	writeFile(t, filepath.Join(dir, "server.js"), `console.log("listening v1"); setInterval(() => {}, 1000)`)
	singleProcess(t, dir, "rwbunwatch", "bun --watch server.js", `
        readiness: {timeout: 15s, checks: [{log: "listening v1"}]}`)
	r := startUp(t, dir, "rwbunwatch")
	r.waitFor(t, "1 process ready", 20*time.Second)

	writeFile(t, filepath.Join(dir, "server.js"), `console.log("listening v2"); setInterval(() => {}, 1000)`)
	r.waitFor(t, "listening v2", 15*time.Second)

	require.NotContains(t, r.out.String(), "\x1b[3J", "bun's scrollback wipe must not reach the console")
	log := processLog(t, latestRun(t, "rwbunwatch"), "app")
	require.Equal(t, "listening v1\n[stackstart] screen cleared\nlistening v2\n", log)
}

func TestRW_Npm_ScriptChildTreeIsStopped(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "npm", "--version")
	secs := uniqueSleep()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"rw","version":"1.0.0","scripts":{"dev":"node server.js"}}`)
	writeFile(t, filepath.Join(dir, "server.js"), fmt.Sprintf(`
require("child_process").spawn("sleep", ["%s"], {stdio: "ignore"});
console.log("npm child ready");
setInterval(() => {}, 1000);`, secs))
	singleProcess(t, dir, "rwnpm", "npm run dev", `
        readiness: {timeout: 30s, checks: [{log: "npm child ready"}]}`)
	r := startUp(t, dir, "rwnpm")
	r.waitFor(t, "1 process ready", 40*time.Second)
	require.Equal(t, 1, sleepCount(secs))

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool { return sleepCount(secs) == 0 }, 5*time.Second, 100*time.Millisecond,
		"npm -> node -> child must all be stopped")
}

func TestRW_Node_HugeJSONLine(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "node", "--version")
	singleProcess(t, dir, "rwnodejson", `node -e 'console.log(JSON.stringify({level:"error",msg:"x".repeat(300000)})); console.log("after json"); setInterval(()=>{},1000)'`, `
        readiness: {timeout: 15s, checks: [{log: "after json"}]}`)
	r := startUp(t, dir, "rwnodejson")
	r.waitFor(t, "1 process ready", 20*time.Second)
}

// --- Databases and servers that go silent once listening ---

func TestRW_Postgres_NativeWithDependentQuery(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "initdb", "--version")
	requireTool(t, dir, "psql", "--version")
	out, err := exec.Command("initdb", "-D", filepath.Join(dir, "pgdata"), "-A", "trust", "-U", "postgres").CombinedOutput()
	require.NoError(t, err, string(out))
	port := freePort(t)
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  rwpg:
    processes:
      postgres:
        cmd: "postgres -D pgdata -p %d -c listen_addresses=127.0.0.1 -c unix_socket_directories=''"
        readiness: {timeout: 30s, checks: [{tcp: "127.0.0.1:%d"}]}
      migrate:
        kind: oneshot
        cmd: "psql -h 127.0.0.1 -p %d -U postgres -c 'select 40 + 2 as answer'"
        depends_on: [postgres]
      app:
        cmd: "echo app started; sleep 30"
        depends_on: [migrate]
`, port, port, port))
	r := startUp(t, dir, "rwpg")
	r.waitFor(t, "all 3 processes ready", 40*time.Second)
	require.Contains(t, processLog(t, latestRun(t, "rwpg"), "migrate"), "42")

	out2, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out2)
	require.False(t, portOpen(port), "postgres must be stopped")
}

func TestRW_Redis_SilentAfterBanner(t *testing.T) {
	dir := t.TempDir()
	requireTool(t, dir, "redis-server", "--version")
	port := freePort(t)
	singleProcess(t, dir, "rwredis", fmt.Sprintf("redis-server --port %d --save '' --appendonly no", port), fmt.Sprintf(`
        readiness: {timeout: 15s, checks: [{tcp: "127.0.0.1:%d"}]}`, port))
	r := startUp(t, dir, "rwredis")
	r.waitFor(t, "1 process ready", 20*time.Second)
	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.False(t, portOpen(port))
}

func TestRW_Python_HTTPServer(t *testing.T) {
	dir := t.TempDir()
	py := "/usr/bin/python3"
	requireTool(t, dir, py, "--version")
	port := freePort(t)
	singleProcess(t, dir, "rwpython", fmt.Sprintf("%s -m http.server %d --bind 127.0.0.1", py, port), fmt.Sprintf(`
        readiness: {timeout: 15s, checks: [{tcp: "127.0.0.1:%d"}]}`, port))
	r := startUp(t, dir, "rwpython")
	r.waitFor(t, "1 process ready", 20*time.Second)
}

func TestRW_Go_RunChildBinaryIsStopped(t *testing.T) {
	// `go run` compiles to a temp binary and runs it as a child process.
	dir := t.TempDir()
	requireTool(t, dir, "go", "version")
	port := freePort(t)
	writeFile(t, filepath.Join(dir, "main.go"), fmt.Sprintf(`package main

import (
	"log"
	"net/http"
)

func main() {
	log.Println("go server starting")
	log.Fatal(http.ListenAndServe("127.0.0.1:%d", nil))
}
`, port))
	singleProcess(t, dir, "rwgo", "go run main.go", fmt.Sprintf(`
        readiness: {timeout: 120s, checks: [{tcp: "127.0.0.1:%d"}]}`, port))
	r := startUp(t, dir, "rwgo")
	r.waitFor(t, "1 process ready", 130*time.Second)

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool { return !portOpen(port) }, 5*time.Second, 100*time.Millisecond,
		"the binary go run started must be stopped too")
}

// --- Docker ---

func TestRW_Docker_PostgresReadyOnlyWhenReallyListening(t *testing.T) {
	// Docker's port proxy accepts TCP before postgres listens, and the
	// image's init step prints "ready to accept connections" for a
	// temporary server first. The real signal is the final listen line.
	dir := t.TempDir()
	requireTool(t, dir, "docker", "image", "inspect", "postgres:16")
	requireTool(t, dir, "psql", "--version")
	port := freePort(t)
	name := fmt.Sprintf("stackstart-rw-%d", rand.Intn(1_000_000))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	writeConfig(t, dir, fmt.Sprintf(`
profiles:
  rwdocker:
    processes:
      db:
        cmd: "docker run --rm --name %s -e POSTGRES_HOST_AUTH_METHOD=trust -p 127.0.0.1:%d:5432 postgres:16"
        stop_grace_period: 20s
        readiness:
          timeout: 90s
          checks:
            - log: 'listening on IPv4 address "0.0.0.0", port 5432'
      query:
        kind: oneshot
        cmd: "psql -h 127.0.0.1 -p %d -U postgres -c 'select 40 + 2 as answer'"
        depends_on: [db]
`, name, port, port))
	r := startUp(t, dir, "rwdocker")
	r.waitFor(t, "all 2 processes ready", 100*time.Second)
	require.Contains(t, processLog(t, latestRun(t, "rwdocker"), "query"), "42",
		"the dependent ran only after postgres really accepted queries")

	out, code := runAndWait(t, dir, "down")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool {
		ps, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+name).Output()
		return strings.TrimSpace(string(ps)) == ""
	}, 30*time.Second, 500*time.Millisecond, "the container must be stopped and removed")
}
