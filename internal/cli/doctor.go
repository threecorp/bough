package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

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
// until v0.27.0. Neither is read any more, and neither is bough's to
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
			keys, err := retiredConfigKeys(path)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: could not check for retired sections: %v", path, err))
			}
			for _, k := range keys {
				notes = append(notes, fmt.Sprintf(
					"%s: section '%s:' is retired and does nothing (delete it; the key stops parsing in v0.29.0)",
					path, k))
			}
		}
	}
	if dir := retiredCorpusDir(); dir != "" {
		notes = append(notes, fmt.Sprintf(
			"%s holds the instinct corpus from v0.27.0 and earlier; bough no longer uses it (doctor only reads its observer.pid files)", dir))
		for _, pid := range liveObserverPIDs(dir) {
			notes = append(notes, fmt.Sprintf(
				"observer.pid names pid %d, which is still alive; if `pgrep -fl 'observer _run-daemon'` lists it, stop it with `pkill -f 'observer _run-daemon'`", pid))
		}
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
// carries, in a fixed order. The file is parsed as YAML rather than scanned
// as text, so every spelling the loader accepts (`"instinct":`, `instinct :`,
// a flow mapping) is found, and a nested key of the same name is not.
func retiredConfigKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	var found []string
	for _, key := range []string{"instinct", "memory_backends", "export", "quality_gates"} {
		if _, ok := top[key]; ok {
			found = append(found, key)
		}
	}
	return found, nil
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

// liveObserverPIDs returns the PIDs recorded in the retired corpus's
// observer.pid files whose process is still alive. The daemon is detached
// and this version has no command to stop it, so doctor is where an
// operator learns it outlived the upgrade. A live PID may have been reused,
// so the caller reports it as a PID to check, not as a confirmed daemon.
func liveObserverPIDs(corpusDir string) []int {
	// ReadDir, not Glob: the corpus path is literal and may contain [ or *.
	projects, _ := os.ReadDir(filepath.Join(corpusDir, "projects"))
	var out []int
	for _, e := range projects {
		b, err := os.ReadFile(filepath.Join(corpusDir, "projects", e.Name(), "observer.pid"))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		// EPERM means the process exists but belongs to another user.
		if err := proc.Signal(syscall.Signal(0)); err == nil || errors.Is(err, syscall.EPERM) {
			out = append(out, pid)
		}
	}
	return out
}
