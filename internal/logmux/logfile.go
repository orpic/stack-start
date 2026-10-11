package logmux

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogFile is a process's plain log plus an index with the time each line
// was written ("<unix ms> <byte offset>" per line, in <log>.idx), so logs
// can be read from a point in time. It is shared by every start of the
// process within a run.
type LogFile struct {
	mu  sync.Mutex
	f   *os.File
	idx *bufio.Writer
	ix  *os.File
	off int64
}

// OpenLogFile opens (appending) a log and its index.
func OpenLogFile(path string) (*LogFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	ix, err := os.OpenFile(path+".idx", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	st, _ := f.Stat()
	return &LogFile{f: f, ix: ix, idx: bufio.NewWriter(ix), off: st.Size()}, nil
}

// Write appends one line (the caller writes whole lines).
func (l *LogFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.idx, "%d %d\n", time.Now().UnixMilli(), l.off)
	_ = l.idx.Flush()
	n, err := l.f.Write(p)
	l.off += int64(n)
	return n, err
}

// Close closes the log and its index.
func (l *LogFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.idx.Flush()
	_ = l.ix.Close()
	return l.f.Close()
}

// OffsetAt returns the byte offset of the first line written at or after t,
// using the log's index; -1 if no line is that recent.
func OffsetAt(logPath string, t time.Time) (int64, error) {
	f, err := os.Open(logPath + ".idx")
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	want := t.UnixMilli()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ms, off, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		m, err1 := strconv.ParseInt(ms, 10, 64)
		o, err2 := strconv.ParseInt(off, 10, 64)
		if err1 == nil && err2 == nil && m >= want {
			return o, nil
		}
	}
	return -1, sc.Err()
}
