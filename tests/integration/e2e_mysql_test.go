//go:build integration

// e2e_mysql_test.go drives the full bough host + bough-plugin-mysql
// loop against a real services-flake-launched mysqld. The cold-start
// path takes ~30-60s on a fresh nix store cache and ~10s on warm
// runs, so this test only runs when invoked with `go test -tags
// integration` (the standard `go test ./...` skips it).
//
// Local invocation:
//
//	make build
//	make integration-test
//
// CI invocation: .github/workflows/ci.yml runs `make integration-test`
// inside the Nix devShell so the same services-flake / mysql 8.4 /
// process-compose-flake versions exercise as locally.

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/cli"
	"github.com/ikeikeikeike/bough/internal/registry"
)

// monorepoFixture is the minimal layout the integration test exercises
// — one repo with an `engine-provider` role + one peer repo that owns
// a .env.local template. The fixture lives entirely in t.TempDir() so
// a run never leaks state into the operator's real source tree.
type monorepoFixture struct {
	root       string
	hostBinDir string
}

func newFixture(t *testing.T) *monorepoFixture {
	t.Helper()
	root := t.TempDir()

	// Resolve bough + bough-plugin-mysql binaries to ./dist (built by
	// `make build` before integration-test fires). Fail loudly if
	// they're missing so the operator gets a clear hint rather than a
	// vague "plugin not found".
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// tests/integration is two levels deep from repo root.
	hostBinDir := filepath.Clean(filepath.Join(repoRoot, "..", "..", "dist"))
	for _, b := range []string{"bough", "bough-plugin-mysql"} {
		if _, err := os.Stat(filepath.Join(hostBinDir, b)); err != nil {
			t.Skipf("missing %s in dist/ — run `make build` before `make integration-test`", b)
		}
	}

	// Materialise a real git repo so `bough create` has something to
	// `git worktree add` against. The repo only needs one commit on
	// `develop` since the YAML branch_strategy points there.
	srcRepo := filepath.Join(root, "demo-db")
	if err := os.MkdirAll(srcRepo, 0o755); err != nil {
		t.Fatalf("mkdir demo-db: %v", err)
	}
	gitInit(t, srcRepo)

	// Write .bough.yaml against this minimal layout. v0.3 fallback for
	// .worktree-isolation.yaml is covered separately by the unit-test
	// migrateLegacy() suite; here we exercise the v0.4 canonical shape
	// end-to-end so a future schema bump cannot regress the wiring
	// silently.
	yaml := `schema_version: 2
monorepo_root: "."
repositories:
  - name: demo-db
    branch_strategy: develop
    direnv: false
    role: engine-provider
    env_local:
      BOUGH_DEMO_PORT: "{{ .Mysql.Port }}"
engines:
  - kind: mysql
    version: "8.4"
    port_ranges:
      main: [42000, 42999]
    socket_dir: "/tmp"
    initial_resources:
      - { type: database, name: bough }
registry:
  path: ".bough-ports.json"
  backup_dir: "/tmp/bough-test-backups"
teardown:
  remove_branch: true
  remove_datadir: true
  graceful_timeout_sec: 10
`
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return &monorepoFixture{root: root, hostBinDir: hostBinDir}
}

func TestE2E_CreateMysqlReadyAndRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	fx := newFixture(t)

	// Inject ./dist at the front of PATH so cli.create → pluginhost
	// resolves bough-plugin-mysql to the just-built binary instead of
	// reaching out to the operator's global install.
	prevPath := os.Getenv("PATH")
	t.Setenv("PATH", fx.hostBinDir+string(os.PathListSeparator)+prevPath)

	// Walk into the fixture root so the relative paths in the YAML
	// (registry.path, monorepo_root) resolve correctly.
	prevCwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prevCwd) })
	if err := os.Chdir(fx.root); err != nil {
		t.Fatalf("chdir fixture root: %v", err)
	}

	// === Create ===
	const wtName = "F-E2E-Mysql"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := runCLI(ctx, "create", "--name", wtName); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Registry should have the F-E2E-Mysql entry with a mysql port.
	store := registry.NewStore(
		filepath.Join(fx.root, ".bough-ports.json"),
		"/tmp/bough-test-backups",
	)
	reg, err := store.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	port, ok := registry.Get(reg, wtName, "mysql.main")
	if !ok || port < 42000 || port > 42999 {
		t.Fatalf("registry missing mysql entry or out of range: port=%d ok=%v", port, ok)
	}

	// mysql must be listening on `port`.
	if !waitForTCP(port, 30*time.Second) {
		t.Fatalf("mysql did not accept TCP on %d within 30s", port)
	}

	// .env.local must exist in the demo-db worktree and contain the
	// templated port.
	envPath := filepath.Join(fx.root, ".worktrees", wtName, "demo-db", ".env.local")
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env.local: %v", err)
	}
	wantPort := fmt.Sprintf("BOUGH_DEMO_PORT=%d", port)
	if !bytes.Contains(envBytes, []byte(wantPort)) {
		t.Errorf(".env.local missing %q; got:\n%s", wantPort, envBytes)
	}

	// === Remove ===
	if err := runCLI(ctx, "remove", "--name", wtName); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// mysql must NOT be listening on the same port any more.
	if probeTCP(port) {
		t.Errorf("mysql still listening on %d after remove", port)
	}

	// Registry entry must be gone.
	reg, err = store.Load()
	if err != nil {
		t.Fatalf("re-load registry: %v", err)
	}
	if _, ok := registry.Get(reg, wtName, "mysql.main"); ok {
		t.Errorf("registry still has entry for %s after remove", wtName)
	}
}

func runCLI(ctx context.Context, args ...string) error {
	root := cli.NewRootCmd("0.0.0-e2e")
	root.SetArgs(args)
	root.SetIn(bytes.NewReader(nil))
	root.SetOut(io.Discard)
	root.SetErr(os.Stderr)
	return root.ExecuteContext(ctx)
}

func waitForTCP(port int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if probeTCP(port) {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

func probeTCP(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
