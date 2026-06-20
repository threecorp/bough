package mcp

import "testing"

// TestSelf wires the bough-mcp-server's in-tree Server through the
// conformance suite with the default fakeBackend. If the dispatch
// table or wire layout drifts, this test surfaces the gap before
// any external MCP client notices.
func TestSelf(t *testing.T) {
	Run(t, Config{})
}
