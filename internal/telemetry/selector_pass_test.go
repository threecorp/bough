package telemetry

import (
	"strings"
	"testing"
	"time"
)

// TestSelectorBarPassesOnAHealthyLog is the direction the bar never had:
// every prior test asserted rows FAIL on a starved log, so a sign error
// in Check — or a SelectionStats that never populates — would print the
// same four WAIT rows a young corpus prints, and nothing would notice.
// A bar with no proven PASS branch cannot distinguish "not earned yet"
// from "broken forever"; this pins that the thresholds are reachable and
// that a passing row still states the numbers it compared.
func TestSelectorBarPassesOnAHealthyLog(t *testing.T) {
	b := DefaultSelectorBar()
	s := SelectorStats{Volume: 250, Distinct: 120, Empty: 25}
	for i := 0; i < 250; i++ {
		s.Timed = append(s.Timed, 12.5) // well under the 500ms ceiling
	}

	checks := b.Check(s)
	if len(checks) != 4 {
		t.Fatalf("want 4 checks, got %d", len(checks))
	}
	for _, c := range checks {
		if !c.Passed {
			t.Errorf("check %q must pass on a healthy log (%s)", c.Name, c.Detail)
		}
		if c.Detail == "" {
			t.Errorf("check %q must state its numbers even when passing", c.Name)
		}
	}
}

// TestSelectorBarEachThresholdBindsAlone pins that every threshold is
// live: from the healthy baseline, degrading exactly one input must fail
// exactly that row. Without this, a threshold could be dead (compared
// against the wrong field, or shadowed by another check) while the
// all-pass and all-fail tests both stay green.
func TestSelectorBarEachThresholdBindsAlone(t *testing.T) {
	healthy := func() SelectorStats {
		s := SelectorStats{Volume: 250, Distinct: 120, Empty: 25}
		for i := 0; i < 250; i++ {
			s.Timed = append(s.Timed, 12.5)
		}
		return s
	}
	cases := []struct {
		name    string
		mutate  func(*SelectorStats)
		failRow string
	}{
		{"volume below 200", func(s *SelectorStats) { s.Volume = 199 }, "selection volume"},
		{"p95 at the ceiling", func(s *SelectorStats) {
			s.Timed = nil
			for i := 0; i < 250; i++ {
				s.Timed = append(s.Timed, 600)
			}
		}, "p95 latency"},
		{"distinct below 100", func(s *SelectorStats) { s.Distinct = 99 }, "distinct instincts"},
		{"empty rate above 30%", func(s *SelectorStats) { s.Empty = 100 }, "non-empty rate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := healthy()
			c.mutate(&s)
			for _, row := range DefaultSelectorBar().Check(s) {
				if row.Name == c.failRow && row.Passed {
					t.Errorf("row %q must fail when %s (%s)", row.Name, c.name, row.Detail)
				}
				if row.Name != c.failRow && !row.Passed {
					t.Errorf("row %q must keep passing when only %s (%s)", row.Name, c.name, row.Detail)
				}
			}
		})
	}
}

// TestSelectionStatsCountsWhatTheLogHolds folds a log through the same
// reader ops/doctor use and checks every aggregate at once, so a field
// rename on either side surfaces here rather than as a silent zero.
func TestSelectionStatsCountsWhatTheLogHolds(t *testing.T) {
	now := time.Now()
	lg := &Log{Events: []Event{
		{TS: now, Kind: KindSelection, IDs: []string{"a", "b"}, N: 2, MS: 10},
		{TS: now, Kind: KindSelection, IDs: []string{"b", "c"}, N: 2, MS: 20},
		{TS: now, Kind: KindSelection, N: 0, MS: 3}, // empty selection is a data point
		{TS: now, Kind: KindSkillPull, Slug: "x"},   // other kinds must not count
	}}
	s := SelectionStats(lg.Events)
	if s.Volume != 3 || s.Empty != 1 || s.Distinct != 3 || len(s.Timed) != 3 {
		t.Errorf("stats = %+v, want Volume=3 Empty=1 Distinct=3 Timed=3", s)
	}
	// And the folded stats drive a coherent report line.
	for _, c := range DefaultSelectorBar().Check(s) {
		if !strings.Contains(c.Detail, "(") {
			t.Errorf("check %q detail must carry its comparison: %s", c.Name, c.Detail)
		}
	}
}
