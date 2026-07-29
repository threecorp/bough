package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/telemetry"
)

// realSkillPullPayload is a PostToolUse payload captured from a LIVE
// Claude Code 2.1.220 session in which a skill was actually pulled —
// not a struct literal built from what this package believes the shape
// to be. Only the identifiers and paths are neutralised (session id,
// transcript path, cwd, prompt/tool-use ids, the free-text args); every
// field NAME and the nesting are verbatim.
//
// That distinction is the whole point of this fixture. A gate of this
// kind has failed before precisely because its test fed the reader a
// hand-written fixture in the shape the author believed in — the same
// belief that produced the bug — so the reader and the real writer were
// never once compared. This file is that comparison. If Claude Code
// moves the slug, or bough's parser stops looking where it is, this
// test fails instead of the gate quietly reporting that nothing was
// ever pulled.
const realSkillPullPayload = `{
 "session_id": "00000000-0000-0000-0000-000000000000",
 "transcript_path": "/tmp/session.jsonl",
 "cwd": "/tmp/project",
 "prompt_id": "00000000-0000-0000-0000-000000000001",
 "permission_mode": "bypassPermissions",
 "effort": {
  "level": "xhigh"
 },
 "hook_event_name": "PostToolUse",
 "tool_name": "Skill",
 "tool_input": {
  "skill": "using-bough",
  "args": "an example request"
 },
 "tool_response": {
  "success": true,
  "commandName": "using-bough"
 },
 "tool_use_id": "toolu_0000000000000000000000",
 "duration_ms": 4
}`

// pullHarness puts the test in a project the resolver can identify and
// points the homunculus at a temp dir, then returns the telemetry path
// the writer should have used.
func pullHarness(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".bough.yaml"),
		[]byte("schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("BOUGH_HOMUNCULUS_DIR", home)
	t.Chdir(proj)

	ident, err := homunculus.DetectIdentity(proj)
	if err != nil {
		t.Fatalf("detect identity: %v", err)
	}
	return homunculus.NewLayout().TelemetryFile(ident.ID)
}

func loadPulls(t *testing.T, path string) map[string]int {
	t.Helper()
	lg, err := telemetry.Load(path)
	if err != nil {
		t.Fatalf("load telemetry: %v", err)
	}
	return telemetry.PullsBySlug(lg.Events)
}

// TestSkillPullContract_RealPayload is the writer↔reader contract: the
// bytes a real host sent, through the real dispatch, read back by the
// real reader, counted as one pull of the named skill.
func TestSkillPullContract_RealPayload(t *testing.T) {
	path := pullHarness(t)
	dispatchSkillPull(&cobra.Command{}, []byte(realSkillPullPayload))

	got := loadPulls(t, path)
	if got["using-bough"] != 1 {
		t.Fatalf("a real skill pull was not counted: %v\n(the payload the host actually sends is above; if this fails the reader and the host disagree)", got)
	}
	if len(got) != 1 {
		t.Errorf("exactly one slug should be counted, got %v", got)
	}
}

// TestSkillPullIgnoresOtherTools: everything else in a session also
// arrives on this hook, and counting any of it would make the gate's
// central number meaningless.
func TestSkillPullIgnoresOtherTools(t *testing.T) {
	path := pullHarness(t)
	dispatchSkillPull(&cobra.Command{}, []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":""}}`))
	dispatchSkillPull(&cobra.Command{}, []byte(`{"tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}`))

	if lg, err := telemetry.Load(path); err != nil {
		t.Fatal(err)
	} else if len(lg.Events) != 0 {
		t.Errorf("non-Skill tools must not be recorded, got %d events", len(lg.Events))
	}
}

// TestFailedPullIsNotEvidence: the gate asks whether the pull path
// WORKS. A load the host reports as failed delivered nothing, so
// counting it would let a portfolio of broken skills satisfy the gate.
func TestFailedPullIsNotEvidence(t *testing.T) {
	path := pullHarness(t)
	dispatchSkillPull(&cobra.Command{},
		[]byte(`{"tool_name":"Skill","tool_input":{"skill":"broken"},"tool_response":{"success":false,"commandName":"broken"}}`))

	if got := loadPulls(t, path); len(got) != 0 {
		t.Errorf("a failed pull must not count as the pull path firing: %v", got)
	}
}

// TestMovedSlugFieldBecomesDriftNotZero is the self-check at the writer
// end. If the host renames the field, the count must not silently
// become zero — the payload is kept so the reader can say so.
func TestMovedSlugFieldBecomesDriftNotZero(t *testing.T) {
	path := pullHarness(t)
	dispatchSkillPull(&cobra.Command{},
		[]byte(`{"tool_name":"Skill","tool_input":{"skillName":"moved"},"tool_response":{"success":true}}`))

	lg, err := telemetry.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lg.Events) != 1 {
		t.Fatalf("an unattributable pull must still be recorded, got %d events", len(lg.Events))
	}
	if len(telemetry.PullsBySlug(lg.Events)) != 0 {
		t.Error("it must not be counted under an empty slug")
	}
	rows := lg.Drift()
	if len(rows) != 1 || !strings.Contains(rows[0], "moved") {
		t.Fatalf("the moved field must surface as a drift row quoting the payload, got %v", rows)
	}
}

// TestResponseNameWinsOverRequestedName: tool_input.skill is what the
// model asked for, tool_response.commandName is what the host ran.
// When they differ the second is the fact.
func TestResponseNameWinsOverRequestedName(t *testing.T) {
	path := pullHarness(t)
	dispatchSkillPull(&cobra.Command{},
		[]byte(`{"tool_name":"Skill","tool_input":{"skill":"asked-for"},"tool_response":{"success":true,"commandName":"actually-ran"}}`))

	got := loadPulls(t, path)
	if got["actually-ran"] != 1 || got["asked-for"] != 0 {
		t.Errorf("the executed skill is the one that happened: %v", got)
	}
}

// TestMalformedPayloadIsIgnoredNotFatal: the hook runs on the
// operator's tool-call path. Telemetry is an observation, never a
// precondition.
func TestMalformedPayloadIsIgnoredNotFatal(t *testing.T) {
	path := pullHarness(t)
	dispatchSkillPull(&cobra.Command{}, []byte(`{not json`))
	dispatchSkillPull(&cobra.Command{}, nil)

	if _, err := os.Stat(path); err == nil {
		if lg, lerr := telemetry.Load(path); lerr == nil && len(lg.Events) != 0 {
			t.Errorf("garbage must not be recorded, got %d events", len(lg.Events))
		}
	}
}

// TestCapturedPayloadStillDecodesIntoTheModelledSubset guards the
// fixture itself: if someone edits realSkillPullPayload into a shape
// the struct no longer covers, that is a change to the contract and
// should be visible here rather than as a silent zero elsewhere.
func TestCapturedPayloadStillDecodesIntoTheModelledSubset(t *testing.T) {
	var p skillPullPayload
	if err := json.Unmarshal([]byte(realSkillPullPayload), &p); err != nil {
		t.Fatalf("the captured payload no longer decodes: %v", err)
	}
	if p.ToolName != "Skill" || p.ToolInput.Skill == "" || p.ToolResponse.CommandName == "" || !p.ToolResponse.Success {
		t.Fatalf("the captured payload lost a field the reader depends on: %+v", p)
	}
}
