package evolve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A skill portfolio is not just "whatever the last evolve run produced".
// Three things make it a portfolio rather than an output directory, and
// each one exists because of a way the naive version loses information:
//
//   - A hand-edited skill must survive the next run. Re-emitting is how
//     an operator's rewrite silently disappears, and the loss is
//     invisible: the file is still there, just not theirs any more.
//   - A retired skill must stay retired. Deleting the directory is the
//     obvious way to reject a bad skill, and the next evolve pass
//     recreates it — so rejection has to be recorded, not performed by
//     deletion.
//   - A skill with nothing concrete in it is worse than no skill. A body
//     of generalities matches every prompt and teaches nothing; the bar
//     is mechanical so it cannot drift with the judge's mood.

// ErrCurated is returned when a skill file carries `curated: true` and
// would otherwise be overwritten. The caller reports it and moves on;
// this is not a failure of the run.
var ErrCurated = errors.New("skill is curated (hand-edited): refusing to overwrite")

// ErrRetired is returned when a slug is in the retire registry. Same
// contract as ErrCurated: reported, not fatal.
var ErrRetired = errors.New("skill slug is retired: refusing to re-emit")

// curatedRe matches the frontmatter flag an operator sets to claim a
// skill. It is a line-anchored match on the raw text rather than a YAML
// parse because the file may have been edited by hand into something
// this package does not model, and losing the claim over a formatting
// quirk is exactly the failure being prevented.
var curatedRe = regexp.MustCompile(`(?m)^curated:\s*true\s*$`)

// IsCurated reports whether the SKILL.md at path claims to be
// hand-maintained. A missing or unreadable file is not curated — there
// is nothing to protect.
func IsCurated(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return curatedRe.Match(raw)
}

// retireRegistryName is the file inside the skills directory recording
// slugs the operator has rejected. It lives beside the skills rather
// than in config because it is corpus state, not configuration: it
// changes as the operator prunes, and it must travel with the corpus.
const retireRegistryName = ".retired.json"

// defaultRetireOverlap is the share of a retired skill's members a new
// cluster may repeat before it counts as the same rejected knowledge.
// It seeds RetireRegistry.overlap rather than being read directly, so
// the threshold is a per-registry value a test can tighten instead of a
// package-scope knob — the same reasoning that put the exclusion window
// on ExclusionWindow rather than in consts.
//
// Slug equality alone does not hold: clustering names a theme from its
// members, so the next pass over a barely-changed corpus produces the
// same grouping under a slightly different label, and a registry keyed
// on the label lets it straight back in. What the operator rejected was
// the GROUPING, not the string.
const defaultRetireOverlap = 0.5

// RetiredSkill is one rejection: why, and what was in it. The members
// are what makes the rejection survive a rename.
type RetiredSkill struct {
	Reason string `json:"reason"`
	// Members are the instinct ids the retired skill was built from.
	// Empty means the entry predates member recording; the guard then
	// falls back to slug equality for that entry and says so, rather
	// than silently letting a renamed cluster through.
	Members []string `json:"members,omitempty"`
}

// RetireRegistry is the set of skills that must never be re-emitted.
type RetireRegistry struct {
	// Slugs maps a retired slug to its rejection, so a later "why is
	// this skill missing?" has an answer on disk.
	Slugs map[string]RetiredSkill `json:"retired"`
	// overlap is the member-overlap share at which a new cluster counts
	// as a retired one. Seeded by LoadRetireRegistry; a zero value falls
	// back to the default rather than meaning "match everything", so a
	// registry built as a literal cannot silently reject every cluster.
	overlap float64
}

// overlapThreshold is the share in force for this registry.
func (r *RetireRegistry) overlapThreshold() float64 {
	if r.overlap <= 0 {
		return defaultRetireOverlap
	}
	return r.overlap
}

// UnmarshalJSON accepts both the current object form and the earlier
// `{slug: "reason"}` string form.
//
// The tolerance is deliberately narrow. An unrecognised value is an
// ERROR, not a skipped entry: a registry whose entries silently vanish
// resurrects every skill the operator rejected, and it does so quietly
// — which is exactly how a reader that cannot parse its writer becomes
// indistinguishable from a reader that found nothing.
func (r *RetireRegistry) UnmarshalJSON(b []byte) error {
	var env struct {
		Retired map[string]json.RawMessage `json:"retired"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	r.Slugs = make(map[string]RetiredSkill, len(env.Retired))
	for slug, raw := range env.Retired {
		var obj RetiredSkill
		if err := json.Unmarshal(raw, &obj); err == nil {
			r.Slugs[slug] = obj
			continue
		}
		var reason string
		if err := json.Unmarshal(raw, &reason); err == nil {
			r.Slugs[slug] = RetiredSkill{Reason: reason}
			continue
		}
		return fmt.Errorf("evolve: retire registry entry %q is neither an object nor a reason string: %s", slug, raw)
	}
	return nil
}

// LoadRetireRegistry reads the registry from a skills directory. A
// missing registry is an empty one, not an error: most corpora have
// retired nothing.
func LoadRetireRegistry(skillsDir string) (*RetireRegistry, error) {
	path := filepath.Join(skillsDir, retireRegistryName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &RetireRegistry{Slugs: map[string]RetiredSkill{}, overlap: defaultRetireOverlap}, nil
		}
		return nil, fmt.Errorf("evolve: read retire registry %s: %w", path, err)
	}
	reg := &RetireRegistry{}
	if err := json.Unmarshal(raw, reg); err != nil {
		return nil, fmt.Errorf("evolve: parse retire registry %s: %w", path, err)
	}
	if reg.Slugs == nil {
		reg.Slugs = map[string]RetiredSkill{}
	}
	reg.overlap = defaultRetireOverlap
	return reg, nil
}

// Retired reports whether a slug has been rejected under that exact
// name. Callers deciding whether to EMIT should use RetiredAs, which
// also catches the same grouping under a new label.
func (r *RetireRegistry) Retired(slug string) bool {
	if r == nil {
		return false
	}
	_, ok := r.Slugs[slug]
	return ok
}

// RetiredAs reports whether emitting this slug with these members would
// resurrect something the operator rejected, and names which rejection
// it matched. A match is either the same slug or a member overlap of at
// least the registry's overlap threshold against a retired entry.
//
// Overlap is measured against the RETIRED skill's members: the question
// is "is this the thing that was rejected", so a new cluster that
// swallows a retired one whole is caught even when it has grown.
func (r *RetireRegistry) RetiredAs(slug string, members []string) (string, bool) {
	if r == nil {
		return "", false
	}
	if e, ok := r.Slugs[slug]; ok {
		return fmt.Sprintf("%s (%s)", slug, e.Reason), true
	}
	if len(members) == 0 {
		return "", false
	}
	have := make(map[string]struct{}, len(members))
	for _, m := range members {
		have[m] = struct{}{}
	}
	// Sorted, not map order: with two retired entries both over the
	// threshold, Go's randomized iteration cited a different one on every
	// run, so "which rejection blocked this" changed with no data change.
	for _, retiredSlug := range r.sortedSlugs() {
		e := r.Slugs[retiredSlug]
		if len(e.Members) == 0 {
			// Recorded before members were kept. Slug equality was
			// already checked above, and guessing is worse than not.
			continue
		}
		shared := 0
		for _, m := range e.Members {
			if _, ok := have[m]; ok {
				shared++
			}
		}
		if float64(shared)/float64(len(e.Members)) >= r.overlapThreshold() {
			return fmt.Sprintf("%s (%s) — %d of its %d instinct(s) are in this cluster, so this is the same grouping under a new label",
				retiredSlug, e.Reason, shared, len(e.Members)), true
		}
	}
	return "", false
}

// Retire records a rejection and persists the registry. Recording the
// reason is not optional decoration: without it the registry becomes a
// list of slugs nobody remembers rejecting, and the first person to
// wonder why deletes the file. Recording the members is what makes the
// rejection outlive the label.
func (r *RetireRegistry) Retire(skillsDir, slug, reason string, members []string) error {
	if r.Slugs == nil {
		r.Slugs = map[string]RetiredSkill{}
	}
	if reason == "" {
		reason = "(no reason recorded)"
	}
	sorted := append([]string(nil), members...)
	sort.Strings(sorted)
	r.Slugs[slug] = RetiredSkill{Reason: reason, Members: sorted}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("evolve: mkdir %s: %w", skillsDir, err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("evolve: marshal retire registry: %w", err)
	}
	path := filepath.Join(skillsDir, retireRegistryName)
	return atomicWriteFile(path, append(raw, '\n'))
}

// identifierRunLength is how many distinct concrete identifiers a skill
// body must carry. Three is the point where a body stops reading as
// advice-shaped prose and starts naming things a reader can go find.
const identifierMin = 3

// descriptionMaxBytes bounds the frontmatter description. The
// description is what a host matches against to decide whether to load
// the skill at all, so an essay there both wastes the listing budget and
// blurs the match.
const descriptionMaxBytes = 240

// bodyMaxBytes bounds the whole skill. A skill that grew past this is
// several skills wearing one label.
const bodyMaxBytes = 8192

// concreteIdentifierRe matches things a reader can act on: dotted or
// pathed names, CamelCase symbols, and names carrying digits or
// separators. Deliberately the same shape the retrieval package treats
// as identifier-like — a body full of ordinary English is precisely what
// this bar exists to reject.
var concreteIdentifierRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:[./-][A-Za-z0-9_]+)+|[A-Za-z_]*[a-z][A-Z][A-Za-z0-9_]*|[A-Za-z_][A-Za-z0-9_]*[0-9]+[A-Za-z0-9_]*`)

// QualityIssues returns every reason the artifact falls below the bar,
// empty when it passes. Returning ALL of them rather than the first is
// deliberate: an author fixing one issue at a time, re-running an LLM
// each round, is the expensive way to discover there were three.
func QualityIssues(art SkillArtifact) []string {
	var issues []string
	if n := len(art.Description); n > descriptionMaxBytes {
		issues = append(issues, fmt.Sprintf("description is %d bytes, over the %d-byte budget the host matches against", n, descriptionMaxBytes))
	}
	if strings.TrimSpace(art.Description) == "" {
		issues = append(issues, "description is empty: the host has nothing to match on")
	}
	if n := len(art.Body); n > bodyMaxBytes {
		issues = append(issues, fmt.Sprintf("body is %d bytes, over the %d-byte cap (this is several skills under one label)", n, bodyMaxBytes))
	}
	if got := countConcreteIdentifiers(art.Body); got < identifierMin {
		issues = append(issues, fmt.Sprintf("body names %d concrete identifiers, needs %d: advice with nothing to look up matches every prompt and teaches none", got, identifierMin))
	}
	return issues
}

// countConcreteIdentifiers counts DISTINCT identifier-shaped tokens in
// the body. Distinct, because one identifier repeated in five bullets is
// still one thing a reader can go find.
func countConcreteIdentifiers(body string) int {
	seen := map[string]struct{}{}
	for _, m := range concreteIdentifierRe.FindAllString(body, -1) {
		seen[strings.ToLower(m)] = struct{}{}
	}
	return len(seen)
}

// sortedSlugs returns the registry's slugs in a stable order for output.
func (r *RetireRegistry) sortedSlugs() []string {
	out := make([]string, 0, len(r.Slugs))
	for s := range r.Slugs {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
