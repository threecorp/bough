package evolve

import (
	"context"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// Outcome is the full result of one evolve pass. It carries enough
// detail for the CLI to render a preview AND for --generate to write
// the artifacts, so the pipeline runs once and the caller decides
// whether to persist.
type Outcome struct {
	InstinctCount int
	ClusterCount  int
	Skills        []SkillResult
	Agents        []AgentResult
	Commands      []CommandResult
	Rejected      []RejectedCluster
	// Assignments is the cluster membership of every discovered cluster,
	// INCLUDING the ones no gate passed. That is deliberate: a family of
	// restatements too incoherent to become a skill is exactly the family
	// most likely to crowd a prompt, so the injector's per-cluster cap
	// needs it stamped. Recorded here (never written by Run) so a preview
	// stays a preview.
	Assignments *ClusterAssignments
}

// SkillResult is a PASS / DOUBT cluster ready to emit as a skill.
// Verdict carries the GATE 5 decision; Label / Description are the
// final values (= the LLM's mint on PASS, the nearest-prior reuse on
// DOUBT). NewLabel is true when this mints a fresh cluster-labels
// entry.
type SkillResult struct {
	Cluster     Cluster
	Gate        GateVerdict
	Verdict     Verdict
	Label       string
	Description string
	NewLabel    bool
}

// AgentResult is an agent-eligible cluster (a superset relationship:
// every agent is also a skill cluster). Label + Description reuse the
// resolved skill label/description so the agent and skill never diverge.
type AgentResult struct {
	Cluster     Cluster
	Label       string
	Description string
}

// CommandResult is a workflow instinct eligible for a slash command.
type CommandResult struct {
	Instinct *homunculus.Instinct
}

// RejectedCluster records a cluster that failed a gate, for the
// preview's "why didn't this become a skill" section.
type RejectedCluster struct {
	Cluster Cluster
	Gate    GateVerdict
	Verdict *Verdict // non-nil when it reached GATE 5 and FAILED, or the judge errored (recorded, not minted)
}

// Pipeline runs the full evolve flow over a corpus. Judge is the
// GATE 5 backend (production = ClaudeJudge, tests = a stub). Now is
// injectable for deterministic output.
type Pipeline struct {
	Judge      Judge
	Thresholds Thresholds
	Now        func() time.Time

	// OnJudge, when set, is invoked once per gate-passing cluster as its
	// GATE 5 verdict lands, so a multi-minute --generate run streams
	// progress instead of looking hung. It is a pure callback — the
	// pipeline writes nothing itself.
	OnJudge func(JudgeProgress)
}

// JudgeProgress reports one GATE 5 verdict as it is produced. Err is
// non-nil when the verdict is an error / rate-limit fallback to DOUBT
// (= NOT the model's actual decision), so the CLI can tell the operator
// which DOUBTs were real judgements and which were the judge being
// unavailable.
type JudgeProgress struct {
	Index    int    // 1-based, among gate-passing clusters
	Sample   string // a representative member id, for a scannable line
	Members  int    // cluster size
	Decision string // PASS / DOUBT / FAIL (matches Verdict.Decision)
	Err      error  // non-nil → verdict is an error/limit fallback to DOUBT
}

// Run executes discovery → GATE 1-4 → GATE 5 → eligibility checks and
// returns the Outcome WITHOUT writing anything. The caller persists
// via the WriteSkill / WriteAgent / WriteCommand emitters (or skips
// them for a preview). priors come from the existing
// cluster-labels.json so the gates measure against published labels.
func (p Pipeline) Run(ctx context.Context, instincts []*homunculus.Instinct, labels *ClusterLabels) (Outcome, error) {
	// p.Now is reserved for future provenance stamping inside Run;
	// v0.9.1 stamps timestamps in the CLI emit layer (RenderSkill /
	// RenderAgent / RenderCommand take their own clock) so Run itself
	// does not need the clock yet.
	th := p.Thresholds
	if th.MemberMin == 0 {
		th = DefaultThresholds()
	}

	priors := labels.Priors()
	priorUnion := labels.PriorUnion()
	clusters := Discover(instincts, priors, th)

	out := Outcome{
		InstinctCount: len(instincts),
		ClusterCount:  len(clusters),
		Assignments:   NewClusterAssignments(clusters),
	}

	judged := 0 // gate-passing clusters sent to GATE 5, for progress
	for _, c := range clusters {
		gate := EvaluateGatesWithPriorUnion(c, priorUnion, th)
		if !gate.Passed {
			out.Rejected = append(out.Rejected, RejectedCluster{Cluster: c, Gate: gate})
			continue
		}

		verdict, err := p.Judge.Judge(ctx, JudgeInput{
			ProjectName:       "", // filled by the CLI; not load-bearing for routing
			MemberCount:       len(c.Members),
			Cohesion:          gate.Cohesion,
			LexiconCoverage:   gate.LexiconCoverage,
			RelIsolation:      gate.RelIsolation,
			MaxPriorOverlap:   gate.MaxPriorOverlap,
			NearestPriorLabel: priorLabel(c.NearestPrior),
			Cluster:           c,
			Gate:              gate,
		})
		if err != nil {
			// Judge unavailable (rate-limit / cap / transport). Shape a
			// DOUBT verdict for the preview's rejected-cluster section, but
			// the cluster is RECORDED AS REJECTED below (not minted) — we
			// never actually judged it.
			verdict = Verdict{
				Decision:   DecisionDoubt,
				Confidence: 0.3,
				Reason:     "GATE 5 judge errored; recorded as rejected (not minted): " + err.Error(),
			}
		}

		judged++
		if p.OnJudge != nil {
			sample := ""
			if len(c.Members) > 0 {
				sample = c.Members[0].ID
			}
			p.OnJudge(JudgeProgress{
				Index: judged, Sample: sample, Members: len(c.Members),
				Decision: verdict.Decision, Err: err,
			})
		}

		// Judge UNAVAILABLE (rate-limit / parse / transport / circuit) is
		// NOT a model decision. Do not mint a skill from a cluster we never
		// actually judged, or a capped run permanently pollutes the catalog.
		// Record it as rejected (the OnJudge line + errCapped note already
		// surfaced it). A real DOUBT (err == nil) still flows to the switch.
		if err != nil {
			vCopy := verdict
			out.Rejected = append(out.Rejected, RejectedCluster{Cluster: c, Gate: gate, Verdict: &vCopy})
			continue
		}

		var skillLabel, skillDesc string
		switch verdict.Decision {
		case DecisionFail:
			vCopy := verdict
			out.Rejected = append(out.Rejected, RejectedCluster{Cluster: c, Gate: gate, Verdict: &vCopy})
			continue
		case DecisionDoubt:
			label, desc, newLabel := resolveDoubtLabel(verdict, c, labels)
			skillLabel, skillDesc = label, desc
			out.Skills = append(out.Skills, SkillResult{
				Cluster: c, Gate: gate, Verdict: verdict,
				Label: label, Description: desc, NewLabel: newLabel,
			})
		case DecisionPass:
			skillLabel, skillDesc = verdict.Label, verdict.Description
			out.Skills = append(out.Skills, SkillResult{
				Cluster: c, Gate: gate, Verdict: verdict,
				Label: verdict.Label, Description: verdict.Description, NewLabel: true,
			})
		}

		// Agent reuses the SAME label + description the skill resolved, so
		// evolved/agents/<slug>.md and evolved/skills/<slug>/ never diverge.
		if AgentEligible(c) && IsValidLabel(skillLabel) {
			out.Agents = append(out.Agents, AgentResult{Cluster: c, Label: skillLabel, Description: skillDesc})
		}
	}

	// Command eligibility is per-instinct, independent of clustering.
	for _, in := range instincts {
		if CommandEligible(in) {
			out.Commands = append(out.Commands, CommandResult{Instinct: in})
		}
	}

	return out, nil
}

func resolveDoubtLabel(v Verdict, c Cluster, labels *ClusterLabels) (label, desc string, newLabel bool) {
	// DOUBT reuses the nearest prior label when one exists.
	if c.NearestPrior != nil {
		return c.NearestPrior.Label, c.NearestPrior.Description, false
	}
	// No prior to reuse → fall back to the LLM's label (or a slugified
	// one) and mint it.
	label = v.Label
	if !IsValidLabel(label) {
		label = Slugify(firstMemberID(c))
	}
	desc = v.Description
	if desc == "" {
		desc = "Apply when the cluster's workflow recurs"
	}
	_, exists := labels.Labels[label]
	return label, desc, !exists
}

func priorLabel(p *Prior) string {
	if p == nil {
		return ""
	}
	return p.Label
}

func firstMemberID(c Cluster) string {
	if len(c.Members) == 0 {
		return "cluster"
	}
	return c.Members[0].ID
}
