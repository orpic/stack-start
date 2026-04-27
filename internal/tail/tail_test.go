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
