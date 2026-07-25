package evolve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// There are three moments knowledge can reach a session, and conflating
// them is why a learning system feels broken even when every piece works:
//
//	the prompt mentions it   → retrieval (internal/retrieve)
//	the model decides it needs it → a skill, matched on its description
//	a file is read           → a path-scoped rule
//
// Only the third reaches a session that never named the subsystem out
// loud — someone opens a file in a subsystem they have not worked in and
// the rules for it arrive without anyone asking. Retrieval cannot cover
// that case: there is no prompt text to match on.
//
// So this is a complement, not a duplicate. A rule is emitted only when
// the cluster actually names repository paths; a rule with no paths
// never fires, and emitting one would be dead weight in the rules
// directory that reads like coverage.

// pathTokenRe matches repository-path-shaped tokens in instinct prose:
// two or more slash-separated segments where the last may be a filename.
// Anchored on a segment start so a bare URL fragment or a package import
// path with a host does not masquerade as a repo path.
var pathTokenRe = regexp.MustCompile(`\b(?:[a-zA-Z0-9_.-]+/){1,}[a-zA-Z0-9_.-]+\b`)

// pathGlobs derives the `paths:` frontmatter entries for a cluster from
// the repository paths its members name. Directories become recursive
// globs; a file becomes the glob of its directory, because a rule scoped
// to exactly one file is nearly always narrower than the knowledge it
// carries.
//
// Returns nil when the cluster names no paths — the caller must then not
// emit a rule at all.
func pathGlobs(members []*homunculus.Instinct) []string {
	seen := map[string]struct{}{}
	for _, m := range members {
		for _, tok := range pathTokenRe.FindAllString(m.ID+" "+m.Trigger+" "+m.Body, -1) {
			glob := globForPath(tok)
			if glob == "" {
				continue
			}
			seen[glob] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// globForPath turns one path-shaped token into a rule glob, or "" when
// the token is not usable as a repository path.
func globForPath(tok string) string {
	tok = strings.Trim(tok, "./")
	if tok == "" || strings.Contains(tok, "://") {
		return ""
	}
	// A URL host or a versioned module path is not a repo path.
	if strings.Count(tok, "/") == 0 {
		return ""
	}
	dir := tok
	if ext := filepath.Ext(tok); ext != "" {
		dir = filepath.Dir(tok)
	}
	dir = strings.Trim(dir, "./")
	if dir == "" || dir == "." {
		return ""
	}
	return dir + "/**"
}

// RuleArtifact is one rendered path-scoped rule.
type RuleArtifact struct {
	Slug  string
	Paths []string
	Body  string
}

// RenderRule builds a `.claude/rules/<slug>.md` body: frontmatter with
// the path globs the rule applies to, then the cluster's actions. The
// second return is false when the cluster names no repository paths, in
// which case there is nothing for a rule to attach to.
func RenderRule(label, description string, c Cluster, now time.Time) (RuleArtifact, bool) {
	globs := pathGlobs(c.Members)
	if len(globs) == 0 {
		return RuleArtifact{}, false
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", label)
	fmt.Fprintf(&b, "description: %s\n", yamlOneLine(description))
	b.WriteString("paths:\n")
	for _, g := range globs {
		fmt.Fprintf(&b, "  - %s\n", g)
	}
	fmt.Fprintf(&b, "generated_by: bough-evolve@v0.9.1\n")
	fmt.Fprintf(&b, "generated_at: %s\n", now.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", label, description)
	b.WriteString("## Applies when you touch these paths\n\n")
	for _, m := range sortedMembers(c.Members) {
		fmt.Fprintf(&b, "- %s\n", firstActionLine(m.Body))
	}
	b.WriteString("\n")
	b.WriteString(sourceInstinctsBlock(c.Members))
	return RuleArtifact{Slug: label, Paths: globs, Body: b.String()}, true
}

// WriteRule persists a rule to <rulesDir>/<slug>.md. Like WriteSkill it
// refuses to overwrite a hand-maintained file: an operator who took over
// a rule has made a decision the next run must not undo.
func WriteRule(rulesDir string, art RuleArtifact) (string, error) {
	if !labelPattern.MatchString(art.Slug) {
		return "", fmt.Errorf("evolve.WriteRule: invalid slug %q", art.Slug)
	}
	path := filepath.Join(rulesDir, art.Slug+".md")
	if IsCurated(path) {
		return "", fmt.Errorf("%w: %s", ErrCurated, path)
	}
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		return "", fmt.Errorf("evolve.WriteRule: mkdir %s: %w", rulesDir, err)
	}
	if err := atomicWriteFile(path, []byte(art.Body)); err != nil {
		return "", err
	}
	return path, nil
}
