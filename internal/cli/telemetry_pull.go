package cli

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/telemetry"
)

// The completion gate asks one question the corpus cannot answer:
// is the PULL path firing? A deployed skill that nothing ever loads
// looks, on disk, exactly like a deployed skill that the model reaches
// for every day. Only the host can tell them apart, and it does — a
// skill load arrives as an ordinary tool use.
//
// Measured against Claude Code 2.1.220 (a real session, not a fixture):
//
//	"tool_name": "Skill",
//	"tool_input":    {"skill": "using-bough", "args": "…"},
//	"tool_response": {"success": true, "commandName": "using-bough"}
//
// The slug appears twice. `tool_input.skill` is what the model asked
// for; `tool_response.commandName` is what the host actually ran. This
// records the second when it is there, because a pull that was
// requested and failed is not a pull — and it takes `success` into
// account for the same reason.

// skillPullPayload is the subset of the PostToolUse payload this
// records. Fields bough does not model are left alone: the raw bytes
// are what a drift row quotes when the shape moves.
type skillPullPayload struct {
	ToolName  string `json:"tool_name"`
	SessionID string `json:"session_id"`
	ToolInput struct {
		Skill string `json:"skill"`
	} `json:"tool_input"`
	ToolResponse struct {
		Success     bool   `json:"success"`
		CommandName string `json:"commandName"`
	} `json:"tool_response"`
}

// dispatchSkillPull records a successful skill load. Every branch that
// declines to record is silent by design EXCEPT the one that matters:
// a Skill tool use whose slug could not be found is still written, with
// the payload attached, so the reader reports drift instead of a zero.
func dispatchSkillPull(c *cobra.Command, payload []byte) {
	var p skillPullPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if p.ToolName != "Skill" {
		return
	}
	path := resolveHomunculusTelemetryPath()
	if path == "" {
		return
	}
	w := telemetry.NewWriter(path)

	// A pull the host reports as failed did not deliver anything, so it
	// is not evidence that the pull path works. Recording it would let
	// a portfolio of broken skills satisfy the gate.
	if !p.ToolResponse.Success {
		return
	}
	slug := p.ToolResponse.CommandName
	if slug == "" {
		slug = p.ToolInput.Skill
	}
	if slug == "" {
		// The shape moved. Keep the payload: a count of zero and a
		// parser that cannot see the field are indistinguishable
		// downstream, and only the bytes tell them apart.
		w.AppendBestEffort(telemetry.Event{
			Kind:    telemetry.KindSkillPull,
			Session: p.SessionID,
			Raw:     json.RawMessage(payload),
		})
		return
	}
	w.AppendBestEffort(telemetry.Event{
		Kind:    telemetry.KindSkillPull,
		Slug:    slug,
		Session: p.SessionID,
	})
}

// resolveHomunculusTelemetryPath asks the shared resolver for this
// project's telemetry file. It goes through resolveHomunculusFile rather
// than repeating the cwd → identity → ensure-dirs walk, because a
// session inside a worktree must record against the same project the
// observations pool into: two resolvers that could disagree about which
// project a session belongs to would split the very history the gate
// reads, and keeping two copies in step is left to whoever edits one.
func resolveHomunculusTelemetryPath() string {
	return resolveHomunculusFile(homunculus.Layout.TelemetryFile)
}

// resolveHomunculusFile resolves the current project and returns the
// file `pick` names for it, or "" when the project cannot be resolved.
// Callers differ only in which file they want.
func resolveHomunculusFile(pick func(homunculus.Layout, string) string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	ident, err := homunculus.DetectIdentity(resolveMonorepoRoot(cwd))
	if err != nil {
		return ""
	}
	layout := homunculus.NewLayout()
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		return ""
	}
	return pick(layout, ident.ID)
}
