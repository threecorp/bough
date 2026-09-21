package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/termio"
)

// newDoctorCmd wires the top-level `bough doctor` alias for
// `bough hook doctor`. Round 5 review insisted the transparency
// check be reachable without remembering the `hook` namespace — the
// doctor is the operator's first stop when the automation surface
// starts to feel surprising. Both spellings render the same report.
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report bough's hook wiring and engine-plugin posture (alias for `bough claude hook doctor`)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runDoctor(c)
		},
	}
}

// renderRetiredConfig names the `.bough.yaml` sections and on-disk
// state left over from the continuous-learning loop bough carried
// until v0.26.0. Neither is read any more, and neither is bough's to
// delete, so the doctor is where an operator finds out they are there.
//
// The config half re-reads the file rather than taking the loaded
// Config: the retired keys are deliberately absent from that struct
// (see LegacyConfig's Retired* fields), and a doctor that reported
// only what the struct models could never report on them.
func renderRetiredConfig(c *cobra.Command, w io.Writer) {
	st := termio.NewStyler(w)
	fmt.Fprintln(w)

	var notes []string
	cwd, err := os.Getwd()
	if err == nil {
		if path := resolveConfigPath(c, resolveMonorepoRoot(cwd)); path != "" {
			if keys := retiredConfigKeys(path); len(keys) > 0 {
				for _, k := range keys {
					notes = append(notes, fmt.Sprintf(
						"%s: section '%s:' is retired and does nothing (delete it; the key stops parsing in v0.28.0)",
						path, k))
				}
			}
		}
	}
	if dir := retiredCorpusDir(); dir != "" {
		notes = append(notes, fmt.Sprintf(
			"%s holds the instinct corpus from v0.26.0 and earlier; bough no longer reads or writes it", dir))
	}

	if len(notes) == 0 {
		fmt.Fprintf(w, "%s Retired state\n", st.Section(termio.StatusOK))
		fmt.Fprintf(w, "    %s none — no retired config sections, no leftover corpus\n",
			st.Mark(termio.StatusOK))
		return
	}
	fmt.Fprintf(w, "%s Retired state\n", st.Section(termio.StatusNeutral))
	for _, n := range notes {
		fmt.Fprintf(w, "    %s %s\n", st.Mark(termio.StatusNeutral), n)
	}
}

// retiredConfigKeys reports which retired sections a .bough.yaml still
// carries, in a fixed order. Matched at the top level only (a key at
// column 0), so a nested `export:` belonging to some other section is
// not reported: the file is scanned as text because the loader
// deliberately drops these keys on the floor.
func retiredConfigKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := splitLines(string(data))
	var found []string
	for _, key := range []string{"instinct", "memory_backends", "export", "quality_gates"} {
		for _, line := range lines {
			// Prefix, not equality: `export: {}`, `quality_gates: []` and a
			// key with a trailing space or `# comment` are all sections the
			// loader warns about, and a doctor that matched only the bare
			// `key:` spelling would hand out a clean bill for a file bough
			// itself complains about on every create.
			if strings.HasPrefix(line, key+":") {
				found = append(found, key)
				break
			}
		}
	}
	return found
}

// retiredCorpusDir returns the instinct corpus directory if it is still
// on disk, else "". Honours the same env override the retired code did,
// so an operator who moved it still gets told where it is.
func retiredCorpusDir() string {
	dir := os.Getenv("BOUGH_HOMUNCULUS_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "share", "bough-homunculus")
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// splitLines splits on newlines and trims the trailing carriage return
// a Windows-edited file leaves behind, so a key match is not missed for
// a reason that has nothing to do with the key.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	return out
}
