package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/version"
)

// The MCP server exposes stackstart's commands as tools. Every tool runs
// this same binary with the matching arguments (and --json where there is
// one), so the tools behave exactly like the CLI.

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	args        func(a map[string]any) ([]string, time.Duration, error)
}

func str(a map[string]any, k string) string {
	if v, ok := a[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func flag(a map[string]any, k string) bool {
	b, _ := a[k].(bool)
	return b
}

func schema(required []string, props map[string]string) map[string]any {
	p := map[string]any{}
	for name, desc := range props {
		typ, d, _ := strings.Cut(desc, ":")
		p[name] = map[string]any{"type": typ, "description": strings.TrimSpace(d)}
	}
	s := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var commonProps = map[string]string{
	"cwd":     "string: project directory to run in (default: the server's working directory)",
	"profile": "string: profile name, when more than one stack runs here",
}

func props(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range commonProps {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func withProfile(a map[string]any, args []string) []string {
	if p := str(a, "profile"); p != "" {
		args = append(args, "--profile", p)
	}
	return args
}

func dur(a map[string]any, k string, def time.Duration) time.Duration {
	if s := str(a, k); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	return def
}

func mcpTools() []mcpTool {
	return []mcpTool{
		{Name: "status", Description: "Running stacks here: every process's state, PID, ports, captured values (like tunnel URLs), restarts, last exit, rebuilds, health, log paths, and the event sequence number.",
			InputSchema: schema(nil, props(map[string]string{"all": "boolean: every running stack on this machine"})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				args := []string{"status", "--json"}
				if flag(a, "all") {
					args = append(args, "--all")
				}
				return withProfile(a, args), 30 * time.Second, nil
			}},
		{Name: "list_profiles", Description: "Profiles available in this directory (from stackstart.yaml files here, above, and in the home directory).",
			InputSchema: schema(nil, map[string]string{"cwd": commonProps["cwd"]}),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				return []string{"list"}, 30 * time.Second, nil
			}},
		{Name: "describe", Description: "A profile as data, without running it: processes, commands, start order, dependencies both ways, readiness checks, captures and who uses them, retry policy, rebuild signals, ports.",
			InputSchema: schema([]string{"profile"}, props(nil)),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				return []string{"describe", str(a, "profile"), "--json"}, 30 * time.Second, nil
			}},
		{Name: "up", Description: "Start a profile in the background and wait until every process is ready (or report the failure with the process's last output). Returns the status document.",
			InputSchema: schema([]string{"profile"}, props(map[string]string{
				"keep_going": "boolean: keep the stack up if a required process fails (its dependents wait)",
				"only":       "string: comma-separated processes to start (with their dependencies)",
				"skip":       "string: comma-separated processes not to start",
				"timeout":    "string: how long to wait for readiness, e.g. 5m",
			})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				t := dur(a, "timeout", 10*time.Minute)
				args := []string{"up", str(a, "profile"), "-d", "--json", "--timeout", t.String()}
				if flag(a, "keep_going") {
					args = append(args, "--keep-going")
				}
				if s := str(a, "only"); s != "" {
					args = append(args, "--only", s)
				}
				if s := str(a, "skip"); s != "" {
					args = append(args, "--skip", s)
				}
				return args, t + 30*time.Second, nil
			}},
		{Name: "down", Description: "Stop the running stack here and wait until every process is gone.",
			InputSchema: schema(nil, props(map[string]string{"force": "boolean: kill everything now instead of waiting for grace periods"})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				args := []string{"down"}
				if flag(a, "force") {
					args = append(args, "--force")
				}
				return withProfile(a, args), 3 * time.Minute, nil
			}},
		processTool("restart", "Restart one process and what depends on it, with fresh captured values; nothing else is touched. Waits until they are ready."),
		processTool("stop", "Stop one process and what depends on it."),
		processTool("start", "Start a stopped or failed process (e.g. after fixing a migration); what waits for it starts once it is ready."),
		{Name: "logs", Description: "A process's log as plain text (no colors or control codes), from the running stack or an earlier run.",
			InputSchema: schema([]string{"process"}, props(map[string]string{
				"process": "string: process name",
				"lines":   "integer: only the last N lines (default 200; 0 for all)",
				"since":   "string: only lines since a duration ago (5m) or a time (14:05)",
				"grep":    "string: only lines matching this regular expression",
				"run":     "string: an earlier run: latest, prev, or a run id",
			})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				args := []string{"logs", str(a, "process"), "--no-follow"}
				lines := 200
				if v, ok := a["lines"].(float64); ok {
					lines = int(v)
				}
				if lines > 0 {
					args = append(args, "--lines", strconv.Itoa(lines))
				}
				for _, k := range []string{"since", "grep", "run"} {
					if s := str(a, k); s != "" {
						args = append(args, "--"+k, s)
					}
				}
				return withProfile(a, args), 30 * time.Second, nil
			}},
		{Name: "wait", Description: "Block until a process is ready, exits, finishes a rebuild, or an event happens; fails with the cause and last output. Pass after (a seq from status) to wait for something caused by an edit you just made.",
			InputSchema: schema(nil, props(map[string]string{
				"process": "string: process name (omit to wait for the whole stack)",
				"for":     "string: ready (default), exit, rebuilt, or event:<type>",
				"after":   "integer: only count events after this sequence number",
				"timeout": "string: give up after this long, e.g. 60s (default 5m)",
			})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				t := dur(a, "timeout", 5*time.Minute)
				args := []string{"wait", "--json", "--timeout", t.String()}
				if p := str(a, "process"); p != "" {
					args = append(args, p)
				}
				if f := str(a, "for"); f != "" {
					args = append(args, "--for", f)
				}
				if v, ok := a["after"].(float64); ok {
					args = append(args, "--after", strconv.Itoa(int(v)))
				}
				return withProfile(a, args), t + 30*time.Second, nil
			}},
		{Name: "events", Description: "What happened in the stack (starts, readiness, captures, rebuilds, failures, restarts, exits), one JSON object per line, optionally only after a sequence number.",
			InputSchema: schema(nil, props(map[string]string{
				"after": "integer: only events after this sequence number",
				"type":  "string: only events of this type",
			})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				args := []string{"events", "--json"}
				if v, ok := a["after"].(float64); ok {
					args = append(args, "--after", strconv.Itoa(int(v)))
				}
				if t := str(a, "type"); t != "" {
					args = append(args, "--type", t)
				}
				return withProfile(a, args), 30 * time.Second, nil
			}},
		{Name: "exec", Description: "Run a shell command with a process's exact environment (captured values, .envrc, defaults) and working directory. Use this instead of sourcing .envrc by hand.",
			InputSchema: schema([]string{"process", "command"}, props(map[string]string{
				"process": "string: process whose environment to use",
				"command": "string: shell command to run",
				"timeout": "string: kill it after this long (default 5m)",
			})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				args := withProfile(a, []string{"exec", str(a, "process")})
				return append(args, "--", "/bin/sh", "-c", str(a, "command")), dur(a, "timeout", 5*time.Minute), nil
			}},
		{Name: "env", Description: "A process's environment and which layer set each variable (shell, default, .envrc, .env, env). Secrets are ***.",
			InputSchema: schema([]string{"process"}, props(map[string]string{"process": "string: process name"})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				return withProfile(a, []string{"env", str(a, "process"), "--json"}), 30 * time.Second, nil
			}},
		{Name: "send", Description: "Type a line into a running process (a REPL like IEx or rails console, or a prompt) and return what it printed.",
			InputSchema: schema([]string{"process", "text"}, props(map[string]string{
				"process": "string: process name",
				"text":    "string: what to type (Enter is added)",
				"echo":    "string: how long to collect the answer (default 1s)",
			})),
			args: func(a map[string]any) ([]string, time.Duration, error) {
				e := dur(a, "echo", time.Second)
				return withProfile(a, []string{"send", str(a, "process"), str(a, "text"), "--json", "--echo", e.String()}), e + 30*time.Second, nil
			}},
	}
}

func processTool(op, desc string) mcpTool {
	return mcpTool{Name: op, Description: desc,
		InputSchema: schema([]string{"process"}, props(map[string]string{
			"process": "string: process name",
			"timeout": "string: how long to wait (default 5m)",
		})),
		args: func(a map[string]any) ([]string, time.Duration, error) {
			t := dur(a, "timeout", 5*time.Minute)
			return withProfile(a, []string{op, str(a, "process"), "--json", "--timeout", t.String()}), t + 30*time.Second, nil
		}}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server on stdin/stdout exposing stackstart to agents",
		Long: `Run a Model Context Protocol server over stdio. Its tools (status, up,
down, restart, start, stop, logs, wait, events, exec, env, send, describe,
list_profiles) run this stackstart binary, so they behave exactly like the
CLI. Each tool takes an optional cwd (the project directory).

Register it with an MCP client, for example:
  claude mcp add stackstart -- stackstart mcp
or in a client's JSON config: {"command": "stackstart", "args": ["mcp"]}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serveMCP(os.Stdin, os.Stdout)
		},
	}
}

func serveMCP(in io.Reader, out io.Writer) error {
	tools := mcpTools()
	byName := map[string]mcpTool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error: " + err.Error()}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification (e.g. notifications/initialized)
		}
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			resp.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "stackstart", "version": version.Version},
				"instructions": "stackstart runs this project's local stack. Use status first; up starts a profile in the background; " +
					"logs/wait/events observe it; restart/stop/start one process; exec runs commands with a process's exact env. " +
					"Never source .envrc by hand: use exec.",
			}
		case "ping":
			resp.Result = map[string]any{}
		case "tools/list":
			resp.Result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp.Error = &rpcError{-32602, "invalid params: " + err.Error()}
				break
			}
			t, ok := byName[p.Name]
			if !ok {
				resp.Error = &rpcError{-32602, "unknown tool " + strconv.Quote(p.Name)}
				break
			}
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			resp.Result = runTool(t, p.Arguments)
		default:
			resp.Error = &rpcError{-32601, "method not found: " + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// runTool runs the stackstart command behind a tool and returns MCP content.
func runTool(t mcpTool, a map[string]any) map[string]any {
	args, timeout, err := t.args(a)
	if err != nil {
		return toolResult(err.Error(), true)
	}
	exe, err := os.Executable()
	if err != nil {
		return toolResult(err.Error(), true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, exe, args...)
	if dir := str(a, "cwd"); dir != "" {
		c.Dir = dir
	}
	// On timeout kill the whole group (exec's command and its children),
	// and don't wait for pipes that grandchildren might still hold open.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Stdin = nil
	runErr := c.Run()
	code := 0
	if runErr != nil {
		code = 1
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if ctx.Err() != nil {
			code = 5
		}
	}
	var b strings.Builder
	b.WriteString(stdout.String())
	if s := strings.TrimSpace(stderr.String()); s != "" {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(s + "\n")
	}
	if code != 0 {
		fmt.Fprintf(&b, "(exit %d)\n", code)
	}
	return toolResult(b.String(), code != 0)
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
