// Package api carries the host-↔-plugin contract for bough instinct
// minter plugins. The bough core ships a default in-process minter
// (`internal/instinct.builtin_minter`) so this interface only matters
// to v0.6+ experimental plugin authors who want to wire an LLM-backed
// candidate generator (SkillX, Anything2Skill, MUSE-Autoskill, ...)
// into bough's per-worktree trace pipeline.
//
// See plugins/memory/api for the persistence contract and
// plugins/capability/api for the compile-into-artifact contract.
// Splitting these concerns keeps memory backend authors (mem0,
// Graphiti) free of bough-specific trace logic.
package api

import "context"

// InstinctMinter turns observed traces (session log lines, test
// failures, lint output, commit messages, post-create hook output)
// into one or more InstinctCandidate rows. The host's poisoning
// guard routes the candidates through approval before they become
// active instincts; minters should focus on extraction quality, not
// on whether a candidate "deserves" to be stored.
type InstinctMinter interface {
	Mint(ctx context.Context, req *MintReq) (*MintResp, error)
}
