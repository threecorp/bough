package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// hostStateFixture writes a ~/.claude.json shaped like a real one — an
// mcpServers block and unrelated project entries beside the one under
// test — and points claudeJSONPath at it.
func hostStateFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readHostState(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("host state is no longer valid JSON: %v\n%s", err, data)
	}
	return out
}

const hostStateWithNeighbours = `{
  "mcpServers": {"serena": {"command": "uvx"}},
  "someTopLevelSetting": 42,
  "projects": {
    "/Users/x/src/other": {"hasTrustDialogAccepted": true, "history": ["a", "b"]}
  }
}`

// The whole point: a path bough just created is not in the host's trust
// list and cannot be, so create records it.
func TestTrustWorktree_RecordsANewWorktree(t *testing.T) {
	path := hostStateFixture(t, hostStateWithNeighbours)
	wt := "/Users/x/src/mono/.worktrees/F-New"

	trustWorktree(io.Discard, wt)

	projects := readHostState(t, path)["projects"].(map[string]any)
	entry, ok := projects[wt].(map[string]any)
	if !ok {
		t.Fatalf("no entry for %s: %v", wt, projects)
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("hasTrustDialogAccepted = %v, want true", entry["hasTrustDialogAccepted"])
	}
}

// Everything else in the file is the operator's, including a key bough
// has never heard of. Losing mcpServers here would cost them every MCP
// server they had configured.
func TestTrustWorktree_LeavesEveryOtherKeyIntact(t *testing.T) {
	path := hostStateFixture(t, hostStateWithNeighbours)

	trustWorktree(io.Discard, "/Users/x/src/mono/.worktrees/F-New")

	got := readHostState(t, path)
	if _, ok := got["mcpServers"].(map[string]any)["serena"]; !ok {
		t.Errorf("mcpServers.serena was lost: %v", got["mcpServers"])
	}
	if got["someTopLevelSetting"] != float64(42) {
		t.Errorf("an unmodelled top-level key was lost: %v", got["someTopLevelSetting"])
	}
	neighbour, ok := got["projects"].(map[string]any)["/Users/x/src/other"].(map[string]any)
	if !ok {
		t.Fatalf("the unrelated project entry was lost: %v", got["projects"])
	}
	if neighbour["hasTrustDialogAccepted"] != true || len(neighbour["history"].([]any)) != 2 {
		t.Errorf("the unrelated project entry was rewritten: %v", neighbour)
	}
}

// A re-create must not rewrite the file (and must not print), or every
// worktree resume churns the operator's host state for nothing.
func TestTrustWorktree_AlreadyTrustedIsANoOp(t *testing.T) {
	wt := "/Users/x/src/mono/.worktrees/F-New"
	path := hostStateFixture(t, `{"projects":{"`+wt+`":{"hasTrustDialogAccepted":true}}}`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	trustWorktree(io.Discard, wt)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("an already-trusted worktree rewrote the file:\nbefore %s\nafter  %s", before, after)
	}
}

// The host's state file is not bough's to repair. A create must survive
// its absence and its corruption without failing and without truncating
// what it could not parse.
func TestTrustWorktree_SurvivesAMissingOrUnparseableFile(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		trustWorktree(io.Discard, "/Users/x/wt") // must not panic
		if _, err := os.Stat(filepath.Join(dir, ".claude.json")); !os.IsNotExist(err) {
			t.Errorf("bough created a host state file that was not there")
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		path := hostStateFixture(t, `{ this is not json`)
		trustWorktree(io.Discard, "/Users/x/wt")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{ this is not json` {
			t.Errorf("bough rewrote a file it could not parse: %s", data)
		}
	})
}
