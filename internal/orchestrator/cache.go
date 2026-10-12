package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/orpic/stack-start/internal/session"
)

// inputsHash fingerprints a oneshot: its resolved command and the content
// of every file matching its inputs globs (relative to cwd).
func inputsHash(cwd, cmd string, globs []string) (string, int, error) {
	files := map[string]bool{}
	for _, g := range globs {
		matches, err := expandGlob(cwd, g)
		if err != nil {
			return "", 0, fmt.Errorf("inputs %q: %w", g, err)
		}
		for _, m := range matches {
			files[m] = true
		}
	}
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	h := sha256.New()
	_, _ = io.WriteString(h, cmd+"\x00")
	for _, name := range names {
		f, err := os.Open(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		_, _ = io.WriteString(h, name+"\x00")
		_, _ = io.Copy(h, f)
		_ = f.Close()
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16], len(names), nil
}

// expandGlob returns files under cwd matching a glob with *, ? and **.
// Only the directory before the first wildcard is walked.
func expandGlob(cwd, glob string) ([]string, error) {
	glob = filepath.ToSlash(filepath.Clean(glob))
	if !strings.ContainsAny(glob, "*?[") {
		if st, err := os.Stat(filepath.Join(cwd, glob)); err == nil {
			if !st.IsDir() {
				return []string{glob}, nil
			}
			glob += "/**"
		} else {
			return nil, nil
		}
	}
	base := glob
	if i := strings.IndexAny(glob, "*?["); i >= 0 {
		base = glob[:i]
		if j := strings.LastIndex(base, "/"); j >= 0 {
			base = base[:j]
		} else {
			base = ""
		}
	}
	re, err := globRegexp(glob)
	if err != nil {
		return nil, err
	}
	var out []string
	root := filepath.Join(cwd, base)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(cwd, path)
		rel = filepath.ToSlash(rel)
		if re.MatchString(rel) {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

func globRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// resolvedEnvBlock is the final value of every variable a process's env:
// block sets (captured values and ports included), for the inputs hash.
func resolvedEnvBlock(block map[string]string, env []string) string {
	keys := make([]string, 0, len(block))
	for k := range block {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, kv := range env {
			if strings.HasPrefix(kv, k+"=") {
				b.WriteString(kv + "\x00")
			}
		}
	}
	return b.String()
}

func (o *Orchestrator) cachePath(name string) string {
	return filepath.Join(session.SessionDir(o.slug), "cache", name+".inputs")
}

func (o *Orchestrator) cachedHash(name string) string {
	data, err := os.ReadFile(o.cachePath(name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (o *Orchestrator) storeHash(name, hash string) {
	path := o.cachePath(name)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(hash+"\n"), 0o644)
}
