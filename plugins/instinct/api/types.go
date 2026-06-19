package api

import "time"

// Scope mirrors plugins/memory/api.Scope. Duplicated here so the
// instinct plugin contract is independently complete (a minter
// plugin should not need to import the memory package).
type Scope struct {
	Level      string // "worktree" | "repo" | "global"
	WorktreeID string
	RepoName   string
}

// TraceBundle is one normalised observation passed from the host's
// observer (stdin ingest in v0.5 primary path, jsonl tail in opt-in
// beta) after redaction has stripped PII/secret patterns.
type TraceBundle struct {
	ID            string
	Source        string // stdin | session_log | test_failure | lint_output | commit_message | post_create_hook
	Scope         Scope
	CapturedAt    time.Time
	Content       string // redacted raw text
	EvidenceRef   string // session id, commit hash, test id, etc.
	SourceEventID string // idempotency token for retry-safe ingest
}

// InstinctCandidate is the unit a minter produces and the host
// considers for approval. Confidence is the minter's best estimate;
// the host clamps it to `instinct.confidence.sources.<source>` if
// the value is higher than the configured ceiling.
type InstinctCandidate struct {
	ID           string
	Rule         string
	Why          string
	HowToApply   string
	Domain       []string
	Scope        Scope
	Source       string
	Confidence   float64
	State        string // always "candidate" out of Mint
	SourceTraces []string
	CreatedAt    time.Time
	DedupeKey    string // sha256(normalize(rule + scope))
}

type MintReq struct {
	TraceBundles []TraceBundle
	Scope        Scope
}
type MintResp struct {
	// Candidates is plural by design: one trace bundle may yield
	// multiple candidates (e.g., a session log surfacing two
	// separate behavioural rules). Returning an empty slice is fine
	// when the bundle does not carry enough signal.
	Candidates []*InstinctCandidate
}
