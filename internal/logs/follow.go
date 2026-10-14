package logs

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ALPHAvibe/shakedown/internal/runner"
)

const maxTailBytes = 16 * 1024

// LastLines returns the last n lines of path, reading at most maxBytes from the end.
func LastLines(path string, n, maxBytes int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	if maxBytes <= 0 {
		maxBytes = maxTailBytes
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	start := int64(0)
	skippedPartial := false
	if size > int64(maxBytes) {
		start = size - int64(maxBytes)
		skippedPartial = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	buf, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)))
	if err != nil {
		return "", err
	}
	text := string(buf)
	if skippedPartial {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n"), nil
}

// WriteTail appends the last lines of src to dest. A missing source is ignored
// so a stream can be tailed before the file exists.
func WriteTail(src, dest string, lines int) error {
	text, err := LastLines(src, lines, maxTailBytes)
	if err != nil || text == "" {
		return err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, text+"\n")
	return err
}

// FollowPath copies a file into dest. Existing content contributes only its
// last preambleLines. Bytes appended after that are copied until ctx is cancelled.
func FollowPath(ctx context.Context, src, dest string, preambleLines int) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if preambleLines > 0 {
		if err := WriteTail(src, dest, preambleLines); err != nil {
			return err
		}
	}
	var offset int64
	var id fileID
	if info, err := os.Stat(src); err == nil {
		offset = info.Size()
		id = identify(info)
	}
	drain := func() error {
		n, next, err := copyNew(src, out, offset, id)
		offset, id = n, next
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = drain()
			return nil
		case <-ticker.C:
			if err := drain(); err != nil {
				return err
			}
		}
	}
}

// FollowCommand runs command on the host and appends its output to dest until
// it exits or ctx is cancelled.
func FollowCommand(ctx context.Context, dir, command string, env []string, dest string) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	err = runner.StreamShell(ctx, dir, command, env, out)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func copyNew(src string, out *os.File, offset int64, prev fileID) (int64, fileID, error) {
	f, err := os.Open(src)
	if err != nil {
		return offset, prev, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return offset, prev, err
	}
	id := identify(info)
	if prev.valid && id != prev {
		offset = 0
	}
	if info.Size() < offset {
		offset = 0
	}
	if info.Size() == offset {
		return offset, id, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, id, err
	}
	n, err := io.Copy(out, f)
	if err != nil {
		return offset, id, err
	}
	return offset + n, id, nil
}
