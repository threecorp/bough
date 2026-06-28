package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/observe"
)

func TestStepBand(t *testing.T) {
	// up from 0.70 → 0.75
	if got := stepBand(0.70, 1); got != 0.75 {
		t.Errorf("stepBand(0.70,+1) = %v, want 0.75", got)
	}
	// down from 0.70 → 0.65
	if got := stepBand(0.70, -1); got != 0.65 {
		t.Errorf("stepBand(0.70,-1) = %v, want 0.65", got)
	}
	// clamp at top
	if got := stepBand(0.85, 1); got != 0.85 {
		t.Errorf("stepBand(0.85,+1) = %v, want 0.85 (clamp)", got)
	}
	// down from 0.50 → 0.40 (v0.9.12 extended the ladder below inject's
	// 0.50 floor so contradicted instincts can decay out of the set)
	if got := stepBand(0.50, -1); got != 0.40 {
		t.Errorf("stepBand(0.50,-1) = %v, want 0.40", got)
	}
	// clamp at the new bottom (0.30)
	if got := stepBand(0.30, -1); got != 0.30 {
		t.Errorf("stepBand(0.30,-1) = %v, want 0.30 (clamp)", got)
	}
	// off-ladder 0.73 snaps to nearest (0.75) then... up = 0.80
	if got := stepBand(0.73, 1); got != 0.80 {
		t.Errorf("stepBand(0.73,+1) = %v, want 0.80", got)
	}
}

func writeInstinct(t *testing.T, dir, id string, conf float64, action string) {
	t.Helper()
	in := &homunculus.Instinct{
		ID:         id,
		Trigger:    "when editing " + id,
		Confidence: conf,
		Domain:     "workflow",
		Scope:      "project",
		Body:       "## Action\n" + action,
	}
	if _, err := homunculus.WriteInstinctFile(dir, in); err != nil {
		t.Fatalf("write instinct: %v", err)
	}
}

func TestEvaluate_ReinforcesExercisedInstinct(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	// instinct about "migration database schema" at 0.70
	writeInstinct(t, dir, "migration-discipline", 0.70, "Run migration after schema change to keep database models in sync")

	// observation stream mentioning migration + schema + database
	obs := []observe.Observation{
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"make migration database schema sync models"}`)},
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"run migration change schema"}`)},
	}
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	res, err := Evaluate(layout, pid, "sess-1", obs, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Reinforced != 1 {
		t.Errorf("Reinforced = %d, want 1 (overlap should trigger reinforce)", res.Reinforced)
	}
	// confidence should have climbed to 0.75
	reloaded, _ := homunculus.ReadInstinctFile(filepath.Join(dir, "migration-discipline.md"))
	if reloaded.Confidence != 0.75 {
		t.Errorf("confidence = %v, want 0.75 after reinforce", reloaded.Confidence)
	}
}

// TestEvaluate_DemotesExercisedInstinctOnCorrection is the v0.9.12 #4
// regression: an instinct the session exercised is DEMOTED (not
// reinforced) when the session shows a correction marker — before this,
// confidence could only ever increase.
func TestEvaluate_DemotesExercisedInstinctOnCorrection(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	writeInstinct(t, dir, "migration-discipline", 0.70, "Run migration after schema change to keep database models in sync")

	// the session exercised the instinct (overlap) AND hit an error in
	// a tool output → correction → demote.
	obs := []observe.Observation{
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"make migration database schema sync models"}`)},
		{Event: "PostToolUse", Tool: "Bash", ToolOutput: json.RawMessage(`{"stderr":"Error 1265: data truncated; migration wrong"}`)},
	}
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	res, err := Evaluate(layout, pid, "sess-1", obs, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Contradicted != 1 || res.Reinforced != 0 {
		t.Errorf("want Contradicted=1 Reinforced=0, got %+v", res)
	}
	reloaded, _ := homunculus.ReadInstinctFile(filepath.Join(dir, "migration-discipline.md"))
	if reloaded.Confidence != 0.65 {
		t.Errorf("confidence = %v, want 0.65 after demotion", reloaded.Confidence)
	}
}

func TestEvaluate_LeavesUnrelatedUnchanged(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	writeInstinct(t, dir, "git-discipline", 0.70, "Verify git status before and after operations")

	obs := []observe.Observation{
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"npm run typecheck frontend vite"}`)},
	}
	res, err := Evaluate(layout, pid, "sess-1", obs, time.Now())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Reinforced != 0 || res.Unchanged != 1 {
		t.Errorf("unrelated instinct should be unchanged: %+v", res)
	}
}

func TestAppendScore(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	res := EvalResult{SessionID: "s1", Observations: 5, Reinforced: 2}
	if err := AppendScore(layout, pid, res); err != nil {
		t.Fatalf("AppendScore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(layout.EvalDir(pid), "scores.jsonl"))
	if err != nil {
		t.Fatalf("read scores: %v", err)
	}
	if !strings.Contains(string(data), `"session_id":"s1"`) {
		t.Errorf("scores.jsonl missing record: %s", data)
	}
}

func TestPreserveInstincts(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	for i, conf := range []float64{0.85, 0.70, 0.60, 0.50, 0.55, 0.80, 0.75} {
		writeInstinct(t, dir, "instinct-"+string(rune('a'+i)), conf, "action "+string(rune('a'+i)))
	}
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	path, block, err := PreserveInstincts(layout, pid, now)
	if err != nil {
		t.Fatalf("PreserveInstincts: %v", err)
	}
	if filepath.Base(path) != "MEMORY.md" {
		t.Errorf("path = %q", path)
	}
	body, _ := os.ReadFile(path)
	// v0.9.11: the returned block (printed to stdout so it folds into
	// the compacted context) must equal the persisted MEMORY.md.
	if block != string(body) {
		t.Errorf("returned block != MEMORY.md content")
	}
	// should contain the top 5 (= 0.85, 0.80, 0.75, 0.70, 0.60)
	if !strings.Contains(string(body), "85%") || !strings.Contains(string(body), "80%") {
		t.Errorf("MEMORY.md missing top instincts:\n%s", body)
	}
	// the lowest (0.50) should be excluded since only top 5 kept
	lines := strings.Count(string(body), "- [")
	if lines != PreservedTopN {
		t.Errorf("MEMORY.md has %d instinct lines, want %d", lines, PreservedTopN)
	}
	// MEMORY.md must not be re-ingested as an instinct
	scanned, _ := homunculus.ScanInstincts(dir)
	for _, in := range scanned {
		if in.ID == "MEMORY" {
			t.Errorf("MEMORY.md was ingested as an instinct")
		}
	}
}

func TestSummary(t *testing.T) {
	res := EvalResult{SessionID: "s1", Observations: 3, Reinforced: 1, Unchanged: 2}
	obs := []observe.Observation{
		{Event: "PostToolUse"}, {Event: "PostToolUse"}, {Event: "Stop"},
	}
	out := Summary(res, obs)
	if !strings.Contains(out, "session s1: 3 observations") {
		t.Errorf("summary header missing: %s", out)
	}
	if !strings.Contains(out, "PostToolUse") {
		t.Errorf("summary missing event breakdown: %s", out)
	}
}
