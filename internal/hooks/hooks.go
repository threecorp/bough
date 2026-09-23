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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ikeikeikeike/bough/internal/termio"
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
	// Extra keeps every other key the entry carries (timeout, statusMessage,
	// async, ...) so rewriting settings.json does not drop them.
	Extra map[string]json.RawMessage `json:"-"`
}

func (e *HookEntry) UnmarshalJSON(b []byte) error {
	type plain HookEntry
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := unknownKeys(b, "type", "command")
	if err != nil {
		return err
	}
	*e = HookEntry(p)
	e.Extra = extra
	return nil
}

func (e HookEntry) MarshalJSON() ([]byte, error) {
	type plain HookEntry
	return marshalWithExtra(plain(e), e.Extra)
}

// HookGroup mirrors one matcher group inside an event's hook list.
// Each event holds an ordered slice of these groups so different
// matchers (= "Edit|Write" vs "Bash") fire separate handler chains.
// Matcher is omitted when empty — Claude Code treats a missing
// matcher as "fire on every tool" for this event.
type HookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []HookEntry `json:"hooks"`
	// Extra keeps group keys bough does not model, for the same reason as
	// HookEntry.Extra.
	Extra map[string]json.RawMessage `json:"-"`
}

func (g *HookGroup) UnmarshalJSON(b []byte) error {
	type plain HookGroup
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := unknownKeys(b, "matcher", "hooks")
	if err != nil {
		return err
	}
	*g = HookGroup(p)
	g.Extra = extra
	return nil
}

func (g HookGroup) MarshalJSON() ([]byte, error) {
	type plain HookGroup
	return marshalWithExtra(plain(g), g.Extra)
}

// unknownKeys returns the keys of the JSON object b other than known, or nil
// when there are none.
func unknownKeys(b []byte, known ...string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// marshalWithExtra marshals v and adds the extra keys back. A modelled field
// wins over an extra key of the same name.
func marshalWithExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for k, raw := range extra {
		if _, ok := all[k]; !ok {
			all[k] = raw
		}
	}
	return json.Marshal(all)
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
// remove the wrapper as they fill the body in. v0.7.0 O-1.2 wired
// install / uninstall / list, O-1.3 wired Replay, and O-1.4 wired
// Doctor — the sentinel is retained as the canonical "not yet
// implemented" signal for any future Manager method.
var ErrNotYetWired = errors.New("hooks: method not yet wired")

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
//
// Behaviour:
//
//   - Looks up the commands wired for the given event in
//     settings.json. If none, returns a ReplayResult with a
//     diagnostic Stderr explaining the wiring is empty — not an
//     error, since "no handler wired" is a legitimate state during
//     install / uninstall cycles.
//   - Spawns each wired command in turn via `sh -c`, piping the
//     fixture into stdin and capturing combined stdout + stderr.
//     The Replay caller is the v0.7.0 debug harness (= `bough hook
//     replay --event ... --fixture ...`), not the production hook
//     dispatch path; the v0.7.0 production dispatcher is
//     `bough hook handle` (= O-1.6 in the same sprint).
//   - Reports the last command's exit code as the result's exit
//     code. Multi-handler events are uncommon in v0.7.0; v0.7.x
//     adds an aggregated-exit-code policy when matchers start
//     producing dependent chains.
func (m *Manager) Replay(ctx context.Context, event HookEvent, fixture []byte) (*ReplayResult, error) {
	set, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	groups, ok := set[event]
	result := &ReplayResult{Event: event}
	if !ok || len(groups) == 0 {
		result.Stderr = fmt.Sprintf("no hook handlers wired for event %q in %s", event, m.SettingsPath)
		return result, nil
	}
	var stdoutAll, stderrAll bytes.Buffer
	for _, g := range groups {
		for _, e := range g.Hooks {
			if e.Type != "command" {
				continue
			}
			cmd := exec.CommandContext(ctx, "sh", "-c", e.Command)
			cmd.Stdin = bytes.NewReader(fixture)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			stdoutAll.Write(stdout.Bytes())
			stderrAll.Write(stderr.Bytes())
			exit := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if errors.As(runErr, &exitErr) {
					exit = exitErr.ExitCode()
				} else {
					exit = -1
					fmt.Fprintf(&stderrAll, "exec error: %v\n", runErr)
				}
			}
			result.ExitCode = exit
		}
	}
	result.Stdout = stdoutAll.String()
	result.Stderr = stderrAll.String()
	return result, nil
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

// DoctorReport is the v0.7.0 transparency surface. Round 5 review
// flagged silent billing / silent observer / silent Haiku as the
// recurring failure mode bough must visibly avoid; the doctor
// report renders everything an operator needs to confirm bough's
// background loop is not running expensive things without their
// knowledge. v0.7.1 extends Cost with per-hook + per-session
// token tallies; v0.7.0 surfaces the structure so downstream
// docs / shell autocompletes can develop in parallel.
type DoctorReport struct {
	SettingsPath string
	Events       []EventStatus
	Observer     ObserverStatus
	Cost         CostStatus
	// HookPlugins names the bough plugin variants that carry hooks and are
	// enabled somewhere Claude Code will honour them. Empty is the common case;
	// non-empty alongside wired settings.json entries is a real double-fire,
	// not a possibility to warn about in the abstract.
	HookPlugins []string
}

// hookBearingPlugins are the published variants whose hooks/hooks.json wires
// the same dispatcher `Install` writes into settings.json. `bough` is absent on
// purpose: it ships commands + skill only, so it cannot double-fire anything.
var hookBearingPlugins = []string{"bough-hooks", "bough-all"}

// enabledHookPlugins reads Claude Code's enabledPlugins map — the same
// settings.json this Manager already owns — and returns the bough variants that
// carry hooks. Keys are "<plugin>@<marketplace>"; the marketplace half is
// whatever the operator named it when adding the source, so only the plugin
// half is matched.
//
// enabledPlugins is used rather than plugins/installed_plugins.json because it
// is the surface Claude Code documents and the file bough already parses. The
// registry file carries more (install scope, project path) but is internal,
// versioned ("version": 2), and would put bough's doctor at the mercy of a
// format bough has no claim on.
func enabledHookPlugins(raw map[string]json.RawMessage) []string {
	blob, ok := raw["enabledPlugins"]
	if !ok {
		return nil
	}
	var enabled map[string]bool
	if err := json.Unmarshal(blob, &enabled); err != nil {
		return nil // a shape bough does not recognise is not bough's to report on
	}
	var found []string
	for key, on := range enabled {
		if !on {
			continue
		}
		name, _, _ := strings.Cut(key, "@")
		if slices.Contains(hookBearingPlugins, name) {
			found = append(found, key)
		}
	}
	sort.Strings(found)
	return found
}

// EventStatus summarises one event's wiring posture. Both flags can
// be true (= the event has both a bough group and a hand-edited
// group); the doctor's render flags that combination explicitly
// because it is the case operators most often misread.
type EventStatus struct {
	Event          HookEvent
	BoughInstalled bool
	HandEdited     bool
	BoughCommand   string
	HandEntries    []HookEntry
}

// ObserverStatus tracks whether the raw-event observer is actually
// capturing into the central homunculus observations.jsonl (since v0.9.10;
// pre-v0.9.10 this was a working-tree .bough/ file). Configured = false means
// the operator has not run any session yet (or has not wired hook install).
type ObserverStatus struct {
	Configured bool
	Path       string
	LineCount  int
}

// CostStatus mirrors the v0.7.1 cost meter shape so the v0.7.0
// doctor can render a "not yet capturing" line and the v0.7.1
// commit only needs to fill Tokens / USDEst / LastSampleAt without
// touching the render path.
type CostStatus struct {
	DataAvailable bool
	Tokens        int
	USDEst        float64
	LastSampleAt  string
	Message       string
}

// Doctor returns a snapshot of the wiring, observer, and cost
// posture an operator needs to confirm bough's background loop is
// safe. Round 5 review front-loaded this from v0.7.1 because the
// hook auto-wire is the moment "silent" failure modes become
// possible; doctor is the operator's first stop when something
// feels off.
func (m *Manager) Doctor(_ context.Context, obsPath string) (*DoctorReport, error) {
	// One read, two views. Both halves of the report — the wired hooks and the
	// enabled plugins — come out of the same settings.json, so reading it twice
	// (List does its own load) would let the file change underneath and report a
	// conflict, or miss one, that never existed at any single instant.
	raw, err := m.loadSettings()
	if err != nil {
		return nil, err
	}
	set, err := decodeHookSet(raw)
	if err != nil {
		return nil, err
	}
	report := &DoctorReport{SettingsPath: m.SettingsPath, HookPlugins: enabledHookPlugins(raw)}
	for _, event := range AllEvents() {
		st := EventStatus{Event: event}
		for _, g := range set[event] {
			if isBoughGroup(g) {
				st.BoughInstalled = true
				if len(g.Hooks) > 0 {
					st.BoughCommand = g.Hooks[0].Command
				}
			} else {
				st.HandEdited = true
				st.HandEntries = append(st.HandEntries, g.Hooks...)
			}
		}
		report.Events = append(report.Events, st)
	}
	// Observer status: since v0.9.10 raw-event capture lands in the central
	// homunculus observations.jsonl for the resolved monorepo project (NOT a
	// working-tree .bough/ file). The caller resolves that path read-only and
	// passes it in; an empty obsPath means no project identity could be
	// resolved (non-git dir, no .bough.yaml), so capture is reported as not
	// yet configured rather than probing a dead, always-absent path.
	if obsPath != "" {
		if info, statErr := os.Stat(obsPath); statErr == nil && info.Mode().IsRegular() {
			report.Observer.Configured = true
			report.Observer.Path = obsPath
			if data, readErr := os.ReadFile(obsPath); readErr == nil {
				report.Observer.LineCount = bytes.Count(data, []byte("\n"))
			}
		}
	}
	// Cost meter: v0.7.0 ships the field shape; the actual counter
	// integration with the MCP write surface + hook handle path
	// lands in v0.7.1 once the LLM judge + per-event token tally
	// have a place to write to.
	report.Cost.DataAvailable = false
	report.Cost.Message = "cost meter wires in alongside the v0.7.1 LLM judge integration; not yet capturing"
	return report, nil
}

// hasBoughSettingsHooks reports whether any event carries a
// bough-installed group in settings.json — the gate for the
// transition double-fire note in Render.
func (r *DoctorReport) hasBoughSettingsHooks() bool {
	for _, st := range r.Events {
		if st.BoughInstalled {
			return true
		}
	}
	return false
}

// Render writes a human-friendly report to w. The doctor surface
// is meant to be read on the terminal; structured output (= JSON
// for CI consumption) lands in v0.7.x behind a --json flag.
func (r *DoctorReport) Render(w io.Writer) {
	st := termio.NewStyler(w)
	r.renderHookWiring(w, st)
	fmt.Fprintln(w)
	r.renderObserver(w, st)
	fmt.Fprintln(w)
	r.renderCostMeter(w, st)
}

// renderHookWiring is the [ ] Hook wiring section. Its header status is the
// worst of the wiring line and the plugin-conflict line, so a real double-fire
// paints the section [✗] while a clean single wiring paints it [✓].
func (r *DoctorReport) renderHookWiring(w io.Writer, st termio.Styler) {
	wired, total := 0, len(r.Events)
	for _, e := range r.Events {
		if e.BoughInstalled || e.HandEdited {
			wired++
		}
	}

	// Only a double-fire happening RIGHT NOW is a problem. The cross-scope
	// caveat (wired here, a plugin MIGHT be on elsewhere) is a heads-up, not a
	// fault — folding it into the rollup would paint every correctly-wired
	// repo red, exactly the nagging-yellow flutter-doctor avoids.
	conflict := termio.StatusOK
	if r.hasBoughSettingsHooks() && len(r.HookPlugins) > 0 {
		conflict = termio.StatusError // wired twice, right now, in this file
	}
	// A full wiring is OK; nothing wired is neutral (bough is simply not set
	// up here — a choice, not a fault); a partial wiring is genuinely odd.
	wiringStatus := termio.StatusOK
	switch {
	case wired == 0:
		wiringStatus = termio.StatusNeutral
	case wired < total:
		wiringStatus = termio.StatusWarn
	}

	fmt.Fprintf(w, "%s Hook wiring · settings.json: %s   (%d/%d wired)\n",
		st.Section(termio.Worst(wiringStatus, conflict)), r.SettingsPath, wired, total)

	// One line per event, mark first so the column of ✓ / · scans vertically.
	// The dispatcher command is identical for every bough-installed event, so
	// it is summarised once below instead of repeated eight times.
	for _, e := range r.Events {
		mark, label := termio.StatusNeutral, "not wired"
		switch {
		case e.BoughInstalled && e.HandEdited:
			mark, label = termio.StatusOK, "bough + hand-edited"
		case e.BoughInstalled:
			mark, label = termio.StatusOK, "bough"
		case e.HandEdited:
			mark, label = termio.StatusOK, "hand-edited"
		}
		fmt.Fprintf(w, "    %s %-18s %s\n", st.Mark(mark), e.Event, label)
	}
	if wired > 0 {
		fmt.Fprintln(w, "    • all bough events run: bough hook handle --event <Event>")
	}
	for _, e := range r.Events {
		for _, h := range e.HandEntries {
			fmt.Fprintf(w, "    • hand-edited %s: %s\n", e.Event, h.Command)
		}
	}

	// The conflict note keeps the exact wording (and the WARNING token) the
	// operator and the tests rely on; only the leading mark is coloured.
	switch conflict {
	case termio.StatusError:
		fmt.Fprintf(w, "    %s WARNING: bough's hooks are wired twice — here, and by %s.\n",
			st.Mark(termio.StatusError), strings.Join(r.HookPlugins, " + "))
		fmt.Fprintln(w, "      Every event fires both: observations double, the instinct block")
		fmt.Fprintln(w, "      is injected twice. Keep one —")
		fmt.Fprintln(w, "        bough claude hook uninstall     (keep the plugin's wiring)")
		fmt.Fprintln(w, "      ...or drop the plugin side, which means ALL of these — uninstalling")
		fmt.Fprintln(w, "      one of two leaves the other still firing:")
		for _, p := range r.HookPlugins {
			fmt.Fprintf(w, "        claude plugin uninstall %s\n", p)
		}
	default:
		switch {
		case len(r.HookPlugins) > 0:
			// Plugin owns the wiring and nothing is wired here — a correct
			// setup, so [✓], but say where the hooks come from or "not wired"
			// reads as "not observing" and invites the install that doubles.
			fmt.Fprintf(w, "    %s nothing is wired here, but %s supplies the same hooks.\n",
				st.Mark(termio.StatusOK), strings.Join(r.HookPlugins, " + "))
			fmt.Fprintln(w, "      Do not also run `bough claude hook install` — that is the double-fire.")
		case r.hasBoughSettingsHooks():
			// The common, correct state: wired here, no plugin here. The
			// cross-scope caveat is informational (•), so it never downgrades
			// the section below [✓].
			fmt.Fprintf(w, "    %s no hook-bearing bough plugin is enabled in this settings.json, so these\n",
				st.Mark(termio.StatusNeutral))
			fmt.Fprintln(w, "      entries are the only wiring bough can see. If bough-hooks or bough-all is")
			fmt.Fprintln(w, "      enabled at another scope (claude plugin list), events double-fire.")
		}
	}
}

func (r *DoctorReport) renderObserver(w io.Writer, st termio.Styler) {
	if r.Observer.Configured {
		fmt.Fprintf(w, "%s Observer\n", st.Section(termio.StatusOK))
		fmt.Fprintf(w, "    %s observations: %s (%d lines)\n",
			st.Mark(termio.StatusOK), r.Observer.Path, r.Observer.LineCount)
		return
	}
	// Not capturing yet is not a fault — it is the pre-first-run state.
	fmt.Fprintf(w, "%s Observer\n", st.Section(termio.StatusNeutral))
	fmt.Fprintf(w, "    %s not yet capturing (no observations.jsonl recorded yet for this project)\n",
		st.Mark(termio.StatusNeutral))
}

func (r *DoctorReport) renderCostMeter(w io.Writer, st termio.Styler) {
	if r.Cost.DataAvailable {
		fmt.Fprintf(w, "%s Cost meter\n", st.Section(termio.StatusOK))
		fmt.Fprintf(w, "    %s tokens=%d est=$%.4f last=%s\n",
			st.Mark(termio.StatusOK), r.Cost.Tokens, r.Cost.USDEst, r.Cost.LastSampleAt)
		return
	}
	fmt.Fprintf(w, "%s Cost meter\n", st.Section(termio.StatusNeutral))
	fmt.Fprintf(w, "    %s %s\n", st.Mark(termio.StatusNeutral), r.Cost.Message)
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
