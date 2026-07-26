package instinctgate

import (
	"strings"
	"testing"
)

// The coverage matrix is the acceptance gate, not the mechanism count.
// Each row is one enforced rule paired with an adversarial probe that
// phrases the violation DIFFERENTLY from how the tripwire is written
// (argument order swapped, a different tool for the same intent). The
// probes are authored here from the shape of real past incidents; they are
// never imported from tripwire.go, so a green matrix cannot be a tautology
// where the checker is tested against its own pattern list.
//
// wantRule == "" means the probe must CLEAR (a legitimate note, or a note
// that only cites a command in prose the surface scan must not see).
func TestCoverageMatrix(t *testing.T) {
	cases := []struct {
		name     string
		cand     Candidate
		wantRule string // "" ⇒ must clear
	}{
		// never-merge-unasked — flags reordered vs the pattern, and a
		// different tool (gh) for the same "merge it now" intent.
		{"merge flag before verb", cand("m1", "when CI is green", "run `git --no-ff merge` into main"), "never-merge-unasked"},
		{"merge via gh", cand("m2", "once gates pass", "`gh pr merge --squash` to land it"), "never-merge-unasked"},
		// never-discard-wip — several surface forms.
		{"reset hard", cand("d1", "when the tree is messy", "`git reset --hard origin/main` to clean up"), "never-discard-wip"},
		{"checkout HEAD --", cand("d2", "to drop a bad edit", "`git checkout HEAD -- file.go`"), "never-discard-wip"},
		{"stash drop", cand("d3", "after debugging", "`git stash drop` to tidy"), "never-discard-wip"},
		{"clean -fd", cand("d4", "to reset the worktree", "`git clean -fd`"), "never-discard-wip"},
		{"branch -D", cand("d5", "when a feature is done", "`git branch -D feature`"), "never-discard-wip"},
		// never-override-author — both the --author and the -c forms.
		{"author flag", cand("a1", "to fix attribution", "`git commit --author=\"x <y>\"`"), "never-override-author"},
		{"-c user.email", cand("a2", "when committing", "`git -c user.email=z commit`"), "never-override-author"},
		// never-force-push — -f short flag and the long form.
		{"push -f", cand("f1", "after a rebase", "`git push -f origin br`"), "never-force-push"},
		{"push --force-with-lease", cand("f2", "to update the PR", "`git push --force-with-lease`"), "never-force-push"},
		// never-delete-remote-branch.
		{"push --delete", cand("r1", "to clean up", "`git push origin --delete old-branch`"), "never-delete-remote-branch"},

		// Must CLEAR: a note that CITES a forbidden command only in its
		// Body/evidence (provenance), never in the propagating surface.
		{"command only in body", Candidate{ID: "c1", Trigger: "when reviewing history", Action: "read the reflog to understand what happened", Body: "context: someone had run git reset --hard earlier"}, ""},
		// Must CLEAR: an ordinary, safe instinct.
		{"benign note", cand("c2", "when tests are flaky", "re-run the suite and inspect the seed"), ""},
		// Must CLEAR: read-only git verbs that merely start with a forbidden
		// verb — the boundary must not over-match these.
		{"merge-base is read-only", cand("fp1", "to find the fork point", "run `git merge-base main HEAD`"), ""},
		{"mergetool is not merge", cand("fp2", "on a conflict", "open `git mergetool` to resolve"), ""},
		// Must CLEAR: `-d` is delete-IF-MERGED, which discards nothing. Only
		// the uppercase `-D` forces a delete over unmerged work.
		{"branch -d is the safe delete", cand("fp3", "after a PR lands", "`git branch -d feature` to tidy up"), ""},
		// Must CLEAR: a branch NAME carrying `-D` is not the `-D` flag.
		{"branch name containing -D", cand("fp4", "after a PR lands", "`git branch -d F-Deploy`"), ""},
	}

	g := New(Config{Enabled: true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := g.Screen([]Candidate{tc.cand})
			gotRule := ""
			if len(res.Held) == 1 {
				gotRule = res.Held[0].Rule
			}
			if gotRule != tc.wantRule {
				t.Errorf("probe %q: held by %q, want %q\n  surface: %s / %s",
					tc.name, gotRule, tc.wantRule, tc.cand.Trigger, tc.cand.Action)
			}
		})
	}
}

// TestAllowIDExemptsRuleCitingInstinct pins the exemption: an instinct that
// IS the rule forbidding a command names that command in its action; the
// allowlist must clear it rather than quarantine the very note that teaches
// the prohibition.
func TestAllowIDExemptsRuleCitingInstinct(t *testing.T) {
	rule := cand("no-force", "before pushing a shared branch", "never run `git push --force` on main; use a PR")
	if got := New(Config{Enabled: true}).Screen([]Candidate{rule}); len(got.Held) != 1 {
		t.Fatalf("precondition: the rule-citing note should trip the pattern, got %d held", len(got.Held))
	}
	got := New(Config{Enabled: true, AllowIDs: []string{"no-force"}}).Screen([]Candidate{rule})
	if len(got.Held) != 0 || len(got.Cleared) != 1 {
		t.Errorf("allowlisted id should clear: held=%d cleared=%d", len(got.Held), len(got.Cleared))
	}
}

// TestDisabledGateClearsEverything pins the safe default: Enabled=false is
// byte-for-byte the pre-gate behaviour.
func TestDisabledGateClearsEverything(t *testing.T) {
	bad := cand("x", "when CI is green", "`gh pr merge`")
	got := New(Config{Enabled: false}).Screen([]Candidate{bad})
	if len(got.Held) != 0 || len(got.Cleared) != 1 {
		t.Errorf("disabled gate must clear all: held=%d cleared=%d", len(got.Held), len(got.Cleared))
	}
}

func cand(id, trigger, action string) Candidate {
	return Candidate{ID: id, Trigger: trigger, Action: action, Body: trigger + " → " + action}
}

// sanity: probes never smuggle the tripwire regexes in.
func TestProbesAreNotImportedFromPatterns(t *testing.T) {
	for _, tw := range DefaultTripwires() {
		if strings.Contains(tw.Re.String(), "flaky") {
			t.Fatal("a probe string leaked into the pattern set")
		}
	}
}
