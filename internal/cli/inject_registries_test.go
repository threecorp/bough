package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
)

func TestManualExclusionsAcceptsBothForms(t *testing.T) {
	dir := t.TempDir()

	jsonPath := filepath.Join(dir, "exclusions.json")
	if err := os.WriteFile(jsonPath,
		[]byte(`{"excluded":{"heard-enough":{"reason":"in CLAUDE.md now"},"also-this":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := manualExclusions(jsonPath)
	if len(got) != 2 {
		t.Errorf("json form = %v, want 2 ids", got)
	}
	if _, ok := got["heard-enough"]; !ok {
		t.Errorf("json form lost an id: %v", got)
	}

	// The plain form exists because the register starts as a scratch list;
	// demanding the structured shape up front means it never gets written.
	txtPath := filepath.Join(dir, "exclusions.txt")
	if err := os.WriteFile(txtPath, []byte("# these are in the rules file now\nfirst-id\n\nsecond-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = manualExclusions(txtPath)
	if len(got) != 2 {
		t.Errorf("plain form = %v, want 2 ids (comments and blanks skipped)", got)
	}
}

// Every failure direction must mean "exclude nothing", never "exclude
// something we guessed at".
func TestManualExclusionsFailuresExcludeNothing(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"unconfigured": "",
		"missing":      filepath.Join(t.TempDir(), "absent.json"),
		"malformed":    bad,
	} {
		if got := manualExclusions(path); got != nil {
			t.Errorf("%s returned %v, want nil", name, got)
		}
	}
}

func TestAliasExpansionsMatchOnlyWhatThePromptSays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alias.json")
	if err := os.WriteFile(path,
		[]byte(`{"_comment":["not a term"],"予約":["booking","reservation"],"移行":["migration"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := aliasExpansions(path, "予約のバグを直したい")
	want := []string{"booking", "reservation"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("aliasExpansions = %v, want %v", got, want)
	}
	if got := aliasExpansions(path, "何も一致しない"); got != nil {
		t.Errorf("a prompt naming no term returned %v, want nil", got)
	}
	if got := aliasExpansions("", "予約"); got != nil {
		t.Errorf("an unconfigured alias file returned %v, want nil", got)
	}
}

// writeBoughYAML gives the fixture repo a config that names the two
// optional selector inputs.
func writeBoughYAML(t *testing.T, repo, exclusions, alias string) {
	t.Helper()
	yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: app\nregistry:\n  path: .bough-ports.json\ninstinct:\n  enabled: true\n  select:\n"
	if exclusions != "" {
		yaml += "    exclusions_path: " + exclusions + "\n"
	}
	if alias != "" {
		yaml += "    alias_path: " + alias + "\n"
	}
	if err := os.WriteFile(filepath.Join(repo, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInjectContext_ManualExclusionIsHonored is the end-to-end proof: a
// field wired only halfway is worse than absent, so the assertion is that
// the operator's register actually stops the push.
func TestInjectContext_ManualExclusionIsHonored(t *testing.T) {
	repo := injectFixture(t, "0.9")
	writeBoughYAML(t, repo, "quiet-ids.txt", "")
	if err := os.WriteFile(filepath.Join(repo, "quiet-ids.txt"), []byte("minted-note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if strings.Contains(buf.String(), "Do the minted thing") {
		t.Errorf("an excluded instinct was still pushed:\n%s", buf.String())
	}

	// Without the register the same instinct must still arrive, or this
	// test would pass for the wrong reason.
	writeBoughYAML(t, repo, "", "")
	buf.Reset()
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if !strings.Contains(buf.String(), "Do the minted thing") {
		t.Errorf("the instinct is missing even unexcluded:\n%s", buf.String())
	}
}

// TestInjectContext_AliasReachesAnEnglishCorpus pins the only path a
// non-English prompt has into an English corpus. BM25 is blind across
// languages, so without the alias the same prompt retrieves nothing —
// which is asserted here too, since that contrast IS the evidence.
func TestInjectContext_AliasReachesAnEnglishCorpus(t *testing.T) {
	repo := injectFixture(t, "0.9")
	const prompt = "予約まわりを直したい"

	writeBoughYAML(t, repo, "", "")
	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{Prompt: prompt}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if strings.Contains(buf.String(), "Do the minted thing") {
		t.Fatalf("precondition: the corpus should be unreachable without an alias:\n%s", buf.String())
	}

	aliasPath := filepath.Join(repo, "alias.json")
	if err := os.WriteFile(aliasPath, []byte(`{"予約":["minted"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBoughYAML(t, repo, "", "alias.json")
	buf.Reset()
	if err := runInjectContext(&buf, repo, inject.Options{Prompt: prompt}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if !strings.Contains(buf.String(), "Do the minted thing") {
		t.Errorf("the alias did not reach the lexical channel:\n%s", buf.String())
	}
}

// TestInjectContext_ArrivalBacklogAnnouncesAndClears covers the notice in
// both directions. A notice that cannot be cleared is a notice that gets
// ignored, which is the whole reason it counts arrivals since the last
// pass rather than unclustered rows.
func TestInjectContext_ArrivalBacklogAnnouncesAndClears(t *testing.T) {
	repo := injectFixture(t, "0.9")
	ident, err := homunculus.DetectIdentity(repo)
	if err != nil {
		t.Skipf("identity resolution needs a git repo: %v", err)
	}
	layout := homunculus.NewLayout()
	arrived := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	for i := 0; i < evolve.DefaultArrivalBacklog().Threshold; i++ {
		id := fmt.Sprintf("arrival-%02d", i)
		body := fmt.Sprintf("---\nid: %s\ntrigger: when the %d-th thing happens\nconfidence: 0.85\nscope: project\nfirst_seen: \"%s\"\nlast_seen: \"%s\"\n---\n\n## Action\nDo thing %d.\n",
			id, i, arrived.Format(time.RFC3339), arrived.Format(time.RFC3339), i)
		if err := os.WriteFile(filepath.Join(layout.InstinctsDir(ident.ID), id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if !strings.Contains(buf.String(), "since the last clustering pass") {
		t.Errorf("a corpus past the backlog threshold said nothing:\n%s", buf.String())
	}

	// A clustering pass stamps the corpus; every arrival predates it, so
	// the notice must go quiet.
	stamp := &evolve.ClusterAssignments{ByInstinct: map[string]int{"arrival-00": 0, "arrival-01": 0}}
	if err := stamp.Save(layout.ClusterAssignmentsFile(ident.ID), arrived.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if strings.Contains(buf.String(), "since the last clustering pass") {
		t.Errorf("the notice survived the pass that answers it:\n%s", buf.String())
	}
}
