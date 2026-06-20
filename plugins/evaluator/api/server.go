package api

import (
	"context"

	pb "github.com/ikeikeikeike/bough/plugins/evaluator/api/proto"
)

// grpcServer translates pb wire → Go types → SkillEvaluator.Impl.
type grpcServer struct {
	pb.UnimplementedSkillEvaluatorServer
	impl SkillEvaluator
}

func (s *grpcServer) Evaluate(ctx context.Context, r *pb.EvaluateRequest) (*pb.EvaluateResponse, error) {
	resp, err := s.impl.Evaluate(ctx, &EvaluateReq{
		ArtifactID:           r.GetArtifactId(),
		ArtifactJSON:         r.GetArtifactJson(),
		EvaluatorContextJSON: r.GetEvaluatorContextJson(),
	})
	if err != nil {
		return &pb.EvaluateResponse{Error: err.Error()}, nil
	}
	return &pb.EvaluateResponse{
		Outcome:              outcomeToProto(resp.Outcome),
		Utility:              resp.Utility,
		ConfidenceDelta:      resp.ConfidenceDelta,
		ShouldPrune:          resp.ShouldPrune,
		EvidenceRefs:         resp.EvidenceRefs,
		Explanation:          resp.Explanation,
		EvaluatorPayloadJson: resp.EvaluatorPayloadJSON,
	}, nil
}

var outcomeMap = map[EvaluationOutcome]pb.EvaluationOutcome{
	OutcomePromote: pb.EvaluationOutcome_OUTCOME_PROMOTE,
	OutcomeKeep:    pb.EvaluationOutcome_OUTCOME_KEEP,
	OutcomeRevise:  pb.EvaluationOutcome_OUTCOME_REVISE,
	OutcomePrune:   pb.EvaluationOutcome_OUTCOME_PRUNE,
}

func outcomeToProto(o EvaluationOutcome) pb.EvaluationOutcome {
	if v, ok := outcomeMap[o]; ok {
		return v
	}
	return pb.EvaluationOutcome_OUTCOME_UNSPECIFIED
}

func outcomeFromProto(p pb.EvaluationOutcome) EvaluationOutcome {
	for k, v := range outcomeMap {
		if v == p {
			return k
		}
	}
	return OutcomeUnspecified
}
