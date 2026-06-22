// Package hooks owns the Claude Code hook-handler lifecycle bough
// drives on behalf of the operator: install / uninstall / list /
// replay / doctor. The v0.7.0 Bootstrap safety floor lands the
// package skeleton + the CLI shape so dependent work (docs,
// fixtures, replay harness) can develop in parallel; the underlying
// Manager methods fill in across the v0.7.0 sub-phases.
//
// Why a dedicated package: Claude Code's settings.json is a
// hand-editable JSON surface a single operator usually trusts but
// teams need to keep in sync. Hand-editing it works for a solo dev,
// but the moment more than one repo / worktree / sibling tool
// touches the same file, the merge story falls over. bough's
// Manager owns the canonical reconciliation so an operator running
// `bough hook install` twice (or running it after a coworker's
// hand-edit) converges on the same set of entries without
// duplicating handlers.
//
// Round 5 review insistence: the package ships with a replay
// harness from day 1 — both external reviewers flagged hook
// auto-wire without a replay path as the single highest carryover
// risk. Fixtures live under `internal/hooks/testdata/` so
// `bough hook replay --event <name> --fixture <path>` round-trips
// without touching a live Claude Code session.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HookEvent is the Claude Code event name a hook handler listens
// for. Strings (not iota) so the JSON round-trip with
// settings.json stays human-grokable.
type HookEvent string

// The v0.7.0 canonical event set. Mirrors the Claude Code 1.x
// reference, plus the bough-specific WorktreeCreate /
// WorktreeRemove pair the engine + memory plugins already key off.
const (
	EventPreToolUse       HookEvent = "PreToolUse"
	EventPostToolUse      HookEvent = "PostToolUse"
	EventUserPromptSubmit HookEvent = "UserPromptSubmit"
	EventStop             HookEvent = "Stop"
	EventSessionEnd       HookEvent = "SessionEnd"
	EventPreCompact       HookEvent = "PreCompact"
	EventWorktreeCreate   HookEvent = "WorktreeCreate"
	EventWorktreeRemove   HookEvent = "WorktreeRemove"
)

// AllEvents lists every event the v0.7.0 install command wires by
// default. Ordering is stable so install / uninstall and the
// doctor's diff output line up reproducibly.
func AllEvents() []HookEvent {
	return []HookEvent{
		EventPreToolUse,
		EventPostToolUse,
		EventUserPromptSubmit,
		EventStop,
		EventSessionEnd,
		EventPreCompact,
		EventWorktreeCreate,
		EventWorktreeRemove,
	}
}

// HookEntry mirrors one command entry inside Claude Code's
// settings.json hook list. The wire format the upstream host
// accepts is:
//
//	{ "hooks": { "PreToolUse": [{"matcher": "Edit", "hooks": [{"type":"command","command":"..."}]}] } }
//
// We keep the surface flat so an operator hand-editing one entry
// can still round-trip through `bough hook list` without bough
// rewriting fields the operator did not touch.
type HookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// HookGroup mirrors one matcher group inside an event's hook list.
// Each event holds an ordered slice of these groups so different
// matchers (= "Edit|Write" vs "Bash") fire separate handler chains.
// Matcher is omitted when empty — Claude Code treats a missing
// matcher as "fire on every tool" for this event.
type HookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []HookEntry `json:"hooks"`
}

// HookSet maps each event to its ordered slice of matcher groups.
// settings.json stores this under the top-level "hooks" key.
type HookSet map[HookEvent][]HookGroup

// Manager is the host-side hooks subsystem. It owns the
// settings.json file lifecycle for one project root. v0.7.x adds
// per-user and per-host scope variants behind the same surface.
type Manager struct {
	// SettingsPath is the absolute path to the Claude Code
	// settings.json the manager edits. The CLI defaults this to
	// <repo-root>/.claude/settings.json.
	SettingsPath string
}

// New creates a Manager rooted at the given settings.json path.
// The file does not need to exist yet — Install creates it on the
// first call.
func New(settingsPath string) *Manager {
	return &Manager{SettingsPath: settingsPath}
}

// ErrNotYetWired signals that a Manager method has not been
// implemented in this commit. The v0.7.0 first commit shipped
// every method behind this sentinel; subsequent O-1.x sub-phases
// remove the wrapper as they fill the body in. v0.7.0 O-1.2 is
// the install / uninstall / list reconciliation — Replay /
// Doctor still wait on their respective sub-phases.
var ErrNotYetWired = errors.New("hooks: method not yet wired (v0.7.0 O-1.1 skeleton)")

// boughCommandPrefix is the canonical prefix every bough-installed
// hook command starts with. Uninstall keys off this prefix to
// distinguish bough's own entries from hand-rolled ones the
// operator may have added; install's idempotent merge keys off
// the same prefix to avoid duplicating handlers on re-run.
//
// The full canonical command bough writes is
// `bough hook handle --event <event>`; the prefix match keeps the
// detector tolerant of future flag additions (e.g. --scope=user)
// without bumping a version field.
const boughCommandPrefix = "bough hook handle"

// CanonicalCommand returns the command string bough writes for a
// given event. Exported so tests + the doctor surface can render
// the canonical wiring without re-deriving the prefix.
func CanonicalCommand(event HookEvent) string {
	return boughCommandPrefix + " --event " + string(event)
}

// isBoughEntry returns true when the command was written by bough
// (= prefix match). Hand-edited entries always have a different
// prefix.
func isBoughEntry(e HookEntry) bool {
	return strings.HasPrefix(strings.TrimSpace(e.Command), boughCommandPrefix)
}

// isBoughGroup returns true when every entry in the group is
// bough-owned. Mixed groups (= some bough, some hand-rolled) are
// treated as hand-edited and preserved; bough's reconciliation
// only touches groups it wholly owns.
func isBoughGroup(g HookGroup) bool {
	if len(g.Hooks) == 0 {
		return false
	}
	for _, e := range g.Hooks {
		if !isBoughEntry(e) {
			return false
		}
	}
	return true
}

// Install adds bough's canonical hook entries to settings.json.
// Idempotent: re-running on a partially-wired file converges to
// the canonical set without duplicating handlers. The command
// argument is currently unused — v0.7.0 hard-codes the canonical
// command per event so the round-trip stays predictable; v0.7.x
// surfaces it as an override for advanced operators wiring a
// custom binary path.
func (m *Manager) Install(_ context.Context, _ string) error {
	raw, err := m.loadSettings()
	if err != nil {
		return err
	}
	set, err := decodeHookSet(raw)
	if err != nil {
		return err
	}
	for _, event := range AllEvents() {
		groups := set[event]
		filtered := groups[:0]
		for _, g := range groups {
			if !isBoughGroup(g) {
				filtered = append(filtered, g)
			}
		}
		filtered = append(filtered, HookGroup{
			Hooks: []HookEntry{{
				Type:    "command",
				Command: CanonicalCommand(event),
			}},
		})
		set[event] = filtered
	}
	encoded, err := encodeHookSet(set)
	if err != nil {
		return err
	}
	raw["hooks"] = encoded
	return m.saveSettings(raw)
}

// Uninstall removes every bough-installed hook entry from
// settings.json. Hand-edited entries — ones where the command
// does not start with the bough canonical prefix — are preserved.
// Events that wind up empty are deleted from the map so the file
// does not accumulate empty arrays.
func (m *Manager) Uninstall(_ context.Context) error {
	raw, err := m.loadSettings()
	if err != nil {
		return err
	}
	set, err := decodeHookSet(raw)
	if err != nil {
		return err
	}
	for event, groups := range set {
		filtered := groups[:0]
		for _, g := range groups {
			if !isBoughGroup(g) {
				filtered = append(filtered, g)
			}
		}
		if len(filtered) == 0 {
			delete(set, event)
		} else {
			set[event] = filtered
		}
	}
	if len(set) == 0 {
		delete(raw, "hooks")
	} else {
		encoded, err := encodeHookSet(set)
		if err != nil {
			return err
		}
		raw["hooks"] = encoded
	}
	return m.saveSettings(raw)
}

// List returns the current HookSet as parsed from settings.json.
// `bough hook list` and `bough hook doctor` both consume this. A
// missing settings.json returns an empty HookSet with a nil error
// so a fresh repo is not noisy.
func (m *Manager) List(_ context.Context) (HookSet, error) {
	raw, err := m.loadSettings()
	if err != nil {
		return nil, err
	}
	return decodeHookSet(raw)
}

// Replay drives a recorded hook-event JSON payload through the
// configured handler so an operator can sanity-check the wiring
// against a fixture without touching a live Claude Code session.
// The fixture argument is the raw bytes of the JSON payload Claude
// Code would have sent into the hook subprocess on stdin.
func (m *Manager) Replay(_ context.Context, _ HookEvent, _ []byte) (*ReplayResult, error) {
	return nil, ErrNotYetWired
}

// ReplayResult describes the outcome of a Replay invocation. The
// shape mirrors the audit-log record bough plans to persist into
// the same observations.jsonl the SessionEnd path writes, so the
// replay path's diagnostics align with production traces.
type ReplayResult struct {
	Event    HookEvent
	Stdout   string
	Stderr   string
	ExitCode int
}

// loadSettings reads the settings.json file into a top-level
// raw map. Unknown fields the operator wrote (= other tools'
// configuration) round-trip through untouched. A missing file
// is not an error — the caller (Install) creates the file on
// the first save.
func (m *Manager) loadSettings() (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(m.SettingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", m.SettingsPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.SettingsPath, err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

// saveSettings writes the raw map back to settings.json atomically
// via tmp + rename. Parent directories are created with 0o755 so
// the first install in a fresh repo does not need a manual `mkdir
// -p .claude` step. Format: pretty-printed JSON with a trailing
// newline (= POSIX file convention, helps diff readability).
func (m *Manager) saveSettings(raw map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(m.SettingsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	payload, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	payload = append(payload, '\n')
	tmp := m.SettingsPath + ".bough.tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, m.SettingsPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp to %s: %w", m.SettingsPath, err)
	}
	return nil
}

// decodeHookSet extracts the "hooks" key from the raw settings
// map. A missing key yields an empty HookSet so callers that
// always loop over AllEvents() do not need a nil guard.
func decodeHookSet(raw map[string]json.RawMessage) (HookSet, error) {
	set := HookSet{}
	rawHooks, ok := raw["hooks"]
	if !ok {
		return set, nil
	}
	var perEvent map[HookEvent][]HookGroup
	if err := json.Unmarshal(rawHooks, &perEvent); err != nil {
		return nil, fmt.Errorf("decode hooks: %w", err)
	}
	for k, v := range perEvent {
		set[k] = v
	}
	return set, nil
}

// encodeHookSet marshals the HookSet back to JSON so saveSettings
// can stash it under the raw "hooks" key. Sorted output is left
// to encoding/json's deterministic map-key sort — fine for v0.7.0
// since the test corpus diffs against canonical output.
func encodeHookSet(set HookSet) (json.RawMessage, error) {
	if len(set) == 0 {
		return nil, nil
	}
	perEvent := map[HookEvent][]HookGroup(set)
	raw, err := json.Marshal(perEvent)
	if err != nil {
		return nil, fmt.Errorf("encode hooks: %w", err)
	}
	return raw, nil
}
