package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The two optional side inputs the prompt-time selector reads, both
// operator-owned files named in .bough.yaml (config.InstinctSelect).
//
// Everything here is best-effort by design: this code runs on the
// UserPromptSubmit path, so a missing, unreadable or malformed file must
// leave its feature OFF rather than cost the operator their turn. The
// failure is silent on purpose — a hook that writes parse errors into the
// prompt has made every prompt worse to report a problem with a sidecar.

// manualExclusions reads the operator's list of instinct ids to stop
// pushing. Accepts either a JSON object — {"excluded": {"<id>":
// {"reason": "..."}}} — or a plain file of one id per line, because the
// register starts as a scratch list and grows reasons later; demanding
// the structured form up front would just mean it never gets written.
//
// Returns nil when there is nothing to exclude, which is the difference
// that matters here: an empty result must read as "exclude nothing", and
// it does.
func manualExclusions(path string) map[string]struct{} {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]struct{}{}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var doc struct {
			Excluded map[string]struct {
				Reason string `json:"reason"`
			} `json:"excluded"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			return nil
		}
		for id := range doc.Excluded {
			if id != "" {
				out[id] = struct{}{}
			}
		}
	} else {
		for _, line := range strings.Split(string(raw), "\n") {
			id := strings.TrimSpace(line)
			// '#' starts a comment so the file can say WHY without a schema.
			if id == "" || strings.HasPrefix(id, "#") {
				continue
			}
			out[id] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// aliasExpansions returns the English terms a prompt's non-English words
// stand for, so the lexical channel can score against vocabulary the
// corpus actually contains. A substring scan over a few dozen keys, which
// is cheap enough for the prompt path and is the only method that works
// on a language the tokenizer does not segment.
//
// The terms join the query as CONTEXT tokens: they are evidence about
// what the prompt is about, not identifiers the operator typed, so they
// feed the lexical channel and the relevance floor without being treated
// as exact hits.
func aliasExpansions(path, prompt string) []string {
	if path == "" || prompt == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var alias map[string][]string
	if json.Unmarshal(raw, &alias) != nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for term, english := range alias {
		// "_"-prefixed keys are the file's own notes to its reader.
		if term == "" || strings.HasPrefix(term, "_") {
			continue
		}
		if !strings.Contains(prompt, term) {
			continue
		}
		for _, en := range english {
			en = strings.ToLower(strings.TrimSpace(en))
			if en == "" {
				continue
			}
			if _, dup := seen[en]; dup {
				continue
			}
			seen[en] = struct{}{}
			out = append(out, en)
		}
	}
	// Map iteration is unordered; the tokens all land in sets downstream so
	// the order cannot change a selection, but a reproducible list is worth
	// having when someone is reading a log to work out what the query was.
	sort.Strings(out)
	return out
}
