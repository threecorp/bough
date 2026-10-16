package fsutil

import (
	"path/filepath"
	"testing"
	"time"
)

// TestLock_SecondHolderWaits pins the flock semantics every caller relies
// on: a second open of the same lock file blocks until the first holder
// releases, even inside one process, and the parent dir is created.
func TestLock_SecondHolderWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "a.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	acquired := make(chan func(), 1)
	go func() {
		second, err := Lock(path)
		if err != nil {
			t.Errorf("second Lock: %v", err)
			return
		}
		acquired <- second
	}()
	select {
	case <-acquired:
		t.Fatal("second Lock returned while the first holder still had it")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case second := <-acquired:
		second()
	case <-time.After(10 * time.Second):
		t.Fatal("second Lock never returned after the first holder released")
	}
}
