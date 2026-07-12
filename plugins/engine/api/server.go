package api

import (
	"context"

	pb "github.com/ikeikeikeike/bough/plugins/engine/api/proto"

	"google.golang.org/protobuf/types/known/emptypb"
)

// grpcServer is the plugin-side adapter: it implements the generated
// pb.EngineProviderServer interface by delegating to a Go
// EngineProvider Impl. All errors are squeezed into the response
// message's `error` string so downstream plugins authored in non-Go
// languages can still produce them without learning gRPC status codes.
type grpcServer struct {
	pb.UnimplementedEngineProviderServer
	impl EngineProvider
}

func (s *grpcServer) Up(ctx context.Context, r *pb.UpRequest) (*pb.UpResponse, error) {
	err := s.impl.Up(ctx, &UpReq{
		Ports:            portSpecsFromProto(r.GetPorts()),
		Datadir:          r.GetDatadir(),
		WorktreeRoot:     r.GetWorktreeRoot(),
		SocketDir:        r.GetSocketDir(),
		InitialResources: resourceSpecsFromProto(r.GetInitialResources()),
		Extras:           r.GetExtras(),
		Plugins:          pluginSpecsFromProto(r.GetPlugins()),
	})
	return &pb.UpResponse{Error: errString(err)}, nil
}

func (s *grpcServer) Down(ctx context.Context, r *pb.DownRequest) (*pb.DownResponse, error) {
	err := s.impl.Down(ctx, &DownReq{
		Ports:              int32sToInts(r.GetPorts()),
		WorktreeRoot:       r.GetWorktreeRoot(),
		GracefulTimeoutSec: int(r.GetGracefulTimeoutSec()),
	})
	return &pb.DownResponse{Error: errString(err)}, nil
}

func (s *grpcServer) ReadyCheck(ctx context.Context, r *pb.ReadyCheckRequest) (*pb.ReadyCheckResponse, error) {
	ready, err := s.impl.ReadyCheck(ctx, int32sToInts(r.GetPorts()), int(r.GetTimeoutSec()))
	return &pb.ReadyCheckResponse{Ready: ready, Error: errString(err)}, nil
}

func (s *grpcServer) Cleanup(ctx context.Context, r *pb.CleanupRequest) (*pb.CleanupResponse, error) {
	err := s.impl.Cleanup(ctx, r.GetDatadir(), int32sToInts(r.GetPorts()))
	return &pb.CleanupResponse{Error: errString(err)}, nil
}

func (s *grpcServer) PortRangeDefault(ctx context.Context, _ *emptypb.Empty) (*pb.PortRangeResponse, error) {
	ranges, err := s.impl.PortRangeDefault(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*pb.PortRange, len(ranges))
	for k, v := range ranges {
		out[k] = &pb.PortRange{Low: int32(v.Low), High: int32(v.High)}
	}
	return &pb.PortRangeResponse{Ranges: out}, nil
}

func (s *grpcServer) EnvVars(ctx context.Context, r *pb.EnvVarsRequest) (*pb.EnvVarsResponse, error) {
	vars, err := s.impl.EnvVars(ctx, &EnvVarsReq{
		Ports:            portSpecsFromProto(r.GetPorts()),
		InitialResources: resourceSpecsFromProto(r.GetInitialResources()),
		SocketDir:        r.GetSocketDir(),
	})
	if err != nil {
		return nil, err
	}
	return &pb.EnvVarsResponse{Vars: vars}, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func portSpecsFromProto(ps []*pb.PortSpec) []PortSpec {
	out := make([]PortSpec, len(ps))
	for i, p := range ps {
		out[i] = PortSpec{Role: p.GetRole(), Port: int(p.GetPort())}
	}
	return out
}

func resourceSpecsFromProto(rs []*pb.ResourceSpec) []ResourceSpec {
	out := make([]ResourceSpec, len(rs))
	for i, r := range rs {
		out[i] = ResourceSpec{Type: r.GetType(), Name: r.GetName(), Params: r.GetParams()}
	}
	return out
}

func pluginSpecsFromProto(ps []*pb.PluginSpec) []PluginSpec {
	out := make([]PluginSpec, len(ps))
	for i, p := range ps {
		out[i] = PluginSpec{ID: p.GetId(), Location: p.GetLocation()}
	}
	return out
}

func int32sToInts(in []int32) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}
