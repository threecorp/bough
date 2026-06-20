package instinct

import (
	"context"
	"fmt"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// Promote walks an instinct's scope tier up: worktree → repo,
// repo → global. Round 3 AI #1 made the rule explicit: backends
// never invent or change scope. The coordinator owns the decision
// and expresses it through plain Store(scope=target) + Forget on
// the old row, so any backend (mem0, Graphiti, SQLite) implements
// the same shape without learning a Promote RPC.
//
// The function takes the existing Instinct (the backend already
// has it), the target scope.Level, and runs the two-step against
// the supplied backend. Errors from either step are returned;
// partial failures (Store succeeded, Forget failed) leave both
// rows in the backend — the coordinator's audit log records the
// state so a retry / repair tool can clean up.
func Promote(ctx context.Context, backend memapi.MemoryBackend, inst schema.Instinct, target schema.ScopeLevel, events *EventWriter) error {
	if inst.Scope.Level == "" {
		return fmt.Errorf("promote: source scope.level is empty")
	}
	if target == "" {
		return fmt.Errorf("promote: target scope.level is empty")
	}
	if inst.Scope.Level == target {
		return fmt.Errorf("promote: source and target scope.level both %q", target)
	}
	if !validPromotion(inst.Scope.Level, target) {
		return fmt.Errorf("promote: %s → %s is not a valid promotion (worktree → repo → global is the only chain)",
			inst.Scope.Level, target)
	}

	newScope := schema.Scope{
		Level:    target,
		RepoName: inst.Scope.RepoName,
	}
	if target == schema.ScopeGlobal {
		newScope.RepoName = "" // global is repo-agnostic
	}

	promoted := schema.Instinct{InstinctCandidate: inst.InstinctCandidate, Hits: inst.Hits, LastHitAt: inst.LastHitAt, EvidenceRefs: inst.EvidenceRefs}
	promoted.Scope = newScope
	promoted.ID = DedupeKey(promoted.Rule, newScope) // new scope → new dedupe identity
	promoted.CreatedAt = time.Now().UTC()

	if _, err := backend.Store(ctx, &memapi.StoreReq{
		Instinct:        instinctToMemAPI(promoted),
		DedupeKey:       promoted.DedupeKey,
		SourceEventID:   "promote/" + inst.ID,
		UpsertSemantics: true,
	}); err != nil {
		return fmt.Errorf("promote: store at target %s: %w", target, err)
	}
	if events != nil {
		_ = events.Append(Event{
			Kind:   "promote",
			Scope:  fmt.Sprintf("%s/%s", target, newScope.RepoName),
			ID:     promoted.ID,
			Detail: fmt.Sprintf("from %s to %s", inst.Scope.Level, target),
		})
	}
	if _, err := backend.Forget(ctx, &memapi.ForgetReq{
		ID:     inst.ID,
		Scope:  scopeToMemAPI(inst.Scope),
		Reason: "promoted to " + string(target),
	}); err != nil {
		return fmt.Errorf("promote: forget at source %s (target row %s already stored): %w",
			inst.Scope.Level, promoted.ID, err)
	}
	return nil
}

// validPromotion guards the worktree → repo → global chain. Side-
// chains (worktree → global, repo → worktree, etc.) are nonsensical
// and produce an explicit error.
func validPromotion(from, to schema.ScopeLevel) bool {
	switch from {
	case schema.ScopeWorktree:
		return to == schema.ScopeRepo
	case schema.ScopeRepo:
		return to == schema.ScopeGlobal
	}
	return false
}

func scopeToMemAPI(s schema.Scope) memapi.Scope {
	return memapi.Scope{
		Level:      string(s.Level),
		WorktreeID: s.WorktreeID,
		RepoName:   s.RepoName,
	}
}

func instinctToMemAPI(i schema.Instinct) memapi.Instinct {
	out := memapi.Instinct{
		ID:           i.ID,
		Rule:         i.Rule,
		Why:          i.Why,
		HowToApply:   i.HowToApply,
		Domain:       i.Domain,
		Scope:        scopeToMemAPI(i.Scope),
		Source:       string(i.Source),
		Confidence:   i.Confidence,
		State:        string(i.State),
		Hits:         i.Hits,
		LastHitAt:    i.LastHitAt,
		CreatedAt:    i.CreatedAt,
		SourceTraces: i.SourceTraces,
		EvidenceRefs: i.EvidenceRefs,
		DedupeKey:    i.DedupeKey,
	}
	if len(i.Metadata) > 0 {
		out.MetadataJSON = string(i.Metadata)
	}
	return out
}
