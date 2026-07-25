package instinctgate

import (
	"os"
	"path/filepath"
	"testing"
)

// governanceFixture writes a small rule document and returns the loaded
// Governance. The text is deliberately generic project policy so the
// tests read as governance rather than as this repository's own rules.
func governanceFixture(t *testing.T) *Governance {
	t.Helper()
	dir := t.TempDir()
	doc := filepath.Join(dir, "RULES.md")
	body := "# Project rules\n\n" +
		"Every change must be reviewed by a second engineer before merge.\n" +
		"Database migrations are applied through the migration job, never by hand.\n"
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadGovernance([]string{doc})
}

// TestGovernanceAbsentIsInert pins the posture with no rule documents: a
// grounding layer with nothing to ground against would reject every
// citation, which is indistinguishable from a broken guard.
func TestGovernanceAbsentIsInert(t *testing.T) {
	g := LoadGovernance([]string{filepath.Join(t.TempDir(), "missing.md")})
	if g.Active() {
		t.Error("no readable documents should leave the layer inert")
	}
	if !g.Grounded("policy: every deploy needs three approvals from the board") {
		t.Error("with no governance loaded, nothing can be ungrounded")
	}
}

// TestGovernanceLoadsDirectory pins that a rules DIRECTORY is read, not
// just a single file — projects keep governance split across several
// documents, and grounding against only one of them would flag the rest
// as invented.
func TestGovernanceLoadsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("Releases are cut on Thursdays only.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("Secrets are stored in the vault, never in the repo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("Deploy whenever you like.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := LoadGovernance([]string{dir})
	if len(g.Sources) != 2 {
		t.Errorf("Sources = %v, want the two .md files only", g.Sources)
	}
	if !g.Grounded("the rule is that secrets are stored in the vault") {
		t.Error("a citation from the second document must ground")
	}
}

// TestClaimsRuleScoping pins WHICH instincts are in scope. A note
// recording an ordinary practice asserts no governance and has nothing
// to be grounded against; grounding it would hold honest notes for
// failing to cite a rule they never claimed.
func TestClaimsRuleScoping(t *testing.T) {
	claims := []string{
		"the rule is that every change needs a second reviewer",
		"per the project policy, migrations run through the job",
		"you must never apply migrations by hand",
		"direct pushes are prohibited on the release branch",
	}
	for _, s := range claims {
		if !ClaimsRule(s) {
			t.Errorf("should be in scope for grounding: %q", s)
		}
	}
	practices := []string{
		"re-run the flaky suite with a fixed seed and inspect the diff",
		"read the enclosing function before editing a single line",
		"prefix grep with command to bypass shell aliases",
	}
	for _, s := range practices {
		if ClaimsRule(s) {
			t.Errorf("a practice note must not be in scope: %q", s)
		}
	}
}

// TestGroundingAcceptsRealCitationRejectsInvention is the core claim,
// with probes phrased differently from the source text: a genuine
// citation survives light rewording at its edges, while a confident
// invention — the shape an LLM actually produces — is held.
func TestGroundingAcceptsRealCitationRejectsInvention(t *testing.T) {
	g := governanceFixture(t)

	grounded := []string{
		// Verbatim run, wrapped in the instinct's own words.
		"the rule is that every change must be reviewed by a second engineer",
		// Same run, different surrounding phrasing.
		"policy: migrations are applied through the migration job, so never do it by hand",
	}
	for _, s := range grounded {
		if !g.Grounded(s) {
			t.Errorf("a real citation must ground: %q", s)
		}
	}

	invented := []string{
		"the rule is that every pull request needs three approvals from the platform team",
		"per the policy, deploys are frozen during the last week of each quarter",
	}
	for _, s := range invented {
		if g.Grounded(s) {
			t.Errorf("an invented rule must NOT ground: %q", s)
		}
	}
}

// TestGroundingIgnoresFormatting pins that markdown noise does not
// decide the verdict: the same citation in back-ticks, bold, or with
// trailing punctuation must ground identically.
func TestGroundingIgnoresFormatting(t *testing.T) {
	g := governanceFixture(t)
	for _, s := range []string{
		"the rule: **every change must be reviewed by** a second engineer.",
		"the rule is `every change must be reviewed by a second engineer`",
	} {
		if !g.Grounded(s) {
			t.Errorf("formatting must not change the verdict: %q", s)
		}
	}
}

// TestGroundingShortClaimIsNotPunished pins that a claim too short to
// contain a full run is treated as grounded — holding it would punish
// brevity rather than invention, which is not what this layer is for.
func TestGroundingShortClaimIsNotPunished(t *testing.T) {
	if !governanceFixture(t).Grounded("policy: review first") {
		t.Error("a claim shorter than the run length must not be held")
	}
}

// TestUngroundedClaimHeldInGate pins the wiring end-to-end, including
// the scoping: an invented rule is held, a real citation clears, and a
// practice note clears without being grounded at all.
func TestUngroundedClaimHeldInGate(t *testing.T) {
	g := New(Config{Enabled: true, Governance: governanceFixture(t)})

	res := g.Screen([]Candidate{
		cand("invented", "before merging", "the rule is that every pull request needs three approvals from the platform team"),
		cand("cited", "before merging", "the rule is that every change must be reviewed by a second engineer"),
		cand("practice", "when tests are flaky", "re-run the suite with a fixed seed"),
	})
	if len(res.Held) != 1 || res.Held[0].ID != "invented" || res.Held[0].Rule != "ungrounded-rule-claim" {
		t.Fatalf("held = %+v, want only the invented rule", res.Held)
	}
	if len(res.Cleared) != 2 {
		t.Errorf("cleared = %d, want the real citation and the practice note", len(res.Cleared))
	}
}
