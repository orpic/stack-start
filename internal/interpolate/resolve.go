package interpolate

import (
	"fmt"
	"strings"
)

// RefData holds the data available for interpolation.
// Keys are dotted references like "cloudflared.url" or "envrc.DATABASE_URL".
type RefData map[string]string

// Resolve interpolates all ${...} references in the input string using the provided data.
func Resolve(input string, data RefData) (string, error) {
	tokens, err := Tokenize(input)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for _, tok := range tokens {
		if tok.Literal {
			b.WriteString(tok.Value)
			continue
		}
		val, ok := data[tok.Value]
		if !ok {
			return "", fmt.Errorf("unresolved reference ${%s}", tok.Value)
		}
		b.WriteString(val)
	}

	return b.String(), nil
}

// BuildRefData constructs a RefData map from captures and envrc values.
func BuildRefData(captures map[string]map[string]string, envrcEnv map[string]string) RefData {
	data := make(RefData)
	for proc, caps := range captures {
		for name, val := range caps {
			data[proc+"."+name] = val
		}
	}
	for k, v := range envrcEnv {
		data["envrc."+k] = v
	}
	return data
}
