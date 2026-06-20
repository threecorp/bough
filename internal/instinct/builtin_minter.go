package instinct

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/pkg/schema"
)

// BuiltinMinter is the in-process default `InstinctMinter`
// shipped with bough core. v0.5 wires it as the only minter so a
// fresh `instinct.enabled: true` profile produces candidates
// without anyone installing a plugin. v0.6+ plugin authors (SkillX
// / Anything2Skill adapters) provide alternatives by setting
// `instinct.default_instinct_minter` to their plugin name.
//
// The strategy is intentionally simple — first non-empty line
// becomes the rule, source / scope come from the TraceBundle, and
// dedupe key is sha256(normalise(rule + scope)). This is enough to
// dogfood the instinct subsystem before a smarter minter exists,
// without doing anything bough cannot defend in code review.
type BuiltinMinter struct{}

// NewBuiltinMinter returns a BuiltinMinter; the type holds no
// state, the constructor is here for symmetry with the plugin
// minters that v0.6+ adapters bring along.
func NewBuiltinMinter() *BuiltinMinter { return &BuiltinMinter{} }

// Mint walks TraceBundles and emits one candidate per non-empty
// bundle. The returned slice is plural by design (round 3 AI #1)
// so a future smarter minter can keep the same shape while
// emitting multiple candidates per bundle.
//
// Round 3 follow-up fix (CRITICAL #5): when a bundle carries its
// own non-zero Scope, the minter prefers it over the batch-level
// `scope` argument. Without this, a coordinator wiring multiple
// worktrees through one Ingest call would collapse all candidates
// to a single scope and quietly lose multi-worktree isolation.
func (m *BuiltinMinter) Mint(_ context.Context, bundles []schema.TraceBundle, scope schema.Scope) ([]*schema.InstinctCandidate, error) {
	out := make([]*schema.InstinctCandidate, 0, len(bundles))
	for _, b := range bundles {
		rule := firstNonEmptyLine(b.Content)
		if rule == "" {
			continue
		}
		candidateScope := scope
		if b.Scope.IsValid() {
			candidateScope = b.Scope
		}
		c := &schema.InstinctCandidate{
			ID:           bundleID(b),
			Rule:         rule,
			Scope:        candidateScope,
			Source:       b.Source,
			Confidence:   0.5, // ConfidencePolicy.ClampInitial lowers if source requires
			State:        schema.InstinctStateCandidate,
			SourceTraces: []string{b.ID},
			CreatedAt:    nonZero(b.CapturedAt),
			DedupeKey:    DedupeKey(rule, candidateScope),
		}
		out = append(out, c)
	}
	return out, nil
}

// DedupeKey is the canonical sha256(rule + scope) hash the host
// shares with backends. Exposed so unit tests and v0.6+ minters
// can produce the same value the host expects.
func DedupeKey(rule string, scope schema.Scope) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(rule))))
	h.Write([]byte("|"))
	h.Write([]byte(scope.Level))
	h.Write([]byte("|"))
	h.Write([]byte(scope.WorktreeID))
	h.Write([]byte("|"))
	h.Write([]byte(scope.RepoName))
	return hex.EncodeToString(h.Sum(nil))
}

func firstNonEmptyLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 240 {
				line = line[:240]
			}
			return line
		}
	}
	return ""
}

func bundleID(b schema.TraceBundle) string {
	if b.ID != "" {
		return b.ID
	}
	h := sha256.Sum256([]byte(b.Content + "|" + b.EvidenceRef))
	return hex.EncodeToString(h[:])[:16]
}

func nonZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}
