package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveMonorepoRoot covers the v0.9.10 ECC-model routing: every
// sub-repo / worktree session must pool into the one monorepo project
// (the dir holding .bough.yaml), so observations land in a single
// homunculus project instead of fragmenting a .bough/ into every cwd.
func TestResolveMonorepoRoot(t *testing.T) {
	// a worktree path resolves to the monorepo parent (before /.worktrees/),
	// verified by the .bough.yaml marker on that parent (v0.9.18).
	mono := t.TempDir()
	if err := os.WriteFile(filepath.Join(mono, ".bough.yaml"), []byte("schema_version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(mono, ".worktrees", "F-feat", "auba-api")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveMonorepoRoot(wt); got != mono {
		t.Errorf("worktree: got %q want %q", got, mono)
	}

	// v0.9.18: a /.worktrees/ segment whose prefix LACKS the marker must not
	// be trusted — it falls through rather than resolving to a bogus root.
	noMarker := t.TempDir()
	stray := filepath.Join(noMarker, ".worktrees", "x", "repo")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveMonorepoRoot(stray); got != stray {
		t.Errorf("unmarked /.worktrees/ prefix: got %q want %q (no bogus split)", got, stray)
	}

	// v0.11: the non-hidden worktrees/ layout resolves the same way, and
	// its fast-path must still jump PAST a worktree sub-repo that happens
	// to carry a stray .bough.yaml (a plain ancestor walk would wrongly
	// stop at that sub-repo).
	monoNew := t.TempDir()
	if err := os.WriteFile(filepath.Join(monoNew, ".bough.yaml"), []byte("schema_version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subNew := filepath.Join(monoNew, "worktrees", "F-feat", "auba-api")
	if err := os.MkdirAll(subNew, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subNew, ".bough.yaml"), []byte("schema_version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveMonorepoRoot(subNew); got != monoNew {
		t.Errorf("worktrees/ layout: got %q want the monorepo root %q (fast-path must skip the sub-repo's stray marker)", got, monoNew)
	}

	// walk up to the nearest ancestor holding the .bough.yaml marker
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte("schema_version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "auba-api", "server")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveMonorepoRoot(sub); got != root {
		t.Errorf("marker walk-up: got %q want %q", got, root)
	}

	// no worktree, no marker → fall back to the original cwd
	bare := t.TempDir()
	if got := resolveMonorepoRoot(bare); got != bare {
		t.Errorf("fallback: got %q want %q", got, bare)
	}
}

// TestExtractSessionID is the regression guard for the wave-4 review
// finding: the SessionEnd hook dispatch always called runSessionEnd
// with a hardcoded "", so eval/scores.jsonl's session_id field was
// dead in normal operation even though Claude Code's real hook
// payload carries it.
func TestExtractSessionID(t *testing.T) {
	if got := extractSessionID([]byte(`{"session_id":"sess-abc123","cwd":"/x"}`)); got != "sess-abc123" {
		t.Errorf("got %q, want %q", got, "sess-abc123")
	}
	if got := extractSessionID([]byte(`{"cwd":"/x"}`)); got != "" {
		t.Errorf("missing field: got %q, want empty", got)
	}
	if got := extractSessionID(nil); got != "" {
		t.Errorf("nil payload: got %q, want empty", got)
	}
	if got := extractSessionID([]byte(`not json`)); got != "" {
		t.Errorf("malformed payload: got %q, want empty", got)
	}
}
