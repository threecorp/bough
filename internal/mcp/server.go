package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// Server is the bough-side MCP server. It speaks JSON-RPC 2.0 over
// any io.Reader / io.Writer pair (= stdio in production, an
// io.Pipe in tests) and routes incoming methods through the
// dispatch table below.
//
// The server holds a MemoryBackend client (= discovered by the host)
// and the close func that disposes the backend subprocess. Round 4
// AI #1 zombie-process guard: when stdin closes, the server marks
// itself shut and invokes close(), which terminates the SQLite
// subprocess so the DB file lock is released.
type Server struct {
	backend memapi.MemoryBackend
	close   func()
	version string

	// shut flips to 1 once Graceful Shutdown begins. New incoming
	// requests after shut=1 return a JSON-RPC error rather than
	// reaching the backend, so the host can drain in-flight RPCs
	// cleanly.
	shut atomic.Int32

	writeMu sync.Mutex // serialises stdout writes (JSON-RPC requires whole-message atomicity)
}

// NewServer constructs a Server from an already-discovered backend
// and a close callback. The host invokes close exactly once at
// shutdown — the watchdog goroutine or a sentinel "shutdown" method
// — so the SQLite subprocess never lingers.
func NewServer(backend memapi.MemoryBackend, closeFn func(), version string) *Server {
	return &Server{backend: backend, close: closeFn, version: version}
}

// Run loops on r, dispatching each incoming line as a JSON-RPC
// request. Newline-delimited framing matches Claude Desktop's
// reference MCP transport. Return on EOF or ctx cancellation.
func (s *Server) Run(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := s.dispatchLine(ctx, line, w); err != nil {
			// dispatchLine already wrote a JSON-RPC error response;
			// surface non-protocol errors (= write failures) so the
			// caller (main.go) can decide whether to retry or abort.
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp scanner: %w", err)
	}
	return nil
}

// Shutdown marks the server shut and disposes the backend subprocess.
// Idempotent — calling twice is safe, the second call short-circuits.
func (s *Server) Shutdown() {
	if !s.shut.CompareAndSwap(0, 1) {
		return
	}
	if s.close != nil {
		s.close()
	}
}

// dispatchLine parses one JSON-RPC request and routes to the
// per-method handler. Parsing errors send a Parse Error response;
// unknown methods send Method Not Found.
func (s *Server) dispatchLine(ctx context.Context, line []byte, w io.Writer) error {
	var req jsonrpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return s.writeError(w, nil, codeParseError, fmt.Sprintf("parse: %v", err))
	}
	if req.JSONRPC != "2.0" {
		return s.writeError(w, req.ID, codeInvalidRequest, fmt.Sprintf("jsonrpc=%q want 2.0", req.JSONRPC))
	}
	if s.shut.Load() != 0 {
		return s.writeError(w, req.ID, codeInternalError, "server shutting down")
	}
	switch req.Method {
	case "initialize":
		return s.handleInitialize(w, req)
	case "tools/list":
		return s.handleToolsList(w, req)
	case "tools/call":
		return s.handleToolsCall(ctx, w, req)
	case "resources/list":
		return s.handleResourcesList(w, req)
	case "resources/read":
		return s.handleResourcesRead(ctx, w, req)
	case "prompts/list":
		// v0.6 ships no prompts; respond with an empty list rather
		// than method-not-found so clients with default UIs do not
		// surface a hard error.
		return s.writeResult(w, req.ID, map[string]any{"prompts": []any{}})
	case "shutdown":
		s.Shutdown()
		return s.writeResult(w, req.ID, map[string]any{"ok": true})
	default:
		return s.writeError(w, req.ID, codeMethodNotFound, fmt.Sprintf("method %q not implemented in v0.6 (read-only first)", req.Method))
	}
}

// handleInitialize answers the spec's negotiation handshake. The
// payload advertises the bough_mcp_server vendor block so clients
// can probe v0.6's read-only boundary programmatically.
func (s *Server) handleInitialize(w io.Writer, req jsonrpcRequest) error {
	result := InitializeResult{
		ProtocolVersion: MCPSpecVersion,
		ServerInfo: ServerInfo{
			Name:    "bough-mcp-server",
			Version: s.version,
		},
		Capabilities: ServerCapabilities{
			Tools:     map[string]any{},
			Resources: map[string]any{},
			Prompts:   map[string]any{},
			BoughMCPServer: BoughMCPCapabilities{
				SpecVersion:        MCPSpecVersion,
				ReadOnly:           true,
				StateChangingTools: false,
				HostVersion:        s.version,
			},
		},
	}
	return s.writeResult(w, req.ID, result)
}

// handleToolsList returns the v0.6 tool catalogue. Only
// "memory.query" ships; state-changing tools are v0.6.x.
func (s *Server) handleToolsList(w io.Writer, req jsonrpcRequest) error {
	tools := []ToolDefinition{
		{
			Name:        "memory.query",
			Description: "Search bough's memory backend within the configured scope. Read-only.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"term": map[string]any{
						"type":        "string",
						"description": "search term; empty string returns the configured-scope window",
					},
					"scope": map[string]any{
						"type":        "string",
						"description": "scope level (worktree | repo | global); defaults to worktree",
					},
					"max_results": map[string]any{
						"type":    "integer",
						"default": 12,
					},
				},
			},
		},
	}
	return s.writeResult(w, req.ID, ToolsListResult{Tools: tools})
}

// handleToolsCall dispatches a tool invocation. v0.6 only supports
// memory.query; every other tool name returns Method Not Found and
// state-changing operations refuse with codeWriteForbidden.
func (s *Server) handleToolsCall(ctx context.Context, w io.Writer, req jsonrpcRequest) error {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.writeError(w, req.ID, codeInvalidRequest, fmt.Sprintf("params: %v", err))
	}
	switch params.Name {
	case "memory.query":
		return s.callMemoryQuery(ctx, w, req, params)
	case "memory.store", "memory.forget", "memory.promote":
		return s.writeError(w, req.ID, codeWriteForbidden, fmt.Sprintf("%s is a state-changing tool; v0.6.0 is read-only first (lands with --allow-write in v0.6.x)", params.Name))
	default:
		return s.writeError(w, req.ID, codeMethodNotFound, fmt.Sprintf("tool %q not registered", params.Name))
	}
}

// callMemoryQuery runs MemoryBackend.Query and renders the result
// as MCP text content. Errors at the backend layer surface as
// isError=true content rather than JSON-RPC errors so the MCP
// client sees them as tool failures, not protocol failures.
func (s *Server) callMemoryQuery(ctx context.Context, w io.Writer, req jsonrpcRequest, params ToolCallParams) error {
	scopeLevel := "worktree"
	if raw, ok := params.Arguments["scope"].(string); ok && raw != "" {
		scopeLevel = raw
	}
	term, _ := params.Arguments["term"].(string)
	maxResults := 12
	if raw, ok := params.Arguments["max_results"].(float64); ok && int(raw) > 0 {
		maxResults = int(raw)
	}
	qresp, err := s.backend.Query(ctx, &memapi.QueryReq{
		Term:       term,
		Scope:      memapi.Scope{Level: scopeLevel},
		MaxResults: maxResults,
		MaxTokens:  4000,
	})
	if err != nil {
		return s.writeResult(w, req.ID, ToolCallResult{
			Content: []ToolCallContent{{Type: "text", Text: fmt.Sprintf("memory.query failed: %v", err)}},
			IsError: true,
		})
	}
	var b strings.Builder
	if len(qresp.Results) == 0 {
		b.WriteString("(no memories matched the term in the configured scope)")
	}
	for _, r := range qresp.Results {
		fmt.Fprintf(&b, "- [%s] %s\n", r.Instinct.ID, r.Instinct.Rule)
	}
	return s.writeResult(w, req.ID, ToolCallResult{
		Content: []ToolCallContent{{Type: "text", Text: b.String()}},
	})
}

// handleResourcesList exposes the static set of resource URIs the
// server publishes. v0.6 only lists scopes; v0.6.x will add
// per-scope resource entries.
func (s *Server) handleResourcesList(w io.Writer, req jsonrpcRequest) error {
	resources := []ResourceDescriptor{
		{
			URI:         "bough://memory/scopes",
			Name:        "Memory Scopes",
			Description: "List of scopes the configured bough memory backend holds.",
			MimeType:    "application/json",
		},
	}
	return s.writeResult(w, req.ID, ResourcesListResult{Resources: resources})
}

// handleResourcesRead serves the body for a known resource URI.
// Unknown URIs respond with Method Not Found.
func (s *Server) handleResourcesRead(ctx context.Context, w io.Writer, req jsonrpcRequest) error {
	var params ResourcesReadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.writeError(w, req.ID, codeInvalidRequest, fmt.Sprintf("params: %v", err))
	}
	switch params.URI {
	case "bough://memory/scopes":
		return s.readScopesResource(ctx, w, req, params.URI)
	default:
		return s.writeError(w, req.ID, codeMethodNotFound, fmt.Sprintf("resource %q not registered", params.URI))
	}
}

// readScopesResource queries the backend across the three scope
// levels and returns a compact JSON listing. v0.6 prefers
// correctness over completeness — an empty backend returns "{}",
// the host's first-call surface for misconfiguration.
func (s *Server) readScopesResource(ctx context.Context, w io.Writer, req jsonrpcRequest, uri string) error {
	scopes := map[string]int{}
	for _, level := range []string{"worktree", "repo", "global"} {
		qresp, err := s.backend.Query(ctx, &memapi.QueryReq{
			Term:       "",
			Scope:      memapi.Scope{Level: level},
			MaxResults: 1000,
			MaxTokens:  4000,
		})
		if err != nil {
			continue
		}
		scopes[level] = len(qresp.Results)
	}
	raw, _ := json.MarshalIndent(scopes, "", "  ")
	return s.writeResult(w, req.ID, ResourcesReadResult{
		Contents: []ResourceContent{{URI: uri, MimeType: "application/json", Text: string(raw)}},
	})
}

// writeResult serialises a JSON-RPC success response. Newline
// framing matches the wire convention.
func (s *Server) writeResult(w io.Writer, id json.RawMessage, result any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	resp := jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result}
	raw, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("mcp marshal: %w", err)
	}
	return writeLine(w, raw)
}

// writeError serialises a JSON-RPC error response.
func (s *Server) writeError(w io.Writer, id json.RawMessage, code int, message string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: message},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("mcp marshal err: %w", err)
	}
	return writeLine(w, raw)
}

// writeLine appends a newline and writes atomically.
func writeLine(w io.Writer, payload []byte) error {
	if _, err := w.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("mcp write: %w", err)
	}
	return nil
}

// WatchStdin is the round 4 AI #1 zombie-process guard: a Goroutine
// that waits on os.Stdin EOF, then triggers Server.Shutdown. The
// MemoryBackend subprocess (= SQLite reference-fallback) is killed
// as part of Shutdown so file locks never linger after the MCP
// client (Claude Desktop, Cursor) exits.
func WatchStdin(s *Server) {
	go func() {
		buf := make([]byte, 4096)
		for {
			_, err := os.Stdin.Read(buf)
			if err == io.EOF {
				s.Shutdown()
				return
			}
		}
	}()
}
