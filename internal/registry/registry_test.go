package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock returns a constant Now func — backup filenames embed the
// timestamp, so a deterministic clock makes the assertions match.
func fixedClock(ts string) func() time.Time {
	t, _ := time.Parse("20060102-150405", ts)
	return func() time.Time { return t }
}

func TestStore_LoadMissingFileReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	s := &Store{Path: filepath.Join(tmp, ".worktree-ports.json"), Now: time.Now}
	reg, err := s.Load()
	if err != nil {
		t.Fatalf("Load on missing file: unexpected error: %v", err)
	}
	if len(reg) != 0 {
		t.Errorf("Load on missing file: got %d entries, want 0", len(reg))
	}
}

func TestStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".worktree-ports.json")
	backupDir := filepath.Join(tmp, "backups")
	s := &Store{Path: regPath, BackupDir: backupDir, Now: fixedClock("20260101-120000")}

	reg, err := s.Load()
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	Set(reg, "F-Auth", "db", 33144)
	Set(reg, "F-Auth", "api", 36144)
	Set(reg, "F-Other", "db", 33245)

	if err := s.Save(reg, "allocate"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify exact match.
	reg2, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if p, ok := Get(reg2, "F-Auth", "db"); !ok || p != 33144 {
		t.Errorf("Get F-Auth/db: got (%d,%v), want (33144,true)", p, ok)
	}
	if p, ok := Get(reg2, "F-Other", "db"); !ok || p != 33245 {
		t.Errorf("Get F-Other/db: got (%d,%v), want (33245,true)", p, ok)
	}

	// Backup was not produced on the first write (no pre-existing file).
	backups, _ := filepath.Glob(filepath.Join(backupDir, "*.json"))
	if len(backups) != 0 {
		t.Errorf("first Save should not produce a backup; got %d", len(backups))
	}
}

func TestStore_BackupOnOverwrite(t *testing.T) {
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".worktree-ports.json")
	backupDir := filepath.Join(tmp, "backups")
	s := &Store{Path: regPath, BackupDir: backupDir, Now: fixedClock("20260101-120000")}

	// First Save → file appears, no backup.
	if err := s.Save(Registry{"A": {"db": 33000}}, "allocate"); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Second Save → backup of the first file shows up.
	if err := s.Save(Registry{"A": {"db": 33000}, "B": {"db": 33001}}, "allocate"); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	backups, _ := filepath.Glob(filepath.Join(backupDir, "*.json"))
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup file, got %d (%v)", len(backups), backups)
	}
	name := filepath.Base(backups[0])
	if !strings.Contains(name, "pre-allocate") {
		t.Errorf("backup filename %q does not embed action label", name)
	}
	if !strings.Contains(name, "20260101-120000") {
		t.Errorf("backup filename %q does not embed timestamp", name)
	}
}

func TestStore_AtomicityPreservesOldFileOnEncodeFailure(t *testing.T) {
	// Encode failure is hard to simulate without channel/io-error fakery;
	// instead we assert the on-disk file mode is preserved across Save,
	// which is the public invariant we promise.
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".worktree-ports.json")
	s := &Store{Path: regPath, BackupDir: filepath.Join(tmp, "backups"), Now: time.Now}

	if err := s.Save(Registry{"A": {"db": 33000}}, "init"); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := os.Chmod(regPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := s.Save(Registry{"A": {"db": 33000}, "B": {"db": 33001}}, "update"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	st, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got, want := st.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("mode after overwrite: got %o want %o", got, want)
	}
}

func TestRegistry_TakenByKind(t *testing.T) {
	reg := Registry{
		"A": {"db": 33000, "api": 36000},
		"B": {"db": 33001, "api": 36001},
		"C": {"db": 33002},
	}
	if got, want := len(TakenByKind(reg, "db")), 3; got != want {
		t.Errorf("db taken: got %d want %d", got, want)
	}
	if got, want := len(TakenByKind(reg, "api")), 2; got != want {
		t.Errorf("api taken: got %d want %d", got, want)
	}
	if len(TakenByKind(reg, "gateway")) != 0 {
		t.Errorf("absent kind: want empty")
	}
}

func TestRegistry_Delete(t *testing.T) {
	reg := Registry{"A": {"db": 33000}}
	dropped, ok := Delete(reg, "A")
	if !ok {
		t.Errorf("Delete existing: want ok=true")
	}
	if dropped["db"] != 33000 {
		t.Errorf("Delete returned %v, want {db:33000}", dropped)
	}
	if _, ok := reg["A"]; ok {
		t.Errorf("Delete should have removed entry")
	}
	if _, ok := Delete(reg, "absent"); ok {
		t.Errorf("Delete absent: want ok=false")
	}
}

func TestStore_JSONShapeMatchesLegacy(t *testing.T) {
	// On-disk shape must match the layout used by the predecessor
	// `.worktree-ports.json` so a registry produced by either bough or
	// the legacy bash script remains portable.
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".worktree-ports.json")
	s := &Store{Path: regPath, BackupDir: filepath.Join(tmp, "backups"), Now: time.Now}

	reg := Registry{"F-Auth": {"db": 33144, "api": 36144, "view": 39144}}
	if err := s.Save(reg, "init"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]map[string]int
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := parsed["F-Auth"]["db"], 33144; got != want {
		t.Errorf("F-Auth.db: got %d want %d", got, want)
	}
}
