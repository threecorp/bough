// Package mem0 is the bough memory plugin for mem0
// (https://github.com/mem0ai/mem0). It speaks mem0's HTTP REST API
// for Store / Query / Forget / Export / Import and translates
// bough's Scope into mem0's user_id / session_id namespace.
//
// bough is not an agent memory system. bough is a per-worktree
// memory orchestration layer. mem0 is the memory engine; this
// plugin is the adapter the host coordinator routes through.
//
// Round 4 review notes wired in here:
//
//   - AI #1 + #2: Read fallback only. Store does NOT fall back to
//     the SQLite reference-fallback on error — that is what causes
//     the "split-brain" hazard the synthesis flagged as Blocker 1.
//     The host coordinator enforces this by consulting
//     `instinct.fallback_on_error` only from Query.
//
//   - AI #2: namespace mapping = repo → user_id, worktree →
//     session_id. The repo identity is the long-lived axis; the
//     worktree identity is the short-lived per-branch axis.
//
//   - AI #1 + #2: short-TTL read-through cache for Query, evicted
//     on any successful Store / Forget / Import.
//
//   - AI #2: advertise the v0.6 Capabilities cluster mem0 actually
//     supports (SemanticQuery / VectorSearch / NamespaceIsolation /
//     TTL / EventualConsistency / MetadataFilter / BulkImport).
package mem0

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// Version is reported through Health and Capabilities. Bumped per
// release; the v0.6.0 ship commit replaces the -dev suffix.
const Version = "v0.6.0-dev"

// Provider is the bough-side handle to a mem0 instance. The HTTP
// client is configurable so tests can swap in an httptest.Server.
// cache is the Ν-1.1e read-through Query cache; see cache.go for
// its bounds and invalidation contract.
type Provider struct {
	client    *http.Client
	endpoint  string        // mem0 base URL, e.g. https://api.mem0.ai
	apiKey    string        // mem0 organisation / API key, empty for self-hosted
	namespace string        // optional prefix injected into every user_id (multi-tenant routing)
	timeout   time.Duration // HTTP request timeout
	cache     *queryCache   // TTL + LRU cache for Query; Store / Forget / Import invalidate
}

// Config carries the values the plugin entry binary reads from
// environment variables before constructing the Provider. Keeping
// it a separate struct lets the binary stay tiny while making
// dependency injection for tests obvious.
type Config struct {
	Endpoint  string
	APIKey    string
	Namespace string
	Timeout   time.Duration
}

// New constructs a Provider. The HTTP client is wired with the
// configured timeout (default 10s) so a stuck mem0 endpoint does
// not block the host coordinator forever. The endpoint is required
// so a misconfigured `.bough.yaml` fails at Discover time rather
// than silently hitting localhost.
func New(cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("mem0 plugin: endpoint is empty (set BOUGH_MEMORY_MEM0_ENDPOINT in .bough.yaml memory_backends[*])")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Provider{
		client:    &http.Client{Timeout: cfg.Timeout},
		endpoint:  cfg.Endpoint,
		apiKey:    cfg.APIKey,
		namespace: cfg.Namespace,
		timeout:   cfg.Timeout,
		cache:     newQueryCache(),
	}, nil
}

// Close releases resources held by the Provider. The http.Client
// does not require explicit close; the receiver is kept for parity
// with the sqlite reference-fallback so the host's lifecycle code
// can treat both backends identically.
func (p *Provider) Close() error {
	return nil
}

// Health reports static plugin metadata. mem0 cloud and self-hosted
// disagree on the exact ping path, and the host's Discover flow
// already issues the first real RPC right after Health — letting
// that RPC surface a misconfigured endpoint keeps Health a cheap
// liveness signal rather than a duplicated probe. Fatal transport
// errors then show up at the first Store / Query rather than here.
func (p *Provider) Health(_ context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{BackendKind: "mem0", PluginVersion: Version}, nil
}

// Capabilities advertises mem0's feature set against the v0.6
// 17-field CapabilitiesResp. See CONTRACT.md for the rationale of
// each flag; the round 4 priority A12 cluster (SemanticQuery /
// VectorSearch / NamespaceIsolation / TTL / EventualConsistency /
// MetadataFilter / BulkExport / BulkImport) is lit up here.
// DedupeKey / SourceEventID stay false because mem0 has no native
// dedupe primitive — the host computes idempotency tokens and the
// SQLite reference-fallback owns that contract during Read fallback.
//
// SoftDelete is **false** (review #23 #8): mem0 cloud hard-deletes
// on DELETE /api/v1/memories/<id>/, so an Export-after-Forget will
// not return the forgotten row. v0.5 reference-fallback (sqlite)
// keeps the row with state=forgotten, the conformance suite gates
// the Export-after-Forget assertion on this flag so plugins with
// honest hard-delete semantics still pass without faking soft-delete
// in the wire layer.
//
// GraphQuery + TemporalQuery stay false: those land with the
// Graphiti plugin (= Ν-1.3 docs + skeleton, full implementation
// deferred to v0.6.x per round 4 AI #2).
func (p *Provider) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{
		// v0.5
		SemanticQuery:    true,
		GraphQuery:       false,
		BulkExport:       true,
		VectorSearch:     true,
		SupportsMetadata: true,
		PluginVersion:    Version,
		// v0.6 (= round 4 priority A12)
		TemporalQuery:       false,
		MetadataFilter:      true,
		NamespaceIsolation:  true,
		SoftDelete:          false,
		BulkImport:          true,
		DedupeKey:           false,
		SourceEventID:       false,
		TTL:                 true,
		EventualConsistency: true,
		MaxBatchSize:        0,
		MaxQueryTokens:      0,
	}, nil
}

// Store routes the upsert through mem0's add-memories endpoint.
// Round 4 AI #1 + #2: Store does NOT fall back to the SQLite
// reference-fallback on error — fallback_on_error is consulted only
// from Query. We pack every Instinct field bough cares about into
// mem0's metadata map so Export → Import round-trips losslessly.
func (p *Provider) Store(ctx context.Context, req *memapi.StoreReq) (*memapi.StoreResp, error) {
	k := p.scopeToMem0(req.Instinct.Scope)
	body := mem0AddReq{
		Messages: []mem0Message{
			{Role: "user", Content: req.Instinct.Rule},
		},
		UserID:    k.UserID,
		SessionID: k.SessionID,
		Metadata:  instinctToMetadata(req.Instinct, req.DedupeKey, req.SourceEventID),
	}
	var addResp mem0Results
	if err := p.doJSON(ctx, http.MethodPost, "/api/v1/memories/", body, &addResp); err != nil {
		return nil, fmt.Errorf("mem0 Store: %w (instinct=%s)", err, req.Instinct.ID)
	}
	storedID := req.Instinct.ID
	wasUpsert := false
	for _, r := range addResp.Results {
		if r.ID != "" {
			storedID = r.ID
		}
		// mem0 reports event ∈ {ADD, UPDATE, DELETE, NONE}. UPDATE
		// + NONE both mean the row already existed.
		if r.Event == "UPDATE" || r.Event == "NONE" {
			wasUpsert = true
		}
	}
	// Round 4 AI #1 + #2: drop every cached Query that targeted the
	// scope we just wrote so the next Query hits mem0 fresh.
	p.cache.invalidateScope(req.Instinct.Scope)
	return &memapi.StoreResp{StoredID: storedID, WasUpsert: wasUpsert}, nil
}

// Query searches mem0 for matching instincts. The host's Read
// fallback path consults this method; on error the coordinator can
// replay against the SQLite reference-fallback (= v0.5.1 wire).
// Results below MinConfidence are dropped here so the host's budget
// aggregator never sees them; MaxResults caps the upstream call so
// we do not pay for unused rows.
func (p *Provider) Query(ctx context.Context, req *memapi.QueryReq) (*memapi.QueryResp, error) {
	cacheK := p.cacheKeyFor(req)
	if cached, ok := p.cache.get(cacheK); ok {
		return cached, nil
	}
	// Review #23 #12: snapshot the cache generation BEFORE issuing
	// the HTTP roundtrip so a concurrent Store / Forget / Import
	// can rescind the would-be put.
	gen := p.cache.currentGen()
	k := p.scopeToMem0(req.Scope)
	body := mem0SearchReq{
		Query:     req.Term,
		UserID:    k.UserID,
		SessionID: k.SessionID,
		Limit:     req.MaxResults,
	}
	var searchResp mem0Results
	if err := p.doJSON(ctx, http.MethodPost, "/api/v1/memories/search/", body, &searchResp); err != nil {
		return nil, fmt.Errorf("mem0 Query: %w", err)
	}
	results := make([]memapi.QueryResult, 0, len(searchResp.Results))
	for _, m := range searchResp.Results {
		if m.Score < req.MinConfidence {
			continue
		}
		inst := mem0MemoryToInstinct(m, req.Scope)
		results = append(results, memapi.QueryResult{
			Instinct:        inst,
			Score:           m.Score,
			EstimatedTokens: estimateTokens(inst.Rule, inst.Why, inst.HowToApply),
		})
	}
	resp := &memapi.QueryResp{Results: results}
	_ = p.cache.put(cacheK, resp, gen)
	return resp, nil
}

// Forget deletes the named memory from mem0. The capabilities
// advertise SoftDelete=true because the row stops being queryable
// from the host's perspective — mem0 itself implements this with a
// hard delete, but the contract still holds.
func (p *Provider) Forget(ctx context.Context, req *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	if req.ID == "" {
		return nil, errors.New("mem0 Forget: id is empty")
	}
	if err := p.doJSON(ctx, http.MethodDelete, "/api/v1/memories/"+url.PathEscape(req.ID)+"/", nil, nil); err != nil {
		return nil, fmt.Errorf("mem0 Forget: %w (id=%s)", err, req.ID)
	}
	// Round 4 AI #1 + #2: forget on this scope invalidates the cache
	// so a follow-up Query never returns the freshly removed row.
	p.cache.invalidateScope(req.Scope)
	return &memapi.ForgetResp{}, nil
}

// Export walks mem0's get-all endpoint for the configured Scope
// and emits the same YAML / JSONL shape the sqlite reference-
// fallback produces, so Import round-trips work across backends.
//
// Review #23 #15: the call paginates until mem0 returns a
// less-than-page-size batch. mem0's v1 list endpoint defaults to
// a page size around 100, so a single GET silently truncated
// long scopes in v0.5.1 / v0.6.0-pre. The loop now drains every
// page before the YAML / JSONL builder runs.
func (p *Provider) Export(ctx context.Context, req *memapi.ExportReq) (*memapi.ExportResp, error) {
	if req.Format != "yaml" && req.Format != "jsonl" {
		return nil, fmt.Errorf("mem0 Export: format %q unsupported in v0.6", req.Format)
	}
	k := p.scopeToMem0(req.Scope)
	const pageSize = 100
	listResp := mem0Results{}
	for page := 1; ; page++ {
		path := fmt.Sprintf("/api/v1/memories/?user_id=%s&page=%d&page_size=%d",
			url.QueryEscape(k.UserID), page, pageSize)
		if k.SessionID != "" {
			path += "&session_id=" + url.QueryEscape(k.SessionID)
		}
		var pageResp mem0Results
		if err := p.doJSON(ctx, http.MethodGet, path, nil, &pageResp); err != nil {
			return nil, fmt.Errorf("mem0 Export page %d: %w", page, err)
		}
		listResp.Results = append(listResp.Results, pageResp.Results...)
		if len(pageResp.Results) < pageSize {
			break
		}
	}
	var b strings.Builder
	contentType := "text/yaml"
	if req.Format == "jsonl" {
		contentType = "application/jsonl"
	}
	for _, m := range listResp.Results {
		inst := mem0MemoryToInstinct(m, req.Scope)
		if req.StateFilter != "" && string(inst.State) != req.StateFilter {
			continue
		}
		// Review #23 #11: pull dedupe_key / source_event_id back
		// off the mem0 metadata so the Export captures the
		// idempotency tokens the host originally supplied.
		dk, _ := m.Metadata["bough_dedupe_key"].(string)
		seid, _ := m.Metadata["bough_source_event_id"].(string)
		writeInstinct(&b, req.Format, inst, dk, seid)
	}
	return &memapi.ExportResp{Payload: []byte(b.String()), ContentType: contentType}, nil
}

// Import replays a previously-exported payload through Store. We
// reuse the sqlite-style parser inline rather than calling into
// the sqlite package — bough plugins do not import each other so
// every backend stays swappable. v0.6.x will lift the parsers into
// pkg/memorywire/ once a second plugin needs them.
//
// Review #23 #11: the parser returns (Instinct, dedupe_key,
// source_event_id) so Import can replay both the row and the
// idempotency tokens the original Export captured. A row missing
// dedupe_key passes through with an empty token (= the v0.5 wire
// shape); a row carrying one preserves it across the round trip.
func (p *Provider) Import(ctx context.Context, req *memapi.ImportReq) (*memapi.ImportResp, error) {
	if req.Format != "yaml" && req.Format != "jsonl" {
		return nil, fmt.Errorf("mem0 Import: format %q unsupported in v0.6", req.Format)
	}
	if len(req.Payload) == 0 {
		return &memapi.ImportResp{}, nil
	}
	var rows []importRow
	if req.Format == "jsonl" {
		rows = parseExportedJSONL(req.Payload)
	} else {
		rows = parseExportedYAML(req.Payload)
	}
	resp := &memapi.ImportResp{}
	for _, row := range rows {
		if row.Instinct.ID == "" {
			resp.SkippedCount++
			continue
		}
		storeResp, err := p.Store(ctx, &memapi.StoreReq{
			Instinct:        row.Instinct,
			DedupeKey:       row.DedupeKey,
			SourceEventID:   row.SourceEventID,
			UpsertSemantics: true,
		})
		if err != nil {
			resp.SkippedCount++
			continue
		}
		if storeResp.WasUpsert {
			resp.UpsertedCount++
		} else {
			resp.ImportedCount++
		}
	}
	return resp, nil
}

// ----- helpers -----

// instinctToMetadata packs every Instinct field bough cares about
// into a flat map. Round 4: dedupe_key / source_event_id are stored
// here too so an Export → Import cycle can rebuild the host-side
// idempotency tokens — mem0 itself ignores them.
//
// Review #23 #10: time.Time zero values are skipped so a freshly
// constructed Instinct never serialises a -6.7e12 epoch into mem0
// metadata (which would round-trip back as a garbage "year 0001"
// timestamp).
func instinctToMetadata(inst memapi.Instinct, dedupeKey, sourceEventID string) map[string]any {
	domain, _ := json.Marshal(inst.Domain)
	traces, _ := json.Marshal(inst.SourceTraces)
	evidence, _ := json.Marshal(inst.EvidenceRefs)
	out := map[string]any{
		"bough_id":                inst.ID,
		"bough_rule":              inst.Rule,
		"bough_why":               inst.Why,
		"bough_how_to_apply":      inst.HowToApply,
		"bough_domain":            string(domain),
		"bough_source":            inst.Source,
		"bough_state":             inst.State,
		"bough_confidence":        inst.Confidence,
		"bough_hits":              inst.Hits,
		"bough_dedupe_key":        dedupeKey,
		"bough_source_event_id":   sourceEventID,
		"bough_scope_level":       inst.Scope.Level,
		"bough_scope_worktree_id": inst.Scope.WorktreeID,
		"bough_scope_repo_name":   inst.Scope.RepoName,
		"bough_source_traces":     string(traces),
		"bough_evidence_refs":     string(evidence),
		"bough_metadata_json":     inst.MetadataJSON,
	}
	if !inst.CreatedAt.IsZero() {
		out["bough_created_at"] = inst.CreatedAt.Unix()
	}
	if !inst.LastHitAt.IsZero() {
		out["bough_last_hit_at"] = inst.LastHitAt.Unix()
	}
	return out
}

// mem0MemoryToInstinct rebuilds a memapi.Instinct from a mem0 row.
// The metadata map is the source of truth; we fall back to the
// `memory` content for Rule and to the caller-supplied scope when
// the metadata is missing fields (= mem0-side direct writes from
// another tool).
func mem0MemoryToInstinct(m mem0Memory, fallbackScope memapi.Scope) memapi.Instinct {
	inst := memapi.Instinct{
		ID:    m.ID,
		Rule:  m.Memory,
		Scope: fallbackScope,
	}
	if m.Metadata == nil {
		return inst
	}
	if s, ok := m.Metadata["bough_id"].(string); ok && s != "" {
		inst.ID = s
	}
	if s, ok := m.Metadata["bough_rule"].(string); ok && s != "" {
		inst.Rule = s
	}
	if s, ok := m.Metadata["bough_why"].(string); ok {
		inst.Why = s
	}
	if s, ok := m.Metadata["bough_how_to_apply"].(string); ok {
		inst.HowToApply = s
	}
	if s, ok := m.Metadata["bough_source"].(string); ok {
		inst.Source = s
	}
	if s, ok := m.Metadata["bough_state"].(string); ok {
		inst.State = s
	}
	if f, ok := m.Metadata["bough_confidence"].(float64); ok {
		inst.Confidence = f
	}
	if f, ok := m.Metadata["bough_hits"].(float64); ok {
		inst.Hits = int(f)
	}
	if s, ok := m.Metadata["bough_dedupe_key"].(string); ok {
		inst.DedupeKey = s
	}
	if s, ok := m.Metadata["bough_scope_level"].(string); ok {
		inst.Scope.Level = s
	}
	if s, ok := m.Metadata["bough_scope_worktree_id"].(string); ok {
		inst.Scope.WorktreeID = s
	}
	if s, ok := m.Metadata["bough_scope_repo_name"].(string); ok {
		inst.Scope.RepoName = s
	}
	if s, ok := m.Metadata["bough_metadata_json"].(string); ok {
		inst.MetadataJSON = s
	}
	if s, ok := m.Metadata["bough_domain"].(string); ok && s != "" {
		_ = json.Unmarshal([]byte(s), &inst.Domain)
	}
	if s, ok := m.Metadata["bough_source_traces"].(string); ok && s != "" {
		_ = json.Unmarshal([]byte(s), &inst.SourceTraces)
	}
	if s, ok := m.Metadata["bough_evidence_refs"].(string); ok && s != "" {
		_ = json.Unmarshal([]byte(s), &inst.EvidenceRefs)
	}
	if f, ok := m.Metadata["bough_created_at"].(float64); ok && f > 0 {
		inst.CreatedAt = time.Unix(int64(f), 0).UTC()
	}
	if f, ok := m.Metadata["bough_last_hit_at"].(float64); ok && f > 0 {
		inst.LastHitAt = time.Unix(int64(f), 0).UTC()
	}
	return inst
}

// writeInstinct emits one row into the export buffer. YAML/JSONL
// shapes mirror the sqlite reference-fallback exactly so Import
// can be backend-agnostic.
//
// Review #23 #10 / #11: zero time.Time is skipped so the JSONL
// path does not emit a -6.7e12 epoch; dedupe_key + source_event_id
// land in the payload so the round-trip preserves the v0.5.1 wire
// (= what sqlite already records in its `instincts` table).
func writeInstinct(b *strings.Builder, format string, inst memapi.Instinct, dedupeKey, sourceEventID string) {
	if format == "jsonl" {
		line := map[string]any{
			"id":              inst.ID,
			"rule":            inst.Rule,
			"why":             inst.Why,
			"scope_level":     inst.Scope.Level,
			"scope_id":        scopeIDFor(inst.Scope),
			"source":          inst.Source,
			"confidence":      inst.Confidence,
			"state":           inst.State,
			"dedupe_key":      dedupeKey,
			"source_event_id": sourceEventID,
		}
		if !inst.CreatedAt.IsZero() {
			line["created_at"] = inst.CreatedAt.Unix()
		}
		raw, _ := json.Marshal(line)
		b.Write(raw)
		b.WriteString("\n")
		return
	}
	fmt.Fprintf(b, "- id: %s\n  rule: %s\n  scope_level: %s\n  scope_id: %s\n  source: %s\n  confidence: %.2f\n  state: %s\n",
		inst.ID, escapeYAML(inst.Rule), inst.Scope.Level, scopeIDFor(inst.Scope), inst.Source, inst.Confidence, inst.State)
	if dedupeKey != "" {
		fmt.Fprintf(b, "  dedupe_key: %s\n", dedupeKey)
	}
	if sourceEventID != "" {
		fmt.Fprintf(b, "  source_event_id: %s\n", sourceEventID)
	}
}

// scopeIDFor mirrors sqlite's scopeID encoding so YAML / JSONL
// payloads round-trip across both backends.
func scopeIDFor(s memapi.Scope) string {
	switch s.Level {
	case "worktree":
		return s.RepoName + "/" + s.WorktreeID
	case "repo":
		return s.RepoName
	default:
		return ""
	}
}

func escapeYAML(s string) string {
	if strings.ContainsAny(s, ":#@\n") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func unescapeYAML(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
	}
	return s
}

// splitScopeID inverts scopeIDFor so a YAML payload restores the
// original Scope.WorktreeID / RepoName split.
func splitScopeID(level, sid string) (worktreeID, repoName string) {
	switch level {
	case "worktree":
		if idx := strings.LastIndex(sid, "/"); idx >= 0 {
			return sid[idx+1:], sid[:idx]
		}
		return sid, ""
	case "repo":
		return "", sid
	default:
		return "", ""
	}
}

// importRow couples the parsed Instinct with the idempotency
// tokens that ride alongside it on the wire. Review #23 #11: an
// Import that drops dedupe_key / source_event_id silently breaks
// every subsequent Store-by-dedupe path.
type importRow struct {
	Instinct      memapi.Instinct
	DedupeKey     string
	SourceEventID string
}

// parseExportedJSONL inverts writeInstinct's JSONL emit.
func parseExportedJSONL(payload []byte) []importRow {
	var out []importRow
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			ID            string  `json:"id"`
			Rule          string  `json:"rule"`
			Why           string  `json:"why"`
			ScopeLevel    string  `json:"scope_level"`
			ScopeID       string  `json:"scope_id"`
			Source        string  `json:"source"`
			Confidence    float64 `json:"confidence"`
			State         string  `json:"state"`
			CreatedAt     int64   `json:"created_at"`
			DedupeKey     string  `json:"dedupe_key"`
			SourceEventID string  `json:"source_event_id"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			out = append(out, importRow{})
			continue
		}
		worktreeID, repoName := splitScopeID(row.ScopeLevel, row.ScopeID)
		inst := memapi.Instinct{
			ID:         row.ID,
			Rule:       row.Rule,
			Why:        row.Why,
			Scope:      memapi.Scope{Level: row.ScopeLevel, WorktreeID: worktreeID, RepoName: repoName},
			Source:     row.Source,
			Confidence: row.Confidence,
			State:      row.State,
		}
		if row.CreatedAt > 0 {
			inst.CreatedAt = time.Unix(row.CreatedAt, 0).UTC()
		}
		out = append(out, importRow{Instinct: inst, DedupeKey: row.DedupeKey, SourceEventID: row.SourceEventID})
	}
	return out
}

// parseExportedYAML inverts writeInstinct's YAML emit. The shape
// is a fixed 7-field block (+ optional dedupe_key / source_event_id
// from review #23 #11) per row, so a line-based scan is sufficient
// and avoids pulling in a full YAML library.
func parseExportedYAML(payload []byte) []importRow {
	var out []importRow
	var cur importRow
	inBlock := false
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "- id:") {
			if inBlock {
				out = append(out, cur)
			}
			cur = importRow{}
			inBlock = true
			cur.Instinct.ID = strings.TrimSpace(strings.TrimPrefix(line, "- id:"))
			continue
		}
		if !inBlock {
			continue
		}
		switch {
		case strings.HasPrefix(line, "  rule:"):
			cur.Instinct.Rule = unescapeYAML(strings.TrimSpace(strings.TrimPrefix(line, "  rule:")))
		case strings.HasPrefix(line, "  scope_level:"):
			cur.Instinct.Scope.Level = strings.TrimSpace(strings.TrimPrefix(line, "  scope_level:"))
		case strings.HasPrefix(line, "  scope_id:"):
			sid := strings.TrimSpace(strings.TrimPrefix(line, "  scope_id:"))
			cur.Instinct.Scope.WorktreeID, cur.Instinct.Scope.RepoName = splitScopeID(cur.Instinct.Scope.Level, sid)
		case strings.HasPrefix(line, "  source:"):
			cur.Instinct.Source = strings.TrimSpace(strings.TrimPrefix(line, "  source:"))
		case strings.HasPrefix(line, "  confidence:"):
			v := strings.TrimSpace(strings.TrimPrefix(line, "  confidence:"))
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				cur.Instinct.Confidence = f
			}
		case strings.HasPrefix(line, "  state:"):
			cur.Instinct.State = strings.TrimSpace(strings.TrimPrefix(line, "  state:"))
		case strings.HasPrefix(line, "  dedupe_key:"):
			cur.DedupeKey = strings.TrimSpace(strings.TrimPrefix(line, "  dedupe_key:"))
		case strings.HasPrefix(line, "  source_event_id:"):
			cur.SourceEventID = strings.TrimSpace(strings.TrimPrefix(line, "  source_event_id:"))
		}
	}
	if inBlock {
		out = append(out, cur)
	}
	return out
}
