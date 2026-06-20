// Package mcp implements the v0.6 bough-mcp-server: a stdio JSON-RPC
// 2.0 server that exposes bough's memory subsystem to MCP clients
// (Claude Desktop, Cursor, etc.) per the MCP 2025-11-25 spec at
// https://modelcontextprotocol.io/specification/2025-11-25.
//
// Round 4 AI #2 mandated read-only first: v0.6.0 only ships the
// memory.query tool + memory scope resources + no state-changing
// surface. Store / Forget / Promote land in v0.6.x behind an
// --allow-write CLI flag. The server's capabilities advertise
// state_changing_tools=false so well-behaved clients know not to
// ask for write tools.
//
// Round 4 AI #1 zombie-process guard: the server runs a watchdog
// goroutine that fires Graceful Shutdown the moment os.Stdin
// closes (= client exited). The MemoryBackend subprocess is killed
// in the same cleanup so SQLite file locks never linger.
package mcp

import (
	"encoding/json"
)

// MCPSpecVersion pins the protocol version the server speaks. Round
// 4 AI #2 + AI #1: bough advertises this in `initialize` so a
// client can refuse the connection rather than silently mismatch.
// v0.6 patch releases bump this if the spec evolves additively;
// breaking changes wait for v0.7.
const MCPSpecVersion = "2025-11-25"

// jsonrpcRequest is the JSON-RPC 2.0 envelope every MCP message
// rides in. ID may be a number or a string (the spec allows both);
// json.RawMessage preserves the original encoding for round-trip
// responses.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is the response envelope. Result and Error are
// mutually exclusive — the helpers below ensure only one populates.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError follows the JSON-RPC 2.0 error object shape.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC error codes the bough-mcp-server returns. Standard codes
// (= -32700 / -32600 / -32601) come from the JSON-RPC 2.0 spec;
// server-defined codes start at -32000 per the spec convention.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
	codeWriteForbidden = -32001 // v0.6 read-only first: state-changing methods refuse
)

// ServerInfo + ServerCapabilities are the response payload for the
// MCP initialize method. Round 4 AI #2: we advertise state_changing
// _tools = false so MCP clients know the v0.6 server is read-only.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ServerCapabilities struct {
	// MCP-defined capability blocks: empty {} signals "supported".
	Tools     map[string]any `json:"tools,omitempty"`
	Resources map[string]any `json:"resources,omitempty"`
	Prompts   map[string]any `json:"prompts,omitempty"`

	// Round 4 priority A7: bough-mcp-server advertises its own
	// vendor extensions so clients can probe v0.6's read-only
	// boundary programmatically.
	BoughMCPServer BoughMCPCapabilities `json:"bough_mcp_server"`
}

type BoughMCPCapabilities struct {
	SpecVersion        string `json:"spec_version"`
	ReadOnly           bool   `json:"read_only"`
	StateChangingTools bool   `json:"state_changing_tools"`
	HostVersion        string `json:"host_version"`
}

// InitializeResult is the payload of the initialize response.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities"`
}

// ToolDefinition is the shape MCP returns for tools/list. v0.6
// ships only "memory.query".
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolsListResult wraps the slice for tools/list.
type ToolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// ToolCallParams is the params shape clients pass to tools/call.
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolCallContent is one chunk of the tools/call result. v0.6 only
// returns text content; richer types (image / audio / resource_link)
// land with the v0.6.x emitters.
type ToolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolCallResult wraps the content + isError flag tools/call
// returns. isError surfaces a tool-level failure (= bad arguments,
// upstream backend error) without breaking JSON-RPC.
type ToolCallResult struct {
	Content []ToolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// ResourceDescriptor is the shape MCP returns for resources/list.
// v0.6 exposes "bough://memory/scopes" (= a JSON listing of every
// scope the configured backend holds).
type ResourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// ResourcesListResult wraps the slice for resources/list.
type ResourcesListResult struct {
	Resources []ResourceDescriptor `json:"resources"`
}

// ResourcesReadParams is the params shape clients pass to
// resources/read.
type ResourcesReadParams struct {
	URI string `json:"uri"`
}

// ResourceContent is one block of the resources/read result. v0.6
// returns text/markdown content only.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

// ResourcesReadResult wraps the contents slice for resources/read.
type ResourcesReadResult struct {
	Contents []ResourceContent `json:"contents"`
}
