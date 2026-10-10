// Package output turns a child process's raw terminal output into lines that
// are safe to interleave on a shared console and readable in a log file.
package output

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const esc = 0x1b

// Line is one logical line of process output.
type Line struct {
	// Console is the line as it should appear on the shared console: colors
	// (SGR) kept, every cursor, screen and title control removed, and a color
	// reset appended when the line used color so it cannot bleed into the
	// next process's prefix.
	Console []byte
	// Plain is the visible text with every escape sequence and control
	// character removed and invalid UTF-8 replaced. Readiness checks,
	// captures and the plain log all use this.
	Plain string
	// Cleared reports that the line asked the terminal to clear the screen
	// (ESC[2J, ESC[3J, ESC c). On a shared console that would erase every
	// process's output, so callers show a marker instead.
	Cleared bool
	// Partial reports that the line was flushed before its newline arrived:
	// either a prompt waiting for input or a line longer than the chunk limit.
	Partial bool
}

// Sanitize converts one raw line (without its trailing newline) into a Line.
//
// Carriage returns are resolved the way a terminal would: a trailing "\r"
// (from CRLF) is dropped, and for progress output like "10%\r20%\r30%" only
// the text after the last "\r" is kept.
func Sanitize(raw []byte) Line {
	raw = bytes.TrimRight(raw, "\r")

	var console, plain bytes.Buffer
	cleared, usedColor := false, false

	for i := 0; i < len(raw); {
		b := raw[i]
		switch {
		case b == esc:
			n, kind := escapeLen(raw[i:])
			seq := raw[i : i+n]
			switch kind {
			case seqSGR:
				console.Write(seq)
				usedColor = true
			case seqClear:
				cleared = true
			}
			i += n
		case b == '\r':
			// Everything before a bare carriage return is overwritten.
			console.Reset()
			plain.Reset()
			if usedColor {
				// Keep the color state: re-emitting a reset is cheaper than
				// tracking which SGR codes are still active.
				console.WriteString("\x1b[0m")
			}
			i++
		case b == '\t':
			console.WriteByte(b)
			plain.WriteByte(b)
			i++
		case b < 0x20 || b == 0x7f:
			// Backspace, bell and other C0 controls have no place in a log line.
			i++
		default:
			console.WriteByte(b)
			plain.WriteByte(b)
			i++
		}
	}

	out := Line{
		Console: console.Bytes(),
		Plain:   plain.String(),
		Cleared: cleared,
	}
	if !utf8.ValidString(out.Plain) {
		out.Plain = strings.ToValidUTF8(out.Plain, "�")
		out.Console = bytes.ToValidUTF8(out.Console, []byte("�"))
	}
	if usedColor && !bytes.HasSuffix(out.Console, []byte("\x1b[0m")) && !bytes.HasSuffix(out.Console, []byte("\x1b[m")) {
		out.Console = append(out.Console, "\x1b[0m"...)
	}
	return out
}

type seqKind int

const (
	seqOther seqKind = iota
	seqSGR
	seqClear
)

// escapeLen returns the length of the escape sequence at the start of b and
// what kind it is. b[0] must be ESC. Unterminated sequences consume the rest
// of the line so their bytes never leak onto the console.
func escapeLen(b []byte) (int, seqKind) {
	if len(b) < 2 {
		return len(b), seqOther
	}
	switch b[1] {
	case '[': // CSI: ESC [ params intermediates final(0x40-0x7e)
		for j := 2; j < len(b); j++ {
			if b[j] >= 0x40 && b[j] <= 0x7e {
				params := b[2:j]
				switch b[j] {
				case 'm':
					return j + 1, seqSGR
				case 'J':
					if bytes.Equal(params, []byte("2")) || bytes.Equal(params, []byte("3")) {
						return j + 1, seqClear
					}
				}
				return j + 1, seqOther
			}
		}
		return len(b), seqOther
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: end at BEL or ST (ESC \)
		for j := 2; j < len(b); j++ {
			if b[j] == 0x07 {
				return j + 1, seqOther
			}
			if b[j] == esc && j+1 < len(b) && b[j+1] == '\\' {
				return j + 2, seqOther
			}
		}
		return len(b), seqOther
	case 'c': // RIS: full terminal reset, clears the screen
		return 2, seqClear
	case '(', ')', '*', '+', '#', '%': // charset designation and similar: one more byte
		if len(b) >= 3 {
			return 3, seqOther
		}
		return len(b), seqOther
	default: // two-byte escapes: ESC 7, ESC 8, ESC =, ESC >, ESC M, ...
		return 2, seqOther
	}
}
