package api

import (
	"context"
	"errors"

	pb "github.com/ikeikeikeike/bough/plugins/engine/api/proto"

	"google.golang.org/protobuf/types/known/emptypb"
)

// grpcClient is the host-side adapter: it implements the Go
// EngineProvider interface by calling the generated
// pb.EngineProviderClient. Errors flow in three ways — gRPC transport
// errors, the wire-side `error` string, or nil/success — and the
// methods normalise all three into plain Go errors at the boundary
// so call sites can `errors.Is` as usual.
type grpcClient struct {
	client pb.EngineProviderClient
}

func (c *grpcClient) Up(ctx context.Context, req *UpReq) error {
	r, err := c.client.Up(ctx, &pb.UpRequest{
		Ports:            portSpecsToProto(req.Ports),
		Datadir:          req.Datadir,
		WorktreeRoot:     req.WorktreeRoot,
		SocketDir:        req.SocketDir,
		InitialResources: resourceSpecsToProto(req.InitialResources),
		Extras:           req.Extras,
		Plugins:          pluginSpecsToProto(req.Plugins),
	})
	if err != nil {
		return err
	}
	if r.GetError() != "" {
		return errors.New(r.GetError())
	}
	return nil
}

func (c *grpcClient) Down(ctx context.Context, req *DownReq) error {
	r, err := c.client.Down(ctx, &pb.DownRequest{
		Ports:              intsToInt32(req.Ports),
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

func (c *grpcClient) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	r, err := c.client.ReadyCheck(ctx, &pb.ReadyCheckRequest{
		Ports:      intsToInt32(ports),
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

func (c *grpcClient) Cleanup(ctx context.Context, datadir string, ports []int) error {
	r, err := c.client.Cleanup(ctx, &pb.CleanupRequest{
		Datadir: datadir,
		Ports:   intsToInt32(ports),
	})
	if err != nil {
		return err
	}
	if r.GetError() != "" {
		return errors.New(r.GetError())
	}
	return nil
}

func (c *grpcClient) PortRangeDefault(ctx context.Context) (map[string]PortRange, error) {
	r, err := c.client.PortRangeDefault(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]PortRange, len(r.GetRanges()))
	for k, v := range r.GetRanges() {
		out[k] = PortRange{Low: int(v.GetLow()), High: int(v.GetHigh())}
	}
	return out, nil
}

func (c *grpcClient) EnvVars(ctx context.Context, req *EnvVarsReq) (map[string]string, error) {
	r, err := c.client.EnvVars(ctx, &pb.EnvVarsRequest{
		Ports:            portSpecsToProto(req.Ports),
		InitialResources: resourceSpecsToProto(req.InitialResources),
		SocketDir:        req.SocketDir,
	})
	if err != nil {
		return nil, err
	}
	return r.GetVars(), nil
}

// portSpecsToProto and friends live next to the client because the
// host calls them on every RPC; the symmetric *fromProto helpers are
// kept next to the server.
func portSpecsToProto(ps []PortSpec) []*pb.PortSpec {
	out := make([]*pb.PortSpec, len(ps))
	for i, p := range ps {
		out[i] = &pb.PortSpec{Role: p.Role, Port: int32(p.Port)}
	}
	return out
}

func resourceSpecsToProto(rs []ResourceSpec) []*pb.ResourceSpec {
	out := make([]*pb.ResourceSpec, len(rs))
	for i, r := range rs {
		out[i] = &pb.ResourceSpec{Type: r.Type, Name: r.Name, Params: r.Params}
	}
	return out
}

func pluginSpecsToProto(ps []PluginSpec) []*pb.PluginSpec {
	out := make([]*pb.PluginSpec, len(ps))
	for i, p := range ps {
		out[i] = &pb.PluginSpec{Id: p.ID, Location: p.Location}
	}
	return out
}

func intsToInt32(in []int) []int32 {
	out := make([]int32, len(in))
	for i, v := range in {
		out[i] = int32(v)
	}
	return out
}
