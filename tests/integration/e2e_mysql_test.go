//go:build integration

// e2e_mysql_test.go drives the full bough host + bough-plugin-mysql
// loop against a real containerised mysqld. It needs a reachable
// docker daemon, and the first run pulls mysql:8.4, so it only runs
// when invoked with `go test -tags integration` (the standard
// `go test ./...` skips it).
//
// Neither fixture below names a backend: the omitted field is exactly
// what most .bough.yaml files carry, so this exercises the path an
// operator actually takes, and the container assertion below is what
// proves docker is what ran.
//
// Local invocation:
//
//	make build
//	make integration-test
//
// No CI job runs it today.

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
	"github.com/ikeikeikeike/bough/pkg/dockerutil"
)

// monorepoFixture is the minimal layout the integration test exercises
// — one repo with an `engine-provider` role + one peer repo that owns
// a .env.local template. The fixture lives entirely in t.TempDir() so
// a run never leaks state into the operator's real source tree.
type monorepoFixture struct {
	root       string
	hostBinDir string
}

// v0.4CanonicalYAML is the schema_version:2 shape (engines: /
// port_ranges: / initial_resources: / role: engine-provider).
const v0_4CanonicalYAML = `schema_version: 2
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

// v0_3LegacyYAML is the schema_version:1 shape (databases: /
// port_range: / initial_databases: / role: db-provider) that
// migrateLegacy() converts in memory before the rest of the pipeline
// ever sees it. Kept as a distinct end-to-end fixture (not just a
// migrateLegacy() unit test) so a regression in how allocateEngines /
// the mysql plugin consume a migrateLegacy()-produced Engine — or in
// engineProviderRepo()'s "db-provider" role-alias branch, which no
// other test in the repo exercises — surfaces here instead of only in
// production, on hosts still running an unconverted
// .worktree-isolation.yaml.
const v0_3LegacyYAML = `schema_version: 1
monorepo_root: "."
repositories:
  - name: demo-db
    branch_strategy: develop
    direnv: false
    role: db-provider
    env_local:
      BOUGH_DEMO_PORT: "{{ .Mysql.Port }}"
databases:
  - kind: mysql
    version: "8.4"
    port_range: [42000, 42999]
    socket_dir: "/tmp"
    initial_databases: ["bough"]
registry:
  path: ".bough-ports.json"
  backup_dir: "/tmp/bough-test-backups"
teardown:
  remove_branch: true
  remove_datadir: true
  graceful_timeout_sec: 10
`

func newFixture(t *testing.T) *monorepoFixture {
	t.Helper()
	return newFixtureWithYAML(t, v0_4CanonicalYAML)
}

func newFixtureWithYAML(t *testing.T, yaml string) *monorepoFixture {
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

	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return &monorepoFixture{root: root, hostBinDir: hostBinDir}
}

func TestE2E_CreateMysqlReadyAndRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	runE2ECreateReadyRemove(t, newFixture(t), "F-E2E-Mysql")
}

// TestE2E_CreateMysqlReadyAndRemove_LegacyV03Schema is the end-to-end
// counterpart to internal/config's migrateLegacy() unit tests: those
// only assert on the in-memory *Config struct LoadFromBytes returns,
// never on how allocateEngines / the mysql plugin actually consume a
// migrateLegacy()-produced Engine, nor on engineProviderRepo()'s
// "db-provider" role-alias branch — which no other test in the repo
// calls at all. A host still running an unconverted
// .worktree-isolation.yaml (the explicitly-still-supported v0.3
// fallback) depends on both working; this drives the real create →
// registry → plugin → remove pipeline against that legacy shape so a
// regression in either surfaces here instead of only in production.
func TestE2E_CreateMysqlReadyAndRemove_LegacyV03Schema(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	runE2ECreateReadyRemove(t, newFixtureWithYAML(t, v0_3LegacyYAML), "F-E2E-Mysql-V03")
}

func runE2ECreateReadyRemove(t *testing.T, fx *monorepoFixture, wtName string) {
	t.Helper()

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
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := runCLI(ctx, "create", "--name", wtName); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Registry should have the wtName entry with a mysql port.
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

	// …and it must be the container that is listening. Without this the
	// test would pass against any mysqld on that port, which is exactly
	// what "the omitted backend field runs docker" needs to prove.
	assertContainerRunning(ctx, t, fmt.Sprintf("bough-mysql-%d", port))

	// .env.local must exist in the demo-db worktree and contain the
	// templated port.
	envPath := filepath.Join(fx.root, "worktrees", wtName, "demo-db", ".env.local")
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

// assertContainerRunning fails unless the docker backend created the
// container the mysql plugin names for this port. The fixtures declare
// no `backend:`, so this is what distinguishes "the default resolved to
// docker" from "something is listening on that port".
func assertContainerRunning(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	cli, err := dockerutil.NewClient()
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil {
		t.Fatalf("look up container %s: %v", name, err)
	}
	if id == "" {
		t.Fatalf("no container named %s — the engine did not run on the docker backend", name)
	}
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
