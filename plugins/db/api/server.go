package api

import (
	"context"

	pb "github.com/ikeikeikeike/bough/plugins/db/api/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// grpcServer is the plugin-side adapter: it implements the generated
// pb.DBProviderServer interface by delegating to a Go DBProvider Impl.
// All errors are squeezed into the response message's `error` string so
// downstream plugins authored in non-Go languages can still produce
// them without needing to learn gRPC status codes.
type grpcServer struct {
	pb.UnimplementedDBProviderServer
	impl DBProvider
}

func (s *grpcServer) Up(ctx context.Context, r *pb.UpRequest) (*pb.UpResponse, error) {
	err := s.impl.Up(ctx, UpReq{
		Port:             int(r.GetPort()),
		Datadir:          r.GetDatadir(),
		WorktreeRoot:     r.GetWorktreeRoot(),
		SocketDir:        r.GetSocketDir(),
		InitialDatabases: r.GetInitialDatabases(),
		Extras:           r.GetExtras(),
	})
	return &pb.UpResponse{Error: errString(err)}, nil
}

func (s *grpcServer) Down(ctx context.Context, r *pb.DownRequest) (*pb.DownResponse, error) {
	err := s.impl.Down(ctx, DownReq{
		Port:               int(r.GetPort()),
		WorktreeRoot:       r.GetWorktreeRoot(),
		GracefulTimeoutSec: int(r.GetGracefulTimeoutSec()),
	})
	return &pb.DownResponse{Error: errString(err)}, nil
}

func (s *grpcServer) ReadyCheck(ctx context.Context, r *pb.ReadyCheckRequest) (*pb.ReadyCheckResponse, error) {
	ready, err := s.impl.ReadyCheck(ctx, int(r.GetPort()), int(r.GetTimeoutSec()))
	return &pb.ReadyCheckResponse{Ready: ready, Error: errString(err)}, nil
}

func (s *grpcServer) Cleanup(ctx context.Context, r *pb.CleanupRequest) (*pb.CleanupResponse, error) {
	err := s.impl.Cleanup(ctx, r.GetDatadir(), int(r.GetPort()))
	return &pb.CleanupResponse{Error: errString(err)}, nil
}

func (s *grpcServer) PortRangeDefault(ctx context.Context, _ *emptypb.Empty) (*pb.PortRangeResponse, error) {
	low, high, err := s.impl.PortRangeDefault(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.PortRangeResponse{Low: int32(low), High: int32(high)}, nil
}

func (s *grpcServer) EnvVars(ctx context.Context, r *pb.EnvVarsRequest) (*pb.EnvVarsResponse, error) {
	vars, err := s.impl.EnvVars(ctx, EnvVarsReq{
		Port:             int(r.GetPort()),
		InitialDatabases: r.GetInitialDatabases(),
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
