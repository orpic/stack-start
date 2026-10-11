package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/orpic/stack-start/internal/control"
	"github.com/orpic/stack-start/internal/env"
	"github.com/orpic/stack-start/internal/interpolate"
)

// processEnv returns the environment and cwd a process runs with: from the
// running stack when there is one (captured values included), otherwise
// composed from the config the same way.
func processEnv(profile, process string) (control.EnvResult, bool, error) {
	if rec, err := findLive(profile, process); err == nil {
		resp, err := call(rec, control.Request{Op: "env", Process: process}, 10*time.Second, nil)
		if err != nil {
			return control.EnvResult{}, true, err
		}
		var r control.EnvResult
		if err := json.Unmarshal(resp.Result, &r); err != nil {
			return control.EnvResult{}, true, err
		}
		return r, true, nil
	}

	// No running stack: compose from the config.
	if profile == "" {
		return control.EnvResult{}, false, usageError(fmt.Errorf("no running stack here; pass --profile <name> to use a profile's config"))
	}
	prof, _, err := resolveProfile(profile)
	if err != nil {
		return control.EnvResult{}, false, err
	}
	pc, ok := prof.Processes[process]
	if !ok {
		var names []string
		for n := range prof.Processes {
			names = append(names, n)
		}
		return control.EnvResult{}, false, usageError(fmt.Errorf("process %q not found in profile %q (processes: %s)", process, profile, strings.Join(names, ", ")))
	}
	for k, v := range pc.Env {
		toks, _ := interpolate.Tokenize(v)
		for _, t := range toks {
			if !t.Literal && !strings.HasPrefix(t.Value, "envrc.") {
				return control.EnvResult{}, false, failedError(fmt.Errorf(
					"%s uses ${%s} (in %s), which is only known while the stack runs; start it with 'stackstart up %s -d'",
					process, t.Value, k, profile))
			}
		}
	}
	vars, err := env.ComposeExplained(prof.ResolvedProjectPath, pc.Env, nil, os.Environ())
	if err != nil {
		return control.EnvResult{}, false, err
	}
	cwd := prof.ResolvedProjectPath
	if pc.Cwd != "" {
		cwd = filepath.Join(cwd, pc.Cwd)
	}
	return control.EnvResult{Process: process, Cwd: cwd, Vars: vars, Secret: pc.SecretEnv}, false, nil
}

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env <process>",
		Short: "Print the environment a process runs with, and where each variable comes from",
		Long: `Print the exact environment of a process: the shell it was started from,
stackstart's defaults, the project's .envrc and .env, the process's env:
block and captured values. With a running stack this is what the process
really got; otherwise it is composed from --profile's config.

--explain adds where each variable came from (and what it overrode).
Variables marked secret (secret_env, or set from a capture with
secret: true) are shown as *** unless --show-secrets.`,
		Example: `  stackstart env phoenix
  stackstart env phoenix --explain | grep DATABASE
  stackstart env api --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			explain, _ := cmd.Flags().GetBool("explain")
			asJSON, _ := cmd.Flags().GetBool("json")
			show, _ := cmd.Flags().GetBool("show-secrets")
			r, _, err := processEnv(profile, args[0])
			if err != nil {
				return err
			}
			secret := map[string]bool{}
			for _, s := range r.Secret {
				secret[s] = true
			}
			for i := range r.Vars {
				if secret[r.Vars[i].Name] && !show {
					r.Vars[i].Value = "***"
				}
			}
			if asJSON {
				return printJSON(r)
			}
			for _, v := range r.Vars {
				if explain {
					note := v.Source
					if len(v.Overrode) > 0 {
						note += ", overrode " + strings.Join(v.Overrode, ", ")
					}
					fmt.Printf("%s=%s  # %s\n", v.Name, v.Value, note)
				} else {
					fmt.Printf("%s=%s\n", v.Name, v.Value)
				}
			}
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (needed without a running stack)")
	cmd.Flags().Bool("explain", false, "show which layer set each variable")
	cmd.Flags().Bool("json", false, "print JSON")
	cmd.Flags().Bool("show-secrets", false, "show secret values instead of ***")
	return cmd
}

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <process> -- <command> [args...]",
		Short: "Run a command with a process's exact environment and working directory",
		Long: `Run a command the way the process itself runs: same environment
(including captured values like a tunnel URL, the database URL from .envrc,
and stackstart's defaults) and same working directory. Use it instead of
sourcing .envrc by hand, which can point a script at the wrong database.

The command's exit code is stackstart's exit code.`,
		Example: `  stackstart exec phoenix -- mix ecto.migrate
  stackstart exec api -- npm run seed
  stackstart exec db -- psql -c 'select 1'`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			dash := cmd.ArgsLenAtDash()
			if dash != 1 {
				return usageError(errors.New("usage: stackstart exec <process> -- <command> [args...]"))
			}
			r, _, err := processEnv(profile, args[0])
			if err != nil {
				return err
			}
			envList := make([]string, 0, len(r.Vars))
			for _, v := range r.Vars {
				envList = append(envList, v.Name+"="+v.Value)
			}
			name := args[1]
			if !strings.Contains(name, "/") {
				// Look the command up on the process's PATH, not ours.
				if p := lookPath(name, r.Vars); p != "" {
					name = p
				}
			}
			c := exec.Command(name, args[2:]...)
			c.Env, c.Dir = envList, r.Cwd
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
			if err := c.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					code := ee.ExitCode()
					if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
						code = 128 + int(ws.Signal())
					}
					return exitError{code: code}
				}
				return fmt.Errorf("%s: %w", args[1], err)
			}
			return nil
		},
	}
	cmd.Flags().String("profile", "", "profile name (needed without a running stack)")
	return cmd
}

func lookPath(name string, vars []env.Var) string {
	for _, v := range vars {
		if v.Name != "PATH" {
			continue
		}
		for _, dir := range filepath.SplitList(v.Value) {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				return p
			}
		}
	}
	return ""
}
