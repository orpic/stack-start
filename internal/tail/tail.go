package tail

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"
)

// Follow tails a file, writing only new content to w until ctx is cancelled.
func Follow(ctx context.Context, path string, w io.Writer) error {
	return FollowFrom(ctx, path, w, 0)
}

// FollowFrom writes the last n lines of the file, then follows it like
// `tail -n N -f` until ctx is cancelled.
func FollowFrom(ctx context.Context, path string, w io.Writer, n int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	start, err := lastLinesOffset(f, n)
	if err != nil {
		return err
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}

	buf := make([]byte, 32*1024)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Drain everything available before waiting again.
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if n == 0 || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// LastLines returns the last n lines of a file (all of it when n <= 0).
func LastLines(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	start := int64(0)
	if n > 0 {
		if start, err = lastLinesOffset(f, n); err != nil {
			return nil, err
		}
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// lastLinesOffset returns the offset where the last n lines of f start.
// n == 0 means the end of the file.
func lastLinesOffset(f *os.File, n int) (int64, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if n <= 0 || size == 0 {
		return size, nil
	}

	const block = 64 * 1024
	pos := size
	newlines := 0
	buf := make([]byte, block)
	for pos > 0 {
		readSize := int64(block)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		if _, err := f.ReadAt(buf[:readSize], pos); err != nil && err != io.EOF {
			return 0, err
		}
		chunk := buf[:readSize]
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] != '\n' {
				continue
			}
			// The file's final newline ends the last line; it does not start one.
			if pos+int64(i) == size-1 {
				continue
			}
			newlines++
			if newlines == n {
				return pos + int64(i) + 1, nil
			}
		}
		if bytes.IndexByte(chunk, '\n') < 0 && pos == 0 {
			break
		}
	}
	return 0, nil
}
