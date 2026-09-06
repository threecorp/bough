//go:build conformance

// This is the minimum conformance harness a bough plugin needs.
// Copy verbatim into your plugin repo; the only edits required are:
//
//   - the package name
//   - the Image to pull (and probably the ReadyTimeout)
//   - the NativeProbe (or leave it nil to rely on AssertReachable only)
//
// The suite spawns the binary at BOUGH_CONFORMANCE_PLUGIN_BIN under
// go-plugin, drives the full lifecycle (PortRangeDefault → Up →
// ReadyCheck → EnvVars → Down → Cleanup × IdempotentCount), and
// asserts the contract invariants documented in
// plugins/engine/api/CONTRACT.md.
//
// CI invokes this with:
//
//	go build -o bin/bough-plugin-myplugin ./cmd/bough-plugin-myplugin
//	BOUGH_CONFORMANCE_PLUGIN_BIN=$(pwd)/bin/bough-plugin-myplugin \
//	    go test -tags=conformance ./...
package myplugin_test

import (
	"os"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/conformance"
)

func TestMyPluginConformance(t *testing.T) {
	bin := os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN")
	if bin == "" {
		t.Skip("set BOUGH_CONFORMANCE_PLUGIN_BIN to the bough-plugin-myplugin binary")
	}
	conformance.Run(t, conformance.Config{
		PluginBinary: bin,
		// TODO: replace with your engine's official image ref.
		Image: "myengine:1.0",
		// TODO: tune to your engine's cold-start. Java-backed engines
		// (elasticsearch, cassandra) typically need 90-300 s.
		ReadyTimeout:    60 * time.Second,
		IdempotentCount: 2,
		// TODO: supply a protocol-level probe if AssertReachable's
		// TCP-only check is too weak for your engine. Examples:
		//
		//   NativeProbe: conformance.RedisPing,
		//   NativeProbe: conformance.ElasticsearchGetRoot,
		//
		// or roll your own stdlib-only handshake check.
		// NativeProbe: nil,

		// TODO (multi-port engines only): the role the fault tests
		// should target. Single-port engines (one role from
		// PortRangeDefault) leave this empty and the suite defaults
		// to "main". Multi-port engines (rabbitmq amqp+management,
		// kafka broker+controller, NATS client+monitor+cluster)
		// override with one of the declared role names. The lifecycle
		// still exercises every role regardless.
		//
		//   MainPortRole: "amqp",
	})
}
