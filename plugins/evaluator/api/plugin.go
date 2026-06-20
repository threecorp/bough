package api

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/ikeikeikeike/bough/plugins/evaluator/api/proto"
)

// SkillEvaluatorPlugin glues a Go SkillEvaluator impl to go-plugin
// gRPC. v0.5 ships only the type; v0.7+ host code spawns evaluator
// plugins for evaluator-driven evolution (GEPA / TextGrad / MUSE /
// SkillAudit).
type SkillEvaluatorPlugin struct {
	plugin.Plugin
	Impl SkillEvaluator
}

func (p *SkillEvaluatorPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterSkillEvaluatorServer(s, &grpcServer{impl: p.Impl})
	return nil
}

func (p *SkillEvaluatorPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &grpcClient{client: pb.NewSkillEvaluatorClient(c)}, nil
}
