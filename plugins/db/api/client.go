package api

import (
	"context"
	"errors"

	pb "github.com/ikeikeikeike/bough/plugins/db/api/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// grpcClient is the host-side adapter: it implements the Go DBProvider
// interface by calling the generated pb.DBProviderClient. Errors flow
// in three ways — gRPC transport errors, the wire-side `error` string,
// or nil/success — and the methods normalise all three into plain Go
// errors at the boundary so call sites can `errors.Is` as usual.
type grpcClient struct {
	client pb.DBProviderClient
}

func (c *grpcClient) Up(ctx context.Context, req UpReq) error {
	r, err := c.client.Up(ctx, &pb.UpRequest{
		Port:             int32(req.Port),
		Datadir:          req.Datadir,
		WorktreeRoot:     req.WorktreeRoot,
		SocketDir:        req.SocketDir,
		InitialDatabases: req.InitialDatabases,
		Extras:           req.Extras,
	})
	if err != nil {
		return err
	}
	if r.GetError() != "" {
		return errors.New(r.GetError())
	}
	return nil
}

func (c *grpcClient) Down(ctx context.Context, req DownReq) error {
	r, err := c.client.Down(ctx, &pb.DownRequest{
		Port:               int32(req.Port),
		WorktreeRoot:       req.WorktreeRoot,
		GracefulTimeoutSec: int32(req.GracefulTimeoutSec),
	})
	if err != nil {
		return err
	}
	if r.GetError() != "" {
		return errors.New(r.GetError())
	}
	return nil
}

func (c *grpcClient) ReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error) {
	r, err := c.client.ReadyCheck(ctx, &pb.ReadyCheckRequest{
		Port:       int32(port),
		TimeoutSec: int32(timeoutSec),
	})
	if err != nil {
		return false, err
	}
	if r.GetError() != "" {
		return false, errors.New(r.GetError())
	}
	return r.GetReady(), nil
}

func (c *grpcClient) Cleanup(ctx context.Context, datadir string, port int) error {
	r, err := c.client.Cleanup(ctx, &pb.CleanupRequest{Datadir: datadir, Port: int32(port)})
	if err != nil {
		return err
	}
	if r.GetError() != "" {
		return errors.New(r.GetError())
	}
	return nil
}

func (c *grpcClient) PortRangeDefault(ctx context.Context) (int, int, error) {
	r, err := c.client.PortRangeDefault(ctx, &emptypb.Empty{})
	if err != nil {
		return 0, 0, err
	}
	return int(r.GetLow()), int(r.GetHigh()), nil
}

func (c *grpcClient) EnvVars(ctx context.Context, req EnvVarsReq) (map[string]string, error) {
	r, err := c.client.EnvVars(ctx, &pb.EnvVarsRequest{
		Port:             int32(req.Port),
		InitialDatabases: req.InitialDatabases,
		SocketDir:        req.SocketDir,
	})
	if err != nil {
		return nil, err
	}
	return r.GetVars(), nil
}
