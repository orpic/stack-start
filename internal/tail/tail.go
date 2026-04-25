package tail

import (
	"context"
	"io"
	"os"
	"time"
)

// Follow tails a file, writing new content to w until ctx is cancelled.
func Follow(ctx context.Context, path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// Seek to end to only show new content
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	buf := make([]byte, 4096)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if err != nil && err != io.EOF {
				_ = f.Close()
				f, err = os.Open(path)
				if err != nil {
					return err
				}
			}
		}
	}
}
