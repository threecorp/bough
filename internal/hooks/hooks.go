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
	"context"
	"errors"
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

// HookEntry mirrors one row inside Claude Code's settings.json
// "hooks" map. The format the upstream host accepts is:
//
//	{ "hooks": { "PreToolUse": [{"type": "command", "command": "..." }] } }
//
// We keep the surface flat so an operator hand-editing one entry
// can still round-trip through `bough hook list` without bough
// rewriting fields the operator did not touch.
type HookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
}

// HookSet groups entries per event. The settings.json schema
// allows multiple handlers per event (e.g. a bough handler + the
// operator's own hand-rolled script); the slice preserves order.
type HookSet map[HookEvent][]HookEntry

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
// implemented in this commit. The v0.7.0 first commit ships the
// skeleton; subsequent commits fill in the bodies. Surfacing the
// error as a sentinel value lets the test suite (and downstream
// docs) flag where the v0.7.0 plan is still on the way without
// silently no-op'ing.
var ErrNotYetWired = errors.New("hooks: method not yet wired (v0.7.0 O-1.1 skeleton)")

// Install adds bough's canonical hook entries to settings.json.
// Idempotent: re-running on a partially-wired file converges to
// the canonical set without duplicating handlers. command is the
// bough binary invocation each entry runs (e.g. "bough hook
// handle"); the manager stamps a matcher tag so uninstall can
// distinguish bough-installed entries from hand-rolled ones.
func (m *Manager) Install(_ context.Context, _ string) error {
	return ErrNotYetWired
}

// Uninstall removes every bough-installed hook entry from
// settings.json. Hand-edited entries (= ones without bough's
// canonical marker) are preserved.
func (m *Manager) Uninstall(_ context.Context) error {
	return ErrNotYetWired
}

// List returns the current HookSet as parsed from settings.json.
// `bough hook list` and `bough hook doctor` both consume this. A
// missing settings.json returns an empty HookSet with a nil error
// so a fresh repo is not noisy.
func (m *Manager) List(_ context.Context) (HookSet, error) {
	return nil, ErrNotYetWired
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
