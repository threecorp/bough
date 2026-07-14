package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAcquireStartLock_MutualExclusion is the regression test for the
// startObserverDaemon check-then-spawn race: two concurrent callers for
// the same lock path must not both succeed, matching the invariant that
// only one caller may proceed to the check-then-spawn critical section.
func TestAcquireStartLock_MutualExclusion(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "observer.pid.lock")

	release1, ok1 := acquireStartLock(lockPath)
	if !ok1 {
		t.Fatal("first acquire should succeed on an unheld lock")
	}
	defer release1()

	_, ok2 := acquireStartLock(lockPath)
	if ok2 {
		t.Fatal("second concurrent acquire must fail while the first holds the lock")
	}

	release1()
	release3, ok3 := acquireStartLock(lockPath)
	if !ok3 {
		t.Fatal("acquire should succeed again after the holder releases")
	}
	defer release3()
}

// TestAcquireStartLock_ReclaimsStaleLock ensures a lock left behind by a
// crashed holder (one that never reached its deferred release) does not
// permanently wedge future daemon starts: once the lock file is older
// than startLockStaleAfter, a new caller reclaims it.
func TestAcquireStartLock_ReclaimsStaleLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "observer.pid.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}
	stale := time.Now().Add(-2 * startLockStaleAfter)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("backdate lock mtime: %v", err)
	}

	release, ok := acquireStartLock(lockPath)
	if !ok {
		t.Fatal("a lock older than startLockStaleAfter must be reclaimed")
	}
	release()
}

// TestAcquireStartLock_FreshLockNotReclaimed guards the other side of the
// staleness window: a lock acquired moments ago (well within
// startLockStaleAfter, as in the real check-then-spawn critical section)
// must NOT be reclaimable by a concurrent caller, or the mutual-exclusion
// guarantee the race fix depends on would be defeated by an overly eager
// staleness check.
func TestAcquireStartLock_FreshLockNotReclaimed(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "observer.pid.lock")
	release, ok := acquireStartLock(lockPath)
	if !ok {
		t.Fatal("first acquire should succeed on an unheld lock")
	}
	defer release()

	if _, ok := acquireStartLock(lockPath); ok {
		t.Fatal("a fresh (non-stale) lock must not be reclaimed by a concurrent caller")
	}
}
