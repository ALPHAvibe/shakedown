package logs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LastLines(path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "c\nd" {
		t.Fatalf("got %q", got)
	}
}

func TestFollowPathAppendsNewLines(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	dest := filepath.Join(dir, "copy.log")
	if err := os.WriteFile(src, []byte("old-1\nold-2\nold-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- FollowPath(ctx, src, dest, 2)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(dest)
		if strings.Contains(string(body), "old-2") && strings.Contains(string(body), "old-3") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new-line\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	deadline = time.Now().Add(2 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		body, _ = os.ReadFile(dest)
		if strings.Contains(string(body), "new-line") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not stop")
	}
	text := string(body)
	if strings.Contains(text, "old-1") {
		t.Fatalf("preamble kept more than the tail:\n%s", text)
	}
	if !strings.Contains(text, "old-3") || !strings.Contains(text, "new-line") {
		t.Fatalf("captured:\n%s", text)
	}
}
