package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// fakeBackend mirrors the host-side fake from internal/instinct;
// only Query is exercised by the v0.6 read-only MCP surface.
type fakeBackend struct {
	results []memapi.Instinct
	queries int
}

func (f *fakeBackend) Health(_ context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{}, nil
}
func (f *fakeBackend) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{}, nil
}
func (f *fakeBackend) Store(_ context.Context, _ *memapi.StoreReq) (*memapi.StoreResp, error) {
	return &memapi.StoreResp{}, nil
}
func (f *fakeBackend) Query(_ context.Context, _ *memapi.QueryReq) (*memapi.QueryResp, error) {
	f.queries++
	out := make([]memapi.QueryResult, len(f.results))
	for i, r := range f.results {
		out[i] = memapi.QueryResult{Instinct: r}
	}
	return &memapi.QueryResp{Results: out}, nil
}
func (f *fakeBackend) Forget(_ context.Context, _ *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	return &memapi.ForgetResp{}, nil
}
func (f *fakeBackend) Export(_ context.Context, _ *memapi.ExportReq) (*memapi.ExportResp, error) {
	return &memapi.ExportResp{}, nil
}
func (f *fakeBackend) Import(_ context.Context, _ *memapi.ImportReq) (*memapi.ImportResp, error) {
	return &memapi.ImportResp{}, nil
}

// runRequest dispatches a single JSON-RPC line through Server.
// Returns the unmarshalled response so each test can assert on the
// shape directly.
func runRequest(t *testing.T, s *Server, line string) jsonrpcResponse {
	t.Helper()
	var buf bytes.Buffer
	if err := s.dispatchLine(context.Background(), []byte(line), &buf); err != nil {
		t.Fatalf("dispatchLine: %v", err)
	}
	var resp jsonrpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, buf.String())
	}
	return resp
}

func TestServer_Initialize_AdvertiseReadOnly(t *testing.T) {
	s := NewServer(&fakeBackend{}, func() {}, "v0.6.0-test")
	resp := runRequest(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if resp.Error != nil {
		t.Fatalf("initialize: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var got InitializeResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.ProtocolVersion != MCPSpecVersion {
		t.Errorf("protocolVersion: got %q want %q", got.ProtocolVersion, MCPSpecVersion)
	}
	if got.Capabilities.BoughMCPServer.ReadOnly != true {
		t.Errorf("bough_mcp_server.read_only should be true on v0.6")
	}
	if got.Capabilities.BoughMCPServer.StateChangingTools {
		t.Errorf("bough_mcp_server.state_changing_tools should be false on v0.6")
	}
}

func TestServer_ToolsList_ReadOnly(t *testing.T) {
	s := NewServer(&fakeBackend{}, func() {}, "v")
	resp := runRequest(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if resp.Error != nil {
		t.Fatalf("tools/list: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var got ToolsListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "memory.query" {
		t.Errorf("v0.6 should expose memory.query only: %+v", got.Tools)
	}
}

func TestServer_ToolsCall_StateChangingRefused(t *testing.T) {
	s := NewServer(&fakeBackend{}, func() {}, "v")
	resp := runRequest(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory.store"}}`)
	if resp.Error == nil {
		t.Fatal("memory.store should refuse on v0.6")
	}
	if resp.Error.Code != codeWriteForbidden {
		t.Errorf("error code: got %d want %d", resp.Error.Code, codeWriteForbidden)
	}
	if !strings.Contains(resp.Error.Message, "v0.6.x") {
		t.Errorf("error should mention v0.6.x deferral: %q", resp.Error.Message)
	}
}

func TestServer_ToolsCall_MemoryQueryRoundTrip(t *testing.T) {
	fb := &fakeBackend{results: []memapi.Instinct{{
		ID:   "rule-1",
		Rule: "prefer early returns",
	}}}
	s := NewServer(fb, func() {}, "v")
	resp := runRequest(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory.query","arguments":{"term":"early"}}}`)
	if resp.Error != nil {
		t.Fatalf("memory.query: %+v", resp.Error)
	}
	if fb.queries != 1 {
		t.Errorf("backend should be queried once: %d", fb.queries)
	}
	raw, _ := json.Marshal(resp.Result)
	var got ToolCallResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.IsError {
		t.Errorf("memory.query should not flag isError: %+v", got)
	}
	if len(got.Content) != 1 || !strings.Contains(got.Content[0].Text, "rule-1") {
		t.Errorf("content should render the row id: %+v", got.Content)
	}
}

func TestServer_ResourcesList(t *testing.T) {
	s := NewServer(&fakeBackend{}, func() {}, "v")
	resp := runRequest(t, s, `{"jsonrpc":"2.0","id":5,"method":"resources/list"}`)
	if resp.Error != nil {
		t.Fatalf("resources/list: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var got ResourcesListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Resources) != 1 || got.Resources[0].URI != "bough://memory/scopes" {
		t.Errorf("v0.6 should expose bough://memory/scopes only: %+v", got.Resources)
	}
}

func TestServer_ShutdownIdempotent(t *testing.T) {
	calls := 0
	s := NewServer(&fakeBackend{}, func() { calls++ }, "v")
	s.Shutdown()
	s.Shutdown()
	if calls != 1 {
		t.Errorf("close should fire exactly once: %d", calls)
	}
	// Subsequent dispatches should refuse cleanly.
	resp := runRequest(t, s, `{"jsonrpc":"2.0","id":99,"method":"tools/list"}`)
	if resp.Error == nil {
		t.Errorf("post-shutdown dispatch should return error")
	}
}
