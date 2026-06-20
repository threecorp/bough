package schema

import "time"

// InstinctState tracks where an instinct sits in its lifecycle. The
// state machine is:
//
//	candidate → active   (after `bough instinct approve` or after
//	                      the hybrid mint policy auto-promotes)
//	candidate → forgotten (auto-forget at candidate_ttl_days)
//	active    → archived (decay scheduler hits a low-confidence row)
//	active    → forgotten (explicit `bough instinct forget`)
//	archived  → forgotten (eventually)
//
// Forget is soft: the row stays in the backend for audit. Hard
// deletion is never performed by the backend; the coordinator's
// decay_scheduler is the only piece that issues a hard delete and
// even then only after a retention window.
type InstinctState string

const (
	InstinctStateUnspecified InstinctState = ""
	InstinctStateCandidate   InstinctState = "candidate"
	InstinctStateActive      InstinctState = "active"
	InstinctStateArchived    InstinctState = "archived"
	InstinctStateForgotten   InstinctState = "forgotten"
)

// InstinctCandidate is what a minter produces. It carries the
// minimum data the host's poisoning guard needs to dedupe (DedupeKey
// = sha256(normalize(Rule + Scope))), to score (Confidence + Source
// for the source-aware ceiling), and to trace back (SourceTraces).
//
// State is always InstinctStateCandidate on a freshly-minted row;
// the coordinator transitions it to Active on approval and emits a
// promoted Instinct (see below) that carries Hits / LastHitAt /
// EvidenceRefs.
type InstinctCandidate struct {
	ID           string
	Rule         string
	Why          string
	HowToApply   string
	Domain       []string
	Scope        Scope
	Source       TraceSource
	Confidence   float64
	State        InstinctState
	SourceTraces []string
	CreatedAt    time.Time
	DedupeKey    string // sha256(normalize(Rule + Scope.Level + Scope.WorktreeID + Scope.RepoName))
}

// Instinct is the active row a memory backend persists. It embeds
// InstinctCandidate and adds the post-approval fields Hits (number
// of dedupe-key matches the backend has reinforced),
// LastHitAt (most recent reinforce), and EvidenceRefs (an append-
// only log of evidence references accumulated across reinforcement
// events).
//
// Metadata is the round 3 AI #2 escape hatch: plugin-defined JSON
// the host treats as opaque on v0.5. v0.6+ memory backends can
// stash MCP resource templates, agent-skill frontmatter, or any
// other plugin-specific metadata here without a wire bump. The
// SQLite reference-fallback schema reserves a `metadata TEXT`
// column for exactly this.
type Instinct struct {
	InstinctCandidate
	Hits         int
	LastHitAt    time.Time
	EvidenceRefs []string
	Metadata     []byte // plugin-defined JSON; opaque to host on v0.5
}
