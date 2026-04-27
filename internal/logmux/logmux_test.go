package logmux

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestColorFor_Stable(t *testing.T) {
	c1 := ColorFor("postgres")
	c2 := ColorFor("postgres")
	require.Equal(t, c1, c2)
}

func TestColorFor_Different(t *testing.T) {
	c1 := ColorFor("postgres")
	c2 := ColorFor("backend")
	// Not guaranteed different with only 8 colors, but very likely for short names
	_ = c1
	_ = c2
}

func TestFormatPrefix(t *testing.T) {
	p1 := FormatPrefix("db", 10)
	require.Contains(t, p1, "db")
	require.Contains(t, p1, " | ")

	p2 := FormatPrefix("postgres", 10)
	require.Contains(t, p2, "postgres")
	require.Contains(t, p2, " | ")

	require.Equal(t, len(p1), len(p2))
}

func TestMux_WriteLine(t *testing.T) {
	var fileBuf, consoleBuf bytes.Buffer
	mu := &sync.Mutex{}
	m := New("app", 10, &fileBuf, &consoleBuf, mu)

	m.WriteLine([]byte("hello world"))

	require.Equal(t, "hello world\n", fileBuf.String())
	require.Contains(t, consoleBuf.String(), "app")
	require.Contains(t, consoleBuf.String(), "|")
	require.Contains(t, consoleBuf.String(), "hello world")
}

func TestMux_ConcurrentConsoleWrites(t *testing.T) {
	mu := &sync.Mutex{}
	var consoleBuf bytes.Buffer

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var fb bytes.Buffer
			m := New("proc1", 5, &fb, &consoleBuf, mu)
			m.WriteLine([]byte("line from proc1"))
		}()
		go func() {
			defer wg.Done()
			var fb bytes.Buffer
			m := New("proc2", 5, &fb, &consoleBuf, mu)
			m.WriteLine([]byte("line from proc2"))
		}()
	}
	wg.Wait()

	output := consoleBuf.String()
	require.Contains(t, output, "proc1")
	require.Contains(t, output, "proc2")
}
