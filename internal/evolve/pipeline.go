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
// every agent is also a skill cluster). Label reuses the skill label.
type AgentResult struct {
	Cluster Cluster
	Label   string
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
	Verdict *Verdict // non-nil only when it reached + failed GATE 5
}

// Pipeline runs the full evolve flow over a corpus. Judge is the
// GATE 5 backend (production = ClaudeJudge, tests = a stub). Now is
// injectable for deterministic output.
type Pipeline struct {
	Judge      Judge
	Thresholds Thresholds
	Now        func() time.Time
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
	}

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
			// Judge failure → treat as DOUBT so the operator reviews
			// rather than the cluster silently disappearing.
			verdict = Verdict{
				Decision:   DecisionDoubt,
				Confidence: 0.3,
				Reason:     "GATE 5 judge errored; surfaced as DOUBT for operator review: " + err.Error(),
			}
		}

		switch verdict.Decision {
		case DecisionFail:
			vCopy := verdict
			out.Rejected = append(out.Rejected, RejectedCluster{Cluster: c, Gate: gate, Verdict: &vCopy})
			continue
		case DecisionDoubt:
			label, desc, newLabel := resolveDoubtLabel(verdict, c, labels)
			out.Skills = append(out.Skills, SkillResult{
				Cluster: c, Gate: gate, Verdict: verdict,
				Label: label, Description: desc, NewLabel: newLabel,
			})
		case DecisionPass:
			out.Skills = append(out.Skills, SkillResult{
				Cluster: c, Gate: gate, Verdict: verdict,
				Label: verdict.Label, Description: verdict.Description, NewLabel: true,
			})
		}

		// Agent eligibility is checked on every accepted cluster.
		if AgentEligible(c) {
			label := verdict.Label
			if label == "" {
				label = resolveDoubtLabelOnly(verdict, c)
			}
			if IsValidLabel(label) {
				out.Agents = append(out.Agents, AgentResult{Cluster: c, Label: label})
			}
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

func resolveDoubtLabelOnly(v Verdict, c Cluster) string {
	if c.NearestPrior != nil {
		return c.NearestPrior.Label
	}
	if IsValidLabel(v.Label) {
		return v.Label
	}
	return Slugify(firstMemberID(c))
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
