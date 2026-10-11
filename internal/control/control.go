// Package control is the protocol between a running stack and the CLI (or
// any other client): newline-delimited JSON over a Unix socket, one request
// per connection. See docs/phase1-design.md section 2.
package control

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/orpic/stack-start/internal/events"
)

// Version is the protocol version, sent in every message.
const Version = 1

// Request is one operation.
type Request struct {
	V       int    `json:"v"`
	Op      string `json:"op"`
	Process string `json:"process,omitempty"`
	// Wait is what start/stop/restart wait for: "none", "started", "ready".
	Wait      string `json:"wait,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	// Data is what send writes to the process.
	Data   string `json:"data,omitempty"`
	EchoMS int64  `json:"echo_ms,omitempty"`
	// After, Follow, Types and For are for events and wait.
	After  int64    `json:"after,omitempty"`
	Follow bool     `json:"follow,omitempty"`
	Types  []string `json:"types,omitempty"`
	For    string   `json:"for,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

// Error is a structured failure.
type Error struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Process   string   `json:"process,omitempty"`
	Hint      string   `json:"hint,omitempty"`
	LastLines []string `json:"last_lines,omitempty"`
	LogFile   string   `json:"log_file,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Error codes.
const (
	CodeInvalid      = "invalid_request"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeFailed       = "failed"
	CodeTimeout      = "timeout"
	CodeShuttingDown = "shutting_down"
)

// Response is one line from the server. Streaming operations send Event
// lines first; the last line has Done set.
type Response struct {
	V      int             `json:"v"`
	Event  *events.Event   `json:"event,omitempty"`
	Done   bool            `json:"done,omitempty"`
	OK     bool            `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// SocketPath returns where a session's control socket lives. Unix socket
// paths are limited to about 104 bytes on macOS, and the session directory
// is usually longer, so long paths move to /tmp/stackstart-<uid>/.
func SocketPath(sessionDir, slug string) (string, error) {
	p := filepath.Join(sessionDir, "control.sock")
	if len(p) < 100 {
		return p, nil
	}
	dir := filepath.Join("/tmp", fmt.Sprintf("stackstart-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	// Anyone can create names in /tmp: only use a directory that is ours,
	// private, and not a symlink.
	st, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !ok || int(sys.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s is not a directory owned by you; remove it, or use a shorter state directory (XDG_STATE_HOME)", dir)
	}
	if st.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256([]byte(slug + "\x00" + sessionDir))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock"), nil
}

// ErrUnreachable means no running stack answered on the socket.
var ErrUnreachable = errors.New("the stack's control socket is not reachable")

// Call sends a request and returns the final response. Event lines are
// passed to onEvent as they arrive (it may be nil).
func Call(socket string, req Request, timeout time.Duration, onEvent func(events.Event)) (*Response, error) {
	req.V = Version
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrUnreachable, err)
	}
	defer func() { _ = conn.Close() }()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r Response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("bad response: %w", err)
		}
		if r.Event != nil && onEvent != nil {
			onEvent(*r.Event)
		}
		if r.Done {
			return &r, nil
		}
	}
	if err := sc.Err(); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, &Error{Code: CodeTimeout, Message: fmt.Sprintf("no answer within %s", timeout)}
		}
		return nil, err
	}
	return nil, ErrStackStopped
}

// ErrStackStopped is returned when the stack closed the connection without
// a final response, which happens when the stack itself stops.
var ErrStackStopped = errors.New("the stack stopped before answering")

// Writer sends responses on a server connection.
type Writer struct {
	enc *json.Encoder
}

// NewWriter wraps a connection.
func NewWriter(c net.Conn) *Writer { return &Writer{enc: json.NewEncoder(c)} }

// Event sends one event line.
func (w *Writer) Event(e events.Event) error {
	return w.enc.Encode(Response{V: Version, Event: &e})
}

// OK sends the final success line with an optional result.
func (w *Writer) OK(result any) error {
	r := Response{V: Version, Done: true, OK: true}
	if result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		r.Result = data
	}
	return w.enc.Encode(r)
}

// Fail sends the final error line.
func (w *Writer) Fail(e *Error) error {
	return w.enc.Encode(Response{V: Version, Done: true, Error: e})
}
