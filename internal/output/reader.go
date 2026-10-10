package output

import (
	"bytes"
	"io"
	"time"
)

const (
	// DefaultMaxLine is the longest line passed on in one piece. Longer lines
	// are split into chunks; reading never stops because of line length.
	DefaultMaxLine = 64 * 1024
	// DefaultIdleFlush is how long an unfinished line may sit before it is
	// flushed as a Partial line. Prompts such as "iex(1)> " never end in a
	// newline, and must still be visible to readiness checks and in logs.
	DefaultIdleFlush = 250 * time.Millisecond
)

// Reader splits a process's output stream into Lines.
type Reader struct {
	// Raw, if set, receives every byte exactly as read, before any processing.
	Raw io.Writer
	// MaxLine overrides DefaultMaxLine.
	MaxLine int
	// IdleFlush overrides DefaultIdleFlush.
	IdleFlush time.Duration
}

// Run reads src until EOF or a read error and calls emit for every line, in
// order, from a single goroutine. A pending unfinished line is flushed at the
// end. Read errors are treated as end of stream: on Linux a PTY master
// returns EIO once the child side closes.
func (r Reader) Run(src io.Reader, emit func(Line)) {
	maxLine := r.MaxLine
	if maxLine <= 0 {
		maxLine = DefaultMaxLine
	}
	idle := r.IdleFlush
	if idle <= 0 {
		idle = DefaultIdleFlush
	}

	chunks := make(chan []byte, 16)
	go func() {
		defer close(chunks)
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				c := make([]byte, n)
				copy(c, buf[:n])
				if r.Raw != nil {
					_, _ = r.Raw.Write(c)
				}
				chunks <- c
			}
			if err != nil {
				return
			}
		}
	}()

	var pending []byte
	// Since Go 1.23, Stop and Reset never leave a stale value in timer.C,
	// so no drain is needed (a drain would block forever).
	timer := time.NewTimer(idle)
	timer.Stop()
	defer timer.Stop()

	flushPartial := func() {
		if len(pending) == 0 {
			return
		}
		l := Sanitize(pending)
		l.Partial = true
		pending = pending[:0]
		emit(l)
	}

	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				flushPartial()
				return
			}
			pending = append(pending, c...)
			for {
				idx := bytes.IndexByte(pending, '\n')
				if idx < 0 {
					break
				}
				emit(Sanitize(pending[:idx]))
				pending = pending[idx+1:]
			}
			for len(pending) >= maxLine {
				l := Sanitize(pending[:maxLine])
				l.Partial = true
				emit(l)
				pending = pending[maxLine:]
			}
			// Copy the remainder so the backing array does not grow forever.
			pending = append([]byte(nil), pending...)
			timer.Stop()
			if len(pending) > 0 {
				timer.Reset(idle)
			}
		case <-timer.C:
			flushPartial()
		}
	}
}
