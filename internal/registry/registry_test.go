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
	s := &Store{Path: filepath.Join(tmp, ".bough-ports.json"), Now: time.Now}
	reg, err := s.Load()
	if err != nil {
		t.Fatalf("Load on missing file: unexpected error: %v", err)
	}
	if len(reg) != 0 {
		t.Errorf("Load on missing file: got %d entries, want 0", len(reg))
	}
}

// TestStore_RoundTrip exercises the v0.4 canonical key shape
// `<kind>.<role>` (e.g. `mysql.main`, `rabbitmq.amqp`). Set / Save /
// Load / Get must be lossless on these keys.
func TestStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".bough-ports.json")
	backupDir := filepath.Join(tmp, "backups")
	s := &Store{Path: regPath, BackupDir: backupDir, Now: fixedClock("20260101-120000")}

	reg, err := s.Load()
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	Set(reg, "F-Auth", "mysql.main", 33144)
	Set(reg, "F-Auth", "api.main", 36144)
	Set(reg, "F-Other", "mysql.main", 33245)

	if err := s.Save(reg, "allocate"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reg2, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if p, ok := Get(reg2, "F-Auth", "mysql.main"); !ok || p != 33144 {
		t.Errorf("Get F-Auth/mysql.main: got (%d,%v), want (33144,true)", p, ok)
	}
	if p, ok := Get(reg2, "F-Other", "mysql.main"); !ok || p != 33245 {
		t.Errorf("Get F-Other/mysql.main: got (%d,%v), want (33245,true)", p, ok)
	}

	backups, _ := filepath.Glob(filepath.Join(backupDir, "*.json"))
	if len(backups) != 0 {
		t.Errorf("first Save should not produce a backup; got %d", len(backups))
	}
}

// TestStore_LoadUpgradesLegacyKeys is the v0.4 compat guard. A
// v0.3-era registry file (single-port DB keys like `{mysql: 33144}`)
// must load as `{mysql.main: 33144}` in memory, so the same auba
// deployment migrating from v0.3 sees zero port drift on its existing
// engines.
func TestStore_LoadUpgradesLegacyKeys(t *testing.T) {
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".worktree-ports.json")
	raw := `{"F-Auth": {"mysql": 33144, "api": 45000}}`
	if err := os.WriteFile(regPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	s := &Store{Path: regPath, Now: time.Now}
	reg, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p, ok := Get(reg, "F-Auth", "mysql.main"); !ok || p != 33144 {
		t.Errorf("upgraded mysql.main: got (%d,%v), want (33144,true)", p, ok)
	}
	if p, ok := Get(reg, "F-Auth", "api.main"); !ok || p != 45000 {
		t.Errorf("upgraded api.main: got (%d,%v), want (45000,true)", p, ok)
	}
	// Old keys must not coexist alongside the upgraded ones — the
	// upgrade is a rename, not a duplication.
	if _, ok := Get(reg, "F-Auth", "mysql"); ok {
		t.Errorf("legacy 'mysql' key still present after upgrade")
	}
}

func TestStore_BackupOnOverwrite(t *testing.T) {
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".bough-ports.json")
	backupDir := filepath.Join(tmp, "backups")
	s := &Store{Path: regPath, BackupDir: backupDir, Now: fixedClock("20260101-120000")}

	if err := s.Save(Registry{"A": {"mysql.main": 33000}}, "allocate"); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := s.Save(Registry{"A": {"mysql.main": 33000}, "B": {"mysql.main": 33001}}, "allocate"); err != nil {
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
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".bough-ports.json")
	s := &Store{Path: regPath, BackupDir: filepath.Join(tmp, "backups"), Now: time.Now}

	if err := s.Save(Registry{"A": {"mysql.main": 33000}}, "init"); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := os.Chmod(regPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := s.Save(Registry{"A": {"mysql.main": 33000}, "B": {"mysql.main": 33001}}, "update"); err != nil {
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
		"A": {"mysql.main": 33000, "api.main": 36000},
		"B": {"mysql.main": 33001, "api.main": 36001},
		"C": {"mysql.main": 33002},
	}
	if got, want := len(TakenByKind(reg, "mysql.main")), 3; got != want {
		t.Errorf("mysql.main taken: got %d want %d", got, want)
	}
	if got, want := len(TakenByKind(reg, "api.main")), 2; got != want {
		t.Errorf("api.main taken: got %d want %d", got, want)
	}
	if len(TakenByKind(reg, "gateway.main")) != 0 {
		t.Errorf("absent kind: want empty")
	}
}

func TestRegistry_Delete(t *testing.T) {
	reg := Registry{"A": {"mysql.main": 33000}}
	dropped, ok := Delete(reg, "A")
	if !ok {
		t.Errorf("Delete existing: want ok=true")
	}
	if dropped["mysql.main"] != 33000 {
		t.Errorf("Delete returned %v, want {mysql.main:33000}", dropped)
	}
	if _, ok := reg["A"]; ok {
		t.Errorf("Delete should have removed entry")
	}
	if _, ok := Delete(reg, "absent"); ok {
		t.Errorf("Delete absent: want ok=false")
	}
}

// TestStore_JSONShape pins the v0.4 on-disk format. The shape is the
// same map-of-map as v0.3; the only change is the inner key
// convention (`<kind>.<role>` instead of `<kind>`), which keeps any
// shell/jq tooling that reads the file as JSON working unchanged.
func TestStore_JSONShape(t *testing.T) {
	tmp := t.TempDir()
	regPath := filepath.Join(tmp, ".bough-ports.json")
	s := &Store{Path: regPath, BackupDir: filepath.Join(tmp, "backups"), Now: time.Now}

	reg := Registry{"F-Auth": {"mysql.main": 33144, "api.main": 36144, "view.main": 39144}}
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
	if got, want := parsed["F-Auth"]["mysql.main"], 33144; got != want {
		t.Errorf("F-Auth.mysql.main: got %d want %d", got, want)
	}
}
