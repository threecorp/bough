package homunculus

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Instinct is the in-memory shape of one observed instinct file.
// The persisted form is YAML frontmatter + Markdown body. Raw holds
// the original frontmatter map so unknown fields survive a
// read-write round-trip (= ECC schema drift safety).
type Instinct struct {
	ID         string
	Trigger    string
	Confidence float64
	Domain     string
	Scope      string // "project" | "global"
	Observed   int
	FirstSeen  time.Time
	LastSeen   time.Time
	Body       string
	Raw        map[string]any
	Path       string
}

// idPattern is the canonical kebab-case slug regex. Filenames MUST
// match this so the ECC v3.2 filename ↔ id mismatch bug (= silent
// skip) cannot recur in bough.
var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ErrIDMismatch is returned when an instinct's frontmatter `id:`
// disagrees with its on-disk filename minus .md. The host fails
// rather than silently dropping the instinct.
var ErrIDMismatch = errors.New("instinct frontmatter id does not match filename")

// ErrIDInvalid is returned when the id is empty or violates the
// kebab-case slug pattern.
var ErrIDInvalid = errors.New("instinct id is empty or not kebab-case")

// ReadInstinctFile loads one instinct from disk, validating the
// filename ↔ id constraint. Callers iterating a directory should
// surface per-file errors as soft (= log + skip) so one malformed
// file does not abort the scan.
func ReadInstinctFile(path string) (*Instinct, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("homunculus.ReadInstinctFile: %w", err)
	}
	in, err := parseInstinct(raw)
	if err != nil {
		return nil, fmt.Errorf("homunculus.ReadInstinctFile: %s: %w", path, err)
	}
	in.Path = path
	wantID := strings.TrimSuffix(filepath.Base(path), ".md")
	if in.ID != wantID {
		return in, fmt.Errorf("%w (filename=%s, frontmatter=%s)", ErrIDMismatch, wantID, in.ID)
	}
	return in, nil
}

// ScanInstincts returns every readable instinct under dir. Files
// without frontmatter (= ECC catalog files: INSTINCTS.md / MEMORY.md
// / README.md) are skipped silently. Returns soft errors per file
// in the second return so a caller can surface them on bough doctor.
func ScanInstincts(dir string) ([]*Instinct, []error) {
	out := []*Instinct{}
	softErrs := []error{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		base := filepath.Base(path)
		switch base {
		case "INSTINCTS.md", "MEMORY.md", "README.md":
			return nil
		}
		in, err := ReadInstinctFile(path)
		if err != nil {
			softErrs = append(softErrs, err)
			return nil
		}
		out = append(out, in)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		softErrs = append(softErrs, fmt.Errorf("homunculus.ScanInstincts: walk %s: %w", dir, err))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, softErrs
}

// WriteInstinctFile writes one instinct to <dir>/<id>.md atomically
// (= tmp + rename). The filename is forced to match the id so the
// ECC v3.2 mismatch bug cannot reappear. Duplicate detection is the
// caller's responsibility; this writer overwrites in place.
func WriteInstinctFile(dir string, in *Instinct) (string, error) {
	if in == nil {
		return "", errors.New("homunculus.WriteInstinctFile: nil instinct")
	}
	if !idPattern.MatchString(in.ID) {
		return "", fmt.Errorf("%w: %q", ErrIDInvalid, in.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("homunculus.WriteInstinctFile: mkdir %s: %w", dir, err)
	}
	target := filepath.Join(dir, in.ID+".md")
	buf, err := renderInstinct(in)
	if err != nil {
		return "", err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return "", fmt.Errorf("homunculus.WriteInstinctFile: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", fmt.Errorf("homunculus.WriteInstinctFile: rename %s → %s: %w", tmp, target, err)
	}
	return target, nil
}

func parseInstinct(raw []byte) (*Instinct, error) {
	// frontmatter requires a leading "---" delimiter line. Without
	// it the file is treated as not-an-instinct (= ECC catalog
	// files: INSTINCTS.md / MEMORY.md / README.md ride that path).
	if !bytes.HasPrefix(raw, []byte("---")) {
		return nil, errors.New("no YAML frontmatter")
	}
	rest := bytes.TrimPrefix(raw, []byte("---"))
	rest = bytes.TrimLeft(rest, "\n\r")
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, errors.New("frontmatter closing --- not found")
	}
	frontBytes := rest[:end]
	body := bytes.TrimLeft(rest[end+len("\n---"):], "\n\r-")
	rawMap := map[string]any{}
	if err := yaml.Unmarshal(frontBytes, &rawMap); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	in := &Instinct{
		ID:         stringField(rawMap, "id"),
		Trigger:    stringField(rawMap, "trigger"),
		Confidence: floatField(rawMap, "confidence"),
		Domain:     stringField(rawMap, "domain"),
		Scope:      stringField(rawMap, "scope"),
		Observed:   intField(rawMap, "observed"),
		FirstSeen:  timeField(rawMap, "first_seen"),
		LastSeen:   timeField(rawMap, "last_seen"),
		Body:       string(body),
		Raw:        rawMap,
	}
	return in, nil
}

func renderInstinct(in *Instinct) ([]byte, error) {
	merged := map[string]any{}
	for k, v := range in.Raw {
		merged[k] = v
	}
	merged["id"] = in.ID
	if in.Trigger != "" {
		merged["trigger"] = in.Trigger
	}
	if in.Confidence > 0 {
		merged["confidence"] = in.Confidence
	}
	if in.Domain != "" {
		merged["domain"] = in.Domain
	}
	if in.Scope != "" {
		merged["scope"] = in.Scope
	}
	if in.Observed > 0 {
		merged["observed"] = in.Observed
	}
	if !in.FirstSeen.IsZero() {
		merged["first_seen"] = in.FirstSeen.UTC().Format(time.RFC3339)
	}
	if !in.LastSeen.IsZero() {
		merged["last_seen"] = in.LastSeen.UTC().Format(time.RFC3339)
	}
	frontBytes, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("homunculus.WriteInstinctFile: marshal frontmatter: %w", err)
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(frontBytes)
	b.WriteString("---\n\n")
	body := strings.TrimRight(in.Body, "\n")
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}

func stringField(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

func floatField(m map[string]any, k string) float64 {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case float32:
			return float64(n)
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0
}

func intField(m map[string]any, k string) int {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}

func timeField(m map[string]any, k string) time.Time {
	if v, ok := m[k]; ok {
		s := fmt.Sprint(v)
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
