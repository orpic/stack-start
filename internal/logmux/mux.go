package logmux

import (
	"io"
	"sync"

	"github.com/fatih/color"
)

// Mux fans out log lines from a process to a file writer and the console.
type Mux struct {
	name          string
	padTo         int
	clr           *color.Color
	fileWriter    io.Writer
	consoleWriter io.Writer
	consoleMu     *sync.Mutex
}

// New creates a Mux. The consoleMu must be shared across all Mux instances
// in a session to prevent torn console lines.
func New(name string, padTo int, fileWriter, consoleWriter io.Writer, consoleMu *sync.Mutex) *Mux {
	return &Mux{
		name:          name,
		padTo:         padTo,
		clr:           color.New(ColorFor(name)),
		fileWriter:    fileWriter,
		consoleWriter: consoleWriter,
		consoleMu:     consoleMu,
	}
}

// WriteLine writes one log line to both the file and the console.
func (m *Mux) WriteLine(line []byte) {
	// Raw bytes to file (no prefix, no color)
	m.fileWriter.Write(line)
	m.fileWriter.Write([]byte("\n"))

	// Prefixed + colored to console
	prefix := m.clr.Sprint(FormatPrefix(m.name, m.padTo))
	m.consoleMu.Lock()
	m.consoleWriter.Write([]byte(prefix))
	m.consoleWriter.Write(line)
	m.consoleWriter.Write([]byte("\n"))
	m.consoleMu.Unlock()
}
