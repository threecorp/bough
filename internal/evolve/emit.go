package evolve

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// AgentMinMembers + AgentMinConfidence are the ECC thresholds for
// promoting a cluster to an agent (= broader-domain artifact than a
// skill). A cluster with >= 3 members AND average confidence >= 0.75
// is agent-eligible.
const (
	AgentMinMembers    = 3
	AgentMinConfidence = 0.75
)

// CommandMinConfidence is the ECC threshold for promoting a single
// instinct to a slash command: domain == "workflow" AND confidence
// >= 0.7. Commands come from individual instincts, not clusters.
const CommandMinConfidence = 0.70

// SkillArtifact is one rendered SKILL.md ready to write. Slug is the
// kebab-case label (= dir name + symlink name); Body is the full
// frontmatter + Markdown.
type SkillArtifact struct {
	Slug        string
	Description string
	Body        string
	Members     []string // member instinct ids, for provenance
}

// RenderSkill builds the SKILL.md body for a PASS cluster. The shape
// mirrors ECC's evolved/skills/<slug>/SKILL.md: YAML frontmatter
// (name + description + evolved_from) followed by an "Evolved from N
// instincts" header + the per-member action lines.
func RenderSkill(label, description string, c Cluster, th Thresholds, now time.Time) SkillArtifact {
	ids := memberIDs(c)
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", label)
	fmt.Fprintf(&b, "description: %s\n", yamlOneLine(description))
	b.WriteString("evolved_from:\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "  - %s\n", id)
	}
	fmt.Fprintf(&b, "generated_by: bough-evolve@v0.9.1\n")
	fmt.Fprintf(&b, "generated_at: %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "evolve_thresholds: {member_min: %d, cohesion_min: %.2f, lexicon_coverage_max: %.2f, relative_isolation_min: %.2f}\n",
		th.MemberMin, th.CohesionMin, th.LexiconCoverageMax, th.RelIsolationMin)
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", label)
	fmt.Fprintf(&b, "%s\n\n", description)
	fmt.Fprintf(&b, "Evolved from %d instincts.\n\n", len(c.Members))
	b.WriteString("## Actions\n\n")
	for _, m := range c.Members {
		fmt.Fprintf(&b, "- %s\n", firstActionLine(m.Body))
	}
	b.WriteString("\n")
	b.WriteString(sourceInstinctsBlock(c.Members))
	return SkillArtifact{
		Slug:        label,
		Description: description,
		Body:        b.String(),
		Members:     ids,
	}
}

// WriteSkill persists a SkillArtifact to
// <skillsDir>/<slug>/SKILL.md atomically and returns the SKILL.md
// path. Existing skill dirs are overwritten (= re-running evolve
// refreshes the body without minting a new label). Symlinking the
// result into project scope is done by cli.deployProjectSkills.
func WriteSkill(skillsDir string, art SkillArtifact) (string, error) {
	if !labelPattern.MatchString(art.Slug) {
		return "", fmt.Errorf("evolve.WriteSkill: invalid slug %q", art.Slug)
	}
	dir := filepath.Join(skillsDir, art.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("evolve.WriteSkill: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := atomicWriteFile(path, []byte(art.Body)); err != nil {
		return "", err
	}
	return path, nil
}

// AgentEligible reports whether a cluster meets the agent promotion
// bar (>= 3 members, average confidence >= 0.75).
func AgentEligible(c Cluster) bool {
	if len(c.Members) < AgentMinMembers {
		return false
	}
	return avgConfidence(c.Members) >= AgentMinConfidence
}

// RenderAgent builds an evolved/agents/<slug>.md body. The frontmatter
// carries name + description (REQUIRED — Claude Code will not load a
// subagent without them) plus model + tools, followed by the source-
// instinct list. Tools default to the read-only set since a learned
// agent should not gain write capability without operator review. The
// label + description are the same the skill resolved, so the agent and
// skill never diverge.
func RenderAgent(label, description string, c Cluster, now time.Time) SkillArtifact {
	ids := memberIDs(c)
	desc := description
	if desc == "" && len(c.Members) > 0 {
		desc = firstActionLine(c.Members[0].Body)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", label)
	fmt.Fprintf(&b, "description: %s\n", yamlOneLine(desc))
	b.WriteString("model: sonnet\n")
	b.WriteString("tools: Read, Grep, Glob\n")
	fmt.Fprintf(&b, "generated_by: bough-evolve@v0.9.1\n")
	fmt.Fprintf(&b, "generated_at: %s\n", now.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", label)
	fmt.Fprintf(&b, "Evolved from %d instincts (avg confidence: %.0f%%).\n\n", len(c.Members), avgConfidence(c.Members)*100)
	b.WriteString(sourceInstinctsBlock(c.Members))
	return SkillArtifact{Slug: label, Description: desc, Body: b.String(), Members: ids}
}

// WriteAgent persists an agent body to <agentsDir>/<slug>.md.
func WriteAgent(agentsDir string, art SkillArtifact) (string, error) {
	if !labelPattern.MatchString(art.Slug) {
		return "", fmt.Errorf("evolve.WriteAgent: invalid slug %q", art.Slug)
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return "", fmt.Errorf("evolve.WriteAgent: mkdir: %w", err)
	}
	path := filepath.Join(agentsDir, art.Slug+".md")
	if err := atomicWriteFile(path, []byte(art.Body)); err != nil {
		return "", err
	}
	return path, nil
}

// CommandEligible reports whether a single instinct should become a
// slash command (domain == "workflow", confidence >= 0.70).
func CommandEligible(in *homunculus.Instinct) bool {
	return strings.EqualFold(in.Domain, "workflow") && in.Confidence >= CommandMinConfidence
}

// RenderCommand builds an evolved/commands/<slug>.md body from one
// workflow instinct. The slug derives from the instinct id; the body
// carries the trigger + action so Claude Code can surface it as a
// slash command.
func RenderCommand(in *homunculus.Instinct, now time.Time) SkillArtifact {
	slug := in.ID
	if !labelPattern.MatchString(slug) {
		slug = Slugify(slug)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", slug)
	fmt.Fprintf(&b, "Evolved from instinct: %s\n", in.ID)
	if in.Path != "" {
		fmt.Fprintf(&b, "Source instinct: %s\n", in.Path)
	}
	fmt.Fprintf(&b, "Confidence: %.0f%%\n\n", in.Confidence*100)
	fmt.Fprintf(&b, "Trigger: %s\n\n", oneLine(in.Trigger))
	b.WriteString("## Action\n\n")
	fmt.Fprintf(&b, "%s\n", firstActionLine(in.Body))
	return SkillArtifact{Slug: slug, Body: b.String(), Members: []string{in.ID}}
}

// WriteCommand persists a command body to <commandsDir>/<slug>.md.
func WriteCommand(commandsDir string, art SkillArtifact) (string, error) {
	if !labelPattern.MatchString(art.Slug) {
		return "", fmt.Errorf("evolve.WriteCommand: invalid slug %q", art.Slug)
	}
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		return "", fmt.Errorf("evolve.WriteCommand: mkdir: %w", err)
	}
	path := filepath.Join(commandsDir, art.Slug+".md")
	if err := atomicWriteFile(path, []byte(art.Body)); err != nil {
		return "", err
	}
	return path, nil
}

// --- helpers ---

func memberIDs(c Cluster) []string {
	ids := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

// sourceInstinctsBlock renders a "## Source instincts" provenance block:
// one "- `<id>` — <path>" line per member, sorted by id so a re-emit is
// byte-stable (and matches the evolved_from frontmatter order). The path
// is the member's absolute on-disk location (ReadInstinctFile enforces
// filename == id), so a reader of a generated artifact — even through a
// worktree symlink, hops away from the homunculus store — can open the
// originating instinct. A member with no Path (an in-memory / test
// instinct) degrades to "- `<id>`".
func sourceInstinctsBlock(members []*homunculus.Instinct) string {
	sorted := append([]*homunculus.Instinct(nil), members...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var b strings.Builder
	b.WriteString("## Source instincts\n\n")
	for _, m := range sorted {
		if m.Path != "" {
			fmt.Fprintf(&b, "- `%s` — %s\n", m.ID, m.Path)
		} else {
			fmt.Fprintf(&b, "- `%s`\n", m.ID)
		}
	}
	return b.String()
}

func avgConfidence(members []*homunculus.Instinct) float64 {
	if len(members) == 0 {
		return 0
	}
	sum := 0.0
	for _, m := range members {
		sum += m.Confidence
	}
	return sum / float64(len(members))
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("evolve: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("evolve: rename %s: %w", path, err)
	}
	return nil
}

func yamlOneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	// quote if it contains a colon followed by space (= YAML flow
	// ambiguity) or starts with a character that would be misparsed
	first := ""
	if len(s) > 0 {
		first = s[:1]
	}
	if strings.Contains(s, ": ") || strings.ContainsAny(first, "[]{}#&*!|>'\"%@`") {
		return `"` + strings.ReplaceAll(s, `"`, `'`) + `"`
	}
	return s
}
