package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// jsonLookup finds a value in a JSON document by a simple path:
// dot-separated keys, each optionally followed by [N] (an index) or
// [key=value] (the first array element whose key equals value), e.g.
// sessions[0].processes[name=db].pid. The result is returned as compact
// JSON text, except strings, which are returned without quotes.
func jsonLookup(doc any, path string) (string, error) {
	cur := doc
	for _, seg := range splitPath(path) {
		name, filters := seg, ""
		if i := strings.IndexByte(seg, '['); i >= 0 {
			name, filters = seg[:i], seg[i:]
		}
		if name != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return "", fmt.Errorf("%q: not an object", name)
			}
			v, ok := m[name]
			if !ok {
				return "", fmt.Errorf("no key %q", name)
			}
			cur = v
		}
		for filters != "" {
			end := strings.IndexByte(filters, ']')
			if !strings.HasPrefix(filters, "[") || end < 0 {
				return "", fmt.Errorf("bad filter in %q", seg)
			}
			f := filters[1:end]
			filters = filters[end+1:]
			arr, ok := cur.([]any)
			if !ok {
				return "", fmt.Errorf("[%s]: not an array", f)
			}
			if n, err := strconv.Atoi(f); err == nil {
				if n < 0 || n >= len(arr) {
					return "", fmt.Errorf("index %d out of range (%d elements)", n, len(arr))
				}
				cur = arr[n]
				continue
			}
			k, want, ok := strings.Cut(f, "=")
			if !ok {
				return "", fmt.Errorf("bad filter [%s]", f)
			}
			found := false
			for _, el := range arr {
				if m, ok := el.(map[string]any); ok && scalar(m[k]) == want {
					cur, found = el, true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("no element with %s=%s", k, want)
			}
		}
	}
	return scalar(cur), nil
}

func splitPath(p string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range p {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case '.':
			if depth == 0 {
				out = append(out, p[start:i])
				start = i + 1
			}
		}
	}
	return append(out, p[start:])
}

func scalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	data, _ := json.Marshal(v)
	return string(data)
}

// checkJSON applies {path: want} pairs to a JSON text. With negate, every
// value must differ from want.
func checkJSON(text string, want map[string]any, negate bool) error {
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return fmt.Errorf("output is not JSON: %v", err)
	}
	for path, w := range want {
		got, err := jsonLookup(doc, path)
		if err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		ws := fmt.Sprint(w)
		if !negate && got != ws {
			return fmt.Errorf("%s = %s, expected %s", path, got, ws)
		}
		if negate && got == ws {
			return fmt.Errorf("%s = %s, expected anything else", path, got)
		}
	}
	return nil
}
