// Package events is a run's append-only event log: every state change,
// readiness, capture, screen clear, restart and exit, numbered so a client
// can ask for "everything after seq N" and wait for more.
package events

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Event is one entry. Fields beyond the common ones are flattened into the
// JSON object, so each type documents its own keys (docs/phase1-design.md).
type Event struct {
	Seq     int64          `json:"seq"`
	TS      time.Time      `json:"ts"`
	Type    string         `json:"type"`
	Process string         `json:"process,omitempty"`
	Fields  map[string]any `json:"-"`
}

// MarshalJSON flattens Fields next to the common keys.
func (e Event) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(e.Fields)+4)
	for k, v := range e.Fields {
		m[k] = v
	}
	m["seq"] = e.Seq
	m["ts"] = e.TS.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	m["type"] = e.Type
	if e.Process != "" {
		m["process"] = e.Process
	}
	return json.Marshal(m)
}

// UnmarshalJSON is the inverse of MarshalJSON.
func (e *Event) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	e.Fields = map[string]any{}
	for k, v := range m {
		switch k {
		case "seq":
			if f, ok := v.(float64); ok {
				e.Seq = int64(f)
			}
		case "ts":
			if s, ok := v.(string); ok {
				e.TS, _ = time.Parse(time.RFC3339Nano, s)
			}
		case "type":
			e.Type, _ = v.(string)
		case "process":
			e.Process, _ = v.(string)
		default:
			e.Fields[k] = v
		}
	}
	return nil
}

// Log keeps every event of a run in memory and appends it to a file.
type Log struct {
	mu      sync.Mutex
	events  []Event
	file    *os.File
	w       *bufio.Writer
	changed chan struct{} // closed and replaced on every Emit
	closed  bool
}

// Open creates the log at path (0600: events can carry captured secrets).
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{file: f, w: bufio.NewWriter(f), changed: make(chan struct{})}
	go l.flusher()
	return l, nil
}

// flusher writes buffered events to disk every 100 ms.
func (l *Log) flusher() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return
		}
		_ = l.w.Flush()
		l.mu.Unlock()
	}
}

// Emit appends an event and wakes every waiter.
func (l *Log) Emit(typ, process string, fields map[string]any) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Event{Seq: int64(len(l.events)) + 1, TS: time.Now(), Type: typ, Process: process, Fields: fields}
	l.events = append(l.events, e)
	if !l.closed {
		if data, err := json.Marshal(e); err == nil {
			_, _ = l.w.Write(append(data, '\n'))
		}
	}
	close(l.changed)
	l.changed = make(chan struct{})
	return e
}

// After returns the events with seq > after, and a channel that is closed
// when another event arrives.
func (l *Log) After(after int64) ([]Event, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if after < 0 {
		after = 0
	}
	var out []Event
	if after < int64(len(l.events)) {
		out = append(out, l.events[after:]...)
	}
	return out, l.changed
}

// LastSeq is the seq of the newest event (0 if none).
func (l *Log) LastSeq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(len(l.events))
}

// Close flushes and closes the file. Events can still be read from memory.
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	_ = l.w.Flush()
	_ = l.file.Close()
}

// ReadFile reads a finished run's events.jsonl.
func ReadFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
