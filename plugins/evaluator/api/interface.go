// Package api freezes the v0.5.0 Go contract for bough skill
// evaluator plugins WITHOUT shipping a working implementation. v0.5
// only commits the wire format (api/proto/evaluator.proto), the Go
// interface, and the request/response types; the host's
// `bough capability evaluate` CLI and the first official evaluators
// (GEPA reflective prompt optimiser, TextGrad gradient evaluator,
// MUSE-Autoskill lifecycle evaluator, SkillAudit paired-trajectory
// auditor) land in v0.7+.
//
// Why split this from CapabilityCompiler: compilation and evaluation
// are different concerns. A compiler turns approved instincts into
// candidate artifacts; an evaluator decides whether a given artifact
// should survive, be revised, or be pruned. The bough vision treats
// evaluator-driven evolution as the v0.7+ layer that sits above
// v0.6's compiler layer. Keeping the interfaces separate lets a v0.7
// evaluator score artifacts produced by any v0.6 compiler without
// the compiler having to expose an evaluate hook.
package api

import "context"

// SkillEvaluator scores a single CapabilityArtifact and recommends
// whether the host should promote, revise, or prune it. Evaluators
// are stateless from the host's point of view; if an evaluator needs
// historical context (paired-trajectory comparisons, prior
// confidence trajectory) it must fetch that from its own backing
// store, not from bough's memory.
//
// v0.5 ships this interface as STUB: no plugin will be discovered
// for "evaluator" kind. The Go interface and types are committed so
// the host's pluginhost can discover evaluator plugins as soon as
// v0.7+ implementations exist.
type SkillEvaluator interface {
	Evaluate(ctx context.Context, req *EvaluateReq) (*EvaluateResp, error)
}
