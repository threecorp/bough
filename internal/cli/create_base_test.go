package cli

import "testing"

// TestChooseBase is the regression for the dogfood-found bug: a
// `git clone --local` records origin/HEAD as whatever the source had
// checked out, and bough used that over the explicit branch_strategy —
// so a clone made while the source sat on a feature branch cut the
// worktree off that feature branch instead of the declared `develop`.
func TestChooseBase(t *testing.T) {
	cases := []struct {
		name           string
		branchStrategy string
		detected       string
		want           string
	}{
		{"explicit strategy wins over detected feature branch", "develop", "feature/issue-1394-video-archive-table", "develop"},
		{"empty strategy falls back to detected", "", "master", "master"},
		{"whitespace strategy treated as empty", "   ", "develop", "develop"},
		{"both equal", "develop", "develop", "develop"},
		{"strategy wins even when detection failed (empty)", "develop", "", "develop"},
		{"surrounding whitespace is trimmed off the returned ref", "  develop  ", "master", "develop"},
		{"detected ref is also trimmed when used", "", "  master  ", "master"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chooseBase(c.branchStrategy, c.detected); got != c.want {
				t.Errorf("chooseBase(%q, %q) = %q, want %q", c.branchStrategy, c.detected, got, c.want)
			}
		})
	}
}
