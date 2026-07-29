package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func at(day int) time.Time {
	return time.Date(2026, 7, day, 12, 0, 0, 0, time.UTC)
}

func writeLog(t *testing.T, events ...Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	w := NewWriter(path)
	for _, e := range events {
		if err := w.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return path
}

func load(t *testing.T, path string) *Log {
	t.Helper()
	lg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return lg
}

// TestMissingLogIsEmptyNotAnError pins the starting state: a project
// that has never recorded anything must not force every caller into a
// special case, or the gate's first question becomes "does the file
// exist" instead of "did anything happen".
func TestMissingLogIsEmptyNotAnError(t *testing.T) {
	lg, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("a missing log must not be an error: %v", err)
	}
	if len(lg.Events) != 0 || len(lg.DriftIn(time.Time{}, time.Now().Add(time.Hour))) != 0 {
		t.Errorf("empty log should be quiet, got %d events / %d drift rows", len(lg.Events), len(lg.DriftIn(time.Time{}, time.Now().Add(time.Hour))))
	}
}

func TestAppendRequiresAKind(t *testing.T) {
	w := NewWriter(filepath.Join(t.TempDir(), "t.jsonl"))
	if err := w.Append(Event{Slug: "x"}); err == nil {
		t.Error("an event with no kind must be refused: it would be invisible to every reader")
	}
}

func TestAppendStampsUnsetTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	w := NewWriter(path)
	w.SetClock(func() time.Time { return at(20) })
	if err := w.Append(Event{Kind: KindSkillPull, Slug: "s"}); err != nil {
		t.Fatal(err)
	}
	lg := load(t, path)
	if len(lg.Events) != 1 || !lg.Events[0].TS.Equal(at(20)) {
		t.Errorf("unstamped event should take the clock, got %+v", lg.Events)
	}
}

// TestWindowCountsRatherThanDiffs is the shape assertion. Two skills
// are pulled, then one is retired and stops appearing. A snapshot-diff
// design reports the retirement as negative usage; counting inside a
// window cannot, because there is nothing to subtract from.
func TestWindowCountsRatherThanDiffs(t *testing.T) {
	path := writeLog(t,
		Event{TS: at(1), Kind: KindSkillPull, Slug: "alpha"},
		Event{TS: at(2), Kind: KindSkillPull, Slug: "beta"},
		// beta is consolidated away here; only alpha keeps being pulled.
		Event{TS: at(10), Kind: KindSkillPull, Slug: "alpha"},
		Event{TS: at(11), Kind: KindSkillPull, Slug: "alpha"},
	)
	lg := load(t, path)

	all := PullsBySlug(lg.Window(at(1), at(11)))
	if all["alpha"] != 3 || all["beta"] != 1 {
		t.Errorf("whole-history counts = %v, want alpha=3 beta=1", all)
	}
	recent := PullsBySlug(lg.Window(at(9), at(11)))
	if recent["alpha"] != 2 {
		t.Errorf("recent window alpha = %d, want 2", recent["alpha"])
	}
	if _, ok := recent["beta"]; ok {
		t.Error("a retired slug should simply be absent from a later window, never negative")
	}
}

// TestWindowIsInclusiveAtBothEnds: an event exactly on the boundary is
// the one a "last N days" question is most likely to be about.
func TestWindowIsInclusiveAtBothEnds(t *testing.T) {
	lg := load(t, writeLog(t,
		Event{TS: at(5), Kind: KindSkillPull, Slug: "edge"},
		Event{TS: at(9), Kind: KindSkillPull, Slug: "edge"},
	))
	if n := len(lg.Window(at(5), at(9))); n != 2 {
		t.Errorf("boundary events dropped: window returned %d, want 2", n)
	}
}

// TestDriftReportsAnUnattributedPullLoudly is the self-check. A pull
// whose slug the reader could not find must produce a row, because a
// silent zero here is indistinguishable from "the portfolio is never
// used" — the exact reading that lets a broken parser look like a
// finding.
func TestDriftReportsAnUnattributedPullLoudly(t *testing.T) {
	lg := load(t, writeLog(t,
		Event{TS: at(1), Kind: KindSkillPull, Slug: "ok"},
		Event{TS: at(2), Kind: KindSkillPull, Raw: json.RawMessage(`{"skillName":"moved-field"}`)},
	))
	if got := PullsBySlug(lg.Events); got["ok"] != 1 || len(got) != 1 {
		t.Errorf("an unattributed pull must not be counted under an empty slug: %v", got)
	}
	rows := lg.DriftIn(time.Time{}, time.Now().Add(time.Hour))
	if len(rows) != 1 || !strings.Contains(rows[0], "moved-field") {
		t.Fatalf("expected one loud drift row quoting the payload, got %v", rows)
	}
}

// TestHealthyLogIsSilent: the tripwire must not add noise to a correct
// run, or it will be ignored on the day it fires.
func TestHealthyLogIsSilent(t *testing.T) {
	lg := load(t, writeLog(t,
		Event{TS: at(1), Kind: KindSkillPull, Slug: "a"},
		Event{TS: at(2), Kind: KindSelection, IDs: []string{"i1", "i2"}},
		Event{TS: at(3), Kind: KindJudge, Counts: map[string]int{"reviewed": 4, "unreviewed": 0}},
		// A judge pass with nothing to review is a legitimate zero, not drift.
		Event{TS: at(4), Kind: KindJudge, Counts: map[string]int{"reviewed": 0}},
	))
	if rows := lg.DriftIn(time.Time{}, time.Now().Add(time.Hour)); len(rows) != 0 {
		t.Errorf("a healthy log must produce no drift rows, got %v", rows)
	}
}

// TestUnknownKindIsDriftNotSilence: a writer ahead of its reader is the
// documented way this class of gate went wrong. Ignoring the line would
// reproduce it.
func TestUnknownKindIsDriftNotSilence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(`{"ts":"2026-07-01T00:00:00Z","kind":"skill_render"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := load(t, path).DriftIn(time.Time{}, time.Now().Add(time.Hour))
	if len(rows) != 1 || !strings.Contains(rows[0], "skill_render") {
		t.Fatalf("an unknown kind must be reported by name, got %v", rows)
	}
}

// TestTornLineIsDamageNotDisagreement keeps the two failure modes
// separate: a half-written line is a truncated write, not a schema
// drift, and telling an operator the wrong one sends them to the wrong
// place.
func TestTornLineIsDamageNotDisagreement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"ts":"2026-07-01T00:00:00Z","kind":"skill_pull","slug":"a"}` + "\n" + `{"ts":"2026-07-0`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lg := load(t, path)
	if len(lg.Events) != 1 {
		t.Errorf("the intact line must still be read, got %d events", len(lg.Events))
	}
	rows := lg.DriftIn(time.Time{}, time.Now().Add(time.Hour))
	if len(rows) != 1 || !strings.Contains(rows[0], "not valid JSON") {
		t.Fatalf("a torn line should be reported as damage, got %v", rows)
	}
}

func TestDistinctIDsMeasuresBreadth(t *testing.T) {
	lg := load(t, writeLog(t,
		Event{TS: at(1), Kind: KindSelection, IDs: []string{"a", "b"}},
		Event{TS: at(2), Kind: KindSelection, IDs: []string{"b", "c"}},
		Event{TS: at(3), Kind: KindSkillPull, Slug: "s"},
	))
	if got := DistinctIDs(lg.Events); got != 3 {
		t.Errorf("distinct injected ids = %d, want 3 (a, b, c)", got)
	}
}

// TestUnjudgedPromotionsIsGreppable: the gate fails open by design, so
// the number of candidates that reached the corpus unjudged is the one
// an operator has to be able to find after the fact.
func TestUnjudgedPromotionsIsGreppable(t *testing.T) {
	lg := load(t, writeLog(t,
		Event{TS: at(1), Kind: KindJudge, Counts: map[string]int{"reviewed": 7, "unreviewed": 0}},
		Event{TS: at(2), Kind: KindJudge, Counts: map[string]int{"reviewed": 2, CountUnjudgedPromoted: 5}},
	))
	if got := UnjudgedPromotions(lg.Events); got != 5 {
		t.Errorf("unjudged promotions = %d, want 5", got)
	}
}

func TestOldestAndNewestReportRealAges(t *testing.T) {
	lg := load(t, writeLog(t,
		Event{TS: at(9), Kind: KindSkillPull, Slug: "a"},
		Event{TS: at(2), Kind: KindSkillPull, Slug: "a"},
		Event{TS: at(5), Kind: KindSkillPull, Slug: "a"},
	))
	oldest, ok := lg.Oldest()
	if !ok || !oldest.Equal(at(2)) {
		t.Errorf("oldest = %v (ok=%v), want %v", oldest, ok, at(2))
	}
	newest, ok := lg.Newest()
	if !ok || !newest.Equal(at(9)) {
		t.Errorf("newest = %v (ok=%v), want %v", newest, ok, at(9))
	}
	empty := &Log{}
	if _, ok := empty.Oldest(); ok {
		t.Error("an empty log has no oldest event to report")
	}
}

// TestPreviewSaysItTruncated: a diagnostic row that silently cuts its
// evidence is the same lie the row exists to expose.
func TestPreviewSaysItTruncated(t *testing.T) {
	long := `{"payload":"` + strings.Repeat("x", 300) + `"}`
	lg := load(t, writeLog(t,
		Event{TS: at(1), Kind: KindSkillPull, Raw: json.RawMessage(long)},
	))
	rows := lg.DriftIn(time.Time{}, time.Now().Add(time.Hour))
	if len(rows) != 1 {
		t.Fatalf("want one drift row, got %v", rows)
	}
	if !strings.Contains(rows[0], "bytes total") {
		t.Errorf("a truncated preview must say so: %q", rows[0])
	}
}

// TestSelectionStatsAggregates covers the four selector-health numbers,
// including the one that used to be structurally unmeasurable: an empty
// selection only counts because the writer now records it.
func TestSelectionStatsAggregates(t *testing.T) {
	events := []Event{
		{Kind: KindSelection, IDs: []string{"a", "b"}, MS: 12.5},
		{Kind: KindSelection, IDs: []string{"b", "c"}, MS: 40.0},
		{Kind: KindSelection, IDs: nil, MS: 3.0}, // chose nothing — still a data point
		{Kind: KindSkillPull, Slug: "s"},         // other kinds do not count
	}
	s := SelectionStats(events)
	if s.Volume != 3 || s.Empty != 1 || s.Distinct != 3 || len(s.Timed) != 3 {
		t.Errorf("stats = %+v, want volume=3 empty=1 distinct=3 timed=3", s)
	}
	if got := s.EmptyRate(); got < 0.33 || got > 0.34 {
		t.Errorf("empty rate = %f, want 1/3", got)
	}
}

// TestP95UsesRoundHalfEven pins the index rounding: with 31 samples the
// index is 0.95*30 = 28.5, which round-half-even takes DOWN to 28 while
// round-half-up would take it to 29. Two tools computing the p95 of the
// same log must not disagree by one sample.
func TestP95UsesRoundHalfEven(t *testing.T) {
	s := SelectorStats{}
	for i := 0; i < 31; i++ {
		s.Timed = append(s.Timed, float64(i))
	}
	got, ok := s.P95MS()
	if !ok || got != 28 {
		t.Errorf("p95 = %v ok=%v, want 28 (round-half-even of index 28.5)", got, ok)
	}
}

// TestZeroVolumeEmptyRateIsHonest: with no evidence, "always empty" is
// the honest read — a 0% rate on nothing would clear the bar for free.
func TestZeroVolumeEmptyRateIsHonest(t *testing.T) {
	if got := (SelectorStats{}).EmptyRate(); got != 1.0 {
		t.Errorf("empty rate on zero volume = %f, want 1.0", got)
	}
}

// TestSelectorBarChecksStateTheirNumbers: every row carries the numbers
// it compared, and an untimed log fails the latency row rather than
// passing it vacuously.
func TestSelectorBarChecksStateTheirNumbers(t *testing.T) {
	checks := DefaultSelectorBar().Check(SelectorStats{Volume: 3, Distinct: 2, Empty: 3})
	if len(checks) != 4 {
		t.Fatalf("want 4 checks, got %d", len(checks))
	}
	for _, c := range checks {
		if c.Passed {
			t.Errorf("check %q should fail on a nearly-empty log (%s)", c.Name, c.Detail)
		}
		if c.Detail == "" {
			t.Errorf("check %q must state what it compared", c.Name)
		}
	}
}

// TestHealthyLogProducesNoDriftRows is the clean direction of the
// tripwire, and it matters as much as the loud one: a detector that
// flags clean input trains the reader to ignore it, and then it
// protects nothing. Every writer shape in current use must pass in
// silence — including an EMPTY selection, which carries no ids and no
// raw payload by design.
func TestHealthyLogProducesNoDriftRows(t *testing.T) {
	now := time.Now()
	lg := &Log{Events: []Event{
		{TS: now, Kind: KindSkillPull, Slug: "using-things", Session: "s1"},
		{TS: now, Kind: KindSelection, IDs: []string{"i1", "i2"}, N: 2, MS: 12.5},
		{TS: now, Kind: KindSelection, N: 0, MS: 3.0}, // chose nothing — a data point, not drift
		{TS: now, Kind: KindJudge, Counts: map[string]int{"reviewed": 3, "held": 1}},
	}}
	if rows := lg.DriftIn(now.Add(-time.Hour), now.Add(time.Hour)); len(rows) != 0 {
		t.Errorf("a healthy log must be silent, got %v", rows)
	}
}
