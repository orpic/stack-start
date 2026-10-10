package tail

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFollow_NewContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.log")

	require.NoError(t, os.WriteFile(path, []byte("existing content\n"), 0644))

	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		time.Sleep(200 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
		_, _ = f.WriteString("new line 1\n")
		_, _ = f.WriteString("new line 2\n")
		_ = f.Close()
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	err := Follow(ctx, path, &buf)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "new line 1")
	require.Contains(t, buf.String(), "new line 2")
	require.NotContains(t, buf.String(), "existing content")
}

func TestFollow_FileNotFound(t *testing.T) {
	err := Follow(context.Background(), "/nonexistent/file.log", &bytes.Buffer{})
	require.Error(t, err)
}

func TestLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	require.NoError(t, os.WriteFile(path, []byte("1\n2\n3\n4\n5\n"), 0644))

	got, err := LastLines(path, 2)
	require.NoError(t, err)
	require.Equal(t, "4\n5\n", string(got))

	got, err = LastLines(path, 10)
	require.NoError(t, err)
	require.Equal(t, "1\n2\n3\n4\n5\n", string(got))

	got, err = LastLines(path, 0)
	require.NoError(t, err)
	require.Equal(t, "1\n2\n3\n4\n5\n", string(got))
}

func TestLastLines_NoTrailingNewlineAndLongFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	var b bytes.Buffer
	for i := 0; i < 20000; i++ {
		b.WriteString("a fairly long log line to push past one read block\n")
	}
	b.WriteString("final partial")
	require.NoError(t, os.WriteFile(path, b.Bytes(), 0644))

	got, err := LastLines(path, 2)
	require.NoError(t, err)
	require.Equal(t, "a fairly long log line to push past one read block\nfinal partial", string(got))
}

func TestFollowFrom_ShowsRecentHistoryThenNewLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	require.NoError(t, os.WriteFile(path, []byte("old 1\nold 2\nold 3\n"), 0644))

	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = FollowFrom(ctx, path, &buf, 2)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	_, _ = f.WriteString("new 1\n")
	_ = f.Close()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	require.Equal(t, "old 2\nold 3\nnew 1\n", buf.String())
}
