// Package sqlite is the bough memory reference-fallback backend.
//
// bough is not a memory system; bough is a per-worktree memory
// orchestration layer. This SQLite backend exists for:
//
//   - offline fallback (a fresh `git clone` of bough works without
//     mem0 or Graphiti running),
//   - conformance fixture (round 3 AI #3 conformance suite drives
//     it to validate the v0.5 contract),
//   - plugin author reference (community plugins for mem0 /
//     Graphiti / Letta can read this as the minimal-correct shape),
//   - GitHub Release single-binary baseline (no external service
//     required to install bough),
//   - audit fallback when an external backend is unreachable.
//
// It is NOT a competitor to mem0 / Graphiti — `role:
// reference-fallback` in `.bough.yaml` is the canonical naming.
//
// Driver choice: modernc.org/sqlite (pure Go). CGO is intentionally
// avoided so the binary cross-compiles to darwin / linux × amd64 /
// arm64 without a C toolchain in CI. The trade-off is some
// performance vs github.com/mattn/go-sqlite3, which the
// reference-fallback role makes acceptable.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

//go:embed migrations/0001_init.sql
var migrationInit string

// Version is reported through Health and Capabilities. Bumped per
// plugin release so `bough memory status` can flag a version skew.
const Version = "v0.5.0"

// Provider implements memapi.MemoryBackend against a single SQLite
// file. The struct is intentionally tiny: every field is set by
// New, and the db handle is concurrent-safe so the gRPC dispatch
// shares one Provider across all RPC goroutines.
type Provider struct {
	db   *sql.DB
	path string
}

// New opens or creates the SQLite database at `path`. Apart from
// applying the v0.5 PRAGMAs (WAL + busy_timeout) and the initial
// migration, the function does no work — every method-side cost
// (parse, FTS, etc.) deferred to the actual RPC call.
//
// The plan §3.5 hard-locks `journal_mode=WAL` and
// `busy_timeout=5000` ms as round 3 AI #3 conformance requirements.
// Without WAL the concurrency conformance test fails on
// "database is locked"; without busy_timeout the test flakes under
// CI load.
func New(path string) (*Provider, error) {
	if path == "" {
		return nil, errors.New("sqlite: path is empty")
	}
	// modernc/sqlite accepts DSN-style options through the URI.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %s: %w", path, err)
	}
	// modernc/sqlite serialises across one connection cheaply; cap
	// the pool low so the WAL writer queue stays predictable.
	db.SetMaxOpenConns(runtime.NumCPU() * 2)
	db.SetMaxIdleConns(2)

	if _, err := db.Exec(migrationInit); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite migration init: %w", err)
	}
	return &Provider{db: db, path: path}, nil
}

// Close shuts down the DB handle. The plugin binary defers this
// when its gRPC server stops.
func (p *Provider) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

// Health is the lightweight liveness probe.
func (p *Provider) Health(ctx context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	if err := p.db.PingContext(ctx); err != nil {
		return &memapi.HealthResp{}, err
	}
	return &memapi.HealthResp{BackendKind: "sqlite", PluginVersion: Version}, nil
}

// Capabilities advertises the v0.5 feature set. SupportsMetadata
// is true because the `metadata` column is committed; everything
// else is false on v0.5 and lights up in v0.6+ plugins.
func (p *Provider) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{
		SemanticQuery:    false,
		GraphQuery:       false,
		BulkExport:       false,
		VectorSearch:     false,
		SupportsMetadata: true,
		PluginVersion:    Version,
	}, nil
}

// Store is the upsert. If a row already exists at (dedupe_key,
// scope_level, scope_id) we increment hits and refresh last_hit_at
// + confidence rather than creating a new row.
func (p *Provider) Store(ctx context.Context, req *memapi.StoreReq) (*memapi.StoreResp, error) {
	i := req.Instinct
	if i.ID == "" && req.DedupeKey != "" {
		i.ID = req.DedupeKey
	}
	if i.ID == "" {
		return nil, errors.New("sqlite Store: instinct.id and dedupe_key both empty")
	}
	if i.Scope.Level == "" {
		return nil, errors.New("sqlite Store: scope.level is empty")
	}

	// Idempotency on source_event_id (round 3 AI #1): if we have
	// seen this exact event before, the call is a no-op upsert.
	if req.SourceEventID != "" {
		var existingID string
		err := p.db.QueryRowContext(ctx,
			`SELECT id FROM instincts WHERE source_event_id = ?1 LIMIT 1`,
			req.SourceEventID,
		).Scan(&existingID)
		if err == nil {
			return &memapi.StoreResp{StoredID: existingID, WasUpsert: true}, nil
		}
		if err != sql.ErrNoRows {
			return nil, fmt.Errorf("sqlite Store source_event_id lookup: %w", err)
		}
	}

	// Dedupe-key match: same rule at same scope folds into an upsert.
	// CRITICAL (round 3 follow-up fix): when a dedupe match exists but
	// the caller did not request UpsertSemantics, the previous
	// INSERT OR REPLACE fall-through could either insert a duplicate
	// row sharing the same dedupe_key (silent contract violation) or
	// overwrite the existing row losing hits / last_hit_at / evidence.
	// We now refuse the call with an explicit error so the caller can
	// either retry with UpsertSemantics=true or pick a fresh
	// dedupe_key.
	var existingID string
	if req.DedupeKey != "" {
		err := p.db.QueryRowContext(ctx,
			`SELECT id FROM instincts WHERE dedupe_key = ?1 AND scope_level = ?2 AND scope_id = ?3 LIMIT 1`,
			req.DedupeKey, i.Scope.Level, scopeID(i.Scope),
		).Scan(&existingID)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("sqlite Store dedupe lookup: %w", err)
		}
	}
	if existingID != "" {
		if !req.UpsertSemantics {
			return nil, fmt.Errorf("sqlite Store: dedupe match for %s/%s at id=%s (set UpsertSemantics=true to reinforce, or change the rule)",
				i.Scope.Level, scopeID(i.Scope), existingID)
		}
		// Reinforce path. Include the State field so the decay
		// scheduler can transition an active row to archived
		// without needing a separate UpdateState RPC. Round 3
		// follow-up fix to CRITICAL #4.
		if _, err := p.db.ExecContext(ctx,
			`UPDATE instincts SET hits = hits + 1, confidence = MAX(confidence, ?1), last_hit_at = ?2, state = ?3 WHERE id = ?4`,
			i.Confidence, time.Now().Unix(), nonZeroState(i.State), existingID,
		); err != nil {
			return nil, fmt.Errorf("sqlite Store reinforce: %w", err)
		}
		return &memapi.StoreResp{StoredID: existingID, WasUpsert: true}, nil
	}

	// Fresh insert. Use INSERT OR REPLACE so an explicit retry at
	// the same ID is also safe.
	evidenceJSON, _ := json.Marshal(i.EvidenceRefs)
	tracesJSON, _ := json.Marshal(i.SourceTraces)
	if _, err := p.db.ExecContext(ctx, `
INSERT OR REPLACE INTO instincts (
  id, rule, why, how_to_apply, domain_csv,
  scope_level, scope_id, source, source_event_id, dedupe_key,
  state, confidence, hits, last_hit_at, created_at,
  evidence_refs, source_traces, metadata
) VALUES (
  ?1, ?2, ?3, ?4, ?5,
  ?6, ?7, ?8, ?9, ?10,
  ?11, ?12, ?13, ?14, ?15,
  ?16, ?17, ?18
)`,
		i.ID, i.Rule, i.Why, i.HowToApply, joinCSV(i.Domain),
		i.Scope.Level, scopeID(i.Scope), i.Source, req.SourceEventID, req.DedupeKey,
		nonZeroState(i.State), i.Confidence, i.Hits, unixOf(i.LastHitAt), unixOrNow(i.CreatedAt),
		string(evidenceJSON), string(tracesJSON), i.MetadataJSON,
	); err != nil {
		return nil, fmt.Errorf("sqlite Store insert: %w", err)
	}
	return &memapi.StoreResp{StoredID: i.ID, WasUpsert: false}, nil
}

// Query searches by FTS MATCH when Term is non-empty, otherwise
// falls back to a scope-filtered scan ordered by confidence.
// MaxResults and MaxTokens are honoured at the backend level so
// the host's budget aggregator gets honest numbers to work with.
func (p *Provider) Query(ctx context.Context, req *memapi.QueryReq) (*memapi.QueryResp, error) {
	var (
		rows *sql.Rows
		err  error
	)
	max := req.MaxResults
	if max <= 0 {
		max = 12
	}
	if req.Term == "" {
		rows, err = p.db.QueryContext(ctx, `
SELECT id, rule, why, how_to_apply, domain_csv,
       scope_level, scope_id, source, source_event_id, dedupe_key,
       state, confidence, hits, last_hit_at, created_at,
       evidence_refs, source_traces, metadata
FROM instincts
WHERE (?1 = '' OR scope_level = ?1)
  AND (?2 = '' OR scope_id    = ?2)
  AND state != 'forgotten'
  AND confidence >= ?3
ORDER BY confidence DESC, last_hit_at DESC
LIMIT ?4`,
			req.Scope.Level, scopeID(req.Scope), req.MinConfidence, max,
		)
	} else {
		// FTS5 MATCH: normalise the user-supplied term to a safe
		// charset (letters, digits, spaces, hyphen, underscore)
		// before wrapping in a phrase quote. CRITICAL fix from
		// round 3 follow-up review: stripping only ASCII `"` left
		// FTS5 query-language injection paths open via unicode
		// quotes, NEAR, column filters, and null bytes. The
		// normalised charset cannot express any FTS5 metasyntax.
		ftsTerm := normalizeFTSTerm(req.Term)
		if ftsTerm == "" {
			// Term is empty after normalisation (e.g. all punctuation);
			// fall through to a scope-only scan instead of a MATCH that
			// would error.
			return p.queryWithoutTerm(ctx, req, max)
		}
		rows, err = p.db.QueryContext(ctx, `
SELECT i.id, i.rule, i.why, i.how_to_apply, i.domain_csv,
       i.scope_level, i.scope_id, i.source, i.source_event_id, i.dedupe_key,
       i.state, i.confidence, i.hits, i.last_hit_at, i.created_at,
       i.evidence_refs, i.source_traces, i.metadata
FROM instincts i
JOIN instincts_fts f ON f.rowid = i.rowid
WHERE f.instincts_fts MATCH ?1
  AND (?2 = '' OR i.scope_level = ?2)
  AND (?3 = '' OR i.scope_id    = ?3)
  AND i.state != 'forgotten'
  AND i.confidence >= ?4
ORDER BY i.confidence DESC, i.last_hit_at DESC
LIMIT ?5`,
			"\""+ftsTerm+"\"", req.Scope.Level, scopeID(req.Scope), req.MinConfidence, max,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite Query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		results    []memapi.QueryResult
		runningTok int
	)
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		// HIGH fix (round 3 follow-up): the previous semantics
		// included the over-cap row with Truncated=true, which
		// (a) let UsedTokens exceed MaxTokens on the host budget
		// aggregator and (b) conflated "this row was elided" with
		// "iteration stopped here". The new semantics stop
		// iteration without appending the row that would breach the
		// cap. The host knows iteration stopped early via
		// `RetrieveBudget.Allow` returning false on the next call.
		if req.MaxTokens > 0 && runningTok+r.EstimatedTokens > req.MaxTokens {
			break
		}
		runningTok += r.EstimatedTokens
		results = append(results, r)
	}
	return &memapi.QueryResp{Results: results}, rows.Err()
}

// queryWithoutTerm runs the scope-only scan path (no FTS MATCH).
// Reuses the same scan helper as the term-bearing path so the two
// branches always materialise identical Instinct shapes.
func (p *Provider) queryWithoutTerm(ctx context.Context, req *memapi.QueryReq, max int) (*memapi.QueryResp, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, rule, why, how_to_apply, domain_csv,
       scope_level, scope_id, source, source_event_id, dedupe_key,
       state, confidence, hits, last_hit_at, created_at,
       evidence_refs, source_traces, metadata
FROM instincts
WHERE (?1 = '' OR scope_level = ?1)
  AND (?2 = '' OR scope_id    = ?2)
  AND state != 'forgotten'
  AND confidence >= ?3
ORDER BY confidence DESC, last_hit_at DESC
LIMIT ?4`,
		req.Scope.Level, scopeID(req.Scope), req.MinConfidence, max,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite Query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var (
		results    []memapi.QueryResult
		runningTok int
	)
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		if req.MaxTokens > 0 && runningTok+r.EstimatedTokens > req.MaxTokens {
			break
		}
		runningTok += r.EstimatedTokens
		results = append(results, r)
	}
	return &memapi.QueryResp{Results: results}, rows.Err()
}

// scanRow materialises one instincts row into a memapi.QueryResult.
// Shared between the FTS path and the scope-only path.
func scanRow(rows *sql.Rows) (memapi.QueryResult, error) {
	var (
		r            memapi.QueryResult
		inst         memapi.Instinct
		scopeLevel   string
		scopeID      string
		sourceEvtID  string
		lastHit      int64
		created      int64
		domainCSV    string
		evidenceJSON string
		tracesJSON   string
	)
	if err := rows.Scan(
		&inst.ID, &inst.Rule, &inst.Why, &inst.HowToApply, &domainCSV,
		&scopeLevel, &scopeID, &inst.Source, &sourceEvtID, &inst.DedupeKey,
		&inst.State, &inst.Confidence, &inst.Hits, &lastHit, &created,
		&evidenceJSON, &tracesJSON, &inst.MetadataJSON,
	); err != nil {
		return r, fmt.Errorf("sqlite Query scan: %w", err)
	}
	_ = sourceEvtID // currently round-trippable through audit only; v0.6 may surface it as a Query response field
	inst.Scope = memapi.Scope{
		Level:      scopeLevel,
		WorktreeID: worktreeFromScopeID(scopeLevel, scopeID),
		RepoName:   repoFromScopeID(scopeLevel, scopeID),
	}
	if lastHit > 0 {
		inst.LastHitAt = time.Unix(lastHit, 0).UTC()
	}
	if created > 0 {
		inst.CreatedAt = time.Unix(created, 0).UTC()
	}
	inst.Domain = splitCSV(domainCSV)
	_ = json.Unmarshal([]byte(evidenceJSON), &inst.EvidenceRefs)
	_ = json.Unmarshal([]byte(tracesJSON), &inst.SourceTraces)
	r.Instinct = inst
	r.Score = inst.Confidence
	r.EstimatedTokens = estimateTokens(inst)
	return r, nil
}

// Forget is the soft delete: state flips, the row stays.
func (p *Provider) Forget(ctx context.Context, req *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	if req.ID == "" {
		return nil, errors.New("sqlite Forget: id is empty")
	}
	if _, err := p.db.ExecContext(ctx, `UPDATE instincts SET state = 'forgotten' WHERE id = ?1`, req.ID); err != nil {
		return nil, fmt.Errorf("sqlite Forget: %w", err)
	}
	return &memapi.ForgetResp{}, nil
}

// Export marshals matching rows as YAML (default) or JSONL.
func (p *Provider) Export(ctx context.Context, req *memapi.ExportReq) (*memapi.ExportResp, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, rule, why, scope_level, scope_id, source, confidence, state, created_at
FROM instincts
WHERE (?1 = '' OR scope_level = ?1)
  AND (?2 = '' OR scope_id    = ?2)
  AND (?3 = '' OR state       = ?3)
ORDER BY created_at`,
		req.Scope.Level, scopeID(req.Scope), req.StateFilter,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite Export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var (
			id, rule, why, lvl, sid, src, state string
			conf                                 float64
			created                              int64
		)
		if err := rows.Scan(&id, &rule, &why, &lvl, &sid, &src, &conf, &state, &created); err != nil {
			return nil, err
		}
		if req.Format == "jsonl" {
			line := map[string]any{
				"id": id, "rule": rule, "why": why, "scope_level": lvl,
				"scope_id": sid, "source": src, "confidence": conf, "state": state,
				"created_at": created,
			}
			out, _ := json.Marshal(line)
			b.Write(out)
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "- id: %s\n  rule: %s\n  scope_level: %s\n  scope_id: %s\n  source: %s\n  confidence: %.2f\n  state: %s\n",
			id, escapeYAML(rule), lvl, sid, src, conf, state)
	}
	ct := "text/yaml"
	if req.Format == "jsonl" {
		ct = "application/jsonl"
	}
	return &memapi.ExportResp{Payload: []byte(b.String()), ContentType: ct}, nil
}

// Import accepts the same shapes Export produces and upserts each
// row. v0.5.1 MEDIUM #17 fix: previously the parser only counted
// `- id:` markers (YAML) or JSON unmarshals (JSONL) without ever
// re-storing the rows, so an Import after Forget left the table
// empty even though the response advertised ImportedCount > 0.
// The fix walks the payload, materialises one memapi.Instinct per
// row, and routes it through the same Store path the host uses for
// fresh ingest — UpsertSemantics=true so an existing row is
// reinforced rather than dropped.
//
// v0.6+ adds full claude-skills / agent-skill / mcp parsing.
func (p *Provider) Import(ctx context.Context, req *memapi.ImportReq) (*memapi.ImportResp, error) {
	if req.Format != "yaml" && req.Format != "jsonl" {
		return nil, fmt.Errorf("sqlite Import: format %q unsupported in v0.5", req.Format)
	}
	if len(req.Payload) == 0 {
		return &memapi.ImportResp{}, nil
	}
	var (
		rows []memapi.Instinct
		err  error
	)
	if req.Format == "jsonl" {
		rows = parseExportedJSONL(req.Payload)
	} else {
		rows, err = parseExportedYAML(req.Payload)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite Import: parse: %w", err)
	}
	resp := &memapi.ImportResp{}
	for _, inst := range rows {
		if inst.ID == "" {
			resp.SkippedCount++
			continue
		}
		storeResp, sErr := p.Store(ctx, &memapi.StoreReq{
			Instinct:        inst,
			UpsertSemantics: true,
		})
		if sErr != nil {
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

// parseExportedJSONL inverts Export's JSONL emit. Malformed lines
// are skipped (= counted as SkippedCount by the caller through
// inst.ID == "" branch).
func parseExportedJSONL(payload []byte) []memapi.Instinct {
	var out []memapi.Instinct
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			ID         string  `json:"id"`
			Rule       string  `json:"rule"`
			Why        string  `json:"why"`
			ScopeLevel string  `json:"scope_level"`
			ScopeID    string  `json:"scope_id"`
			Source     string  `json:"source"`
			Confidence float64 `json:"confidence"`
			State      string  `json:"state"`
			CreatedAt  int64   `json:"created_at"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			out = append(out, memapi.Instinct{})
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
		out = append(out, inst)
	}
	return out
}

// parseExportedYAML inverts Export's YAML emit. The emit uses a
// fixed 7-field block per row, so we can do a line-based scan
// without pulling in a full YAML library.
func parseExportedYAML(payload []byte) ([]memapi.Instinct, error) {
	var out []memapi.Instinct
	var cur memapi.Instinct
	inBlock := false
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "- id:") {
			if inBlock {
				out = append(out, cur)
			}
			cur = memapi.Instinct{}
			inBlock = true
			cur.ID = strings.TrimSpace(strings.TrimPrefix(line, "- id:"))
			continue
		}
		if !inBlock {
			continue
		}
		switch {
		case strings.HasPrefix(line, "  rule:"):
			cur.Rule = unescapeYAML(strings.TrimSpace(strings.TrimPrefix(line, "  rule:")))
		case strings.HasPrefix(line, "  scope_level:"):
			cur.Scope.Level = strings.TrimSpace(strings.TrimPrefix(line, "  scope_level:"))
		case strings.HasPrefix(line, "  scope_id:"):
			sid := strings.TrimSpace(strings.TrimPrefix(line, "  scope_id:"))
			cur.Scope.WorktreeID, cur.Scope.RepoName = splitScopeID(cur.Scope.Level, sid)
		case strings.HasPrefix(line, "  source:"):
			cur.Source = strings.TrimSpace(strings.TrimPrefix(line, "  source:"))
		case strings.HasPrefix(line, "  confidence:"):
			v := strings.TrimSpace(strings.TrimPrefix(line, "  confidence:"))
			if conf, err := strconv.ParseFloat(v, 64); err == nil {
				cur.Confidence = conf
			}
		case strings.HasPrefix(line, "  state:"):
			cur.State = strings.TrimSpace(strings.TrimPrefix(line, "  state:"))
		}
	}
	if inBlock {
		out = append(out, cur)
	}
	return out, nil
}

// splitScopeID inverts scopeID: a "worktree" scope is encoded as
// "<RepoName>/<WorktreeID>", "repo" is just "<RepoName>", and
// "global" carries no id at all.
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

// unescapeYAML inverts escapeYAML's quote-and-escape behaviour.
// Bare values pass through untouched; quoted values lose the
// surrounding `"` and revert the embedded `\"` to `"`.
func unescapeYAML(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
	}
	return s
}

// ---- helpers ----

func scopeID(s memapi.Scope) string {
	switch s.Level {
	case "worktree":
		return s.RepoName + "/" + s.WorktreeID
	case "repo":
		return s.RepoName
	default:
		return ""
	}
}

// normalizeFTSTerm strips every character the FTS5 query language
// treats as metasyntax. Round 3 follow-up review flagged the
// previous "strip just ASCII `\"`" approach as inadequate because
// unicode quotes, NEAR, and column filters could still slip past.
// We keep only Letters, Digits, Spaces, hyphen and underscore —
// none of which carry FTS5 special meaning inside a phrase quote.
func normalizeFTSTerm(term string) string {
	var b strings.Builder
	b.Grow(len(term))
	for _, r := range term {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// worktreeFromScopeID derives the worktree id from the scope_id
// column shape (`<repo>/<worktree>`).
func worktreeFromScopeID(level, sid string) string {
	if level != "worktree" {
		return ""
	}
	if i := strings.LastIndex(sid, "/"); i >= 0 {
		return sid[i+1:]
	}
	return sid
}

func repoFromScopeID(level, sid string) string {
	switch level {
	case "worktree":
		if i := strings.LastIndex(sid, "/"); i >= 0 {
			return sid[:i]
		}
		return ""
	case "repo":
		return sid
	}
	return ""
}

func joinCSV(items []string) string { return strings.Join(items, ",") }
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, ",")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

func nonZeroState(s string) string {
	if s == "" {
		return "candidate"
	}
	return s
}

func unixOrNow(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}

func unixOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func estimateTokens(i memapi.Instinct) int {
	// Rough heuristic: English ~ 4 chars/token. Min 1 so the
	// budget aggregator never divides by zero on an empty row.
	n := (len(i.Rule) + len(i.Why) + len(i.HowToApply)) / 4
	if n < 1 {
		n = 1
	}
	return n
}

func escapeYAML(s string) string {
	// Just wrap in quotes if the string contains characters YAML
	// would interpret. Good enough for the human-readable export.
	if strings.ContainsAny(s, ":#@\n") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

