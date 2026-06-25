package evolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ClusterLabels is the evolve catalog: a kebab-case-slug → "Apply
// when ..." description map. The schema matches ECC's
// cluster-labels.json so an operator can point bough at an existing
// ECC catalog (and vice versa). The label string is sacred once
// published — renaming it breaks the SKILL.md / symlink chain — so
// the writer only ever ADDS keys, never renames existing ones.
type ClusterLabels struct {
	Labels  map[string]string `json:"labels"`
	Updated string            `json:"updated"`
}

// LoadLabels reads cluster-labels.json. A missing file returns an
// empty catalog (= the first evolve pass starts from zero priors).
func LoadLabels(path string) (*ClusterLabels, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ClusterLabels{Labels: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("evolve.LoadLabels: %w", err)
	}
	var cl ClusterLabels
	if err := json.Unmarshal(raw, &cl); err != nil {
		return nil, fmt.Errorf("evolve.LoadLabels: parse %s: %w", path, err)
	}
	if cl.Labels == nil {
		cl.Labels = map[string]string{}
	}
	return &cl, nil
}

// Priors projects the catalog into the []Prior slice the clustering
// pipeline measures candidates against. Sorted by label for
// deterministic ordering (= reproducible nearest-prior ties).
func (cl *ClusterLabels) Priors() []Prior {
	out := make([]Prior, 0, len(cl.Labels))
	keys := make([]string, 0, len(cl.Labels))
	for k := range cl.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, Prior{Label: k, Description: cl.Labels[k]})
	}
	return out
}

// PriorUnion returns the union of every prior's (label + description)
// tokens — the precise GATE 3' comparison surface.
func (cl *ClusterLabels) PriorUnion() map[string]struct{} {
	out := map[string]struct{}{}
	for label, desc := range cl.Labels {
		for tok := range Tokenize(label + " " + desc) {
			out[tok] = struct{}{}
		}
	}
	return out
}

// Add inserts a new label. Existing labels are never overwritten
// (= the "sacred string" rule); Add reports whether the label was
// new so the caller can tell a fresh mint from a re-run.
func (cl *ClusterLabels) Add(label, description string) (added bool) {
	if cl.Labels == nil {
		cl.Labels = map[string]string{}
	}
	if _, exists := cl.Labels[label]; exists {
		return false
	}
	cl.Labels[label] = description
	return true
}

// Save writes the catalog atomically (= tmp + rename) after taking a
// timestamped backup of the previous file, mirroring ECC's
// backup-before-edit guarantee. now is injected so tests pin the
// Updated stamp + backup suffix.
func (cl *ClusterLabels) Save(path string, now time.Time) error {
	cl.Updated = now.UTC().Format(time.RFC3339Nano)
	if err := backupExisting(path, now); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(cl, "", "  ")
	if err != nil {
		return fmt.Errorf("evolve.ClusterLabels.Save: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("evolve.ClusterLabels.Save: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("evolve.ClusterLabels.Save: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("evolve.ClusterLabels.Save: rename: %w", err)
	}
	return nil
}

// backupExisting copies path to path.bak-<YYYYMMDD-HHMMSS> before a
// mutation. Missing source is a no-op (= first write has nothing to
// back up). The backup is best-effort copy via read+write so it
// survives across filesystems.
func backupExisting(path string, now time.Time) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("evolve.backupExisting: read %s: %w", path, err)
	}
	bak := fmt.Sprintf("%s.bak-%s", path, now.UTC().Format("20060102-150405"))
	if err := os.WriteFile(bak, raw, 0o644); err != nil {
		return fmt.Errorf("evolve.backupExisting: write %s: %w", bak, err)
	}
	return nil
}
