// Mock MemoryBackend plugin used by the conformance suite's self-
// tests. The implementation is in-memory only — no SQLite, no
// network — so the suite can verify its own behaviour without a
// real backend in the loop.
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/go-plugin"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: memapi.Handshake,
		Plugins: map[string]plugin.Plugin{
			memapi.MemoryBackendPluginKey: &memapi.MemoryBackendPlugin{Impl: &mockBackend{rows: map[string]memapi.Instinct{}}},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}

type mockBackend struct {
	mu   sync.Mutex
	rows map[string]memapi.Instinct
}

func (m *mockBackend) Health(_ context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{BackendKind: "mock", PluginVersion: "v0.5.0-mock"}, nil
}

func (m *mockBackend) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{PluginVersion: "v0.5.0-mock"}, nil
}

func (m *mockBackend) Store(_ context.Context, req *memapi.StoreReq) (*memapi.StoreResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := req.Instinct.ID
	if req.DedupeKey != "" {
		key = req.DedupeKey
	}
	if existing, ok := m.rows[key]; ok && req.UpsertSemantics {
		existing.Hits++
		m.rows[key] = existing
		return &memapi.StoreResp{StoredID: existing.ID, WasUpsert: true}, nil
	}
	m.rows[key] = req.Instinct
	return &memapi.StoreResp{StoredID: req.Instinct.ID, WasUpsert: false}, nil
}

func (m *mockBackend) Query(_ context.Context, req *memapi.QueryReq) (*memapi.QueryResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memapi.QueryResult, 0, len(m.rows))
	term := strings.ToLower(req.Term)
	for _, r := range m.rows {
		if r.State == "forgotten" {
			continue
		}
		if req.Scope.Level != "" && r.Scope.Level != req.Scope.Level {
			continue
		}
		if req.Scope.WorktreeID != "" && r.Scope.WorktreeID != req.Scope.WorktreeID {
			continue
		}
		if term != "" && !strings.Contains(strings.ToLower(r.Rule), term) {
			continue
		}
		out = append(out, memapi.QueryResult{
			Instinct:        r,
			Score:           1.0,
			EstimatedTokens: estimateTokens(r),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instinct.ID < out[j].Instinct.ID })
	if req.MaxResults > 0 && len(out) > req.MaxResults {
		out = out[:req.MaxResults]
	}
	return &memapi.QueryResp{Results: out}, nil
}

func (m *mockBackend) Forget(_ context.Context, req *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.rows {
		if r.ID == req.ID {
			r.State = "forgotten"
			m.rows[k] = r
			return &memapi.ForgetResp{}, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockBackend) Export(_ context.Context, _ *memapi.ExportReq) (*memapi.ExportResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.rows {
		fmt.Fprintf(&b, "- id: %s\n  rule: %s\n", r.ID, r.Rule)
	}
	return &memapi.ExportResp{Payload: []byte(b.String()), ContentType: "text/yaml"}, nil
}

func (m *mockBackend) Import(_ context.Context, req *memapi.ImportReq) (*memapi.ImportResp, error) {
	return &memapi.ImportResp{ImportedCount: 1, UpsertedCount: 0, SkippedCount: 0}, nil
}

func estimateTokens(i memapi.Instinct) int {
	// Crude: roughly len/4 (English) but min 1.
	n := (len(i.Rule) + len(i.Why) + len(i.HowToApply)) / 4
	if n < 1 {
		n = 1
	}
	return n
}
