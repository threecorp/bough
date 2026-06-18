// Package conformance is the bough plugin contract test suite.
//
// Plugin authors verify their implementation by adding one file:
//
//	//go:build conformance
//	package myplugin_test
//
//	import (
//	    "os"
//	    "testing"
//	    "github.com/ikeikeikeike/bough/conformance"
//	)
//
//	func TestPluginConformance(t *testing.T) {
//	    conformance.Run(t, conformance.Config{
//	        PluginBinary: os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN"),
//	        Image:        "myengine:1.0",
//	    })
//	}
//
// The suite spawns the plugin binary under hashicorp/go-plugin (the
// same path the bough host uses in production), exercises the full
// lifecycle (PortRangeDefault → Up → ReadyCheck → EnvVars → Down →
// Cleanup), and asserts a battery of invariants that, taken together,
// would have caught the v0.2.5 (shell-metachar in env value) and
// v0.2.6 (container bridge-IP advertised via EnvVars) regressions
// before they shipped.
//
// # Multi-port engines
//
// The lifecycle iterates over every role PortRangeDefault declares
// and allocates one port per role from each range's Low end. Single-
// port engines (mysql / postgres / redis / elasticsearch) see one
// PortSpec with Role="main"; multi-port engines (rabbitmq AMQP +
// Management, kafka broker + controller, NATS client + monitor +
// cluster) see one entry per role. Set Config.MainPortRole to the
// role the fault injections should target; lifecycle exercises every
// role regardless.
//
// # Reporter
//
// AssertReachable / AssertShellSafe / AssertNonEmpty accept a
// Reporter interface (a *testing.T subset) so the suite's own self-
// tests can drive the helpers with a recording double — useful when
// asserting "AssertReachable flags the v0.2.6 bridge-IP" without
// contaminating the parent test's pass/fail state.
//
// Design references:
//
//   - kubernetes-csi/csi-test pkg/sanity — the "one func, full
//     contract" ergonomics this package mirrors.
//   - terraform-plugin-sdk helper/resource — the package-only import
//     path (no separate CLI required for plugin authors).
//   - hashicorp/go-plugin testing helpers — the real-binary-spawn
//     guarantee (in-process mocks cannot catch container env bugs).
package conformance

import (
	"context"
	"testing"
	"time"
)

// Config drives a conformance run. Only PluginBinary is required;
// every other field has a sane default chosen to match bough's
// existing plugins (mysql / postgres / redis / elasticsearch).
type Config struct {
	// PluginBinary is the absolute path to the go-plugin server binary
	// the suite will spawn. Typically the CI job builds it first via
	// `go build -o bin/bough-plugin-<kind> ./cmd/bough-plugin-<kind>`
	// and passes the result through BOUGH_CONFORMANCE_PLUGIN_BIN.
	PluginBinary string

	// Image is the container image ref the plugin will pull. Carried
	// verbatim into Extras["docker.image"] so plugins that key on
	// either convention see it.
	Image string

	// Extras is forwarded to UpReq.Extras unchanged — heap sizes,
	// charset overrides, version pins, anything plugin-specific.
	Extras map[string]string

	// UpTimeout bounds the full Up call. Default: 120s (mysql initdb +
	// elasticsearch JVM warmup both fit comfortably).
	UpTimeout time.Duration

	// ReadyTimeout bounds ReadyCheck's poll loop. Default: 60s.
	ReadyTimeout time.Duration

	// IdempotentCount is how many times the suite loops the
	// lifecycle (Up → Ready → EnvVars → Down) before final Cleanup.
	// >=2 catches "Up on existing state" and "Down on already-down"
	// regressions. Default: 2.
	IdempotentCount int

	// SkipPortConflict / SkipDatadirPermission / SkipImagePullFailure
	// turn off the corresponding fault-injection cases. Plugin authors
	// who cannot fault-simulate a given path (e.g. an in-cluster
	// provisioner that cannot bind a sidecar listener) declare a skip
	// here rather than silently leaving the contract unverified.
	//
	// The fault checks themselves land in Λ-6.2; the fields ride
	// the Config now so the signature is stable from v0.3.0 onward.
	SkipPortConflict      bool
	SkipDatadirPermission bool
	SkipImagePullFailure  bool

	// AllowShellMetachars lifts the AssertShellSafe invariant. Plugins
	// whose URL/DSN values legitimately contain `(`, `&`, `?`, etc.
	// (the historical mysql DSN format is the canonical example) must
	// set this to true; otherwise the suite fails on those values,
	// which is the v0.2.5 guard.
	AllowShellMetachars bool

	// JUnitFile, when set, writes a JUnit XML report on suite exit
	// for CI artifact upload. Implemented in Λ-6.2.
	JUnitFile string

	// NativeProbe runs after EnvVars succeed. It receives the host:port
	// pair the plugin advertised and is expected to issue a real-
	// protocol query (mysql `SELECT 1`, redis `PING`, ...). If nil,
	// the suite dispatches a default probe via kind→probe lookup; an
	// unknown plugin kind without a probe is a Skip, not a Fail.
	NativeProbe func(ctx context.Context, hostPort string) error

	// MainPortRole is the role the suite treats as the engine's
	// primary listen point. Single-port plugins (mysql / postgres /
	// redis / elasticsearch) use the default "main"; multi-port
	// plugins (rabbitmq amqp+management, kafka broker+controller,
	// nats client+monitor+cluster) override this with whichever role
	// the fault suite should target (port-conflict / datadir-perm)
	// and which role the lifecycle test should pin for diagnostic
	// output. The lifecycle test still iterates over every role
	// returned by PortRangeDefault — MainPortRole only affects the
	// single-port-shaped corners (faults + error messages).
	MainPortRole string
}

// Run executes the conformance suite against the plugin binary
// declared in cfg. It assumes a testing.T context — most plugin
// authors call it directly from their `Test<X>PluginConformance`
// func. For ginkgo-based suites see RunGinkgo (Λ-6.2).
//
// Run will t.Skip if cfg.PluginBinary is empty so that running
// `go test ./...` without the conformance build tag does not
// accidentally fail.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.PluginBinary == "" {
		t.Skip("conformance: cfg.PluginBinary empty (set BOUGH_CONFORMANCE_PLUGIN_BIN or pass it directly)")
	}
	cfg = applyDefaults(cfg)
	runLifecycle(t, cfg)
}

// applyDefaults fills the zero-valued knobs with the values plugin
// authors would otherwise have to spell out. Kept as a separate
// function so the defaults are visible in one place and unit-testable
// without having to spawn a plugin binary.
func applyDefaults(cfg Config) Config {
	if cfg.UpTimeout == 0 {
		cfg.UpTimeout = 120 * time.Second
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	if cfg.IdempotentCount <= 0 {
		cfg.IdempotentCount = 2
	}
	if cfg.MainPortRole == "" {
		cfg.MainPortRole = "main"
	}
	return cfg
}
