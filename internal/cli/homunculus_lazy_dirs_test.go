package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// projectDir is the per-project subtree root the resolvers name.
func projectDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ident, err := homunculus.DetectIdentity(resolveMonorepoRoot(cwd))
	if err != nil {
		t.Fatalf("detect identity: %v", err)
	}
	return filepath.Dir(homunculus.NewLayout().ObservationsFile(ident.ID))
}

// Naming a path must not mint a project. The hook resolves the
// observations path on every invocation — including the worktree verbs,
// which record nothing — so a resolver that created directories left a
// permanent seven-directory subtree behind for every cwd bough was ever
// run from, none of them in projects.json and none ever pruned.
func TestResolvingAPathCreatesNoProjectSubtree(t *testing.T) {
	pullHarness(t)
	dir := projectDir(t)

	if got := resolveHomunculusObsPath(); got == "" {
		t.Fatal("resolveHomunculusObsPath returned empty — the project no longer resolves")
	}
	if got := resolveHomunculusTelemetryPath(); got == "" {
		t.Fatal("resolveHomunculusTelemetryPath returned empty — the project no longer resolves")
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("resolving a path created %s (%v) — it must stay absent until something is recorded", dir, names)
	}
}

// The other direction: an actual record must still materialise the
// subtree, so the fix above cannot pass by disabling recording.
func TestRecordingStillCreatesTheProjectSubtree(t *testing.T) {
	pullHarness(t)
	dir := projectDir(t)

	dispatchSkillPull(&cobra.Command{}, []byte(realSkillPullPayload))

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a recorded pull left no %s: %v", dir, err)
	}
}
