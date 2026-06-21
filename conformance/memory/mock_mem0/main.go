// Command mock_mem0 is the conformance suite's stand-in for the
// real mem0 cloud / self-hosted endpoint. It runs an in-process
// HTTP server that speaks mem0's v1 REST API just enough for the
// conformance suite to exercise the bough mem0 plugin end-to-end,
// then serves itself as a bough MemoryBackend gRPC plugin.
//
// Layout:
//
//	main()
//	  ├─ start in-memory HTTP server on a random port  (= the mem0 mock)
//	  ├─ mem0.New(Config{Endpoint: that server's URL}) (= the bough plugin)
//	  └─ plugin.Serve over gRPC                         (= the bough host's wire)
//
// The mock mirrors real mem0 cloud semantics where it matters for
// contract verification: DELETE /api/v1/memories/<id>/ hard-deletes
// the row, dedupe is the host's responsibility (the plugin advertises
// DedupeKey=false), and an Export-after-Forget therefore returns
// nothing. Review #23 #8: an earlier revision of this mock faked
// soft-delete via metadata.bough_state="forgotten"; that hid the
// production divergence from the conformance suite. The suite now
// gates its dedupe and Export-after-Forget assertions on the
// advertised Capabilities so backends with honest hard-delete
// behaviour still pass without faking soft-delete in the wire layer.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/go-plugin"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/plugins/memory/mem0"
)

// mockRow is the unit the HTTP handler reads / writes.
type mockRow struct {
	ID        string
	Memory    string
	UserID    string
	SessionID string
	Metadata  map[string]any
}

// mockStore is the in-memory map every handler routes through. The
// mutex serialises writers; reads take the same lock for simplicity
// (the conformance suite only fires a few thousand RPCs).
type mockStore struct {
	mu   sync.Mutex
	seq  int
	rows map[string]mockRow
}

func newMockStore() *mockStore { return &mockStore{rows: make(map[string]mockRow)} }

// add inserts a row keyed by metadata.bough_dedupe_key + user_id +
// session_id. When a matching row exists the call upserts and
// returns event=UPDATE so the bough plugin reports WasUpsert=true.
//
// Real mem0 generates its own UUID for each memory; the mock instead
// honours metadata.bough_id when the host supplied one, so the
// conformance suite's Forget(ID=inst.ID) and Export → Import round
// trips work without an extra id translation step.
func (s *mockStore) add(userID, sessionID, memory string, metadata map[string]any) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dk, ok := metadata["bough_dedupe_key"].(string); ok && dk != "" {
		for existingID, r := range s.rows {
			existing, _ := r.Metadata["bough_dedupe_key"].(string)
			if r.UserID == userID && r.SessionID == sessionID && existing == dk {
				r.Memory = memory
				r.Metadata = metadata
				s.rows[existingID] = r
				return existingID, "UPDATE"
			}
		}
	}
	id, _ := metadata["bough_id"].(string)
	if id == "" {
		s.seq++
		id = fmt.Sprintf("m-%d", s.seq)
	}
	if _, exists := s.rows[id]; exists {
		s.rows[id] = mockRow{
			ID: id, Memory: memory, UserID: userID, SessionID: sessionID, Metadata: metadata,
		}
		return id, "UPDATE"
	}
	s.rows[id] = mockRow{
		ID: id, Memory: memory, UserID: userID, SessionID: sessionID, Metadata: metadata,
	}
	return id, "ADD"
}

// search returns rows whose memory content contains the term within
// the (user, session) namespace. Score is naive (1.0 for hits) —
// the conformance suite cares about whose rows survive, not about
// scoring nuance.
func (s *mockStore) search(userID, sessionID, term string) []mockRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mockRow
	for _, r := range s.rows {
		if r.UserID != userID {
			continue
		}
		if sessionID != "" && r.SessionID != sessionID {
			continue
		}
		if term != "" && !strings.Contains(strings.ToLower(r.Memory), strings.ToLower(term)) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// list returns every row for the (user, session) tuple; used by
// Export. mem0 hard-deletes on DELETE so forgotten rows are gone for
// good — no soft-delete bookkeeping needed.
func (s *mockStore) list(userID, sessionID string) []mockRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mockRow
	for _, r := range s.rows {
		if r.UserID != userID {
			continue
		}
		if sessionID != "" && r.SessionID != sessionID {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// hardDelete drops the row entirely — mirroring real mem0 cloud's
// DELETE /api/v1/memories/<id>/ behaviour (review #23 #8). The
// conformance suite gates Export-after-Forget assertions on the
// plugin-advertised Capabilities.SoftDelete so an honest
// hard-deleting backend still passes.
func (s *mockStore) hardDelete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[id]; !ok {
		return false
	}
	delete(s.rows, id)
	return true
}

// handler routes all paths through the same ServeMux so the bough
// mem0 plugin sees a wire surface identical to mem0's documented
// v1 endpoints.
func (s *mockStore) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/memories/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/memories/":
			s.handleAdd(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/memories/":
			s.handleList(w, r)
		case r.Method == http.MethodDelete:
			s.handleDelete(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/memories/search/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSearch(w, r)
	})
	return mux
}

func (s *mockStore) handleAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		UserID    string         `json:"user_id"`
		SessionID string         `json:"session_id"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	memory := ""
	if len(body.Messages) > 0 {
		memory = body.Messages[0].Content
	}
	id, event := s.add(body.UserID, body.SessionID, memory, body.Metadata)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": []map[string]any{{"id": id, "memory": memory, "event": event}},
	})
}

func (s *mockStore) handleList(w http.ResponseWriter, r *http.Request) {
	rows := s.list(r.URL.Query().Get("user_id"), r.URL.Query().Get("session_id"))
	results := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		results = append(results, map[string]any{
			"id":         row.ID,
			"memory":     row.Memory,
			"user_id":    row.UserID,
			"session_id": row.SessionID,
			"metadata":   row.Metadata,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func (s *mockStore) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/memories/"), "/")
	if id == "" {
		http.Error(w, "delete requires an id", http.StatusBadRequest)
		return
	}
	if dec, err := url.PathUnescape(id); err == nil {
		id = dec
	}
	if !s.hardDelete(id) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockStore) handleSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string `json:"query"`
		UserID    string `json:"user_id"`
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows := s.search(body.UserID, body.SessionID, body.Query)
	if body.Limit > 0 && len(rows) > body.Limit {
		rows = rows[:body.Limit]
	}
	results := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		results = append(results, map[string]any{
			"id":       row.ID,
			"memory":   row.Memory,
			"metadata": row.Metadata,
			"score":    1.0,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func main() {
	// 1. In-process HTTP mock listening on a random local port. The
	//    bough plugin reads BOUGH_MEMORY_MEM0_ENDPOINT to discover
	//    this URL — we set it ourselves before constructing the
	//    Provider so the env never leaks back to the parent process.
	store := newMockStore()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_, _ = os.Stderr.WriteString("mock_mem0: cannot bind listener: " + err.Error() + "\n")
		os.Exit(1)
	}
	srv := &http.Server{Handler: store.handler()}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	endpoint := "http://" + ln.Addr().String()

	// 2. Wire the bough mem0 plugin to that endpoint.
	prov, err := mem0.New(mem0.Config{Endpoint: endpoint})
	if err != nil {
		_, _ = os.Stderr.WriteString("mock_mem0: plugin init: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer func() { _ = prov.Close() }()

	// 3. Serve as a bough memory plugin.
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: memapi.Handshake,
		Plugins: map[string]plugin.Plugin{
			memapi.MemoryBackendPluginKey: &memapi.MemoryBackendPlugin{Impl: prov},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
