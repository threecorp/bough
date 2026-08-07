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

// TestEvaluate_RecordsThatAnInstinctWasExercised separates the two
// halves the advisory switch has to keep apart. The CONFIDENCE verdict
// is advisory and must not be written; LastSeen / Observed are not
// credit assignment — they record that the session used the instinct,
// which the overlap check just established. The first cut froze them
// too, which made `instinct status` report an instinct used every day
// as weeks old and left the injection ranker's recency prior ordering
// by mint date.
func TestEvaluate_RecordsThatAnInstinctWasExercised(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	writeInstinct(t, dir, "migration-discipline", 0.70, "Run migration after schema change to keep database models in sync")

	obs := []observe.Observation{
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"make migration database schema sync models"}`)},
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"run migration change schema"}`)},
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if _, err := Evaluate(layout, pid, "s-recency", obs, now); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	reloaded, err := homunculus.ReadInstinctFile(filepath.Join(dir, "migration-discipline.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.LastSeen.Equal(now) {
		t.Errorf("LastSeen = %v, want %v (an exercised instinct must record that it was used)", reloaded.LastSeen, now)
	}
	if reloaded.Observed != 1 {
		t.Errorf("Observed = %d, want 1", reloaded.Observed)
	}
	if reloaded.Confidence != 0.70 {
		t.Errorf("confidence = %v, want 0.70 — the band move stays advisory", reloaded.Confidence)
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
	// ADVISORY: the count is reported but the file is NOT rewritten. The
	// write path stays off until observations can attribute an outcome to
	// a specific instinct (see the confidenceBands comment in evaluate.go).
	reloaded, _ := homunculus.ReadInstinctFile(filepath.Join(dir, "migration-discipline.md"))
	if reloaded.Confidence != 0.70 {
		t.Errorf("confidence = %v, want 0.70 unchanged (advisory evaluation must not write)", reloaded.Confidence)
	}
}

// TestEvaluate_DemotesExercisedInstinctOnCorrection is the #4 regression
// (v0.9.12), updated for v0.9.18: an instinct the session exercised is DEMOTED
// when the USER's prompt shows a whole-word correction marker. (Pre-v0.9.18
// the scan also covered tool outputs, so build-log tokens demoted good
// instincts every session — see TestEvaluate_ToolOutputMarkersDoNotDemote.)
func TestEvaluate_DemotesExercisedInstinctOnCorrection(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	writeInstinct(t, dir, "migration-discipline", 0.70, "Run migration after schema change to keep database models in sync")

	// the session exercised the instinct (overlap) AND the user corrected
	// bough in a prompt ("wrong", "undo") → correction → demote.
	obs := []observe.Observation{
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"make migration database schema sync models"}`)},
		{Event: "UserPromptSubmit", Prompt: "no, that migration is wrong — undo it"},
	}
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	res, err := Evaluate(layout, pid, "sess-1", obs, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Contradicted != 1 || res.Reinforced != 0 {
		t.Errorf("want Contradicted=1 Reinforced=0, got %+v", res)
	}
	// ADVISORY: the contradiction is counted for the audit log, but the
	// corpus is untouched. A session-wide correction flag cannot say WHICH
	// instinct was wrong; on the live corpus it demoted everything a busy
	// session brushed and sank 407 of 409 instincts below the injection
	// gate. Not writing is the regression being pinned here.
	reloaded, _ := homunculus.ReadInstinctFile(filepath.Join(dir, "migration-discipline.md"))
	if reloaded.Confidence != 0.70 {
		t.Errorf("confidence = %v, want 0.70 unchanged (advisory evaluation must not demote the corpus)", reloaded.Confidence)
	}
}

// TestEvaluate_ToolOutputMarkersDoNotDemote is the v0.9.18 regression for the
// over-broad correction scan: marker SUBSTRINGS in TOOL OUTPUT ("0 errors",
// "fixtures", "correctly") must NOT demote — only a whole-word marker in the
// USER's prompt does. Before the fix these tokens (in essentially every
// session) demoted good instincts out of the injected set.
func TestEvaluate_ToolOutputMarkersDoNotDemote(t *testing.T) {
	root := t.TempDir()
	layout := homunculus.FromRoot(root)
	pid := "abc123"
	dir := layout.InstinctsDir(pid)
	_ = os.MkdirAll(dir, 0o755)
	writeInstinct(t, dir, "migration-discipline", 0.70, "Run migration after schema change to keep database models in sync")

	obs := []observe.Observation{
		{Event: "PostToolUse", Tool: "Bash", ToolInput: json.RawMessage(`{"command":"make migration database schema sync models"}`)},
		{Event: "PostToolUse", Tool: "Bash", ToolOutput: json.RawMessage(`{"stdout":"build ok, 0 errors; loaded test/fixtures; passed correctly"}`)},
		{Event: "UserPromptSubmit", Prompt: "great, now add the prefix column too"},
	}
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	res, err := Evaluate(layout, pid, "sess-1", obs, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Reinforced != 1 || res.Contradicted != 0 {
		t.Errorf("tool-output markers must not demote: want Reinforced=1 Contradicted=0, got %+v", res)
	}
	reloaded, _ := homunculus.ReadInstinctFile(filepath.Join(dir, "migration-discipline.md"))
	if reloaded.Confidence != 0.70 {
		t.Errorf("confidence = %v, want 0.70 unchanged (advisory; classification still must not read tool output as a correction)", reloaded.Confidence)
	}
}

// TestSessionHadCorrection_PromptScopedWordBoundary locks the v0.9.18 signal:
// scan user prompts only, as whole words.
func TestSessionHadCorrection_PromptScopedWordBoundary(t *testing.T) {
	cases := []struct {
		name string
		obs  []observe.Observation
		want bool
	}{
		{
			"tool output '0 errors' is not a correction",
			[]observe.Observation{{Event: "PostToolUse", ToolOutput: json.RawMessage(`{"stdout":"build ok, 0 errors"}`)}},
			false,
		},
		{
			"tool output 'fixtures'/'correctly' substrings ignored",
			[]observe.Observation{{Event: "PostToolUse", ToolOutput: json.RawMessage(`{"stdout":"loaded fixtures, passed correctly"}`)}},
			false,
		},
		{
			"prompt 'prefix' substring does not match fix",
			[]observe.Observation{{Event: "UserPromptSubmit", Prompt: "add a prefix to the filename"}},
			false,
		},
		{
			"prompt 'that's wrong' is a correction",
			[]observe.Observation{{Event: "UserPromptSubmit", Prompt: "no, that's wrong"}},
			true,
		},
		{
			"prompt 'undo that' is a correction",
			[]observe.Observation{{Event: "UserPromptSubmit", Prompt: "undo that change"}},
			true,
		},
		{
			"task prompt 'fix the bug' is NOT a correction (v0.9.19 marker narrowing)",
			[]observe.Observation{{Event: "UserPromptSubmit", Prompt: "fix the login bug"}},
			false,
		},
		{
			"task prompt 'there is an error' is NOT a correction",
			[]observe.Observation{{Event: "UserPromptSubmit", Prompt: "there is an error in the parser, please handle it"}},
			false,
		},
		{
			"prompt 'you broke it' is a correction",
			[]observe.Observation{{Event: "UserPromptSubmit", Prompt: "you broke the build"}},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionHadCorrection(tc.obs); got != tc.want {
				t.Errorf("sessionHadCorrection = %v, want %v", got, tc.want)
			}
		})
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
	// v0.9.11: the returned block (printed to the transcript on PreCompact;
	// re-surfacing into context is via the next UserPromptSubmit inject) must
	// equal the persisted MEMORY.md.
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

// TestFirstActionLine_CaseInsensitiveHeading is the regression guard
// for the wave-4 review finding: firstActionLine matched "## Action"
// with a case-sensitive ==, unlike every other implementation of this
// helper (inject.go, evolve/judge.go, cli/claudemd.go), so a
// differently-cased heading (e.g. hand-edited or migrated in via
// `bough ecc import`) fell through to the fallback loop and returned
// the wrong line (typically the Trigger description) instead of the
// real action.
func TestFirstActionLine_CaseInsensitiveHeading(t *testing.T) {
	got := firstActionLine("## action\nretry the request")
	if got != "retry the request" {
		t.Errorf("firstActionLine with lowercase heading = %q, want %q", got, "retry the request")
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

// TestAddTokens_NonASCIIProducesTokens is the regression guard for the
// wave-4 review finding: addTokens only recognized ASCII a-z/0-9, so
// instinctOverlap always evaluated to 0 for Japanese (or any
// non-Latin script) instinct/observation text, permanently disabling
// confidence reinforcement/demotion for it.
func TestAddTokens_NonASCIIProducesTokens(t *testing.T) {
	set := map[string]struct{}{}
	addTokens(set, "データベース接続")
	if len(set) == 0 {
		t.Fatalf("addTokens produced no tokens for Japanese text: %q", "データベース接続")
	}
}

func TestInstinctOverlap_NonASCIITriggerMatchesObservation(t *testing.T) {
	// Quoted/spaced so the shared Japanese substring tokenizes as its
	// own token on both sides, the same shape a real JSON tool_input
	// or a prompt sentence produces.
	obsTokens := map[string]struct{}{}
	addTokens(obsTokens, `retry "データベース接続" failed`)

	in := &homunculus.Instinct{Trigger: `see "データベース接続" error`}
	overlap := instinctOverlap(in, obsTokens)
	if overlap <= 0 {
		t.Errorf("instinctOverlap = %v for a Japanese trigger sharing a token with the observation, want > 0", overlap)
	}
}
