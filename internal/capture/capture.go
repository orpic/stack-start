package capture

import (
	"regexp"
	"sync/atomic"
)

// Capture represents a named regex extraction from a process's log stream.
type Capture struct {
	Name     string
	Pattern  *regexp.Regexp
	Required bool
	captured atomic.Pointer[string]
}

// New creates a Capture from the config fields. The regex must have exactly one
// capturing group (validated upstream).
func New(name, pattern string, required bool) (*Capture, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &Capture{
		Name:     name,
		Pattern:  re,
		Required: required,
	}, nil
}

// Match evaluates the line against the capture regex. Returns true and the
// captured value on first match. Subsequent calls after a match are no-ops.
func (c *Capture) Match(line []byte) (bool, string) {
	if c.captured.Load() != nil {
		return false, ""
	}
	sub := c.Pattern.FindSubmatch(line)
	if len(sub) < 2 {
		return false, ""
	}
	val := string(sub[1])
	c.captured.Store(&val)
	return true, val
}

// Value returns the captured value, or empty string if not yet captured.
func (c *Capture) Value() string {
	v := c.captured.Load()
	if v == nil {
		return ""
	}
	return *v
}

// Done returns true if this capture has matched.
func (c *Capture) Done() bool {
	return c.captured.Load() != nil
}
