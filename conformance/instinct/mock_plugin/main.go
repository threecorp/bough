// Mock InstinctMinter plugin used by the conformance suite's self-
// tests. The mock derives one candidate per non-empty TraceBundle,
// computes a fake dedupe key, and otherwise has no behaviour worth
// debugging.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/hashicorp/go-plugin"

	instapi "github.com/ikeikeikeike/bough/plugins/instinct/api"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: instapi.Handshake,
		Plugins: map[string]plugin.Plugin{
			instapi.InstinctMinterPluginKey: &instapi.InstinctMinterPlugin{Impl: &mockMinter{}},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}

type mockMinter struct{}

func (mockMinter) Mint(_ context.Context, req *instapi.MintReq) (*instapi.MintResp, error) {
	out := make([]*instapi.InstinctCandidate, 0, len(req.TraceBundles))
	for i, b := range req.TraceBundles {
		if strings.TrimSpace(b.Content) == "" {
			continue
		}
		rule := summarise(b.Content)
		out = append(out, &instapi.InstinctCandidate{
			ID:           b.ID,
			Rule:         rule,
			Why:          "mock-derived from trace",
			Domain:       []string{"mock"},
			Scope:        req.Scope,
			Source:       b.Source,
			Confidence:   0.5 - float64(i)*0.05,
			State:        "candidate",
			SourceTraces: []string{b.ID},
			CreatedAt:    time.Now().UTC(),
			DedupeKey:    dedupe(rule, req.Scope),
		})
	}
	return &instapi.MintResp{Candidates: out}, nil
}

func summarise(content string) string {
	if i := strings.IndexAny(content, ".:\n"); i > 0 && i < 80 {
		return strings.TrimSpace(content[:i])
	}
	if len(content) > 80 {
		return content[:80]
	}
	return content
}

func dedupe(rule string, s instapi.Scope) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(rule)))
	h.Write([]byte("|"))
	h.Write([]byte(s.Level))
	return hex.EncodeToString(h.Sum(nil))
}
