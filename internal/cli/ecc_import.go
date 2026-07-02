package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
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
			eccRoot, err := expandHome(from)
			if err != nil {
				return err
			}
			if _, err := os.Stat(eccRoot); err != nil {
				return fmt.Errorf("ecc import: ECC root not found at %s: %w", eccRoot, err)
			}
			dst := homunculus.NewLayout()
			stdout := cmd.OutOrStdout()

			projects, err := readECCProjects(eccRoot)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "ECC root:   %s\n", eccRoot)
			fmt.Fprintf(stdout, "bough root: %s\n", dst.Root)
			fmt.Fprintf(stdout, "projects:   %d\n\n", len(projects))

			imported := 0
			for id, meta := range projects {
				srcDir := filepath.Join(eccRoot, "projects", id)
				if _, err := os.Stat(srcDir); err != nil {
					continue
				}
				instCount := countInstincts(filepath.Join(srcDir, "instincts", "personal"))
				fmt.Fprintf(stdout, "  %s (%s): %d instincts\n", id, meta.Name, instCount)
				if dryRun {
					continue
				}
				if err := copyProject(srcDir, dst.ProjectDir(id)); err != nil {
					return fmt.Errorf("ecc import: copy project %s: %w", id, err)
				}
				reg := homunculus.NewRegistryRW(dst)
				if err := reg.WriteUpsert(homunculus.Project{
					ID: id, Name: meta.Name, Root: meta.Root, Remote: meta.Remote,
				}); err != nil {
					return err
				}
				imported++
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

			if dryRun {
				fmt.Fprintf(stdout, "\ndry-run: nothing copied. Re-run with --apply to import.\n")
			} else {
				fmt.Fprintf(stdout, "\nimported %d projects into %s\n", imported, dst.Root)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", DefaultECCRoot, "ECC homunculus root to import from")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "report what would be copied without writing (default true)")
	// --apply is the inverse of --dry-run for ergonomics.
	cmd.Flags().BoolFunc("apply", "perform the copy (= --dry-run=false)", func(string) error {
		dryRun = false
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
func copyProject(src, dst string) error {
	return copyTree(src, dst, 0)
}

func copyTree(src, dst string, depth int) error {
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
			return copySymlink(path, target, depth)
		}
		return copyFile(path, target)
	})
}

// copySymlink resolves a symlink and copies the content it points at: a
// symlink to a directory is walked as if it were a real subtree (the ECC
// dedup case); a symlink to a file is copied by value (copyFile opens
// through the link); a dangling or unreadable link is skipped so one bad
// link never aborts the migration.
func copySymlink(path, target string, depth int) error {
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
		return copyTree(real, target, depth+1)
	}
	return copyFile(path, target)
}

func copyFile(src, dst string) error {
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
		return copyInstinctFile(src, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
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

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
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
