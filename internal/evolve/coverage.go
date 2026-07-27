package evolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Once a cluster becomes a skill, its member instincts are reachable two
// ways: pushed into every prompt by the injector, and pulled by the
// model when the skill's description matches. Both at once is double
// supply — the same knowledge spending the prompt budget AND sitting in
// the skill listing.
//
// The fix is a registry of what a skill already covers, with exactly ONE
// consumer (the injector). One registry and one consumer is the whole
// design constraint: a second writer, or a second reader with its own
// idea of "covered", is how the two paths drift into disagreeing about
// what is delivered.
//
// The switch that acts on it is OFF by default, and that is not
// timidity. Turning off the push for knowledge whose pull path has not
// demonstrably fired removes it from BOTH paths at once — the skill
// exists but nothing loads it, and the instincts are no longer injected.
// The evidence that the pull path works has to come first; until then
// the registry is recorded and reported, not enforced.

// SkillCoverage records which instinct ids each evolved skill delivers.
type SkillCoverage struct {
	// BySkill maps a skill slug to the instinct ids it was built from.
	// Keyed by skill rather than a flat id set so removing one skill
	// removes exactly its coverage, with no need to recompute the rest.
	BySkill map[string][]string `json:"by_skill"`
	// UpdatedAt is when the last evolve pass rewrote this file.
	UpdatedAt string `json:"updated_at"`
}

// LoadSkillCoverage reads the registry. A missing file is an empty
// registry: a corpus that has never evolved covers nothing.
func LoadSkillCoverage(path string) (*SkillCoverage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SkillCoverage{BySkill: map[string][]string{}}, nil
		}
		return nil, fmt.Errorf("evolve: read skill coverage %s: %w", path, err)
	}
	cov := &SkillCoverage{}
	if err := json.Unmarshal(raw, cov); err != nil {
		return nil, fmt.Errorf("evolve: parse skill coverage %s: %w", path, err)
	}
	if cov.BySkill == nil {
		cov.BySkill = map[string][]string{}
	}
	return cov, nil
}

// Record notes that a skill delivers these instinct ids, replacing any
// previous entry for that slug — a re-evolved skill covers what it
// covers NOW, and carrying forward ids it dropped would keep suppressing
// instincts nothing delivers any more.
func (c *SkillCoverage) Record(slug string, instinctIDs []string) {
	if c.BySkill == nil {
		c.BySkill = map[string][]string{}
	}
	ids := append([]string(nil), instinctIDs...)
	sort.Strings(ids)
	c.BySkill[slug] = ids
}

// CoveredIDs returns the flat set of instinct ids delivered by some
// skill.
func (c *SkillCoverage) CoveredIDs() map[string]struct{} {
	out := map[string]struct{}{}
	if c == nil {
		return out
	}
	for _, ids := range c.BySkill {
		for _, id := range ids {
			out[id] = struct{}{}
		}
	}
	return out
}

// Save persists the registry atomically.
func (c *SkillCoverage) Save(path string, now time.Time) error {
	c.UpdatedAt = now.UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("evolve: marshal skill coverage: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("evolve: mkdir %s: %w", filepath.Dir(path), err)
	}
	return atomicWriteFile(path, append(raw, '\n'))
}
