// Package signals has the rebuild patterns of common dev tools, taken from
// what each tool really prints (ROADMAP.md, "Findings log"). A process with
// `signals: {preset: <name>}` gets rebuild_started, rebuild_finished and
// rebuild_failed events from its output.
package signals

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Patterns are the regexes for one tool; empty means "not recognised".
// A rebuild also finishes when one of the process's readiness log checks
// matches again (servers that restart print their ready line again).
type Patterns struct {
	Rebuilding    string
	Rebuilt       string
	RebuildFailed string
	// RebuiltAfterStart ends a rebuild only if one started (for lines that
	// also appear in ordinary output, like a request log line).
	RebuiltAfterStart string
}

// Presets maps a tool name to its patterns.
var Presets = map[string]Patterns{
	// mix phx.server: the code reloader compiles on the next request; the
	// esbuild and tailwind watchers rebuild assets.
	"phoenix": {
		Rebuilding:        `Compiling \d+ files? \(\.ex\)|\[watch\] build started`,
		Rebuilt:           `\[watch\] build finished|^Done in \d+m?s`,
		RebuiltAfterStart: `^\[info\] (GET|POST|PUT|PATCH|DELETE) `,
		RebuildFailed:     `== Compilation error|\((CompileError|SyntaxError|TokenMissingError)\)|✘ \[ERROR\]`,
	},
	// Vite (React Router, SvelteKit, plain Vite).
	"vite": {
		Rebuilding:    `changed, restarting server\.\.\.`,
		Rebuilt:       `page reload |hmr update |server restarted\.`,
		RebuildFailed: `Internal server error|Pre-transform error|\[vite\] error`,
	},
	// tsx watch: restarts on change; the app's own ready line ends it.
	"tsx": {
		Rebuilding:    `Restarting\.\.\.`,
		RebuildFailed: `^(\w*Error|SyntaxError): |Transform failed with \d+ error`,
	},
	// node --watch (Node 22+).
	"node-watch": {
		Rebuilding:    `^Restarting '`,
		RebuildFailed: `^Failed running '`,
	},
	"nodemon": {
		Rebuilding:    `\[nodemon\] restarting due to changes`,
		RebuildFailed: `\[nodemon\] app crashed`,
	},
	// Django runserver (StatReloader); the startup banner ends it.
	"django": {
		Rebuilding:    ` changed, reloading\.$`,
		Rebuilt:       `^Starting development server at `,
		RebuildFailed: `^Traceback \(most recent call last\)`,
	},
	// uvicorn --reload (WatchFiles).
	"uvicorn": {
		Rebuilding:    `WatchFiles detected changes in .* Reloading\.\.\.`,
		Rebuilt:       `Application startup complete\.`,
		RebuildFailed: `Traceback \(most recent call last\)|Error loading ASGI app`,
	},
	"next": {
		Rebuilding:    `○ Compiling |^ ?Compiling `,
		Rebuilt:       `✓ Compiled`,
		RebuildFailed: `⨯ |Failed to compile`,
	},
	"webpack": {
		Rebuilding:    `^\[webpack-dev-server\] |compiling\.\.\.`,
		Rebuilt:       `compiled successfully`,
		RebuildFailed: `compiled with \d+ errors?|Failed to compile`,
	},
	"tsc": {
		Rebuilding:    `File change detected\. Starting incremental compilation`,
		Rebuilt:       `Found 0 errors\. Watching for file changes`,
		RebuildFailed: `Found [1-9]\d* errors?\. Watching for file changes`,
	},
	// The tailwind CLI watcher (also inside Phoenix and Rails).
	"tailwind": {
		Rebuilt:       `^Done in \d+m?s`,
		RebuildFailed: `^Error: `,
	},
	"esbuild": {
		Rebuilding:    `\[watch\] build started`,
		Rebuilt:       `\[watch\] build finished`,
		RebuildFailed: `✘ \[ERROR\]`,
	},
	// bin/dev (foreman: puma + tailwind). Rails reloads code silently.
	"rails": {
		Rebuilt:       `css\.1 +\| Done in \d+m?s`,
		RebuildFailed: `css\.1 +\| Error: `,
	},
}

// Names lists the presets, sorted.
func Names() []string {
	var out []string
	for n := range Presets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Resolve merges a preset with explicit patterns (explicit wins).
func Resolve(preset, rebuilding, rebuilt, rebuildFailed string) (Patterns, error) {
	var p Patterns
	if preset != "" {
		pre, ok := Presets[preset]
		if !ok {
			return p, fmt.Errorf("unknown preset %q (presets: %s)", preset, strings.Join(Names(), ", "))
		}
		p = pre
	}
	if rebuilding != "" {
		p.Rebuilding = rebuilding
	}
	if rebuilt != "" {
		p.Rebuilt = rebuilt
	}
	if rebuildFailed != "" {
		p.RebuildFailed = rebuildFailed
	}
	for name, re := range map[string]string{"rebuilding": p.Rebuilding, "rebuilt": p.Rebuilt, "rebuild_failed": p.RebuildFailed} {
		if re == "" {
			continue
		}
		if _, err := regexp.Compile(re); err != nil {
			return p, fmt.Errorf("%s: invalid regex %q: %v", name, re, err)
		}
	}
	return p, nil
}
