package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/fsutil"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
)

// DefaultECCRoot is the canonical ECC homunculus path. `bough ecc
// import` reads from here and writes into bough's separate namespace.
const DefaultECCRoot = "~/.local/share/ecc-homunculus"

// newEccImportCmd wires `bough ecc import` — the migration tool that
// copies an existing affaan-m/everything-claude-code corpus into
// bough's `~/.local/share/bough-homunculus/`. The two layouts are
// structurally identical (= bough mirrored ECC's shape on purpose),
// so the migration is per-project: copy each project's instincts /
// cluster-labels / evolved artifacts and re-register it in bough's
// projects.json.
//
// Default is --dry-run: it reports what would be copied without
// touching bough's namespace. The deliberate namespace separation
// means an import never clobbers the live ECC corpus.
func newEccImportCmd() *cobra.Command {
	var (
		from   string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Migrate an existing ECC homunculus corpus into bough's namespace",
		Long: `bough ecc import copies an affaan-m/everything-claude-code
homunculus corpus (default: ~/.local/share/ecc-homunculus) into
bough's separate ~/.local/share/bough-homunculus/. The ECC corpus is
never modified — the namespace separation means the two systems keep
coexisting after the import.

Default is --dry-run; pass --dry-run=false (or --apply) to perform
the copy.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eccRoot, err := fsutil.ExpandHomeStrict(from)
			if err != nil {
				return fmt.Errorf("ecc import: resolve --from %q: %w", from, err)
			}
			if _, err := os.Stat(eccRoot); err != nil {
				return fmt.Errorf("ecc import: ECC root not found at %s: %w", eccRoot, err)
			}
			dst := homunculus.NewLayout()
			stdout := cmd.OutOrStdout()

			// The gate is built ONCE for the whole import, from the config
			// at the cwd's monorepo root. gateConfigFor falls back to
			// Enabled:true when it cannot read a config, which is the
			// fail-closed half of this path: an unreadable config must not
			// let a foreign corpus in unscreened.
			cwd, cwderr := os.Getwd()
			if cwderr != nil {
				return fmt.Errorf("ecc import: getwd: %w", cwderr)
			}
			screen := &importScreen{
				gate:   instinctgate.New(gateConfigFor(cmd, resolveMonorepoRoot(cwd))),
				layout: dst,
				now:    time.Now(),
			}

			projects, err := readECCProjects(eccRoot)
			if err != nil {
				return err
			}
			// Sorted, not map-order: a corpus large enough to hit a
			// mid-import failure must fail the same way on every run so
			// an operator investigating a failure (and a retry) sees a
			// consistent picture of what was/wasn't attempted.
			ids := make([]string, 0, len(projects))
			for id := range projects {
				ids = append(ids, id)
			}
			sort.Strings(ids)

			fmt.Fprintf(stdout, "ECC root:   %s\n", eccRoot)
			fmt.Fprintf(stdout, "bough root: %s\n", dst.Root)
			fmt.Fprintf(stdout, "projects:   %d\n\n", len(projects))

			var toRegister []homunculus.Project
			var failed []string
			for _, id := range ids {
				meta := projects[id]
				srcDir := filepath.Join(eccRoot, "projects", id)
				if _, err := os.Stat(srcDir); err != nil {
					continue
				}
				instCount := countInstincts(filepath.Join(srcDir, "instincts", "personal"))
				fmt.Fprintf(stdout, "  %s (%s): %d instincts\n", id, meta.Name, instCount)
				if dryRun {
					continue
				}
				// A copy failure (permission denied, disk full, an
				// unreadable file) must not abort the whole migration —
				// it is reported and the remaining projects are still
				// attempted, with a non-zero exit + failure summary at
				// the end so the operator knows exactly what needs a
				// retry.
				screen.projectID = id
				if err := copyProject(srcDir, dst.ProjectDir(id), screen); err != nil {
					fmt.Fprintf(stdout, "    FAILED to copy: %v\n", err)
					failed = append(failed, id)
					continue
				}
				// Printed even when zero: an unmeasured 0 and an unswept
				// directory read identically in a report.
				scanned, heldN, batch, ferr := screen.flush()
				fmt.Fprintf(stdout, "    screened %d instinct(s), held %d\n", scanned, heldN)
				if heldN > 0 {
					fmt.Fprintf(stdout, "      → %s (reversible; `bough instinct verdict keep|retire`)\n", batch)
				}
				if ferr != nil {
					fmt.Fprintf(stdout, "      WARNING: quarantine report: %v\n", ferr)
				}
				toRegister = append(toRegister, homunculus.Project{
					ID: id, Name: meta.Name, Root: meta.Root, Remote: meta.Remote,
				})
			}

			imported := 0
			if len(toRegister) > 0 {
				if err := homunculus.NewRegistryRW(dst).WriteUpsertMany(toRegister); err != nil {
					return fmt.Errorf("ecc import: register imported projects: %w", err)
				}
				imported = len(toRegister)
			}

			// Warn on project dirs present on disk but absent from
			// projects.json. After an ECC re-key the old physical id is
			// usually unregistered and reached only via a registered
			// project's symlink (so its instincts still import); but a
			// genuinely standalone orphan would otherwise be dropped with
			// no signal.
			if entries, derr := os.ReadDir(filepath.Join(eccRoot, "projects")); derr == nil {
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					if _, registered := projects[e.Name()]; !registered {
						fmt.Fprintf(stdout, "  note: projects/%s is on disk but not in projects.json — skipped (orphan)\n", e.Name())
					}
				}
			}

			switch {
			case dryRun:
				fmt.Fprintf(stdout, "\ndry-run: nothing copied. Re-run with --apply to import.\n")
			case len(failed) > 0:
				fmt.Fprintf(stdout, "\nimported %d of %d projects into %s; failed: %s\n",
					imported, len(projects), dst.Root, strings.Join(failed, ", "))
				return fmt.Errorf("ecc import: %d of %d project(s) failed to copy: %s",
					len(failed), len(projects), strings.Join(failed, ", "))
			default:
				fmt.Fprintf(stdout, "\nimported %d projects into %s\n", imported, dst.Root)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", DefaultECCRoot, "ECC homunculus root to import from")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "report what would be copied without writing (default true)")
	// --apply is the inverse of --dry-run for ergonomics. pflag's
	// BoolFunc passes the flag's literal string value on --apply,
	// --apply=true, AND --apply=false alike — it must be parsed, not
	// ignored, or an explicit --apply=false would silently still
	// perform the copy.
	cmd.Flags().BoolFunc("apply", "perform the copy (= --dry-run=false)", func(v string) error {
		apply, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("--apply: %w", err)
		}
		dryRun = !apply
		return nil
	})
	return cmd
}

type eccProjectMeta struct {
	Name   string `json:"name"`
	Root   string `json:"root"`
	Remote string `json:"remote"`
}

func readECCProjects(eccRoot string) (map[string]eccProjectMeta, error) {
	raw, err := os.ReadFile(filepath.Join(eccRoot, "projects.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]eccProjectMeta{}, nil
		}
		return nil, fmt.Errorf("ecc import: read projects.json: %w", err)
	}
	var out map[string]eccProjectMeta
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ecc import: parse projects.json: %w", err)
	}
	return out, nil
}

func countInstincts(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		switch e.Name() {
		case "INSTINCTS.md", "MEMORY.md", "README.md":
			continue
		}
		n++
	}
	return n
}

// maxSymlinkDepth caps how deep copyProject follows nested directory
// symlinks so a pathological symlink cycle in a corpus cannot spin the
// import forever. ECC's real layout nests a single level (a re-keyed
// project's dirs link to the one physical project that holds the
// files), so 32 is far beyond any legitimate corpus.
const maxSymlinkDepth = 32

// copyProject recursively copies the ECC project subtree into bough's
// project dir. Existing files are overwritten (= a re-import refreshes
// the corpus).
//
// Directory symlinks are FOLLOWED, not skipped: ECC dedups storage by
// symlinking a re-keyed project's instincts/, memory/ and evolved/
// dirs at the physical project that still holds the files (e.g.
// projects/<new-id>/instincts -> ../<old-id>/instincts). Skipping those
// links silently dropped the entire corpus while the count probe — which
// reads *through* the link — still reported thousands of instincts, so
// `import --apply` looked like a success yet migrated nothing. A
// dangling link (e.g. a stale ~/.claude/skills entry pointing outside
// the tree) is skipped rather than failing the whole import.
func copyProject(src, dst string, screen *importScreen) error {
	return copyTree(src, dst, dst, 0, screen)
}

// root is the destination PROJECT dir, carried unchanged through the
// recursion so a file's path relative to the project — which is what
// decides whether it lands in the instincts tree — survives the symlink
// descent that rebases src/dst.
func copyTree(src, dst, root string, depth int, screen *importScreen) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return copySymlink(path, target, root, depth, screen)
		}
		return copyFile(path, target, root, screen)
	})
}

// copySymlink resolves a symlink and copies the content it points at: a
// symlink to a directory is walked as if it were a real subtree (the ECC
// dedup case); a symlink to a file is copied by value (copyFile opens
// through the link); a dangling or unreadable link is skipped so one bad
// link never aborts the migration.
func copySymlink(path, target, root string, depth int, screen *importScreen) error {
	if depth >= maxSymlinkDepth {
		// A nesting this deep is a cycle, not a real corpus (ECC nests a
		// single level). Skip this one link rather than returning an error:
		// a pathological/cyclic link must not abort the WHOLE import — the
		// same tolerance the dangling/unreadable cases below already apply.
		// Shallower levels were already copied, so no real data is lost.
		return nil
	}
	info, err := os.Stat(path) // Stat follows the link; Lstat would not
	if err != nil {
		return nil // dangling / unreadable → skip, don't fail the import
	}
	if info.IsDir() {
		real, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			return nil
		}
		return copyTree(real, target, root, depth+1, screen)
	}
	return copyFile(path, target, root, screen)
}

func copyFile(src, dst, root string, screen *importScreen) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// An instinct .md can arrive from a foreign ECC corpus serialized as
	// a single physical line with literal \n escapes (the observer model
	// wrote it via its Write tool + JSON-escaped the body). Heal that at
	// the import boundary so bough's strict reader does not silently drop
	// it. Catalog files (INSTINCTS.md / MEMORY.md / README.md) are never
	// instincts and can grow large over months of logging, so they skip
	// the read-whole-file normalize path and stream like any other file
	// below — matching the same trio ScanInstincts ignores.
	base := filepath.Base(src)
	if strings.HasSuffix(src, ".md") && base != "INSTINCTS.md" && base != "MEMORY.md" && base != "README.md" {
		// Screen before writing, and only ever SUBTRACT: a held note is
		// diverted to quarantine and this write is skipped, so a refusal
		// can never destroy a good file already sitting at dst.
		if rel, rerr := filepath.Rel(root, dst); rerr == nil && screensInstinct(rel) {
			held, serr := screen.screen(src, dst)
			if serr != nil {
				return serr
			}
			if held {
				return nil
			}
		}
		return copyInstinctFile(src, dst)
	}
	return fsutil.CopyFile(src, dst)
}

// copyInstinctFile copies a .md file, un-escaping a single-line
// corrupted instinct back into real newlines before writing so bough's
// homunculus receives a parseable file. wantID is the filename minus
// .md (bough enforces filename ↔ frontmatter id); NormalizeInstinct
// only rewrites when the repair re-parses and its id matches wantID,
// otherwise the bytes are copied verbatim.
func copyInstinctFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	wantID := strings.TrimSuffix(filepath.Base(src), ".md")
	out, _ := homunculus.NormalizeInstinct(raw, wantID)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// newEccCmd is the `bough ecc` namespace parent.
func newEccCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ecc",
		Short: "Interoperate with an existing everything-claude-code corpus",
	}
	cmd.AddCommand(newEccImportCmd())
	return cmd
}
