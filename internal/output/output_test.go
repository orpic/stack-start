package output

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		plain   string
		console string
		cleared bool
	}{
		{"plain text", "hello", "hello", "hello", false},
		{"CRLF from a PTY", "listening on 4000\r", "listening on 4000", "listening on 4000", false},
		{"colors kept on console, stripped from plain", "\x1b[32mok\x1b[0m", "ok", "\x1b[32mok\x1b[0m", false},
		{"color without reset gets a reset", "\x1b[31merror", "error", "\x1b[31merror\x1b[0m", false},
		{"tsc/vite/nodemon clear screen", "\x1b[2J\x1b[3J\x1b[Hbuild 2 ok", "build 2 ok", "build 2 ok", true},
		{"node --watch clear", "\x1bc\x1b[3JRestarting 'app.js'", "Restarting 'app.js'", "Restarting 'app.js'", true},
		{"ESC[J (erase below) is not a clear", "\x1b[Jtext", "text", "text", false},
		{"spinner keeps last frame", "| 10%\r/ 50%\r- 100% done", "- 100% done", "- 100% done", false},
		{"erase-line after CR", "50%\r\x1b[Kdone", "done", "done", false},
		{"cursor movement dropped (docker compose redraw)", "\x1b[2A\x1b[0G[+] Running 3/3", "[+] Running 3/3", "[+] Running 3/3", false},
		{"OSC window title dropped (BEL)", "\x1b]0;my title\x07visible", "visible", "visible", false},
		{"OSC 8 hyperlink keeps the text (ST)", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link", "link", false},
		{"charset designation dropped", "\x1b(Bbox", "box", "box", false},
		{"backspace and bell dropped", "a\bb\x07c", "abc", "abc", false},
		{"tabs kept", "a\tb", "a\tb", "a\tb", false},
		{"unicode kept (vite, cloudflared banners)", "  ➜  Local:   http://localhost:5173/ │", "  ➜  Local:   http://localhost:5173/ │", "  ➜  Local:   http://localhost:5173/ │", false},
		{"unterminated CSI does not leak", "text\x1b[12", "text", "text", false},
		{"iex prompt", "iex(1)> ", "iex(1)> ", "iex(1)> ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Sanitize([]byte(tt.raw))
			require.Equal(t, tt.plain, l.Plain)
			require.Equal(t, tt.console, string(l.Console))
			require.Equal(t, tt.cleared, l.Cleared)
		})
	}
}

func TestSanitize_InvalidUTF8(t *testing.T) {
	l := Sanitize([]byte("ok \xff\xfe bytes"))
	require.Equal(t, "ok � bytes", l.Plain)
}

func TestSanitize_ClearThenCR(t *testing.T) {
	// A clear before a carriage return still counts as a clear.
	l := Sanitize([]byte("\x1b[2Jold\rnew"))
	require.True(t, l.Cleared)
	require.Equal(t, "new", l.Plain)
}

// collect runs a Reader over src and returns the lines it emitted.
func collect(t *testing.T, r Reader, src io.Reader) []Line {
	t.Helper()
	var lines []Line
	done := make(chan struct{})
	go func() {
		r.Run(src, func(l Line) { lines = append(lines, l) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Reader.Run did not return")
	}
	return lines
}

func plains(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Plain
	}
	return out
}

func TestReader_SplitsLines(t *testing.T) {
	lines := collect(t, Reader{}, strings.NewReader("one\r\ntwo\nthree"))
	require.Equal(t, []string{"one", "two", "three"}, plains(lines))
	require.False(t, lines[0].Partial)
	require.True(t, lines[2].Partial, "a final line without newline is flushed as partial")
}

func TestReader_VeryLongLineDoesNotStopReading(t *testing.T) {
	long := strings.Repeat("x", 300_000)
	src := strings.NewReader("before\n" + long + "\nafter\n")
	lines := collect(t, Reader{MaxLine: 64 * 1024}, src)

	var total int
	for _, l := range lines[1 : len(lines)-1] {
		total += len(l.Plain)
	}
	require.Equal(t, "before", lines[0].Plain)
	require.Equal(t, 300_000, total, "every byte of the long line is delivered")
	require.Equal(t, "after", lines[len(lines)-1].Plain, "lines after a long line still arrive")
	require.True(t, lines[1].Partial)
}

func TestReader_RawTeeGetsExactBytes(t *testing.T) {
	var raw bytes.Buffer
	in := "\x1b[2Jcolor \x1b[32mgreen\x1b[0m\r\nnext\n"
	collect(t, Reader{Raw: &raw}, strings.NewReader(in))
	require.Equal(t, in, raw.String())
}

// slowWriter feeds a Reader through a pipe so tests control timing.
type slowWriter struct {
	pr *io.PipeReader
	pw *io.PipeWriter
}

func newPipe() slowWriter {
	pr, pw := io.Pipe()
	return slowWriter{pr, pw}
}

func TestReader_PromptWithoutNewlineIsFlushedWhenIdle(t *testing.T) {
	p := newPipe()
	var mu sync.Mutex
	var lines []Line
	done := make(chan struct{})
	go func() {
		Reader{IdleFlush: 50 * time.Millisecond}.Run(p.pr, func(l Line) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		})
		close(done)
	}()

	_, _ = p.pw.Write([]byte("Interactive Elixir (1.17.3)\r\niex(1)> "))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) == 2
	}, 2*time.Second, 10*time.Millisecond, "the prompt must be emitted without waiting for a newline")

	mu.Lock()
	require.Equal(t, "iex(1)> ", lines[1].Plain)
	require.True(t, lines[1].Partial)
	mu.Unlock()

	_ = p.pw.Close()
	<-done
}

func TestReader_LineWrittenInPiecesIsNotSplit(t *testing.T) {
	// Many loggers write a line in several writes; pieces that arrive within
	// the idle window must be joined.
	p := newPipe()
	var lines []Line
	done := make(chan struct{})
	go func() {
		Reader{IdleFlush: 200 * time.Millisecond}.Run(p.pr, func(l Line) { lines = append(lines, l) })
		close(done)
	}()
	for _, piece := range []string{"[info] ", "Running ", "Endpoint", "\n"} {
		_, _ = p.pw.Write([]byte(piece))
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.pw.Close()
	<-done
	require.Equal(t, []string{"[info] Running Endpoint"}, plains(lines))
}

func TestReader_BurstOfLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50_000; i++ {
		b.WriteString("webpack compiled module number something\n")
	}
	lines := collect(t, Reader{}, strings.NewReader(b.String()))
	require.Len(t, lines, 50_000)
}
