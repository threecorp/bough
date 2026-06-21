package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/capability"
	"github.com/ikeikeikeike/bough/internal/export"
	"github.com/ikeikeikeike/bough/pkg/schema"
	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// Ν-1.6 wires `bough capability compile` and four sibling
// subcommands so an operator can take the v0.6 default emitter set
// (agent-skill / claude-skill / mcp) and render artifacts straight
// from the instinct subsystem. The dispatch path is:
//
//	bough capability compile --to agent-skill --profile claude-code \
//	    --instinct-id rule-1 --instinct-id rule-2 --out-dir ./skills
//
// list   → enumerate the registered emitter formats
// preview → DryRun=true; print the artifacts as JSON
// install → stub, v0.6.x
// lint    → stub, v0.6.x

func newCapabilityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capability",
		Short: "Compile instincts into Claude Skills / Agent Skills / MCP artifacts",
		Long: `bough capability synthesises CapabilityArtifacts from the v0.5
instinct subsystem and renders them through the v0.6 emitter
registry (agent-skill, claude-skill, mcp).

The default emitter target is agent-skill (round 4 priority A2:
bough is a host-neutral OSS orchestration layer). Pass
--profile claude-code to switch the agent-skill emitter into the
Claude-compatible layout, or --to claude-skill / mcp to pick a
different format outright.`,
	}
	cmd.AddCommand(
		newCapabilityCompileCmd(),
		newCapabilityListCmd(),
		newCapabilityPreviewCmd(),
		newCapabilityInstallCmd(),
		newCapabilityLintCmd(),
	)
	return cmd
}

// compileOpts gathers the flag values every compile-flavoured
// subcommand reuses. Keeping the struct in one place makes the
// CLI surface easier to extend in v0.6.x (= a single field add
// versus chasing flag bindings across files).
type compileOpts struct {
	target      string
	profile     string
	instinctIDs []string
	outDir      string
	dryRun      bool
}

func bindCompileFlags(cmd *cobra.Command, opts *compileOpts) {
	cmd.Flags().StringVar(&opts.target, "to", "agent-skill", "emitter format (agent-skill | claude-skill | mcp)")
	cmd.Flags().StringVar(&opts.profile, "profile", "generic", "target host profile (claude-code | github-copilot | cursor | codex | gemini-cli | generic)")
	cmd.Flags().StringSliceVar(&opts.instinctIDs, "instinct-id", nil, "instinct IDs to compile (repeatable; omitted = every active instinct in scope)")
	cmd.Flags().StringVar(&opts.outDir, "out-dir", "", "write emitter outputs to this directory (omit to print bytes to stdout)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "synthesise artifacts but skip emit (= preview the shape without writing bytes)")
}

func newCapabilityCompileCmd() *cobra.Command {
	opts := &compileOpts{}
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile instincts into emitter outputs",
		RunE: func(c *cobra.Command, _ []string) error {
			return runCompile(c, opts)
		},
	}
	bindCompileFlags(cmd, opts)
	return cmd
}

func newCapabilityListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the registered emitter formats",
		RunE: func(c *cobra.Command, _ []string) error {
			r := export.DefaultRegistry()
			fmt.Fprintln(c.OutOrStdout(), "Available emitter formats:")
			for _, f := range r.Formats() {
				fmt.Fprintf(c.OutOrStdout(), "  - %s\n", f)
			}
			return nil
		},
	}
}

func newCapabilityPreviewCmd() *cobra.Command {
	opts := &compileOpts{}
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Synthesise artifacts and print them as JSON (no emit)",
		RunE: func(c *cobra.Command, _ []string) error {
			opts.dryRun = true
			return runCompile(c, opts)
		},
	}
	bindCompileFlags(cmd, opts)
	return cmd
}

func newCapabilityInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install compiled artifacts into a host's skill directory",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("install: not yet wired (lands in v0.6.x); use `bough capability compile --out-dir <dir>` for now")
		},
	}
}

func newCapabilityLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint",
		Short: "Validate compiled artifacts against their validation probes",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("lint: not yet wired (lands in v0.6.x); validation.probes are recorded but not executed by v0.6.0")
		},
	}
}

// runCompile is the shared body for compile / preview. The flow is
// host-side and unconditional: spin up the coordinator, harvest the
// instincts the operator asked for, build a CompileRequest, hand it
// to the v0.6 capability.Compiler, then write or print the emissions.
//
// Scope inference for v0.6.0 is intentionally minimal: we hard-code
// the worktree-level "default" scope so runCompile never depends on
// nil cfg / Repositories slices. The --scope / --worktree-id flags
// land in v0.6.x; this default mirrors the value `bough instinct
// ingest --stdin` writes against when no flag is given so the two
// CLIs see the same scope by default.
func runCompile(c *cobra.Command, opts *compileOpts) error {
	coord, closeAll, err := loadInstinctCoordinator(c)
	if err != nil {
		return err
	}
	defer closeAll()

	scope := schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "default"}
	instincts, err := harvestInstincts(coord, scope, opts.instinctIDs)
	if err != nil {
		return err
	}
	if len(instincts) == 0 {
		return fmt.Errorf("compile: no instincts to compile in scope %s/%s (Approve some first or pass --instinct-id)", scope.Level, scope.WorktreeID)
	}

	registry := export.DefaultRegistry()
	if _, err := registry.Lookup(opts.target); err != nil {
		return err
	}
	compiler := capability.NewCompiler(registry)
	req := &capability.CompileRequest{
		SourceInstincts: instincts,
		TargetKinds:     []schema.CapabilityArtifactKind{schema.ArtifactKindSkill},
		Targets:         []schema.Target{{Format: opts.target, Host: opts.profile}},
		Scope:           scope,
		DryRun:          opts.dryRun,
	}
	result, err := compiler.Compile(context.Background(), req, capapi.EmitOptions{Host: opts.profile, OutDir: opts.outDir})
	if err != nil {
		return err
	}
	if opts.dryRun {
		raw, _ := json.MarshalIndent(result.Artifacts, "", "  ")
		fmt.Fprintln(c.OutOrStdout(), string(raw))
		return nil
	}
	return writeEmissions(c, result.Emissions, opts.outDir)
}

// harvestInstincts pulls the source instincts the operator wants
// compiled. An explicit --instinct-id list filters the active set;
// an empty list means "compile every active row in scope".
func harvestInstincts(coord interface {
	Query(ctx context.Context, term string, scope schema.Scope) ([]schema.Instinct, error)
}, scope schema.Scope, idFilter []string,
) ([]schema.Instinct, error) {
	rows, err := coord.Query(context.Background(), "", scope)
	if err != nil {
		return nil, fmt.Errorf("query instincts: %w", err)
	}
	if len(idFilter) == 0 {
		return rows, nil
	}
	want := make(map[string]bool, len(idFilter))
	for _, id := range idFilter {
		want[id] = true
	}
	out := make([]schema.Instinct, 0, len(idFilter))
	missing := make([]string, 0)
	for _, r := range rows {
		if want[r.ID] {
			out = append(out, r)
		}
	}
	for id := range want {
		found := false
		for _, r := range out {
			if r.ID == id {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("instinct ids not found in scope: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// writeEmissions persists each EmitResult to disk under outDir, or
// prints the byte payload to stdout if outDir is empty.
//
// Review #23 #14: emitter Filename values arrive from registered
// Emitter implementations; v0.6.0 ships three first-party emitters
// but the registry is plugin-extensible from v0.6.x. A malicious or
// buggy emitter that returns `../../etc/whatever` would otherwise
// land bytes outside the operator's --out-dir. We canonicalise the
// resolved path through filepath.Rel and refuse anything that
// escapes the directory — same guard pattern the Go archive/tar
// reference applies to extracted entries.
func writeEmissions(c *cobra.Command, emissions []capapi.EmitResult, outDir string) error {
	if outDir == "" {
		for _, e := range emissions {
			fmt.Fprintf(c.OutOrStdout(), "// %s (%s)\n%s\n", e.Filename, e.ContentType, string(e.Bytes))
		}
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return fmt.Errorf("resolve out-dir %s: %w", outDir, err)
	}
	for _, e := range emissions {
		cleaned := filepath.Clean(e.Filename)
		if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
			return fmt.Errorf("emitter %q returned an unsafe filename %q (absolute or escapes --out-dir)", e.ContentType, e.Filename)
		}
		path := filepath.Join(absOut, cleaned)
		rel, err := filepath.Rel(absOut, path)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("emitter %q returned filename %q that escapes --out-dir %s", e.ContentType, e.Filename, outDir)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, e.Bytes, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(c.OutOrStdout(), "wrote %s (%d bytes)\n", path, len(e.Bytes))
	}
	return nil
}
