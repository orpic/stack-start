package capture

import "sync"

// Registry stores captured values keyed by (process_name, capture_name).
type Registry struct {
	mu     sync.RWMutex
	values map[string]map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		values: make(map[string]map[string]string),
	}
}

// Store records a captured value.
func (r *Registry) Store(process, name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values[process] == nil {
		r.values[process] = make(map[string]string)
	}
	r.values[process][name] = value
}

// Get retrieves a captured value. Returns empty string and false if not found.
func (r *Registry) Get(process, name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	caps, ok := r.values[process]
	if !ok {
		return "", false
	}
	val, ok := caps[name]
	return val, ok
}

// Snapshot returns a copy of all captured values for use in interpolation.
func (r *Registry) Snapshot() map[string]map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]map[string]string, len(r.values))
	for proc, caps := range r.values {
		capsCopy := make(map[string]string, len(caps))
		for k, v := range caps {
			capsCopy[k] = v
		}
		result[proc] = capsCopy
	}
	return result
}
