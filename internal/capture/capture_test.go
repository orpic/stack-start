package capture

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapture_Match(t *testing.T) {
	c, err := New("url", "(https://[a-z]+\\.example\\.com)", true)
	require.NoError(t, err)

	matched, val := c.Match([]byte("some noise"))
	require.False(t, matched)
	require.Empty(t, val)

	matched, val = c.Match([]byte("URL=https://tunnel.example.com"))
	require.True(t, matched)
	require.Equal(t, "https://tunnel.example.com", val)
	require.True(t, c.Done())
	require.Equal(t, "https://tunnel.example.com", c.Value())
}

func TestCapture_AlreadyCaptured(t *testing.T) {
	c, err := New("url", "(https://.*)", true)
	require.NoError(t, err)

	c.Match([]byte("https://first.com"))
	require.True(t, c.Done())

	matched, _ := c.Match([]byte("https://second.com"))
	require.False(t, matched)
	require.Equal(t, "https://first.com", c.Value())
}

func TestCapture_NoGroups(t *testing.T) {
	c, err := New("url", "(https://.*)", true)
	require.NoError(t, err)

	matched, _ := c.Match([]byte("no match here"))
	require.False(t, matched)
	require.False(t, c.Done())
	require.Empty(t, c.Value())
}

func TestCapture_InvalidRegex(t *testing.T) {
	_, err := New("url", "[invalid", true)
	require.Error(t, err)
}

func TestRegistry_StoreAndGet(t *testing.T) {
	r := NewRegistry()
	r.Store("tunnel", "url", "https://example.com")

	val, ok := r.Get("tunnel", "url")
	require.True(t, ok)
	require.Equal(t, "https://example.com", val)
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()

	_, ok := r.Get("nonexistent", "url")
	require.False(t, ok)

	r.Store("tunnel", "url", "val")
	_, ok = r.Get("tunnel", "missing")
	require.False(t, ok)
}

func TestRegistry_Snapshot(t *testing.T) {
	r := NewRegistry()
	r.Store("a", "x", "1")
	r.Store("b", "y", "2")

	snap := r.Snapshot()
	require.Equal(t, "1", snap["a"]["x"])
	require.Equal(t, "2", snap["b"]["y"])

	r.Store("a", "x", "modified")
	require.Equal(t, "1", snap["a"]["x"])
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.Store("proc", "cap", "value")
		}(i)
		go func(i int) {
			defer wg.Done()
			r.Get("proc", "cap")
		}(i)
	}
	wg.Wait()
}
