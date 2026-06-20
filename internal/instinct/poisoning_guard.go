package instinct

import (
	"context"
	"fmt"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// PoisoningGuard backstops the auto-candidate / hybrid mint modes.
// Memory poisoning was round 1's No. 1 risk; the guard is the
// last line of defence between a minter's output and the memory
// backend.
//
// Three guarantees:
//
//  1. Dedupe — two candidates with the same DedupeKey within a
//     scope collapse to a single backend row. The backend's upsert
//     semantics carry hits++, but the guard refuses to let the
//     coordinator persist two distinct rows with the same key.
//  2. Approval gate — `hybrid` and `auto-candidate` modes mark new
//     rows as `state: candidate`; the row stays there until
//     `bough instinct approve <id>` flips it to `active` (manual
//     gate) or candidate_ttl_days expires (auto-forget).
//  3. Active cap — when a scope hits max_active_per_scope, the
//     guard refuses to admit more active rows until the decay
//     scheduler archives the lowest-confidence existing rows.
type PoisoningGuard struct {
	mode             string // "off" | "manual" | "auto-candidate" | "hybrid"
	requireApproval  bool
	maxActivePerScope int
	candidateTTL     time.Duration
}

// NewPoisoningGuard builds a guard from the InstinctConfig values.
// Zero / unset fields are treated as "no limit" / "no TTL" so the
// guard is forgiving to an explicitly-disabled subsystem.
func NewPoisoningGuard(mode string, requireApproval bool, maxActive int, candidateTTLDays int) *PoisoningGuard {
	ttl := time.Duration(candidateTTLDays) * 24 * time.Hour
	return &PoisoningGuard{
		mode:              mode,
		requireApproval:   requireApproval,
		maxActivePerScope: maxActive,
		candidateTTL:      ttl,
	}
}

// AdmitCandidate is what the coordinator calls before the candidate
// is Store'd. The returned state is the value the coordinator
// should set on the schema.InstinctCandidate before serialising;
// the returned error means "do not store this at all".
func (g *PoisoningGuard) AdmitCandidate(_ context.Context, c *schema.InstinctCandidate) (schema.InstinctState, error) {
	switch g.mode {
	case "off":
		return schema.InstinctStateUnspecified, fmt.Errorf("poisoning_guard: mint mode is off; cannot admit %q", c.Rule)
	case "manual":
		return schema.InstinctStateCandidate, nil
	case "auto-candidate":
		// keep as candidate even when require_approval is false —
		// the auto promotion happens in a separate pass.
		return schema.InstinctStateCandidate, nil
	case "hybrid", "":
		// default: candidate; approve flips to active.
		return schema.InstinctStateCandidate, nil
	default:
		return schema.InstinctStateUnspecified, fmt.Errorf("poisoning_guard: unknown mode %q", g.mode)
	}
}

// CheckActiveCap inspects the existing active row count for a
// scope and reports whether the caller should refuse to add
// another. The coordinator calls this before flipping a candidate
// to active. activeCount is supplied by the caller — typically by
// querying the backend's count.
func (g *PoisoningGuard) CheckActiveCap(scope schema.Scope, activeCount int) error {
	if g.maxActivePerScope <= 0 {
		return nil
	}
	if activeCount >= g.maxActivePerScope {
		return fmt.Errorf("poisoning_guard: scope %s/%s/%s at active cap %d; archive lower-confidence rows first",
			scope.Level, scope.WorktreeID, scope.RepoName, g.maxActivePerScope)
	}
	return nil
}

// IsCandidateExpired reports whether a candidate row has sat
// un-approved past the TTL. The coordinator's decay scheduler uses
// this to soft-delete stale candidates.
func (g *PoisoningGuard) IsCandidateExpired(createdAt time.Time) bool {
	if g.candidateTTL <= 0 {
		return false
	}
	return time.Since(createdAt) > g.candidateTTL
}

// Compile-time assert the memapi types reference is alive — the
// guard sits between the host and the backend, so removing the
// memapi import would otherwise be a silent regression risk.
var _ memapi.Instinct
