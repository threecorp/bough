package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
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
		Counts: map[string]int{"reviewed": 2, telemetry.CountUnjudgedPromoted: 5},
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

// TestStagedUnjudgedIsNotReportedAsPromoted is the regression for a
// false alarm on the loudest row here. Interrupting a run, or running
// out of the judge budget, leaves candidates STAGED — the observer even
// prints "left STAGED, not promoted" — but both were folded into the
// same "unreviewed" number, so this summary announced promotions that
// the very same run had refused to make.
func TestStagedUnjudgedIsNotReportedAsPromoted(t *testing.T) {
	path := opsHarness(t)
	now := time.Now()
	w := telemetry.NewWriter(path)
	if err := w.Append(telemetry.Event{
		TS:     now.Add(-2 * time.Hour),
		Kind:   telemetry.KindJudge,
		Counts: map[string]int{"reviewed": 0, telemetry.CountUnjudgedStaged: 6},
	}); err != nil {
		t.Fatal(err)
	}
	out := summary(t, now)
	if strings.Contains(out, "✗ promoted without being judged: 6") {
		t.Errorf("staged candidates must not be reported as promoted:\n%s", out)
	}
	if !strings.Contains(out, "left staged unjudged: 6") {
		t.Errorf("staged candidates must still be reported, on their own row:\n%s", out)
	}
	if !strings.Contains(out, "NOT in the corpus") {
		t.Errorf("the row must say where they are:\n%s", out)
	}
}

// TestDriftOutsideTheWindowIsNotShown keeps this summary and the gate
// telling one story: both count inside the window, so both must
// self-check inside it. A row about a line no number here reads would
// send an operator to fix something that is not affecting anything.
func TestDriftOutsideTheWindowIsNotShown(t *testing.T) {
	path := opsHarness(t)
	now := time.Now()
	w := telemetry.NewWriter(path)
	for _, e := range []telemetry.Event{
		{TS: now.Add(-2 * time.Hour), Kind: telemetry.KindSkillPull, Slug: "alpha"},
		{TS: now.Add(-100 * 24 * time.Hour), Kind: telemetry.KindSkillPull, Raw: []byte(`{"tool_name":"Skill"}`)},
	} {
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if out := summary(t, now); strings.Contains(out, "SCHEMA DRIFT") {
		t.Errorf("drift 100 days outside the window must not be reported here:\n%s", out)
	}
}

// TestEmptySelectionIsRecordedWithTiming: the hook writes a selection
// event even when it chose nothing — the empty-rate is a selector-health
// signal, and a skipped write made "chose nothing" indistinguishable
// from "never ran".
func TestEmptySelectionIsRecordedWithTiming(t *testing.T) {
	path := opsHarness(t)
	var buf bytes.Buffer
	if err := runInjectContext(&cobra.Command{}, &buf, "", inject.Options{Prompt: "anything at all"}); err != nil {
		t.Fatal(err)
	}
	log, err := telemetry.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var sel []telemetry.Event
	for _, e := range log.Events {
		if e.Kind == telemetry.KindSelection {
			sel = append(sel, e)
		}
	}
	if len(sel) != 1 {
		t.Fatalf("want exactly one selection event on an empty corpus, got %d", len(sel))
	}
	if len(sel[0].IDs) != 0 || sel[0].N != 0 {
		t.Errorf("the empty selection must record n=0, got %+v", sel[0])
	}
	if sel[0].MS <= 0 {
		t.Errorf("the selection must carry its latency, got ms=%v", sel[0].MS)
	}
}

// TestSelfLimitOverrunFailsOpen: past the selection deadline the prompt
// loses the instinct block, never the turn — and the overrun is still a
// recorded (empty) selection so the health numbers see it.
func TestSelfLimitOverrunFailsOpen(t *testing.T) {
	path := opsHarness(t)
	var buf bytes.Buffer
	err := runInjectContext(&cobra.Command{}, &buf, "", inject.Options{Prompt: "anything", SelfLimit: time.Nanosecond})
	if err != nil {
		t.Fatalf("an overrun must not fail the hook: %v", err)
	}
	log, lerr := telemetry.Load(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	stats := telemetry.SelectionStats(log.Events)
	if stats.Volume != 1 || stats.Empty != 1 {
		t.Errorf("the overrun must be recorded as an empty selection, got %+v", stats)
	}
}

// TestUnreviewedQuarantineIsAnnouncedPerPrompt: the gate's own output
// goes nowhere an operator reads, so the prompt context is where a hold
// must surface — and the notice must CLEAR once the batch is marked
// reviewed, or it trains the reader to ignore it.
func TestUnreviewedQuarantineIsAnnouncedPerPrompt(t *testing.T) {
	opsHarness(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ident, err := homunculus.DetectIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	layout := homunculus.NewLayout()
	batch := filepath.Join(layout.QuarantineDir(ident.ID), "20260701-000000")
	if err := os.MkdirAll(batch, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"held-note.md", "REPORT.md"} {
		if err := os.WriteFile(filepath.Join(batch, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	if err := runInjectContext(&cobra.Command{}, &buf, "", inject.Options{Prompt: "anything"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "1 held instinct(s) in 1 unreviewed batch(es)") {
		t.Errorf("the hold must be announced in the prompt context:\n%s", buf.String())
	}

	// Marking the batch reviewed clears the notice.
	if err := os.WriteFile(filepath.Join(batch, "REVIEWED"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := runInjectContext(&cobra.Command{}, &buf, "", inject.Options{Prompt: "anything"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "unreviewed batch") {
		t.Errorf("a REVIEWED batch must not keep announcing:\n%s", buf.String())
	}
}
