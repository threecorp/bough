package api

import "time"

// Scope addresses one of three lifetime tiers. WorktreeID is set only
// for Level="worktree"; RepoName is set for Level="repo" and
// Level="global". The host's promote.go controller is what moves an
// instinct between tiers — backends never invent or change scope.
type Scope struct {
	Level      string // "worktree" | "repo" | "global"
	WorktreeID string
	RepoName   string
}

// Instinct is the row a backend persists. Fields mirror
// pkg/schema/Instinct so the coordinator can move data between the
// two without translation. MetadataJSON is a plugin-defined byte
// slot reserved on the wire (and on the SQLite reference-fallback
// schema) so v0.6+ plugins can attach MCP resource templates, skill
// frontmatter, etc. without a wire bump.
type Instinct struct {
	ID            string
	Rule          string
	Why           string
	HowToApply    string
	Domain        []string
	Scope         Scope
	Source        string // explicit_user_feedback | test_failure | session_summary | llm_only | ...
	Confidence    float64
	State         string // candidate | active | archived | forgotten
	Hits          int
	LastHitAt     time.Time
	CreatedAt     time.Time
	SourceTraces  []string
	EvidenceRefs  []string
	DedupeKey     string // sha256(normalize(rule + scope))
	MetadataJSON  string // plugin-defined free-form JSON; opaque to host on v0.5
}

type HealthReq struct{}
type HealthResp struct {
	BackendKind   string // "sqlite" | "mem0" | "graphiti" | ...
	PluginVersion string
}

type CapabilitiesResp struct {
	SemanticQuery    bool // dense vector + reranker (v0.6+)
	GraphQuery       bool // entity / relation query (v0.6+)
	BulkExport       bool // streaming export (v0.6+)
	VectorSearch     bool // pure ANN backend (v0.6+)
	SupportsMetadata bool // honours Instinct.MetadataJSON
	PluginVersion    string
}

type StoreReq struct {
	Instinct        Instinct
	DedupeKey       string // sha256(normalize(rule + scope)); host may pre-compute
	SourceEventID   string // observer retry / CI rerun idempotency token
	UpsertSemantics bool   // true: existing ID → hits++ / confidence reinforce
}
type StoreResp struct {
	StoredID  string
	WasUpsert bool // true if existing row updated, false if new row inserted
}

type QueryReq struct {
	Term          string
	Scope         Scope
	MaxResults    int     // host-imposed hard limit (validator default 12)
	MaxTokens     int     // host-imposed hard limit (validator default 4000)
	MinConfidence float64
}
type QueryResult struct {
	Instinct        Instinct
	Score           float64
	EstimatedTokens int  // backend's best estimate; host aggregates
	Truncated       bool // true if max_tokens cap elided content
}
type QueryResp struct {
	Results []QueryResult
}

type ForgetReq struct {
	ID     string
	Scope  Scope
	Reason string
}
type ForgetResp struct{}

type ExportReq struct {
	Format      string // "yaml" | "jsonl"  (v0.6: + "claude-skills" / "agent-skill" / "mcp")
	Scope       Scope  // empty Level = all scopes
	StateFilter string // "" = all; otherwise "active" | "candidate" | ...
}
type ExportResp struct {
	Payload     []byte
	ContentType string
}

type ImportReq struct {
	Format            string
	Payload           []byte
	OverwriteExisting bool
}
type ImportResp struct {
	ImportedCount int
	UpsertedCount int
	SkippedCount  int
}
