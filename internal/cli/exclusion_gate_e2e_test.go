package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/telemetry"

	"github.com/spf13/cobra"
)

// TestExclusionGateOpensFromRealPullEvents drives the gate end to end
// through the writer the HOOK actually uses. The existing ON-side test
// seeds telemetry by composing Event literals, which cannot catch a
// writer/reader shape mismatch — if dispatchSkillPull started writing a
// field the reader no longer parses, that test would stay green while
// every real deployment stayed WAIT forever. Here the recent pulls are
// recorded from real PostToolUse payloads (the shape measured against a
// live 2.1.220 session), resolved through the same cwd-to-identity walk
// the hook performs; only the history anchor — an event that must be 14+
// days old, which no live writer can produce inside a test — is aged
// explicitly.
func TestExclusionGateOpensFromRealPullEvents(t *testing.T) {
	monoRoot := t.TempDir()
	corpus := t.TempDir()
	t.Setenv(homunculus.DefaultDirEnv, corpus)
	t.Chdir(monoRoot)

	// The hook resolves project identity from its cwd; use the same
	// derivation so the test reads the file the writer wrote, rather
	// than assuming they agree.
	ident, err := homunculus.DetectIdentity(monoRoot)
	if err != nil {
		t.Fatal(err)
	}
	layout := homunculus.NewLayout()
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		t.Fatal(err)
	}

	// Coverage registry + the skills on disk in both places the gate
	// checks (the evolved dir it stats, the .claude dir the host loads).
	cov := &evolve.SkillCoverage{BySkill: map[string][]string{}}
	cov.Record("search-conventions", []string{"reindex-after-schema"})
	cov.Record("a", []string{"instinct-a"})
	cov.Record("b", []string{"instinct-b"})
	if err := cov.Save(layout.SkillCoverageFile(ident.ID), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"search-conventions", "a", "b"} {
		for _, d := range []string{
			filepath.Join(monoRoot, ".claude", "skills", slug),
			filepath.Join(layout.EvolvedSkillsDir(ident.ID), slug),
		} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\n\nbody\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// History anchor, aged 20d — the one thing the live writer cannot
	// produce here by construction.
	telemetryPath := layout.TelemetryFile(ident.ID)
	if err := telemetry.NewWriter(telemetryPath).Append(telemetry.Event{
		TS: time.Now().Add(-20 * 24 * time.Hour), Kind: telemetry.KindSkillPull, Slug: "search-conventions",
	}); err != nil {
		t.Fatal(err)
	}

	// The recent pulls: real payloads through the real dispatch — parser,
	// success gate, slug resolution, writer, path resolution all live.
	for _, slug := range []string{"search-conventions", "a", "b"} {
		payload := fmt.Sprintf(
			`{"tool_name":"Skill","session_id":"e2e","tool_input":{"skill":%q},"tool_response":{"success":true,"commandName":%q}}`,
			slug, slug)
		dispatchSkillPull(&cobra.Command{}, []byte(payload))
	}

	ready := evolve.ExclusionReadiness(
		layout.EvolvedSkillsDir(ident.ID), telemetryPath, layout.SkillCoverageFile(ident.ID),
		time.Now(), evolve.DefaultExclusionWindow())
	if !ready.Ready() {
		for _, c := range ready.Checks {
			t.Logf("  %s passed=%v blocking=%v — %s", c.Name, c.Passed, c.Blocking, c.Detail)
		}
		t.Fatal("gate must open from pulls recorded by the real hook writer")
	}
}
