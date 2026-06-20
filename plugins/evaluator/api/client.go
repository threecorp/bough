package api

import (
	"context"
	"errors"

	pb "github.com/ikeikeikeike/bough/plugins/evaluator/api/proto"
)

// grpcClient routes Go args → pb wire and rematerialises
// response.error as a Go error.
type grpcClient struct {
	client pb.SkillEvaluatorClient
}

func (c *grpcClient) Evaluate(ctx context.Context, req *EvaluateReq) (*EvaluateResp, error) {
	resp, err := c.client.Evaluate(ctx, &pb.EvaluateRequest{
		ArtifactId:           req.ArtifactID,
		ArtifactJson:         req.ArtifactJSON,
		EvaluatorContextJson: req.EvaluatorContextJSON,
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	return &EvaluateResp{
		Outcome:              outcomeFromProto(resp.GetOutcome()),
		Utility:              resp.GetUtility(),
		ConfidenceDelta:      resp.GetConfidenceDelta(),
		ShouldPrune:          resp.GetShouldPrune(),
		EvidenceRefs:         resp.GetEvidenceRefs(),
		Explanation:          resp.GetExplanation(),
		EvaluatorPayloadJSON: resp.GetEvaluatorPayloadJson(),
	}, nil
}
