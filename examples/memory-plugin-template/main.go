// Command bough-plugin-memory-template is a starting point for
// writing a new bough memory backend. Copy this directory to your
// own repository, rename the binary, and replace each method body
// with your backend's call (REST, gRPC, embedded DB, ...).
//
// Build: `go build -o dist/bough-plugin-memory-<name> .`
// Test:  `BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN=$PWD/dist/bough-plugin-memory-<name> \
//             go test -tags=conformance ./...`
package main

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: memapi.Handshake,
		Plugins: map[string]plugin.Plugin{
			memapi.MemoryBackendPluginKey: &memapi.MemoryBackendPlugin{Impl: &templateBackend{}},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}

// templateBackend is the empty MemoryBackend implementation. Replace
// each method body with your backend's call. Read
// `plugins/memory/api/CONTRACT.md` for the contract before you start.
type templateBackend struct{}

func (templateBackend) Health(ctx context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{BackendKind: "template", PluginVersion: "v0.0.0"}, nil
}

func (templateBackend) Capabilities(ctx context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{PluginVersion: "v0.0.0"}, nil
}

func (templateBackend) Store(ctx context.Context, req *memapi.StoreReq) (*memapi.StoreResp, error) {
	// TODO: write req.Instinct to your backend, honouring DedupeKey
	// + SourceEventID for idempotent upsert. See CONTRACT.md.
	return nil, errors.New("template plugin: Store not implemented")
}

func (templateBackend) Query(ctx context.Context, req *memapi.QueryReq) (*memapi.QueryResp, error) {
	// TODO: read from your backend; honour MaxResults + MaxTokens;
	// set EstimatedTokens + Truncated per result.
	return &memapi.QueryResp{}, nil
}

func (templateBackend) Forget(ctx context.Context, req *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	// TODO: soft-delete (state="forgotten"); the host's decay
	// scheduler issues hard deletes.
	return &memapi.ForgetResp{}, nil
}

func (templateBackend) Export(ctx context.Context, req *memapi.ExportReq) (*memapi.ExportResp, error) {
	return &memapi.ExportResp{Payload: []byte{}, ContentType: "text/yaml"}, nil
}

func (templateBackend) Import(ctx context.Context, req *memapi.ImportReq) (*memapi.ImportResp, error) {
	return &memapi.ImportResp{}, nil
}
