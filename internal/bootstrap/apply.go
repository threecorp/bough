// Package bootstrap promotes evolve-pipeline candidates into the
// monorepo's .claude/ artifact tree (CLAUDE.md + .claude/skills/*.md).
//
// v0.7.0 ships the read-only dry-run path in internal/cli/bootstrap.go.
// v0.7.1 layers the apply path on top — same observation log + same
// evolve pipeline, but the per-cluster candidates land as Markdown
// frontmatter blobs in the operator's .claude/ tree, atomically
// written with git-diff-friendly framing so the operator can review
// + revert any change in one step.
//
// Deviation from plan §2.5: the m plan called for default = apply.
// Reviewer round 5 risk note explicitly flagged silent CLAUDE.md
// overwrite as the highest blast-radius failure mode, so v0.7.1
// ships --apply as opt-in (= explicit operator gesture) instead.
// v0.7.2 dogfooding can flip the default once we have audit logs
// from real worktrees.
package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/pkg/schema"
	api "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// ApplyOptions tunes how Apply promotes candidates into the
// monorepo. MonorepoRoot is the directory the .claude/ tree lives
// under; Force bypasses the verdict gate (= promotes DOUBT too)
// and the git-dirty check.
type ApplyOptions struct {
	MonorepoRoot string
	Force        bool
	Now          func() time.Time
	Stdout       io.Writer
}

// ApplyResult summarises what Apply wrote. Diff is a one-paragraph
// `git diff --stat`-style summary the CLI prints so the operator
// sees the blast radius without scrolling the whole diff.
type ApplyResult struct {
	WrittenFiles []string
	Skipped      []string
	Diff         string
	Promoted     int
	Demoted      int
}

// Apply takes the evolve Result and writes the PASS candidates
// into .claude/CLAUDE.md + .claude/skills/<label>.md. DOUBT verdicts
// land only when opts.Force is true; FAIL verdicts are always
// skipped.
//
// Safety contract (round 5 reviewer non-negotiable):
//
//  1. Refuses if `.claude/` has uncommitted changes (= operator
//     hand-edits are at risk). opts.Force overrides.
//  2. Writes via tmp+rename so a half-flushed file never appears
//     in the working tree.
//  3. Returns a Diff summary the CLI surfaces so the operator can
//     immediately git diff to review.
func Apply(ctx context.Context, res evolve.Result, opts ApplyOptions) (ApplyResult, error) {
	if opts.MonorepoRoot == "" {
		return ApplyResult{}, fmt.Errorf("bootstrap.Apply: MonorepoRoot empty")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stderr
	}

	claudeDir := filepath.Join(opts.MonorepoRoot, ".claude")
	if !opts.Force {
		if dirty, summary, err := claudeDirty(opts.MonorepoRoot); err != nil {
			return ApplyResult{}, fmt.Errorf("bootstrap.Apply: git status check: %w", err)
		} else if dirty {
			return ApplyResult{}, fmt.Errorf("refuse to apply: .claude/ has uncommitted changes:\n%s\n(use --force to override)", summary)
		}
	}

	if err := os.MkdirAll(filepath.Join(claudeDir, "skills"), 0o755); err != nil {
		return ApplyResult{}, fmt.Errorf("mkdir .claude/skills: %w", err)
	}

	out := ApplyResult{}
	byLabel := groupByLabel(res.Candidates)

	labels := make([]string, 0, len(byLabel))
	for l := range byLabel {
		labels = append(labels, l)
	}
	sort.Strings(labels)

	for _, label := range labels {
		bunch := byLabel[label]
		audit := lookupAuditForLabel(label, res.PerClusterAudit, res.Candidates)
		if audit.Verdict.Verdict == api.VerdictFail {
			out.Skipped = append(out.Skipped, label)
			continue
		}
		if audit.Verdict.Verdict == api.VerdictDoubt && !opts.Force {
			out.Demoted++
			out.Skipped = append(out.Skipped, label+" (DOUBT, needs --force)")
			continue
		}
		path := filepath.Join(claudeDir, "skills", safeFilename(label)+".md")
		body := renderSkillBody(label, bunch, audit, now())
		if err := atomicWrite(path, []byte(body)); err != nil {
			return out, fmt.Errorf("write %s: %w", path, err)
		}
		out.WrittenFiles = append(out.WrittenFiles, path)
		out.Promoted++
	}

	if len(out.WrittenFiles) > 0 {
		out.Diff = diffSummary(opts.MonorepoRoot, out.WrittenFiles)
	}
	return out, nil
}

// claudeDirty asks git whether the .claude/ subtree has any
// uncommitted changes. Returns (true, "porcelain output") when
// the subtree is dirty; (false, "") when clean.
func claudeDirty(root string) (bool, string, error) {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain", "--", ".claude")
	out, err := cmd.Output()
	if err != nil {
		// Treat "not a git repo" as a non-fatal: bootstrap can run
		// in a fresh scratch dir without git history. Report
		// clean.
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			return false, "", nil
		}
		return false, "", err
	}
	if len(out) == 0 {
		return false, "", nil
	}
	return true, strings.TrimRight(string(out), "\n"), nil
}

// asExit unwraps an *exec.ExitError so we can branch on it without
// importing errors twice in a hot path.
func asExit(err error, target **exec.ExitError) bool {
	for cur := err; cur != nil; {
		ex, ok := cur.(*exec.ExitError)
		if ok {
			*target = ex
			return true
		}
		// Unwrap manually to avoid the import in the apply.go hot
		// path. Standard error wrapping chains stop on nil.
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}

func groupByLabel(cands []schema.InstinctCandidate) map[string][]schema.InstinctCandidate {
	out := map[string][]schema.InstinctCandidate{}
	for _, c := range cands {
		label := c.HowToApply
		if label == "" {
			label = "uncategorised"
		}
		out[label] = append(out[label], c)
	}
	return out
}

// lookupAuditForLabel finds the ClusterAudit whose label matches.
// Used to gate by verdict + carry the verdict reason into the
// rendered skill body. Falls back to a PASS-with-low-confidence
// shape when no audit matches (= shouldn't happen in practice,
// but defends against partial Result inputs).
func lookupAuditForLabel(label string, audits []evolve.ClusterAudit, cands []schema.InstinctCandidate) evolve.ClusterAudit {
	clusterID := ""
	for _, c := range cands {
		if c.HowToApply == label {
			// Cluster ID is the first 8 hex chars of the candidate
			// derive prefix — strip the "cand_" + take next 8.
			if strings.HasPrefix(c.ID, "cand_") && len(c.ID) >= 13 {
				clusterID = c.ID[5:13]
			}
			break
		}
	}
	for _, a := range audits {
		if a.ClusterID == clusterID {
			return a
		}
	}
	return evolve.ClusterAudit{
		Verdict: api.JudgeVerdict{Verdict: api.VerdictPass, Confidence: 0.5},
	}
}

func renderSkillBody(label string, bunch []schema.InstinctCandidate, audit evolve.ClusterAudit, now time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", label)
	fmt.Fprintf(&b, "description: %s\n", quote(audit.Verdict.Reason))
	fmt.Fprintf(&b, "generated_by: bough@v0.7.1\n")
	fmt.Fprintf(&b, "generated_at: %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "verdict: %s\n", audit.Verdict.Verdict)
	fmt.Fprintf(&b, "confidence: %.2f\n", audit.Verdict.Confidence)
	fmt.Fprintf(&b, "cluster_size: %d\n", audit.Size)
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", label)
	fmt.Fprintf(&b, "%s\n\n", audit.Verdict.Reason)
	if len(bunch) > 0 {
		b.WriteString("## Evidence\n\n")
		for _, c := range bunch {
			fmt.Fprintf(&b, "- %s (scope=%s)\n", oneLine(c.Rule), c.Scope.Level)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func quote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\"", "'")
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return "\"" + s + "\""
}

func safeFilename(label string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		case r == ' ', r == '.':
			return '-'
		default:
			return -1
		}
	}, label)
	if out == "" {
		out = "skill"
	}
	return out
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func diffSummary(root string, files []string) string {
	args := append([]string{"-C", root, "diff", "--stat", "--"}, files...)
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// git not available or not a repo: render a plain
		// "files written" summary instead. The apply path still
		// succeeds; the operator just sees fewer details.
		var b strings.Builder
		fmt.Fprintf(&b, "%d files written (git diff unavailable: %v)\n", len(files), err)
		for _, f := range files {
			fmt.Fprintf(&b, "  %s\n", f)
		}
		return b.String()
	}
	return strings.TrimRight(string(out), "\n")
}
