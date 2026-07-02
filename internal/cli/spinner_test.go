package cli

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

// TestSpinner_InertOnNonTTY is the hook-safety contract: when stderr is
// not a terminal (the WorktreeCreate hook pipes it, CI captures it), the
// spinner must write nothing so the log stays plain and greppable.
func TestSpinner_InertOnNonTTY(t *testing.T) {
	var buf bytes.Buffer
	sp := startSpinner(&buf, "cloning something")
	sp.Stop()
	if buf.Len() != 0 {
		t.Errorf("spinner wrote %q to a non-TTY writer; want inert (empty)", buf.String())
	}
}

// TestIsInteractive_nonTerminalIsFalse covers both the non-*os.File case
// (a buffer in tests) and a real *os.File that is not a terminal (an
// os.Pipe, which mirrors the hook's piped stderr).
func TestIsInteractive_nonTerminalIsFalse(t *testing.T) {
	if isInteractive(&bytes.Buffer{}) {
		t.Errorf("bytes.Buffer reported interactive")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	if isInteractive(w) {
		t.Errorf("os.Pipe write end reported interactive")
	}
}

// TestSpinner_StopOnInertIsSafe guards the hook path: an inert spinner's
// Stop must never touch its nil channels (no panic).
func TestSpinner_StopOnInertIsSafe(t *testing.T) {
	sp := startSpinner(&bytes.Buffer{}, "x")
	sp.Stop()
}

// TestSpinner_AnimatesAndStops exercises the TTY animation path the
// inert-only tests never reach: run() must paint frame 0 (carrying the
// message) immediately, and Stop() must observe the stop signal, unblock,
// and stay panic-free on a second call (the sync.Once guard).
func TestSpinner_AnimatesAndStops(t *testing.T) {
	pr, pw := io.Pipe()
	s := &spinner{w: pw, tty: true, stop: make(chan struct{}), done: make(chan struct{})}

	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := pr.Read(buf) // blocks until run() writes frame 0
		got <- append([]byte(nil), buf[:n]...)
		_, _ = io.Copy(io.Discard, pr) // drain later frames / clear-line so run()/Stop() never block
	}()
	go s.run("booting")

	select {
	case frame := <-got:
		if !bytes.Contains(frame, []byte("booting")) {
			t.Errorf("first spinner frame missing the message: %q", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("spinner never painted a frame")
	}

	// Stop() twice: it must unblock (run() observed stop) and the second
	// call must be a no-op, not a double-close panic.
	stopped := make(chan struct{})
	go func() { s.Stop(); s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("spinner Stop() hung")
	}
	_ = pw.Close()
}
