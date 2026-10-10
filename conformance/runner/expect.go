package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// assertionNames are the output assertions, used bare in expect_output and
// with a stdout_/stderr_/output_ prefix in expect.
var assertionNames = map[string]bool{
	"contains": true, "not_contains": true, "equals": true, "matches": true,
	"count": true, "in_order": true, "valid_utf8": true,
}

// checkAssertion applies one assertion to text and describes any failure.
func checkAssertion(name string, want any, text string) error {
	switch name {
	case "contains":
		for _, s := range stringList(want) {
			if !strings.Contains(text, s) {
				return fmt.Errorf("expected to contain %q", s)
			}
		}
	case "not_contains":
		for _, s := range stringList(want) {
			if strings.Contains(text, s) {
				return fmt.Errorf("expected not to contain %q", s)
			}
		}
	case "equals":
		s := fmt.Sprint(want)
		if text != s {
			return fmt.Errorf("expected to equal %q\n  got %q", s, truncate(text, 2000))
		}
	case "matches":
		for _, s := range stringList(want) {
			re, err := regexp.Compile(s)
			if err != nil {
				return fmt.Errorf("invalid regex %q: %v", s, err)
			}
			if !re.MatchString(text) {
				return fmt.Errorf("expected to match /%s/", s)
			}
		}
	case "count":
		m, ok := want.(map[string]any)
		if !ok {
			return fmt.Errorf("count must be a mapping of text to number")
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n, err := toInt(m[k])
			if err != nil {
				return fmt.Errorf("count[%q]: %v", k, err)
			}
			if got := strings.Count(text, k); got != n {
				return fmt.Errorf("expected %q %d times, found %d", k, n, got)
			}
		}
	case "in_order":
		pos := 0
		for _, s := range stringList(want) {
			i := strings.Index(text[pos:], s)
			if i < 0 {
				if strings.Contains(text, s) {
					return fmt.Errorf("expected %q after the previous strings, but it only occurs before", s)
				}
				return fmt.Errorf("expected to contain %q", s)
			}
			pos += i + len(s)
		}
	case "valid_utf8":
		if b, _ := want.(bool); b && !utf8.ValidString(text) {
			return fmt.Errorf("expected valid UTF-8")
		}
	default:
		return fmt.Errorf("unknown assertion %q", name)
	}
	return nil
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case nil:
		return nil
	default:
		return []string{fmt.Sprint(x)}
	}
}

func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		return int(x), nil
	default:
		return 0, fmt.Errorf("expected a number, got %v", v)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// checkExpect applies an `expect` block (exit plus prefixed assertions) to
// a finished command.
func checkExpect(expect map[string]any, c *command) error {
	wantExit := 0
	if v, ok := expect["exit"]; ok {
		n, err := toInt(v)
		if err != nil {
			return fmt.Errorf("expect.exit: %v", err)
		}
		wantExit = n
	}
	if c.code != wantExit {
		return fmt.Errorf("expected exit %d, got %d", wantExit, c.code)
	}
	keys := make([]string, 0, len(expect))
	for k := range expect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "exit" {
			continue
		}
		stream, name, ok := strings.Cut(k, "_")
		if !ok || (stream != "stdout" && stream != "stderr" && stream != "output") || !assertionNames[name] {
			return fmt.Errorf("unknown expectation %q", k)
		}
		if stream == "output" {
			stream = "any"
		}
		if err := checkAssertion(name, expect[k], c.out.get(stream)); err != nil {
			return fmt.Errorf("%s: %v", k, err)
		}
	}
	return nil
}
