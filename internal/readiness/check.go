package readiness

import "context"

// Check is the interface for all readiness check types.
type Check interface {
	// Satisfied returns true once the check has passed.
	Satisfied() bool
	// String describes the check for error messages, e.g. `tcp 127.0.0.1:5432`.
	String() string
}

// LineEvaluator is implemented by checks that evaluate against log lines.
type LineEvaluator interface {
	Check
	Evaluate(line []byte)
}

// Backgrounder is implemented by checks that run in background goroutines (e.g. TCP).
type Backgrounder interface {
	Check
	Run(ctx context.Context)
}

// FuncCheck is satisfied when its function returns true. It backs checks
// whose state lives elsewhere, such as `capture: <name>`.
type FuncCheck struct {
	Desc string
	Fn   func() bool
}

func (c FuncCheck) Satisfied() bool { return c.Fn() }
func (c FuncCheck) String() string  { return c.Desc }
