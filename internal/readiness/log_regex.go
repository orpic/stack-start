package readiness

import (
	"regexp"
	"sync/atomic"
)

// LogRegexCheck matches a compiled regex against log lines.
type LogRegexCheck struct {
	Pattern *regexp.Regexp
	matched atomic.Bool
}

func NewLogRegexCheck(pattern string) (*LogRegexCheck, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &LogRegexCheck{Pattern: re}, nil
}

func (c *LogRegexCheck) Evaluate(line []byte) {
	if c.matched.Load() {
		return
	}
	if c.Pattern.Match(line) {
		c.matched.Store(true)
	}
}

func (c *LogRegexCheck) Satisfied() bool {
	return c.matched.Load()
}
