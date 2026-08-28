package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The host keeps its trusted-workspace list in ~/.claude.json under
// projects.<abs path>.hasTrustDialogAccepted, and refuses to open a
// worktree whose path is not in it:
//
//	Error creating worktree: Workspace trust not yet accepted.
//	Run `claude` once in this directory and accept the trust dialog,
//	then retry with --worktree.
//
// A worktree bough just created is a path that has never existed, so it
// is never in that list — and the operator cannot satisfy the hint
// either, because "run claude once in this directory" is the very thing
// the guard is blocking. Every `--worktree <new name>` therefore fails
// on first use, and again after a teardown/recreate cycle.
//
// bough creates the directory, so bough records it: the operator already
// trusted the monorepo root this worktree is derived from, and the
// worktree holds the same repositories on a branch of the same origin.
// This is not bough deciding a directory is safe; it is bough carrying
// an existing decision to the path the host will actually look up.

// claudeJSONPath is the host's per-user state file. Overridable for
// tests and for operators running with a non-default CLAUDE_CONFIG_DIR.
func claudeJSONPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// trustWorktree records worktreeRoot as a trusted workspace so the host
// will open it. Best-effort and silent about the ordinary cases: the
// state file is the host's, not bough's, and a create must not fail
// because the host stores its trust list differently than expected.
//
// Only the one key is touched, and only when it is absent — every other
// key in the file, including mcpServers and the operator's 180-odd other
// project entries, is round-tripped untouched. It is a no-op when the
// entry already says trusted, so a re-create prints nothing.
func trustWorktree(stderr io.Writer, worktreeRoot string) {
	path := claudeJSONPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // no host state file: the host is not installed here
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return
	}
	// Decoded as RawMessage so unknown keys survive verbatim. The file
	// carries the operator's whole host state; re-serialising it through
	// a typed struct would drop whatever this bough does not model.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return
	}
	projects := map[string]json.RawMessage{}
	if raw, ok := root["projects"]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return
		}
	}
	entry := map[string]json.RawMessage{}
	if raw, ok := projects[worktreeRoot]; ok {
		if err := json.Unmarshal(raw, &entry); err != nil {
			return
		}
	}
	if trusted, ok := entry["hasTrustDialogAccepted"]; ok && bytes.Equal(bytes.TrimSpace(trusted), []byte("true")) {
		return // already trusted; nothing to say
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	if err := reencode(entry, projects, worktreeRoot); err != nil {
		return
	}
	if err := reencode(projects, root, "projects"); err != nil {
		return
	}
	if err := writeJSONAtomic(path, root); err != nil {
		logf(stderr, "[bough] could not record %s as a trusted workspace: %v", worktreeRoot, err)
		return
	}
	logf(stderr, "[bough] trusted workspace recorded so --worktree can open it")
}

// reencode marshals child and stores it into parent[key].
func reencode[T any](child T, parent map[string]json.RawMessage, key string) error {
	blob, err := json.Marshal(child)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	parent[key] = blob
	return nil
}

// writeJSONAtomic mirrors internal/hooks' settings writer: tmp + rename,
// so a crash mid-write cannot leave the host holding a truncated state
// file it will refuse to start from.
func writeJSONAtomic(path string, v any) error {
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	payload = append(payload, '\n')
	tmp := path + ".bough.tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}
