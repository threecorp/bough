package instinct

import (
	"context"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// DecayScheduler walks the backend periodically and:
//   - soft-deletes candidate rows older than candidate_ttl_days
//     that never got approved;
//   - moves active rows past decay_after_days into "archived"
//     state.
//
// Round 3 AI #2 made it explicit that decay is the coordinator's
// responsibility, not the backend's: backends report
// EstimatedTokens / Truncated but the host applies time-based
// transitions so the rule is consistent across backends.
//
// v0.5 ships the scheduler as a method the CLI can invoke on
// demand (`bough instinct decay --now`); the background goroutine
// version lands in v0.6 along with the long-lived `bough instinct
// daemon` mode.
type DecayScheduler struct {
	backend       memapi.MemoryBackend
	guard         *PoisoningGuard
	events        *EventWriter
	decayAfter    time.Duration
}

// NewDecayScheduler reads the relevant config knobs. The host
// constructs a scheduler once per coordinator lifetime.
func NewDecayScheduler(backend memapi.MemoryBackend, guard *PoisoningGuard, events *EventWriter, decayAfterDays int) *DecayScheduler {
	return &DecayScheduler{
		backend:    backend,
		guard:      guard,
		events:     events,
		decayAfter: time.Duration(decayAfterDays) * 24 * time.Hour,
	}
}

// RunOnce iterates the backend's active + candidate rows for the
// given scope (empty Scope = all scopes) and applies decay
// transitions. Returns counts of rows touched per transition.
//
// Round 3 follow-up fix: MaxTokens=0 (= no token cap) ensures the
// decay walk does not stop short on a token-budget boundary;
// MaxResults=1000 still bounds the run so a single corrupted
// scope cannot starve the rest. poisoning_guard's
// max_active_per_scope default (200) is well below this ceiling,
// so 1000 covers reasonable worktrees without a second run.
func (s *DecayScheduler) RunOnce(ctx context.Context, scope schema.Scope) (forgotten, archived int, err error) {
	if s == nil || s.backend == nil {
		return 0, 0, nil
	}
	resp, err := s.backend.Query(ctx, &memapi.QueryReq{
		Term:       "",
		Scope:      memapi.Scope{Level: string(scope.Level), WorktreeID: scope.WorktreeID, RepoName: scope.RepoName},
		MaxResults: 1000,
		MaxTokens:  0,
	})
	if err != nil {
		return 0, 0, err
	}
	now := time.Now().UTC()
	for _, r := range resp.Results {
		inst := r.Instinct
		// Skip archived / forgotten rows — they have already
		// completed their transition. Without this guard the
		// pre-fix Store reinforce path could be triggered to "re-
		// archive" an archived row, which the round 3 follow-up
		// flagged as CRITICAL #4.
		switch schema.InstinctState(inst.State) {
		case schema.InstinctStateCandidate:
			if s.guard.IsCandidateExpired(inst.CreatedAt) {
				_, fe := s.backend.Forget(ctx, &memapi.ForgetReq{
					ID:     inst.ID,
					Scope:  inst.Scope,
					Reason: "candidate_ttl_days exceeded",
				})
				if fe == nil {
					forgotten++
					_ = s.events.Append(Event{Kind: "decay", ID: inst.ID, Detail: "candidate_ttl exceeded"})
				}
			}
		case schema.InstinctStateActive:
			ref := inst.LastHitAt
			if ref.IsZero() {
				ref = inst.CreatedAt
			}
			if s.decayAfter > 0 && now.Sub(ref) > s.decayAfter {
				// Archive: persist with state=archived. The host's
				// CLI / coordinator surfaces this so a user can opt
				// to keep an archived row alive by re-approving.
				inst.State = string(schema.InstinctStateArchived)
				if _, se := s.backend.Store(ctx, &memapi.StoreReq{
					Instinct:        inst,
					DedupeKey:       inst.DedupeKey,
					SourceEventID:   "decay/" + inst.ID,
					UpsertSemantics: true,
				}); se == nil {
					archived++
					_ = s.events.Append(Event{Kind: "decay", ID: inst.ID, Detail: "decay_after_days exceeded; archived"})
				}
			}
		}
	}
	return forgotten, archived, nil
}
