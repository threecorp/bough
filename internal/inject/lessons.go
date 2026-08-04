package inject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Human-authored corrections outrank anything the observer mints, and
// the two are not comparable on the same axis. A minted instinct is an
// LLM's inference from tool-use traces, carrying a confidence score that
// decides whether it is injected at all. A lessons file is a person
// writing down what went wrong and what to do instead — ground truth,
// with no score to compare against and nothing to threshold on.
//
// So lessons are not merged into the confidence ranking: they are
// prepended above it, never dropped by the confidence floor, and given
// their own byte budget so a long lessons file cannot silently starve
// the minted block (or vice versa).

// DefaultLessonsPaths are the conventional locations, relative to the
// monorepo root, where a lessons / corrections file lives. The first one
// that exists wins; operators with a different layout set
// instinct.lessons.paths in .bough.yaml. These are conventions rather
// than a hard-coded single path because the file predates bough in most
// repos that have one — bough should find it, not demand it move.
var DefaultLessonsPaths = []string{
	".claude/lessons.md",
	"tasks/lessons.md",
	"lessons.md",
}

// DefaultLessonsBytes is the lessons block's own byte budget — the rest
// of DefaultTotalBytes after the instinct block's DefaultBlockBytes.
// Ground truth ranks first, but a 9KB lessons file would otherwise
// consume the whole prompt block and leave no room for the minted
// instincts that are the rest of the point.
//
// It is a budget of its own rather than a fraction of the instinct
// block's: the two are not competing for one pool, and a fraction made
// the operator's corrections shrink whenever someone tuned an unrelated
// knob.
const DefaultLessonsBytes = 3000

// LessonsBlock renders the human-authored corrections block for the
// monorepo at root, or "" when no lessons file exists. paths may be nil,
// in which case DefaultLessonsPaths is used; entries are resolved
// relative to root. budget is this block's byte allowance; zero or below
// means DefaultLessonsBytes. Truncation is stated in the output rather
// than applied quietly — a silently halved lessons file reads as "this
// is all the guidance there is".
func LessonsBlock(root string, paths []string, budget int) string {
	// Zero is what callers pass to mean "the standard budget" (the CLI
	// does), so it must not be taken literally: a budget of 0 would
	// truncate the operator's corrections to nothing.
	if budget <= 0 {
		budget = DefaultLessonsBytes
	}
	if len(paths) == 0 {
		paths = DefaultLessonsPaths
	}
	path, content := firstExistingLessons(root, paths)
	if path == "" {
		return ""
	}
	body := strings.TrimSpace(content)
	if body == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Corrections from the operator (%s) — these outrank the learned instincts below\n\n", path)
	if len(body) > budget {
		body = truncateAtLineBoundary(body, budget)
		fmt.Fprintf(&b, "%s\n\n(truncated to %d bytes — read %s for the rest)\n\n", body, budget, path)
		return b.String()
	}
	fmt.Fprintf(&b, "%s\n\n", body)
	return b.String()
}

// firstExistingLessons returns the first readable candidate and its
// contents. The returned path is the candidate as written (relative to
// the root) so the block, and its truncation notice, name something the
// operator can open.
func firstExistingLessons(root string, paths []string) (string, string) {
	for _, rel := range paths {
		full := rel
		if !filepath.IsAbs(full) {
			full = filepath.Join(root, rel)
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		return rel, string(raw)
	}
	return "", ""
}

// truncateAtLineBoundary cuts to at most n bytes without splitting a
// line, so the block never ends mid-sentence in a way that inverts the
// meaning of a correction ("never run X" cut after "never run").
func truncateAtLineBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return cut[:i]
	}
	return cut
}
