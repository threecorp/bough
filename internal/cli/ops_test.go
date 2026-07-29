package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/telemetry"
)

// opsHarness puts the test in an identifiable project with an isolated
// homunculus and returns the telemetry path the summary will read.
func opsHarness(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".bough.yaml"),
		[]byte("schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOUGH_HOMUNCULUS_DIR", t.TempDir())
	t.Chdir(proj)
	ident, err := homunculus.DetectIdentity(proj)
	if err != nil {
		t.Fatalf("detect identity: %v", err)
	}
	return homunculus.NewLayout().TelemetryFile(ident.ID)
}

func summary(t *testing.T, now time.Time) string {
	t.Helper()
	var buf bytes.Buffer
	renderUsageSummary(&buf, now)
	return buf.String()
}

// TestOpsIsReadOnlyByDefault is the property that makes the routine
// safe to run whenever an operator wonders about the loop: checking on
// a system must not change it. Without --generate nothing is written,
// and the run says so.
func TestOpsIsReadOnlyByDefault(t *testing.T) {
	cmd := newOpsCmd()
	f := cmd.Flags().Lookup("generate")
	if f == nil {
		t.Fatal("ops must expose --generate")
	}
	if f.DefValue != "false" {
		t.Errorf("--generate must default to false, got %q", f.DefValue)
	}
	if !strings.Contains(cmd.Long, "READ-ONLY") {
		t.Error("the long help must state that the default run writes nothing")
	}
}

// TestSummaryOnAnEmptyLogSaysSoRatherThanReportingZeros: a project that
// has never recorded anything is the normal starting state, and it must
// not read as "the loop is broken".
func TestSummaryOnAnEmptyLogSaysSoRatherThanReportingZeros(t *testing.T) {
	opsHarness(t)
	out := summary(t, time.Now())
	if !strings.Contains(out, "nothing recorded yet") {
		t.Errorf("an empty log should say so explicitly:\n%s", out)
	}
	if !strings.Contains(out, "expected state") {
		t.Errorf("and should say that is expected, not a fault:\n%s", out)
	}
}

// TestSummaryCountsWhatTheHostDid pins the three numbers the routine
// exists to surface.
func TestSummaryCountsWhatTheHostDid(t *testing.T) {
	path := opsHarness(t)
	now := time.Now()
	w := telemetry.NewWriter(path)
	for _, e := range []telemetry.Event{
		{TS: now.Add(-24 * time.Hour), Kind: telemetry.KindSkillPull, Slug: "alpha"},
		{TS: now.Add(-2 * time.Hour), Kind: telemetry.KindSkillPull, Slug: "alpha"},
		{TS: now.Add(-3 * time.Hour), Kind: telemetry.KindSkillPull, Slug: "beta"},
		{TS: now.Add(-4 * time.Hour), Kind: telemetry.KindSelection, IDs: []string{"i1", "i2"}},
		{TS: now.Add(-5 * time.Hour), Kind: telemetry.KindSelection, IDs: []string{"i2", "i3"}},
		{TS: now.Add(-6 * time.Hour), Kind: telemetry.KindJudge, Counts: map[string]int{"reviewed": 4, "unreviewed": 0}},
	} {
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	out := summary(t, now)
	for _, want := range []string{
		"3 pull(s) across 2 skill(s)", // pulls
		"alpha×2",                     // named, most-pulled first
		"3 distinct instinct(s)",      // retrieval breadth: i1,i2,i3
		"promoted without being judged: 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

// TestUnjudgedPromotionsAreMarkedNotBuried: the gate fails open by
// design, so this number is the one an operator must not scroll past.
func TestUnjudgedPromotionsAreMarkedNotBuried(t *testing.T) {
	path := opsHarness(t)
	now := time.Now()
	w := telemetry.NewWriter(path)
	if err := w.Append(telemetry.Event{
		TS: now.Add(-time.Hour), Kind: telemetry.KindJudge,
		Counts: map[string]int{"reviewed": 2, "unreviewed": 5},
	}); err != nil {
		t.Fatal(err)
	}
	out := summary(t, now)
	if !strings.Contains(out, "✗ promoted without being judged: 5") {
		t.Errorf("unjudged promotions must carry a failure marker:\n%s", out)
	}
	if !strings.Contains(out, "unscreened") {
		t.Errorf("and must say what that means:\n%s", out)
	}
}

// TestDriftIsPrintedBeforeTheNumbersItInvalidates is the ordering the
// addendum's lesson turns on: every count below a parse failure is a
// zero that means "I could not tell", so putting them first is how a
// broken parser gets read as a finding.
func TestDriftIsPrintedBeforeTheNumbersItInvalidates(t *testing.T) {
	path := opsHarness(t)
	now := time.Now()
	w := telemetry.NewWriter(path)
	if err := w.Append(telemetry.Event{
		TS: now.Add(-time.Hour), Kind: telemetry.KindSkillPull,
		Raw: json.RawMessage(`{"skillName":"moved"}`),
	}); err != nil {
		t.Fatal(err)
	}
	out := summary(t, now)
	drift := strings.Index(out, "SCHEMA DRIFT")
	counts := strings.Index(out, "skills pulled:")
	if drift < 0 {
		t.Fatalf("drift must be reported:\n%s", out)
	}
	if counts < 0 || drift > counts {
		t.Errorf("drift must come BEFORE the counts it invalidates (drift=%d counts=%d):\n%s", drift, counts, out)
	}
	if !strings.Contains(out, "looks exactly like a count of zero") {
		t.Errorf("the row must say why it matters:\n%s", out)
	}
}

// TestSummaryStatesWhatItDoesNotEstablish: a routine that prints "OK"
// and means "the four things I measure are fine" trains the reader to
// hear "everything is fine".
func TestSummaryStatesWhatItDoesNotEstablish(t *testing.T) {
	path := opsHarness(t)
	now := time.Now()
	if err := telemetry.NewWriter(path).Append(telemetry.Event{
		TS: now.Add(-time.Hour), Kind: telemetry.KindSkillPull, Slug: "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	out := summary(t, now)
	if !strings.Contains(out, "does NOT establish") {
		t.Errorf("the summary must state its own limits:\n%s", out)
	}
}

// TestTopSlugsSaysWhenItTruncates: a bounded list rendered as the whole
// set is the silent-cap failure this project's rules forbid.
func TestTopSlugsSaysWhenItTruncates(t *testing.T) {
	got := topSlugs(map[string]int{"a": 5, "b": 4, "c": 3, "d": 2, "e": 1})
	if !strings.Contains(got, "showing 3 of 5") {
		t.Errorf("a truncated list must say so, got %q", got)
	}
	if short := topSlugs(map[string]int{"a": 1}); strings.Contains(short, "showing") {
		t.Errorf("a complete list must not claim truncation, got %q", short)
	}
}
