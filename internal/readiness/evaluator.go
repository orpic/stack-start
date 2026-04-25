package readiness

// Evaluator combines multiple checks with an AND/OR mode.
type Evaluator struct {
	Mode   string // "any" or "all"
	Checks []Check
}

func NewEvaluator(mode string, checks []Check) *Evaluator {
	if mode == "" {
		mode = "all"
	}
	return &Evaluator{Mode: mode, Checks: checks}
}

// Satisfied returns true when the mode condition is met.
func (e *Evaluator) Satisfied() bool {
	if len(e.Checks) == 0 {
		return true
	}
	if e.Mode == "any" {
		for _, c := range e.Checks {
			if c.Satisfied() {
				return true
			}
		}
		return false
	}
	for _, c := range e.Checks {
		if !c.Satisfied() {
			return false
		}
	}
	return true
}
