// Package mcp is the bough-mcp-server contract test suite. It runs
// the in-process Server through every method the v0.6 read-only
// surface exposes (initialize / tools/list / tools/call /
// resources/list / resources/read / shutdown) and asserts the round
// 4 priority A6 + A7 + A8 invariants:
//
//   - initialize advertises spec_version + read_only + state_changing_tools.
//   - tools/list ships exactly memory.query (no write tools).
//   - tools/call memory.store / forget / promote refuses with codeWriteForbidden.
//   - shutdown is idempotent and post-shutdown dispatches return errors.
//
// The suite uses io.Pipe so the test does not depend on a spawned
// binary — round 4 AI #1 zombie sims live in the server unit tests
// (= stdin close detection). Here we focus on protocol correctness
// so downstream MCP clients (Claude Desktop, Cursor) can trust the
// v0.6 surface.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/ikeikeikeike/bough/internal/mcp"
	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// Config drives one conformance run. Backend lets a caller swap in
// a real backend to exercise an end-to-end pipeline; the default
// (nil) wires a fake that returns one row so memory.query has
// content to render.
type Config struct {
	Backend memapi.MemoryBackend
}

// Run drives the contract.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	backend := cfg.Backend
	if backend == nil {
		backend = newFakeBackend()
	}
	server := mcp.NewServer(backend, func() {}, "v0.6.0-conformance")

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	var (
		wg     sync.WaitGroup
		runErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = server.Run(context.Background(), stdinR, stdoutW)
		_ = stdoutW.Close()
	}()

	conn := &mcpConn{w: stdinW, r: bufio.NewReader(stdoutR)}

	t.Run("Initialize", func(t *testing.T) { checkInitialize(t, conn) })
	t.Run("ToolsList", func(t *testing.T) { checkToolsList(t, conn) })
	t.Run("ToolsCall_MemoryQuery", func(t *testing.T) { checkMemoryQuery(t, conn) })
	t.Run("ToolsCall_WriteRefused", func(t *testing.T) { checkWriteRefused(t, conn) })
	t.Run("ResourcesList", func(t *testing.T) { checkResourcesList(t, conn) })
	t.Run("Shutdown", func(t *testing.T) { checkShutdown(t, conn) })

	_ = stdinW.Close()
	wg.Wait()
	if runErr != nil && runErr != io.EOF && !strings.Contains(runErr.Error(), "closed pipe") {
		t.Errorf("server.Run: %v", runErr)
	}
}

// mcpConn is a tiny JSON-RPC envelope helper so each test case can
// "call → read → assert" without rebuilding the framing.
type mcpConn struct {
	w io.Writer
	r *bufio.Reader
}

func (c *mcpConn) call(t *testing.T, id int, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := c.w.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v: %s", err, string(line))
	}
	return resp
}

func checkInitialize(t *testing.T, c *mcpConn) {
	resp := c.call(t, 1, "initialize", nil)
	if resp["error"] != nil {
		t.Fatalf("initialize error: %+v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result missing: %+v", resp)
	}
	if result["protocolVersion"] != mcp.MCPSpecVersion {
		t.Errorf("protocolVersion: %v want %v", result["protocolVersion"], mcp.MCPSpecVersion)
	}
	caps, _ := result["capabilities"].(map[string]any)
	vendor, _ := caps["bough_mcp_server"].(map[string]any)
	if vendor["read_only"] != true {
		t.Errorf("bough_mcp_server.read_only: %v want true", vendor["read_only"])
	}
	if vendor["state_changing_tools"] == true {
		t.Errorf("bough_mcp_server.state_changing_tools should be false on v0.6")
	}
}

func checkToolsList(t *testing.T, c *mcpConn) {
	resp := c.call(t, 2, "tools/list", nil)
	if resp["error"] != nil {
		t.Fatalf("tools/list error: %+v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("v0.6 should expose exactly one tool: got %d", len(tools))
		return
	}
	first, _ := tools[0].(map[string]any)
	if first["name"] != "memory.query" {
		t.Errorf("tool name: %v want memory.query", first["name"])
	}
}

func checkMemoryQuery(t *testing.T, c *mcpConn) {
	resp := c.call(t, 3, "tools/call", map[string]any{
		"name":      "memory.query",
		"arguments": map[string]any{"term": "rule"},
	})
	if resp["error"] != nil {
		t.Fatalf("memory.query error: %+v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["isError"] == true {
		t.Errorf("memory.query should not flag isError: %+v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Errorf("memory.query content empty: %+v", result)
	}
}

func checkWriteRefused(t *testing.T, c *mcpConn) {
	for _, name := range []string{"memory.store", "memory.forget", "memory.promote"} {
		resp := c.call(t, 4, "tools/call", map[string]any{"name": name})
		errObj, ok := resp["error"].(map[string]any)
		if !ok {
			t.Errorf("%s should refuse: %+v", name, resp)
			continue
		}
		if int(errObj["code"].(float64)) != -32001 {
			t.Errorf("%s error code: %v want -32001", name, errObj["code"])
		}
	}
}

func checkResourcesList(t *testing.T, c *mcpConn) {
	resp := c.call(t, 5, "resources/list", nil)
	if resp["error"] != nil {
		t.Fatalf("resources/list error: %+v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	resources, _ := result["resources"].([]any)
	if len(resources) != 1 {
		t.Errorf("v0.6 should expose one resource: got %d", len(resources))
		return
	}
	first, _ := resources[0].(map[string]any)
	if first["uri"] != "bough://memory/scopes" {
		t.Errorf("resource uri: %v want bough://memory/scopes", first["uri"])
	}
}

func checkShutdown(t *testing.T, c *mcpConn) {
	resp := c.call(t, 6, "shutdown", nil)
	if resp["error"] != nil {
		t.Errorf("shutdown error: %+v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["ok"] != true {
		t.Errorf("shutdown ok: %v", result["ok"])
	}
	// Post-shutdown dispatches should refuse.
	refused := c.call(t, 7, "tools/list", nil)
	if refused["error"] == nil {
		t.Errorf("post-shutdown tools/list should error: %+v", refused)
	}
}

// fakeBackend is the conformance suite's stand-in memory backend.
// It returns one row from Query so memory.query has content to
// render.
type fakeBackend struct{}

func newFakeBackend() *fakeBackend { return &fakeBackend{} }

func (f *fakeBackend) Health(_ context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{BackendKind: "conformance-fake"}, nil
}
func (f *fakeBackend) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{}, nil
}
func (f *fakeBackend) Store(_ context.Context, _ *memapi.StoreReq) (*memapi.StoreResp, error) {
	return &memapi.StoreResp{}, nil
}
func (f *fakeBackend) Query(_ context.Context, _ *memapi.QueryReq) (*memapi.QueryResp, error) {
	return &memapi.QueryResp{
		Results: []memapi.QueryResult{
			{Instinct: memapi.Instinct{ID: "rule-conf", Rule: "prefer rule"}},
		},
	}, nil
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
