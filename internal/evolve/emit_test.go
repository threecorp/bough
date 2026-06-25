package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func mkCluster(confs ...float64) Cluster {
	members := make([]*homunculus.Instinct, len(confs))
	for i, c := range confs {
		members[i] = &homunculus.Instinct{
			ID:         "member-" + string(rune('a'+i)),
			Trigger:    "when x",
			Confidence: c,
			Domain:     "workflow",
			Body:       "## Action\nDo thing " + string(rune('a'+i)) + ".",
		}
	}
	return Cluster{Members: members}
}

func TestRenderSkill_Frontmatter(t *testing.T) {
	c := mkCluster(0.8, 0.7, 0.9)
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	art := RenderSkill("io-data-layer", "Apply when wrapping I/O", c, DefaultThresholds(), now)
	for _, want := range []string{
		"name: io-data-layer",
		"description: Apply when wrapping I/O",
		"evolved_from:",
		"- member-a",
		"generated_by: bough-evolve@v0.9.1",
		"Evolved from 3 instincts.",
		"## Actions",
	} {
		if !strings.Contains(art.Body, want) {
			t.Errorf("SKILL.md missing %q:\n%s", want, art.Body)
		}
	}
}

func TestWriteSkill_AtomicAndSymlink(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	symlinkDir := filepath.Join(dir, "claude-skills")
	art := SkillArtifact{Slug: "io-data-layer", Body: "---\nname: io-data-layer\n---\n# x\n"}
	path, err := WriteSkill(skillsDir, symlinkDir, art)
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("SKILL.md not written: %v", err)
	}
	// symlink resolves to the skill dir
	link := filepath.Join(symlinkDir, "io-data-layer")
	resolved, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if resolved != filepath.Join(skillsDir, "io-data-layer") {
		t.Errorf("symlink points at %q", resolved)
	}
	// no .tmp leftover
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover .tmp")
	}
}

func TestWriteSkill_RejectsBadSlug(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteSkill(dir, "", SkillArtifact{Slug: "Bad Slug"})
	if err == nil {
		t.Errorf("expected error for bad slug")
	}
}

func TestWriteSkill_RefusesToClobberRealFile(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	symlinkDir := filepath.Join(dir, "claude-skills")
	// place a real file where the symlink would go
	_ = os.MkdirAll(symlinkDir, 0o755)
	realFile := filepath.Join(symlinkDir, "io-data-layer")
	_ = os.WriteFile(realFile, []byte("operator's hand-written skill"), 0o644)

	art := SkillArtifact{Slug: "io-data-layer", Body: "x"}
	_, err := WriteSkill(skillsDir, symlinkDir, art)
	if err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("expected refusal to clobber real file, got %v", err)
	}
}

func TestAgentEligible(t *testing.T) {
	if !AgentEligible(mkCluster(0.8, 0.75, 0.9)) {
		t.Errorf("3 members avg>=0.75 should be agent-eligible")
	}
	if AgentEligible(mkCluster(0.8, 0.7)) {
		t.Errorf("2 members should not be agent-eligible")
	}
	if AgentEligible(mkCluster(0.5, 0.6, 0.7)) {
		t.Errorf("avg confidence < 0.75 should not be agent-eligible")
	}
}

func TestRenderAgent(t *testing.T) {
	c := mkCluster(0.8, 0.8, 0.8)
	art := RenderAgent("diagnostic-batching", c, time.Now())
	if !strings.Contains(art.Body, "model: sonnet") {
		t.Errorf("agent missing model line")
	}
	if !strings.Contains(art.Body, "tools: Read, Grep, Glob") {
		t.Errorf("agent missing read-only tools line")
	}
	if !strings.Contains(art.Body, "avg confidence: 80%") {
		t.Errorf("agent missing avg confidence: %s", art.Body)
	}
}

func TestCommandEligible(t *testing.T) {
	cases := []struct {
		domain string
		conf   float64
		want   bool
	}{
		{"workflow", 0.7, true},
		{"workflow", 0.69, false},
		{"testing", 0.9, false},
		{"WORKFLOW", 0.8, true}, // case-insensitive
	}
	for _, tc := range cases {
		in := &homunculus.Instinct{Domain: tc.domain, Confidence: tc.conf}
		if got := CommandEligible(in); got != tc.want {
			t.Errorf("CommandEligible(%s, %.2f) = %t, want %t", tc.domain, tc.conf, got, tc.want)
		}
	}
}

func TestRenderCommand(t *testing.T) {
	in := &homunculus.Instinct{
		ID:         "github-issue-structure",
		Trigger:    "when creating github issues",
		Confidence: 0.7,
		Domain:     "workflow",
		Body:       "## Action\nStructure issues with Symptom / Root Cause / Decision.",
	}
	art := RenderCommand(in, time.Now())
	if art.Slug != "github-issue-structure" {
		t.Errorf("slug = %q", art.Slug)
	}
	if !strings.Contains(art.Body, "Evolved from instinct: github-issue-structure") {
		t.Errorf("command missing provenance: %s", art.Body)
	}
	if !strings.Contains(art.Body, "Structure issues with Symptom") {
		t.Errorf("command missing action: %s", art.Body)
	}
}

func TestWriteCommand(t *testing.T) {
	dir := t.TempDir()
	art := SkillArtifact{Slug: "my-command", Body: "# my-command\n"}
	path, err := WriteCommand(dir, art)
	if err != nil {
		t.Fatalf("WriteCommand: %v", err)
	}
	if filepath.Base(path) != "my-command.md" {
		t.Errorf("path = %q", path)
	}
}
