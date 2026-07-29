package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/telemetry"
)

var refNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// gateFixture builds a project whose portfolio is registered and whose
// telemetry says the pull path has been firing for long enough. Each
// test then removes exactly one condition, so a WAIT can be attributed.
type gateFixture struct {
	skillsDir     string
	telemetryPath string
	coveragePath  string
}

func newGateFixture(t *testing.T) gateFixture {
	t.Helper()
	dir := t.TempDir()
	f := gateFixture{
		skillsDir:     filepath.Join(dir, "skills"),
		telemetryPath: filepath.Join(dir, "telemetry.jsonl"),
		coveragePath:  filepath.Join(dir, "skill-coverage.json"),
	}
	cov := &SkillCoverage{BySkill: map[string][]string{}}
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		cov.Record(slug, []string{"i-" + slug})
		if err := os.MkdirAll(filepath.Join(f.skillsDir, slug), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.skillsDir, slug, "SKILL.md"), []byte("# "+slug+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := cov.Save(f.coveragePath, refNow); err != nil {
		t.Fatal(err)
	}
	return f
}

// pull appends one recorded pull at now-ago.
func (f gateFixture) pull(t *testing.T, slug string, ago time.Duration) {
	t.Helper()
	w := telemetry.NewWriter(f.telemetryPath)
	if err := w.Append(telemetry.Event{TS: refNow.Add(-ago), Kind: telemetry.KindSkillPull, Slug: slug}); err != nil {
		t.Fatal(err)
	}
}

func (f gateFixture) verdict() Readiness {
	return ExclusionReadiness(f.skillsDir, f.telemetryPath, f.coveragePath, refNow, DefaultExclusionWindow())
}

func blockerNames(r Readiness) string {
	var out []string
	for _, b := range r.Blockers() {
		out = append(out, b.Name)
	}
	return strings.Join(out, " | ")
}

func detailOf(r Readiness, name string) string {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Detail
		}
	}
	return ""
}

// healthy seeds the whole happy path: three covered skills pulled, one
// of them recently, with 20 days of history behind it.
func (f gateFixture) healthy(t *testing.T) {
	t.Helper()
	f.pull(t, "alpha", 20*24*time.Hour)
	f.pull(t, "alpha", 3*24*time.Hour)
	f.pull(t, "beta", 5*24*time.Hour)
	f.pull(t, "gamma", 2*24*time.Hour)
}

func TestReadyWhenThePullPathIsDemonstrablyFiring(t *testing.T) {
	f := newGateFixture(t)
	f.healthy(t)
	if r := f.verdict(); !r.Ready() {
		t.Fatalf("expected READY, blocked by: %s", blockerNames(r))
	}
}

// TestDeployedButNeverPulledIsNotReady is the defect this gate was
// rewritten for: the old check counted skills on disk, so a portfolio
// nothing had ever loaded passed. Everything is deployed here and
// registered; only the pulls are missing.
func TestDeployedButNeverPulledIsNotReady(t *testing.T) {
	f := newGateFixture(t)
	r := f.verdict()
	if r.Ready() {
		t.Fatal("a portfolio that has never been pulled must not be READY")
	}
	if !strings.Contains(blockerNames(r), "the pull path is firing") {
		t.Errorf("the blocker should name the pull path, got: %s", blockerNames(r))
	}
}

func TestTooFewDistinctSkillsPulled(t *testing.T) {
	f := newGateFixture(t)
	f.pull(t, "alpha", 20*24*time.Hour)
	f.pull(t, "alpha", 1*24*time.Hour)
	f.pull(t, "beta", 2*24*time.Hour)
	r := f.verdict()
	if r.Ready() {
		t.Fatal("two pulled skills is below the minimum of three")
	}
	if d := detailOf(r, "the pull path is firing"); !strings.Contains(d, "alpha, beta") {
		t.Errorf("the row should name which skills were pulled, got %q", d)
	}
}

// TestOnlyRegisteredSkillsCount: pulling three skills that deliver no
// instincts is not evidence that suppressing instincts is safe.
func TestOnlyRegisteredSkillsCount(t *testing.T) {
	f := newGateFixture(t)
	f.pull(t, "alpha", 20*24*time.Hour)
	for _, unrelated := range []string{"some-other-skill", "another", "third"} {
		f.pull(t, unrelated, 1*24*time.Hour)
	}
	if f.verdict().Ready() {
		t.Fatal("pulls of skills outside the registry must not satisfy the gate")
	}
}

// TestRetiringSkillsNeverSubtracts is the shape guarantee. A snapshot
// diff would read a consolidation as lost usage; counting inside a
// window cannot.
func TestRetiringSkillsNeverSubtracts(t *testing.T) {
	f := newGateFixture(t)
	f.healthy(t)
	before := f.verdict()
	f.pull(t, "alpha", 1*24*time.Hour) // only alpha is still in use
	after := f.verdict()
	if !before.Ready() || !after.Ready() {
		t.Fatalf("consolidation must not flip the verdict: before=%v after=%v (%s)",
			before.Ready(), after.Ready(), blockerNames(after))
	}
}

// TestQuietRecentWindowBlocks: history alone is not enough — a
// portfolio used a month ago and never since is not carrying delivery.
func TestQuietRecentWindowBlocks(t *testing.T) {
	f := newGateFixture(t)
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		f.pull(t, slug, 13*24*time.Hour) // inside history, outside the recent window
	}
	f.pull(t, "alpha", 20*24*time.Hour)
	r := f.verdict()
	if r.Ready() {
		t.Fatal("no pulls in the recent window must block")
	}
	if !strings.Contains(blockerNames(r), "still firing") {
		t.Errorf("the blocker should name the recency check, got: %s", blockerNames(r))
	}
}

// TestShortHistoryBlocksAndStatesTheRealAge: the label must name what
// was measured, not the window that was asked for.
func TestShortHistoryBlocksAndStatesTheRealAge(t *testing.T) {
	f := newGateFixture(t)
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		f.pull(t, slug, 2*24*time.Hour)
	}
	r := f.verdict()
	if r.Ready() {
		t.Fatal("two days of history is not enough to judge by")
	}
	if d := detailOf(r, "enough history to judge by"); !strings.Contains(d, "2.0d") {
		t.Errorf("the row must state the history it actually has, got %q", d)
	}
}

// TestSchemaDriftBlocksInsteadOfReadingAsNoUsage is the heart of the
// addendum's lesson: a reader that cannot parse its writer reports the
// same thing as one that read correctly and found nothing. The pulls
// ARE there, in a shape the reader does not understand, and the gate
// must refuse rather than quietly say the portfolio is unused.
func TestSchemaDriftBlocksInsteadOfReadingAsNoUsage(t *testing.T) {
	f := newGateFixture(t)
	f.healthy(t)
	if !f.verdict().Ready() {
		t.Fatal("precondition: the fixture should be READY before drift is introduced")
	}
	w := telemetry.NewWriter(f.telemetryPath)
	if err := w.Append(telemetry.Event{
		TS: refNow.Add(-time.Hour), Kind: telemetry.KindSkillPull,
		Raw: []byte(`{"skillName":"alpha"}`),
	}); err != nil {
		t.Fatal(err)
	}
	r := f.verdict()
	if r.Ready() {
		t.Fatal("a log the reader cannot parse must not pass the gate")
	}
	d := detailOf(r, "gate self-check")
	if !strings.Contains(d, "SCHEMA DRIFT") || !strings.Contains(d, "skillName") {
		t.Fatalf("the self-check row must be loud and quote the payload, got %q", d)
	}
}

// TestHealthyLogSaysTheNumbersMeanWhatTheySay: the self-check must be
// quiet on a clean run, or it becomes noise nobody reads.
func TestHealthyLogSaysTheNumbersMeanWhatTheySay(t *testing.T) {
	f := newGateFixture(t)
	f.healthy(t)
	d := detailOf(f.verdict(), "gate self-check")
	if strings.Contains(d, "DRIFT") {
		t.Errorf("a clean log must not raise drift, got %q", d)
	}
}

func TestUnreadableCoverageRegistryBlocks(t *testing.T) {
	f := newGateFixture(t)
	if err := os.WriteFile(f.coveragePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.verdict()
	if r.Ready() {
		t.Fatal("an unreadable registry must block")
	}
	if !strings.Contains(blockerNames(r), "coverage registry readable") {
		t.Errorf("blockers = %s", blockerNames(r))
	}
}

func TestEmptyCoverageRegistryBlocks(t *testing.T) {
	f := newGateFixture(t)
	f.healthy(t)
	empty := &SkillCoverage{BySkill: map[string][]string{}}
	if err := empty.Save(f.coveragePath, refNow); err != nil {
		t.Fatal(err)
	}
	if f.verdict().Ready() {
		t.Fatal("an empty registry suppresses nothing or everything — it must block")
	}
}

// TestStaleTelemetrySaysSo: a zero that means "nobody has recorded
// anything lately" calls for a different action than a zero that means
// "the portfolio is not being used", so the row distinguishes them.
func TestStaleTelemetrySaysSo(t *testing.T) {
	f := newGateFixture(t)
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		f.pull(t, slug, 30*24*time.Hour)
	}
	if d := detailOf(f.verdict(), "and still firing"); !strings.Contains(d, "newest event is") {
		t.Errorf("a stale log must say so rather than reporting a bare zero, got %q", d)
	}
}

// TestStaleCoverageIsAdvisoryNotBlocking keeps the drift-vs-portfolio
// check out of the blocking set, and pins that it is not even computed
// until a caller asks — the injector runs this on every prompt.
func TestStaleCoverageIsAdvisoryNotBlocking(t *testing.T) {
	f := newGateFixture(t)
	f.healthy(t)
	if err := os.RemoveAll(filepath.Join(f.skillsDir, "beta")); err != nil {
		t.Fatal(err)
	}
	r := f.verdict()
	if !r.Ready() {
		t.Fatalf("a stale registry entry must not block delivery: %s", blockerNames(r))
	}
	if detailOf(r, "coverage registry matches the skills on disk") != "" {
		t.Fatal("the advisory must not be computed before WithAdvisory()")
	}
	withAdv := r.WithAdvisory()
	d := detailOf(withAdv, "coverage registry matches the skills on disk")
	if d == "" {
		t.Fatal("WithAdvisory() should materialise the drift row")
	}
	if !strings.Contains(d, "beta") {
		t.Errorf("the row should name the missing skill, got %q", d)
	}
	for _, c := range withAdv.Checks {
		if c.Name == "coverage registry matches the skills on disk" && (c.Passed || c.Blocking) {
			t.Errorf("stale entry should be a failing NON-blocking row, got passed=%v blocking=%v", c.Passed, c.Blocking)
		}
	}
}

// TestLongListsSayTheyAreCut: a bounded list that reads as the whole
// set is the silent-cap failure this project's own rules forbid.
func TestLongListsSayTheyAreCut(t *testing.T) {
	dir := t.TempDir()
	f := gateFixture{
		skillsDir:     filepath.Join(dir, "skills"),
		telemetryPath: filepath.Join(dir, "telemetry.jsonl"),
		coveragePath:  filepath.Join(dir, "skill-coverage.json"),
	}
	cov := &SkillCoverage{BySkill: map[string][]string{}}
	for _, slug := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7"} {
		cov.Record(slug, []string{"i-" + slug})
		f.pull(t, slug, 2*24*time.Hour)
	}
	if err := cov.Save(f.coveragePath, refNow); err != nil {
		t.Fatal(err)
	}
	f.pull(t, "s1", 20*24*time.Hour)
	if d := detailOf(f.verdict(), "the pull path is firing"); !strings.Contains(d, "showing 5 of 7") {
		t.Errorf("a truncated list must say it was truncated, got %q", d)
	}
}
