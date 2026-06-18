// The mock_plugin binary is the in-tree go-plugin server the
// conformance suite uses to self-test itself. It implements every
// EngineProvider method without launching a real container — Up binds
// loopback listeners on the requested ports (so AssertReachable from
// the suite passes), Down closes them, Cleanup wipes the datadir.
//
// Several modes ride along, gated on BOUGH_MOCK_FAIL_MODE, so the
// invariant + lifecycle tests can prove the suite catches them:
//
//   - "bridge-ip"      — EnvVars advertises 172.17.0.4 instead of
//     127.0.0.1, mimicking the v0.2.6 elasticsearch sniff bug.
//   - "shell-metachar" — EnvVars emits a DSN that contains `(`, `&`
//     and `$`, mimicking the v0.2.5 bash-source-aborts bug.
//   - "multi-port"     — PortRangeDefault declares two roles (amqp +
//     management); Up binds both ports; EnvVars emits the role-
//     suffixed naming convention (BOUGH_MOCK_AMQP_PORT /
//     BOUGH_MOCK_MANAGEMENT_PORT). Lets the suite exercise its
//     multi-port handling end-to-end without a real rabbitmq image.
//
// The default (empty BOUGH_MOCK_FAIL_MODE) is the green path the
// Λ-6.1 lifecycle test relies on.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/hashicorp/go-plugin"
)

const (
	failModeEnv = "BOUGH_MOCK_FAIL_MODE"

	failBridgeIP  = "bridge-ip"
	failShellMeta = "shell-metachar"
	modeMultiPort = "multi-port"

	mockHost = "127.0.0.1"
	bridgeIP = "172.17.0.4"

	// Single-port mode: one role, one range.
	singlePortLow  = 51000
	singlePortHigh = 51999

	// Multi-port mode: amqp + management, distinct non-overlapping
	// ranges so the per-role allocator hands out distinct ports.
	amqpPortLow        = 52000
	amqpPortHigh       = 52499
	managementPortLow  = 52500
	managementPortHigh = 52999
)

type mockProvider struct {
	mu        sync.Mutex
	listeners map[int]net.Listener
	mode      string
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		listeners: map[int]net.Listener{},
		mode:      os.Getenv(failModeEnv),
	}
}

func (p *mockProvider) Up(_ context.Context, req *api.UpReq) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
		return fmt.Errorf("mock: mkdir datadir: %w", err)
	}
	for _, ps := range req.Ports {
		if _, exists := p.listeners[ps.Port]; exists {
			continue // up-or-reuse: contract requires a second Up to be a no-op.
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", mockHost, ps.Port))
		if err != nil {
			return fmt.Errorf("mock: listen %s:%d (role=%s): %w", mockHost, ps.Port, ps.Role, err)
		}
		p.listeners[ps.Port] = ln
		go acceptLoop(ln)
	}
	sentinel := filepath.Join(req.Datadir, "up.sentinel")
	if err := os.WriteFile(sentinel, []byte("up"), 0o644); err != nil {
		// Roll back any newly bound listeners so a retry can succeed.
		for _, ps := range req.Ports {
			if ln, ok := p.listeners[ps.Port]; ok {
				_ = ln.Close()
				delete(p.listeners, ps.Port)
			}
		}
		return fmt.Errorf("mock: write sentinel: %w", err)
	}
	return nil
}

func acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func (p *mockProvider) Down(_ context.Context, req *api.DownReq) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, port := range req.Ports {
		if ln, ok := p.listeners[port]; ok {
			_ = ln.Close()
			delete(p.listeners, port)
		}
	}
	return nil
}

func (p *mockProvider) ReadyCheck(_ context.Context, _ []int, _ int) (bool, error) {
	return true, nil
}

func (p *mockProvider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return nil
	}
	if err := os.RemoveAll(datadir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("mock: cleanup %s: %w", datadir, err)
	}
	return nil
}

func (p *mockProvider) PortRangeDefault(_ context.Context) (map[string]api.PortRange, error) {
	if p.mode == modeMultiPort {
		return map[string]api.PortRange{
			"amqp":       {Low: amqpPortLow, High: amqpPortHigh},
			"management": {Low: managementPortLow, High: managementPortHigh},
		}, nil
	}
	return map[string]api.PortRange{
		"main": {Low: singlePortLow, High: singlePortHigh},
	}, nil
}

func (p *mockProvider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	host := mockHost
	if p.mode == failBridgeIP {
		host = bridgeIP
	}
	if p.mode == failShellMeta {
		port := api.PickMainPort(req.Ports)
		return map[string]string{
			"BOUGH_MOCK_DSN": fmt.Sprintf(
				"user:p$(whoami)@tcp(%s:%d)/db?parseTime=true&loc=UTC",
				host, port,
			),
		}, nil
	}
	if p.mode == modeMultiPort {
		// Multi-port engines share one HOST and emit a per-role _PORT
		// / _URL — matches the bough naming convention for rabbitmq /
		// kafka / nats and lets AssertReachable's longest-prefix host
		// lookup pair each role's port back to BOUGH_MOCK_HOST.
		out := map[string]string{
			"BOUGH_MOCK_HOST": host,
		}
		for _, ps := range req.Ports {
			role := strings.ToUpper(ps.Role)
			out[fmt.Sprintf("BOUGH_MOCK_%s_PORT", role)] = fmt.Sprintf("%d", ps.Port)
			out[fmt.Sprintf("BOUGH_MOCK_%s_URL", role)] = fmt.Sprintf("mock://%s:%d", host, ps.Port)
		}
		return out, nil
	}
	port := api.PickMainPort(req.Ports)
	return map[string]string{
		"BOUGH_MOCK_HOST": host,
		"BOUGH_MOCK_PORT": fmt.Sprintf("%d", port),
		"BOUGH_MOCK_URL":  fmt.Sprintf("mock://%s:%d", host, port),
	}, nil
}

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: api.Handshake,
		Plugins: map[string]plugin.Plugin{
			api.EngineProviderPluginKey: &api.EngineProviderPlugin{Impl: newMockProvider()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
