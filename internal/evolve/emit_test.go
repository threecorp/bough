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
		"## Source instincts",
		"- `member-a`", // mkCluster members have no Path → degrade to id only
	} {
		if !strings.Contains(art.Body, want) {
			t.Errorf("SKILL.md missing %q:\n%s", want, art.Body)
		}
	}
}

func TestSourceInstinctsBlock_PathsDegradeAndSorted(t *testing.T) {
	// input order is b-then-a; output must be id-sorted (a before b),
	// path members render "- `id` — <path>", path-less members degrade.
	withPath := &homunculus.Instinct{ID: "member-b", Path: "/abs/instincts/member-b.md"}
	noPath := &homunculus.Instinct{ID: "member-a"} // Path == ""
	blk := sourceInstinctsBlock([]*homunculus.Instinct{withPath, noPath})

	if !strings.Contains(blk, "## Source instincts") {
		t.Errorf("missing heading:\n%s", blk)
	}
	if !strings.Contains(blk, "- `member-b` — /abs/instincts/member-b.md") {
		t.Errorf("path member should render its absolute path:\n%s", blk)
	}
	// degrade: id only, no em-dash for the path-less member
	if !strings.Contains(blk, "- `member-a`\n") || strings.Contains(blk, "member-a` —") {
		t.Errorf("path-less member must degrade to id only:\n%s", blk)
	}
	// determinism: sorted by id regardless of input order
	if strings.Index(blk, "member-a") > strings.Index(blk, "member-b") {
		t.Errorf("block not id-sorted:\n%s", blk)
	}
}

func TestWriteSkill_Atomic(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	art := SkillArtifact{Slug: "io-data-layer", Body: "---\nname: io-data-layer\n---\n# x\n"}
	path, err := WriteSkill(skillsDir, art)
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("SKILL.md not written: %v", err)
	}
	// no .tmp leftover
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover .tmp")
	}
}

func TestWriteSkill_RejectsBadSlug(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteSkill(dir, SkillArtifact{Slug: "Bad Slug"})
	if err == nil {
		t.Errorf("expected error for bad slug")
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
	art := RenderAgent("diagnostic-batching", "Apply when batching diagnostics", c, time.Now())
	for _, want := range []string{
		"name: diagnostic-batching",                    // REQUIRED for Claude Code to load the agent
		"description: Apply when batching diagnostics", // ditto
		"model: sonnet",
		"tools: Read, Grep, Glob",
		"avg confidence: 80%",
		"## Source instincts",
	} {
		if !strings.Contains(art.Body, want) {
			t.Errorf("agent missing %q:\n%s", want, art.Body)
		}
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
	// no Path → no "Source instinct:" line
	if strings.Contains(art.Body, "Source instinct:") {
		t.Errorf("command without Path should not emit a Source instinct line: %s", art.Body)
	}
	// with Path → emits the resolvable line
	in.Path = "/abs/instincts/github-issue-structure.md"
	art2 := RenderCommand(in, time.Now())
	if !strings.Contains(art2.Body, "Source instinct: /abs/instincts/github-issue-structure.md") {
		t.Errorf("command with Path missing Source instinct line: %s", art2.Body)
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
