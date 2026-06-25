package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// PreservedTopN is how many top-confidence instincts the PreCompact
// handler snapshots. ECC snapshots the top 5 before context
// compaction so the most-reliable patterns survive the window reset;
// bough keeps the same count.
const PreservedTopN = 5

// PreserveInstincts writes a MEMORY.md snapshot of the top-confidence
// instincts into the project's instincts dir, so a context
// compaction does not lose the operator's most-reliable learned
// patterns. The PreCompact hook calls this; it is pure filesystem,
// no LLM. Returns the snapshot path.
//
// MEMORY.md is one of the catalog filenames ScanInstincts skips, so
// the snapshot never gets re-ingested as an instinct.
func PreserveInstincts(layout homunculus.Layout, projectID string, now time.Time) (string, error) {
	instincts, _ := homunculus.ScanInstincts(layout.InstinctsDir(projectID))
	if len(instincts) == 0 {
		return "", nil
	}
	sort.SliceStable(instincts, func(i, j int) bool {
		if instincts[i].Confidence != instincts[j].Confidence {
			return instincts[i].Confidence > instincts[j].Confidence
		}
		return instincts[i].ID < instincts[j].ID
	})
	top := instincts
	if len(top) > PreservedTopN {
		top = top[:PreservedTopN]
	}

	var content string
	content += "# MEMORY — top instincts preserved before context compaction\n\n"
	content += fmt.Sprintf("Snapshot: %s\n\n", now.UTC().Format(time.RFC3339))
	for _, in := range top {
		content += fmt.Sprintf("- [%.0f%%] %s\n", in.Confidence*100, firstActionLine(in.Body))
	}

	dir := layout.InstinctsDir(projectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("session.PreserveInstincts: mkdir: %w", err)
	}
	path := filepath.Join(dir, "MEMORY.md")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("session.PreserveInstincts: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("session.PreserveInstincts: rename: %w", err)
	}
	return path, nil
}

// firstActionLine mirrors the inject/evolve helper so session has no
// cross-package import for one small function.
func firstActionLine(body string) string {
	lines := splitLines(body)
	inAction := false
	for _, t := range lines {
		if t == "## Action" {
			inAction = true
			continue
		}
		if inAction {
			if t == "" {
				continue
			}
			if len(t) >= 3 && t[:3] == "## " {
				break
			}
			return t
		}
	}
	for _, t := range lines {
		if t == "" || (len(t) >= 1 && t[0] == '#') {
			continue
		}
		return t
	}
	return "(no action)"
}

func splitLines(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, trimSpace(cur))
			cur = ""
			continue
		}
		if r == '\r' {
			continue
		}
		cur += string(r)
	}
	out = append(out, trimSpace(cur))
	return out
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
