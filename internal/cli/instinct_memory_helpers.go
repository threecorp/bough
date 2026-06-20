package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/instinct"
	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// isAllowlisted is the v0.5 plugin trust check. An empty allowlist
// is interpreted as "v0.5 bundled plugins only" so the default
// config path works for solo dev (the SQLite reference-fallback
// passes silently) but a third-party `bough-plugin-memory-foo` on
// PATH triggers the warn-only NOTICE every spawn. Production teams
// set allowlist explicitly per docs/SECURITY.md.
func isAllowlisted(binName string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return binName == "bough-plugin-memory-sqlite"
	}
	for _, a := range allowlist {
		if a == binName {
			return true
		}
	}
	return false
}

// discoverMemoryBackend spawns the configured memory plugin (only
// `bough-plugin-memory-sqlite` ships in v0.5; v0.6+ adds mem0 /
// Graphiti). The returned cleanup func MUST be deferred — the
// host's go-plugin client keeps the subprocess alive otherwise.
//
// Round 3 follow-up fix (HIGH #8): v0.5 honours
// `instinct.plugin_security.allowlist` in warn-only mode.
// A plugin not in the allowlist still spawns, but a stderr
// NOTICE is emitted so the user sees "untrusted third-party
// plugin running" every time. v0.6 graduates this to an enforce
// option per docs/SECURITY.md.
func discoverMemoryBackend(cfg *config.Config) (memapi.MemoryBackend, func(), string, error) {
	kind := cfg.Instinct.DefaultMemoryBackend
	if kind == "" {
		kind = "sqlite"
	}
	role := ""
	dbPath := ""
	for _, b := range cfg.MemoryBackends {
		if b.Kind == kind {
			role = b.Role
			dbPath = b.Path
			break
		}
	}
	binName := "bough-plugin-memory-" + kind
	if cfg.Instinct.PluginSecurity.UntrustedWarning && !isAllowlisted(binName, cfg.Instinct.PluginSecurity.Allowlist) {
		fmt.Fprintf(os.Stderr,
			"[WARNING] memory plugin %q is not in plugin_security.allowlist.\n"+
				"          Third-party plugins are untrusted code (see docs/SECURITY.md).\n",
			binName,
		)
	}
	binPath, err := exec.LookPath(binName)
	if err != nil {
		return nil, nil, role, fmt.Errorf("%s not found on PATH (run `make build` or install the plugin): %w", binName, err)
	}
	cmd := exec.Command(binPath)
	if dbPath != "" {
		cmd.Env = append(cmd.Environ(), "BOUGH_MEMORY_SQLITE_PATH="+dbPath)
	}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  memapi.Handshake,
		Plugins:          memapi.PluginMap,
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, role, fmt.Errorf("gRPC dial %s: %w", binName, err)
	}
	raw, err := rpc.Dispense(memapi.MemoryBackendPluginKey)
	if err != nil {
		client.Kill()
		return nil, nil, role, fmt.Errorf("dispense memory_backend: %w", err)
	}
	backend, ok := raw.(memapi.MemoryBackend)
	if !ok {
		client.Kill()
		return nil, nil, role, fmt.Errorf("plugin returned %T, not MemoryBackend", raw)
	}
	return backend, func() { client.Kill() }, role, nil
}

// loadInstinctCoordinator does the heavy lifting both instinct and
// memory subcommands need: load .bough.yaml, discover the backend,
// construct the coordinator. The returned close func disposes
// both the backend subprocess and the coordinator's events file.
func loadInstinctCoordinator(cmd *cobra.Command) (*instinct.Coordinator, func(), error) {
	_, cfg, err := loadConfigAndRoot(cmd, "")
	if err != nil {
		return nil, nil, err
	}
	if !cfg.Instinct.Enabled {
		return nil, nil, fmt.Errorf("instinct subsystem disabled in .bough.yaml (set `instinct.enabled: true` to use)")
	}
	backend, killBackend, _, err := discoverMemoryBackend(cfg)
	if err != nil {
		return nil, nil, err
	}
	eventsPath := ".bough/memory/events.jsonl"
	for _, b := range cfg.MemoryBackends {
		if b.EventsLog != "" {
			eventsPath = b.EventsLog
			break
		}
	}
	coord, err := instinct.New(cfg, backend, filepath.Clean(eventsPath))
	if err != nil {
		killBackend()
		return nil, nil, err
	}
	close := func() {
		_ = coord.Close()
		killBackend()
	}
	return coord, close, nil
}

// currentScope returns the worktree-level Scope the CLI runs in.
// We derive it from the cwd's parent monorepo + the current branch
// for now; v0.6+ adds explicit --scope / --worktree-id flags.
func currentScope(cfg *config.Config, repoName, worktreeID string) schema.Scope {
	if repoName == "" && len(cfg.Repositories) > 0 {
		repoName = cfg.Repositories[0].Name
	}
	if worktreeID == "" {
		worktreeID = "default"
	}
	return schema.Scope{
		Level:      schema.ScopeWorktree,
		WorktreeID: worktreeID,
		RepoName:   repoName,
	}
}

// noopCtx returns a context that the caller can cancel; the CLI
// does not currently propagate signal handlers down into the
// subprocess but the placeholder makes future wiring trivial.
func noopCtx() context.Context { return context.Background() }
