package evaluators

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/ikeikeikeike/bough/plugins/evaluator/api"
)

// SkillAudit implements the paired-trajectory auditor strategy:
// compare the current artifact against a peer in the same Kind +
// scope family and recommend keep on the survivor / prune on the
// loser based on token overlap + confidence delta.
//
// When the host wants to retire duplicates in the memory backend
// (= e.g. two competing "test-naming" instincts that overlap 80%
// of their token sets) this evaluator points at which one to keep.
type SkillAudit struct{}

// Name returns the canonical evaluator name.
func (SkillAudit) Name() string { return "skillaudit" }

// skillauditContext is the shape EvaluatorContextJSON carries:
// Peer is the JSON-encoded competing artifact the auditor scores
// against. PeerConfidence is the peer's bough confidence.
type skillauditContext struct {
	Peer           json.RawMessage `json:"peer"`
	PeerConfidence float64         `json:"peer_confidence"`
}

// SkillAuditOverlapThreshold is the token-overlap point above which
// two artifacts are considered "competing for the same niche". If
// overlap is below this they are not paired siblings and the
// auditor returns keep without comparing.
const SkillAuditOverlapThreshold = 0.5

// Evaluate decision:
//
//	overlap < 0.5            → keep (= not actually competing)
//	overlap ≥ 0.5 AND
//	  artifact > peer in
//	  confidence              → keep current, recommend prune peer
//	  peer > artifact in
//	  confidence              → recommend prune current
//	  tie                     → keep both, surface revise
func (s SkillAudit) Evaluate(_ context.Context, req *api.EvaluateReq) (*api.EvaluateResp, error) {
	a, err := decodeArtifact(req.ArtifactJSON)
	if err != nil {
		return nil, fmt.Errorf("skillaudit: decode artifact: %w", err)
	}
	var ctx skillauditContext
	if len(req.EvaluatorContextJSON) > 0 {
		_ = json.Unmarshal(req.EvaluatorContextJSON, &ctx)
	}
	if len(ctx.Peer) == 0 {
		return &api.EvaluateResp{
			Outcome:     api.OutcomeKeep,
			Utility:     0.5,
			Explanation: "no peer artifact supplied; nothing to audit — keep",
		}, nil
	}
	peer, err := decodeArtifact(ctx.Peer)
	if err != nil {
		return nil, fmt.Errorf("skillaudit: decode peer: %w", err)
	}

	artifactText := strings.Join(append([]string{a.Description}, append(a.Steps, a.Constraints...)...), "\n")
	peerText := strings.Join(append([]string{peer.Description}, append(peer.Steps, peer.Constraints...)...), "\n")

	overlap := 1 - jaccardDistance(tokenSet(artifactText), tokenSet(peerText))

	if overlap < SkillAuditOverlapThreshold {
		return &api.EvaluateResp{
			Outcome:     api.OutcomeKeep,
			Utility:     0.55,
			Explanation: fmt.Sprintf("overlap %.2f < %.2f → not paired, keep both", overlap, SkillAuditOverlapThreshold),
		}, nil
	}

	currConf := a.Confidence
	peerConf := ctx.PeerConfidence
	if peerConf == 0 && peer.Confidence > 0 {
		peerConf = peer.Confidence
	}
	delta := currConf - peerConf

	var outcome api.EvaluationOutcome
	var utility float64
	var expl string
	prune := false
	switch {
	case delta > 0.05:
		outcome = api.OutcomeKeep
		utility = 0.8
		expl = fmt.Sprintf("overlap %.2f, current %.2f > peer %.2f → keep current; recommend prune peer", overlap, currConf, peerConf)
	case delta < -0.05:
		outcome = api.OutcomePrune
		prune = true
		utility = 0.2
		expl = fmt.Sprintf("overlap %.2f, peer %.2f > current %.2f → prune current", overlap, peerConf, currConf)
	default:
		outcome = api.OutcomeRevise
		utility = 0.5
		expl = fmt.Sprintf("overlap %.2f, |conf delta| %.2f ≤ 0.05 → tie, revise both", overlap, delta)
	}
	payload, _ := json.Marshal(struct {
		Overlap     float64 `json:"overlap"`
		Delta       float64 `json:"confidence_delta"`
		PeerName    string  `json:"peer_name"`
		CurrentName string  `json:"current_name"`
	}{Overlap: overlap, Delta: delta, PeerName: peer.Name, CurrentName: a.Name})
	return &api.EvaluateResp{
		Outcome:              outcome,
		Utility:              utility,
		ConfidenceDelta:      utility - currConf,
		ShouldPrune:          prune,
		Explanation:          expl,
		EvaluatorPayloadJSON: payload,
	}, nil
}
