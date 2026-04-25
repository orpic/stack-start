package readiness

import "context"

// Check is the interface for all readiness check types.
type Check interface {
	// Satisfied returns true once the check has passed.
	Satisfied() bool
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
