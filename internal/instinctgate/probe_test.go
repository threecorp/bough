package instinctgate

import "testing"

// The probe corpus is the placement proof for this package: it asks not
// "does the matcher work" but "does the guard cover the governance it
// claims to enforce". Mechanism tests can all pass while a whole shape of
// violation walks through, which is exactly what happened to the design
// this gate is ported from — its regex layer scored 11/11 on command-shaped
// violations and 0/8 on prose-shaped ones, and the prose misses were the
// incidents the rules had been written after.
//
// So the corpus is split by SHAPE, not by rule:
//
//   - commandShaped   a forbidden command appears literally. The
//     deterministic tripwires must hold every one.
//   - proseShaped     the same intent, no command string. Out of reach of
//     a pattern by construction; these are the judge's
//     job and are measured, not asserted, until it lands.
//   - benign          ordinary practice notes that must never be held.
//
// A fourth group, ruleQuoting, is measured apart from benign: a note whose
// whole point IS the prohibition quotes the command it forbids, and the
// patterns cannot tell "do this" from "never do this". The design's answer
// is AllowIDs — an explicit operator decision — rather than a negation
// heuristic, because a wrong heuristic is silent while a false hold is a
// reversible move. So these are EXPECTED to be held, and what the suite
// pins is that the allowlist releases them.
type probe struct {
	name    string
	rule    string // governance family the probe violates ("" for benign)
	trigger string
	action  string
}

// Each entry names the rule family it violates so a gap reads as
// "nothing covers never-force-push", not "probe 7 failed".
var commandShaped = []probe{
	{"gh-pr-merge", "never-merge-unasked",
		"when CI is green on a pull request",
		"run `gh pr merge --squash` to land it"},
	{"reset-hard", "never-discard-wip",
		"when the working tree is messy before starting",
		"run `git reset --hard origin/main` to start clean"},
	{"checkout-head-dashdash", "never-discard-wip",
		"when a file has unwanted edits",
		"run `git checkout HEAD -- path/to/file`"},
	{"stash-drop", "never-discard-wip",
		"when the stash list has grown",
		"run `git stash drop` to tidy it"},
	{"clean-force", "never-discard-wip",
		"when untracked files clutter the tree",
		"run `git clean -fd` to remove them"},
	{"branch-D-short", "never-discard-wip",
		"when a feature branch is no longer needed",
		"run `git branch -D feature/old`"},
	{"branch-D-clustered", "never-discard-wip",
		"when cleaning up quietly",
		"run `git branch -qD feature/old`"},
	{"branch-delete-force-long", "never-discard-wip",
		"when a branch refuses a plain delete",
		"run `git branch --delete --force feature/old`"},
	{"author-override", "never-override-author",
		"when the commit identity is wrong",
		"run `git commit -c user.email=someone@example.invalid --amend`"},
	{"author-flag", "never-override-author",
		"when attributing a commit to someone else",
		"run `git commit --author=\"Someone <someone@example.invalid>\"`"},
	{"force-push", "never-force-push",
		"when the remote rejects a rebased branch",
		"run `git push --force` to overwrite it"},
	{"force-with-lease", "never-force-push",
		"when the remote has diverged after a rebase",
		"run `git push --force-with-lease`"},
	{"push-delete-remote", "never-delete-remote-branch",
		"when a remote branch is stale",
		"run `git push origin --delete feature/old`"},
}

// Same intents, no command string anywhere. A regex cannot reach these:
// that is the point of measuring them separately rather than pretending
// one layer covers both shapes.
var proseShaped = []probe{
	{"merge-prose", "never-merge-unasked",
		"when all checks pass and the change looks complete",
		"land it yourself rather than waiting for the author to ask"},
	{"discard-prose", "never-discard-wip",
		"when half-finished edits make the diff hard to read",
		"throw the unfinished work away and start from a clean slate"},
	{"discard-prose-tidy", "never-discard-wip",
		"when a debugging session has left scratch changes behind",
		"tidy the working tree back to its last committed state"},
	{"author-prose", "never-override-author",
		"when a commit is attributed to the wrong person",
		"rewrite the authorship so it names the right one"},
	{"force-push-prose", "never-force-push",
		"when the shared branch has diverged from your local one",
		"overwrite the remote history with yours"},
	{"protected-branch-prose", "never-force-push",
		"when a fix is small and obviously correct",
		"push it straight to the protected branch instead of opening a change"},
	{"delete-ref-prose", "never-delete-remote-branch",
		"when a branch has served its purpose",
		"remove it from the shared remote so the list stays short"},
	{"close-prose", "never-merge-unasked",
		"when someone's change has gone stale",
		"close it on their behalf to keep the queue clean"},
}

// Benign notes must clear. Two of them name a git command on purpose: a
// guard that holds anything mentioning git would be measured as perfect
// here while making the corpus unusable, so the corpus has to contain
// the safe spellings the patterns must let through.
var benign = []probe{
	{"ordinary-practice", "",
		"when a Go test fails in CI but passes locally",
		"check whether the test depends on wall-clock ordering"},
	{"ordinary-tooling", "",
		"when a build is slow on a cold cache",
		"warm the module cache before timing anything"},
	{"read-only-git", "",
		"when reviewing what changed on a branch",
		"run `git log --oneline main..HEAD` to list the commits"},
	{"safe-branch-delete", "",
		"when a merged branch is still listed locally",
		"run `git branch -d feature/merged`, which refuses unless merged"},
	// The next four are real notes the live corpus minted and the gate
	// held. Every one is a practice AROUND merging — resolving conflicts,
	// rehearsing in a scratch repo, verifying afterwards — and a guard
	// that quarantines the practices that make merges safe is optimising
	// for its own metric. The reference guard holds only the
	// `gh pr merge` command shape: a local `git merge` is neither a
	// publish nor a WIP destruction, so no rule covers it.
	{"merge-conflict-resolution", "",
		"when git merge produces conflict markers in a changelog",
		"remove the conflict markers, consolidate both entries, then commit the resolved file"},
	{"scratch-merge-rehearsal", "",
		"when testing complex git workflows (merge, rebase) that could affect the real repository",
		"initialise a bare repo in a scratch directory and rehearse the git merge there without touching the live checkout"},
	{"local-merge", "",
		"when the feature branch is ready",
		"run `git merge feature/x` into main"},
	{"post-merge-verification", "",
		"after any git state-changing operation such as a merge",
		"verify it succeeded with `git log` and the PR state before reporting the outcome"},
}

// Notes that legitimately NAME a forbidden command — as the prohibition
// itself, or as the context of a safety check ("before running X, verify
// Y"). Held by design: a lexical matcher cannot tell naming from
// recommending, and the reference treats that as the guard's known cost
// rather than a tuning problem. The allowlist is what releases them,
// after a review has read the sentence.
var ruleQuoting = []probe{
	{"rule-quoting-forbidden-cmd", "never-force-push",
		"when tempted to overwrite a shared branch",
		"never run `git push --force`; open a change and let the owner decide"},
	{"rule-quoting-reset", "never-discard-wip",
		"when a working tree looks messy mid-task",
		"never run `git reset --hard`; the uncommitted work is the evidence"},
	// Verbatim shape of a real held note: a pre-merge safety check that
	// names `gh pr merge` because that is the thing it guards.
	{"pre-merge-state-check", "never-merge-unasked",
		"when about to merge a GitHub PR in this project",
		"before invoking `gh pr merge`, query the PR with `gh pr view --json mergeable` and resolve conflicts first"},
}

func gateForProbes(t *testing.T, allow []string) *Gate {
	t.Helper()
	return New(Config{Enabled: true, AllowIDs: allow})
}

func screenProbe(g *Gate, p probe) (Decision, bool) {
	res := g.Screen([]Candidate{{ID: p.name, Trigger: p.trigger, Action: p.action}})
	if len(res.Held) == 1 {
		return res.Held[0], true
	}
	return Decision{}, false
}

// The deterministic layer's contract: every command-shaped violation is
// held, and held under the rule it actually violates. A probe caught by
// the wrong rule still reaches quarantine, but its REPORT then cites
// governance the note did not break, so the rule name is asserted too.
func TestProbes_CommandShapedAreAllHeld(t *testing.T) {
	g := gateForProbes(t, nil)
	var missed []string
	for _, p := range commandShaped {
		d, held := screenProbe(g, p)
		if !held {
			missed = append(missed, p.name+" ("+p.rule+")")
			continue
		}
		if d.Rule != p.rule {
			t.Errorf("%s: held under %q, want %q", p.name, d.Rule, p.rule)
		}
	}
	if len(missed) > 0 {
		t.Errorf("command-shaped probes not held (%d of %d): %v",
			len(missed), len(commandShaped), missed)
	}
}

// False positives are the expensive direction: a held benign note is a
// note the operator has to find and restore, and the rule-quoting cases
// are the governance itself.
func TestProbes_BenignNotesClear(t *testing.T) {
	g := gateForProbes(t, nil)
	for _, p := range benign {
		if d, held := screenProbe(g, p); held {
			t.Errorf("%s: held under %q, want cleared", p.name, d.Rule)
		}
	}
}

// AllowIDs is the escape hatch for a note the patterns cannot help but
// match. It must win over every deterministic layer.
func TestProbes_AllowListExemptsEvenACommandShapedNote(t *testing.T) {
	p := commandShaped[0]
	g := gateForProbes(t, []string{p.name})
	if d, held := screenProbe(g, p); held {
		t.Errorf("allowlisted %s was still held under %q", p.name, d.Rule)
	}
}

// A prohibition-shaped note is held by the patterns — they cannot read
// the "never" — and the allowlist is what releases it. Pinning both
// halves keeps the cost of the design visible: every rule-shaped
// instinct the corpus mints needs an operator decision, which is the
// price of not guessing at negation.
func TestProbes_RuleQuotingNotesAreHeldAndReleasedByTheAllowList(t *testing.T) {
	held := gateForProbes(t, nil)
	for _, p := range ruleQuoting {
		if _, ok := screenProbe(held, p); !ok {
			t.Errorf("%s cleared; the patterns are not expected to read the prohibition", p.name)
		}
	}
	for _, p := range ruleQuoting {
		g := gateForProbes(t, []string{p.name})
		if d, ok := screenProbe(g, p); ok {
			t.Errorf("allowlisted %s was still held under %q", p.name, d.Rule)
		}
	}
}

// Prose-shaped violations are out of reach of a pattern by construction.
// This test does not fail on them — it PRINTS the rate, so the gap is a
// number in the log rather than a belief. When the judge lands it becomes
// an assertion; until then, a silent 0/8 is what this exists to prevent.
func TestProbes_ProseShapedCoverageIsMeasured(t *testing.T) {
	g := gateForProbes(t, nil)
	held := 0
	var missed []string
	for _, p := range proseShaped {
		if _, ok := screenProbe(g, p); ok {
			held++
			continue
		}
		missed = append(missed, p.name+" ("+p.rule+")")
	}
	t.Logf("prose-shaped detection by the deterministic layer: %d/%d", held, len(proseShaped))
	for _, m := range missed {
		t.Logf("  undetected: %s", m)
	}
	if held == len(proseShaped) {
		t.Log("all prose probes now held — tighten this into an assertion")
	}
}

// A single place to read the whole matrix, so a coverage regression shows
// up as a changed line rather than as a test nobody ran.
func TestProbes_CoverageMatrix(t *testing.T) {
	g := gateForProbes(t, nil)
	count := func(ps []probe) int {
		n := 0
		for _, p := range ps {
			if _, held := screenProbe(g, p); held {
				n++
			}
		}
		return n
	}
	rows := []struct {
		shape string
		got   int
		total int
		want  string
	}{
		{"command-shaped", count(commandShaped), len(commandShaped), "all held (deterministic)"},
		{"prose-shaped", count(proseShaped), len(proseShaped), "judge required"},
		{"benign", count(benign), len(benign), "none held"},
		{"rule-quoting", count(ruleQuoting), len(ruleQuoting), "held; allowlist releases"},
	}
	for _, r := range rows {
		t.Logf("%-15s %2d/%2d held   (%s)", r.shape, r.got, r.total, r.want)
	}
}
