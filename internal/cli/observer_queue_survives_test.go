package cli

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// failingClaudeOnPath puts a `claude` that exits non-zero on an
// otherwise empty PATH, so the mint call fails the way a real outage
// fails — through the provider's own exec path, not a stub of it.
func failingClaudeOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\necho 'model overloaded' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func digest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(b)
}

// TestFailedMintLeavesTheObservationQueueIntact is the corpus-hygiene
// guarantee: a pass whose analysis dies must not consume what it was
// analysing. Losing observations on failure is unrecoverable — the
// sessions that produced them are over — so the queue has to outlive
// every error the pass can hit, not just the ones anticipated here.
func TestFailedMintLeavesTheObservationQueueIntact(t *testing.T) {
	root := t.TempDir()
	yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: app\n" +
		"registry:\n  path: .bough-ports.json\n" +
		"instinct:\n  enabled: true\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homunculus.DefaultDirEnv, t.TempDir())

	ident, err := homunculus.DetectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	layout := homunculus.NewLayout()
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		t.Fatal(err)
	}

	obsPath := layout.ObservationsFile(ident.ID)
	var lines string
	for i := range 3 {
		lines += `{"ts":"` + time.Now().UTC().Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano) +
			`","event":"PostToolUse","tool":"Bash"}` + "\n"
	}
	if err := os.WriteFile(obsPath, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	before := digest(t, obsPath)

	failingClaudeOnPath(t)

	cmd := newObserverRunOnceCmd()
	cmd.SetArgs([]string{"--root", root, "--judge=false"})
	cmd.SetOut(&nopWriter{})
	cmd.SetErr(&nopWriter{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a mint call that cannot reach the model must report the failure, not succeed quietly")
	}

	if digest(t, obsPath) != before {
		t.Error("the observation queue changed during a failed pass — the records are unrecoverable once dropped")
	}
	staged, _ := os.ReadDir(layout.StagingDir(ident.ID))
	if len(staged) != 0 {
		t.Errorf("a failed pass staged %d file(s); it reached no verdict, so it has nothing to stage", len(staged))
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
