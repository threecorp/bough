// Package registry is the on-disk owner of `.bough-ports.json` (v0.4+)
// or `.worktree-ports.json` (v0.3 legacy) — the per-monorepo source of
// truth that maps each worktree branch to its deterministic per-engine,
// per-role port allocation.
//
// The file format is `{<worktree-name>: {<kind>.<role>: <port>}}` from
// v0.4.0 onward (e.g. `"mysql.main": 42345`, `"rabbitmq.amqp": 60123`,
// `"rabbitmq.management": 60556`). A v0.3 entry written as
// `{F-Auth: {mysql: 42345}}` (no dot, single-port DB) is auto-upgraded
// to `{F-Auth: {"mysql.main": 42345}}` on Load, so a production monorepo
// migrating from v0.3 sees zero port drift on its existing engines.
//
// All writes go through atomicWriteJSON (tempfile + os.Rename) so a
// concurrent `bough create` and `bough remove` can never observe a
// partial JSON document, and the CLI mutates through Store.Update so two
// processes cannot both read the same snapshot and drop each other's
// reservation on save. Every write is preceded by a copy of the
// previous file to ${backupDir}/<basename>-pre-<action>-<ts>[-<n>].json so
// human mistakes (an accidental port collision the schema doesn't
// catch, a corrupted file from a SIGKILL'd hook) can be recovered
// without reconstructing port history by hand.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/fsutil"
)

// Registry maps `worktree name → kind → port`. The outer map is keyed by
// branch / feature name (e.g. "F-Auth"), the inner map by port kind
// ("db" / "api" / "gateway" / "view" / ...). Storing the table as a plain
// map of maps mirrors the JSON shape on disk and keeps Load/Save trivial.
type Registry map[string]map[string]int

// Store wraps the I/O concerns (file path, backup directory, time source)
// so they can be set once at startup and injected wherever a registry
// mutation happens. Callers should construct Store via NewStore.
//
// `now` is injected so tests can pin the backup-file timestamp without
// patching time.Now globally — see registry_test.go.
type Store struct {
	Path      string
	BackupDir string
	Now       func() time.Time
}

// NewStore returns a Store with the wall-clock as the time source. Tests
// should construct Store directly so they can swap Now for a fixture.
func NewStore(path, backupDir string) *Store {
	return &Store{Path: path, BackupDir: backupDir, Now: time.Now}
}

// Load reads the registry from disk. An absent file is not an error —
// the first invocation of `bough create` ever runs against an empty
// registry and the file materialises on the first Save. Keys without
// a "." (v0.3 single-port DB entries like `mysql: 42345`) are
// auto-upgraded to `<kind>.main`.
func (s *Store) Load() (Registry, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Registry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry: read %s: %w", s.Path, err)
	}
	if len(raw) == 0 {
		return Registry{}, nil
	}
	var r Registry
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", s.Path, err)
	}
	if r == nil {
		r = Registry{}
	}
	upgradeLegacyKeys(r)
	return r, nil
}

// upgradeLegacyKeys appends ".main" to any kind key that does not
// already contain a "." — v0.3 single-port DBs were written as
// `{F-Auth: {mysql: 42345}}` and v0.4 expects
// `{F-Auth: {mysql.main: 42345}}`. Removed in v0.5.0 alongside the
// other legacy fallbacks.
func upgradeLegacyKeys(r Registry) {
	for name, entry := range r {
		upgraded := false
		for k := range entry {
			if !strings.Contains(k, ".") {
				upgraded = true
				break
			}
		}
		if !upgraded {
			continue
		}
		newEntry := make(map[string]int, len(entry))
		for k, v := range entry {
			if strings.Contains(k, ".") {
				newEntry[k] = v
			} else {
				newEntry[k+".main"] = v
			}
		}
		r[name] = newEntry
	}
}

// LegacyPath returns the v0.3 default registry path so the host CLI
// can fall back when the v0.4 `.bough-ports.json` is absent. Removed
// in v0.5.0.
const LegacyPath = ".worktree-ports.json"

// CanonicalPath is the v0.4 default registry filename.
const CanonicalPath = ".bough-ports.json"

// Save writes the registry back to disk after copying the previous file
// to ${BackupDir}/<basename>-pre-<action>-<ts>[-<n>].json. The action label
// (e.g. "allocate" / "cleanup") shows up in the backup filename so
// reviewers can tell what mutation produced each backup.
//
// The temp file is created in the same directory as the destination so
// os.Rename is a same-filesystem move (atomic on POSIX). On error the
// temp file is removed best-effort to avoid littering.
func (s *Store) Save(reg Registry, action string) error {
	if _, err := os.Stat(s.Path); err == nil {
		if backupErr := s.backup(action); backupErr != nil {
			return fmt.Errorf("registry: backup: %w", backupErr)
		}
	}
	if err := s.atomicWriteJSON(reg); err != nil {
		return fmt.Errorf("registry: atomic write %s: %w", s.Path, err)
	}
	return nil
}

// Update serializes read-modify-write across processes. The sibling lock
// file stays in place because Save replaces the registry inode atomically.
func (s *Store) Update(action string, mutate func(Registry) (bool, error)) error {
	release, err := fsutil.Lock(s.Path + ".lock")
	if err != nil {
		return fmt.Errorf("registry: lock: %w", err)
	}
	defer release() // also on errors below
	reg, err := s.Load()
	if err != nil {
		return err
	}
	changed, err := mutate(reg)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.Save(reg, action)
}

func (s *Store) backup(action string) error {
	if s.BackupDir == "" {
		return nil
	}
	dir := fsutil.ExpandHome(s.BackupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(s.Path)
	// Drop a leading "." so the backup name reads as
	// "worktree-ports-pre-allocate-…" rather than ".worktree-ports-pre-…".
	base = strings.TrimPrefix(base, ".")
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	ts := s.Now().UTC().Format("20060102-150405")
	// Serialized creates save several times a second, and monorepos may
	// share one backup dir, so the name is claimed with O_EXCL and a taken
	// one moves to the next numbered suffix instead of being replaced.
	for n := 1; ; n++ {
		dst := filepath.Join(dir, fmt.Sprintf("%s-pre-%s-%s.json", stem, action, ts))
		if n > 1 {
			dst = filepath.Join(dir, fmt.Sprintf("%s-pre-%s-%s-%d.json", stem, action, ts, n))
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		return s.copyInto(out, dst)
	}
}

// copyInto writes the current registry into out, a backup file backup
// just created, and removes it again on failure.
func (s *Store) copyInto(out *os.File, dst string) error {
	in, err := os.Open(s.Path)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	defer func() { _ = in.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

func (s *Store) atomicWriteJSON(reg Registry) error {
	dir := filepath.Dir(s.Path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(s.Path)+".")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(reg); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}

	// Preserve mode from the existing file (e.g. 0o600 if the user
	// previously tightened it); fall back to 0o644 on first write.
	var mode os.FileMode = 0o644
	if st, err := os.Stat(s.Path); err == nil {
		mode = st.Mode()
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Get returns the previously allocated port for (name, kind), or (0, false)
// when the entry is absent. Used by the allocator wrapper to short-circuit
// re-allocation when a `bough remove`/`bough create` cycle should land on
// the same port.
func Get(reg Registry, name, kind string) (int, bool) {
	if entry, ok := reg[name]; ok {
		if p, present := entry[kind]; present {
			return p, true
		}
	}
	return 0, false
}

// Set inserts or updates the (name, kind) → port mapping in place.
// Callers mutate the snapshot supplied by Store.Update so allocation
// and persistence share the same cross-process lock.
func Set(reg Registry, name, kind string, port int) {
	if _, ok := reg[name]; !ok {
		reg[name] = map[string]int{}
	}
	reg[name][kind] = port
}

// Delete removes the entire entry for `name` and returns the dropped
// inner map plus a boolean that mirrors the standard map semantics.
// Whole-entry deletion is correct — `bough remove` tears down every kind
// (db / api / gateway / …) that belonged to the worktree.
func Delete(reg Registry, name string) (map[string]int, bool) {
	entry, ok := reg[name]
	if ok {
		delete(reg, name)
	}
	return entry, ok
}

// Taken returns every port any entry holds, whatever its kind. This is
// the input the allocator expects in its `taken` parameter: kinds may
// declare overlapping ranges, and the allocator starts each kind at the
// same hash offset of the worktree name, so a per-kind set handed two
// kinds sharing a range the same port.
func Taken(reg Registry) map[int]bool {
	out := map[int]bool{}
	for _, entry := range reg {
		for _, p := range entry {
			out[p] = true
		}
	}
	return out
}
