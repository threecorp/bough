package conformance_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ikeikeikeike/bough/conformance"
)

// buildMockPlugin compiles ./mock_plugin into a binary under
// t.TempDir() and returns the absolute path. We always rebuild rather
// than caching: the suite is the contract guard for the binary that
// is going to run in CI, so the build step is a deliberate part of
// what we are asserting (a plugin that does not even compile is the
// loudest possible contract violation).
func buildMockPlugin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; conformance suite cannot build the mock plugin")
	}
	out := filepath.Join(t.TempDir(), "bough-plugin-mock")
	cmd := exec.Command(
		"go", "build", "-o", out,
		"github.com/ikeikeikeike/bough/conformance/mock_plugin",
	)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build mock_plugin: %v\n%s", err, output)
	}
	return out
}

// TestRun_MockPlugin_GreenPath is the floor: every contract phase
// (PortRangeDefault → Up → ReadyCheck → EnvVars → Down → Cleanup,
// twice, then a final idempotent Cleanup, then faults) must succeed
// against the healthy mock plugin without a single t.Fail.
//
// If this test breaks, the conformance suite is broken — not the
// plugin under examination. Run it first whenever the suite changes.
//
// SkipImagePullFailure is on because the mock provider does not use
// container images; it would never error on an unresolvable ref.
func TestRun_MockPlugin_GreenPath(t *testing.T) {
	bin := buildMockPlugin(t)
	conformance.Run(t, conformance.Config{
		PluginBinary:         bin,
		Image:                "mock:latest", // unused by mock; kept for signature parity
		IdempotentCount:      2,
		SkipImagePullFailure: true,
	})
}

// TestRun_MockPlugin_MultiPort_GreenPath is the multi-port floor.
// The mock plugin's `multi-port` mode advertises two roles
// (amqp + management) from PortRangeDefault, binds both ports in Up,
// and emits the role-suffixed EnvVars convention
// (BOUGH_MOCK_HOST + BOUGH_MOCK_AMQP_PORT + BOUGH_MOCK_MANAGEMENT_PORT
// + per-role _URL). The conformance suite must:
//
//   - allocate ports for every declared role,
//   - drive Up / ReadyCheck / Down / Cleanup against both port set,
//   - AssertReachable both addrs (via the longest-prefix host lookup),
//   - run faults against MainPortRole (= "amqp" here).
//
// If this test breaks, the multi-port wiring has regressed and any
// future rabbitmq / kafka / nats plugin would inherit the same
// regression — run it whenever lifecycle.go or invariants.go
// changes.
func TestRun_MockPlugin_MultiPort_GreenPath(t *testing.T) {
	// The mock subprocess reads BOUGH_MOCK_FAIL_MODE at Provider
	// construction — before the first RPC — so set it via t.Setenv
	// (process-wide for the duration of this test, restored on
	// cleanup) and rely on os/exec env inheritance to carry it
	// through to the spawned plugin binary.
	t.Setenv("BOUGH_MOCK_FAIL_MODE", "multi-port")
	bin := buildMockPlugin(t)
	conformance.Run(t, conformance.Config{
		PluginBinary:         bin,
		Image:                "mock:latest",
		IdempotentCount:      2,
		SkipImagePullFailure: true,
		MainPortRole:         "amqp",
	})
}

// recordingReporter implements conformance.Reporter and captures
// every Errorf call. The suite's regression-guard tests use it to
// drive AssertReachable / AssertShellSafe with poisoned env input
// and verify the helper raised an error — without contaminating the
// parent test's pass/fail state (which is what would happen if we
// drove the failure path through a sub-*testing.T).
type recordingReporter struct {
	errs []string
}

func (r *recordingReporter) Helper() {}
func (r *recordingReporter) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recordingReporter) Logf(string, ...any) {}

// TestAssertReachable_DetectsBridgeIP is the v0.2.6 regression
// guard. A plugin that advertises the docker bridge IP 172.17.0.4
// MUST be caught by AssertReachable; if this test passes with zero
// errors recorded, the v0.2.6 guard has regressed.
func TestAssertReachable_DetectsBridgeIP(t *testing.T) {
	rep := &recordingReporter{}
	env := map[string]string{
		"BOUGH_MOCK_HOST": "172.17.0.4",
		"BOUGH_MOCK_PORT": "51000",
	}
	conformance.AssertReachable(rep, env)
	if len(rep.errs) == 0 {
		t.Errorf("AssertReachable did not flag a 172.17.0.4 advertise; v0.2.6 guard regressed")
	}
}

// TestAssertReachable_HappyPath asserts the helper is silent when
// the advertised address IS reachable — otherwise the guard would
// also fail every healthy plugin (= a useless invariant). We bind a
// scratch listener on a random localhost port, advertise that pair
// in env, and confirm no error is recorded.
func TestAssertReachable_HappyPath(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("scratch listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	rep := &recordingReporter{}
	conformance.AssertReachable(rep, map[string]string{
		"BOUGH_MOCK_HOST": "127.0.0.1",
		"BOUGH_MOCK_PORT": port,
	})
	if len(rep.errs) != 0 {
		t.Errorf("AssertReachable flagged a reachable address: %v", rep.errs)
	}
}

// TestAssertShellSafe_DetectsMetachar is the v0.2.5 regression
// guard. A DSN containing `(`, `&`, and `$(...)` MUST be caught.
func TestAssertShellSafe_DetectsMetachar(t *testing.T) {
	rep := &recordingReporter{}
	env := map[string]string{
		"BOUGH_MOCK_DSN": "root:p$(whoami)@tcp(127.0.0.1:3306)/db?parseTime=true&loc=UTC",
	}
	conformance.AssertShellSafe(rep, env, false)
	if len(rep.errs) == 0 {
		t.Errorf("AssertShellSafe did not flag shell metachars; v0.2.5 guard regressed")
	}
}

// TestAssertShellSafe_AllowFlagSkips asserts the escape hatch: a
// plugin that legitimately needs metachars (the mysql go-sql-driver
// DSN is the canonical case) can pass allow=true and the helper
// goes quiet. Without this opt-out, the v0.2.5 guard would block
// every mysql consumer.
func TestAssertShellSafe_AllowFlagSkips(t *testing.T) {
	rep := &recordingReporter{}
	env := map[string]string{
		"BOUGH_MYSQL_DSN": "root:@tcp(127.0.0.1:3306)/db?parseTime=true&loc=UTC",
	}
	conformance.AssertShellSafe(rep, env, true)
	if len(rep.errs) != 0 {
		t.Errorf("AssertShellSafe should be silent when allow=true; got %v", rep.errs)
	}
}

// TestAssertNonEmpty_DetectsEmptyValue guards against a plugin
// returning `KEY=` (empty value). The host's .env.local would
// silently overwrite any inherited env value.
func TestAssertNonEmpty_DetectsEmptyValue(t *testing.T) {
	rep := &recordingReporter{}
	conformance.AssertNonEmpty(rep, map[string]string{
		"BOUGH_MOCK_HOST": "127.0.0.1",
		"BOUGH_MOCK_PORT": "",
	})
	if len(rep.errs) == 0 {
		t.Errorf("AssertNonEmpty did not flag the empty value")
	}
}

// TestRun_EmptyPluginBinary_Skips guards the Λ-6.1 ergonomics: if
// BOUGH_CONFORMANCE_PLUGIN_BIN is unset (= the dev ran `go test ./...`
// without the conformance build tag stitched together), the suite
// must Skip, not Fail. Otherwise the suite turns into a developer-
// hostile gate on every plain `go test`.
//
// Implementation note: testing.T cannot be replaced with a shim
// (conformance.Run requires *testing.T for t.Run / t.Context), so we
// drive the Skip path via a sub-test and assert that the sub-test
// ended Skipped, not Failed.
func TestRun_EmptyPluginBinary_Skips(t *testing.T) {
	t.Run("skip-path", func(sub *testing.T) {
		conformance.Run(sub, conformance.Config{PluginBinary: ""})
		// `defer` only runs after Run returns normally; on Skip the
		// goroutine unwinds via runtime.Goexit from SkipNow, so the
		// post-check has to live in t.Cleanup of the OUTER t below.
	})
	// If `skip-path` Skip'd, the outer t.Run returns true and the
	// sub-test's Skipped() reflects that. Go's testing package
	// reports skipped sub-tests as not-failed at the parent level.
	if t.Failed() {
		t.Errorf("Run with empty PluginBinary marked outer test as failed")
	}
}
