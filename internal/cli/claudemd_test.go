package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func TestCollectClaudemdProposals(t *testing.T) {
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -40) // older than the 30-day remove gate
	recent := now.AddDate(0, 0, -5)

	ins := []*homunculus.Instinct{
		{ID: "top-band", Confidence: 0.85},                          // ADD
		{ID: "add-floor", Confidence: 0.80},                         // ADD (gate is >=0.80)
		{ID: "middling", Confidence: 0.70},                          // neither
		{ID: "decayed-old", Confidence: 0.30, FirstSeen: old},       // REMOVE
		{ID: "decayed-recent", Confidence: 0.30, FirstSeen: recent}, // neither (not aged)
		{ID: "decayed-undated", Confidence: 0.30},                   // neither (no first_seen)
	}
	p := collectClaudemdProposals(ins, now)

	gotAdd := ids(p.add)
	wantAdd := []string{"top-band", "add-floor"} // sorted by confidence desc
	if strings.Join(gotAdd, ",") != strings.Join(wantAdd, ",") {
		t.Errorf("add = %v, want %v", gotAdd, wantAdd)
	}
	gotRemove := ids(p.remove)
	if strings.Join(gotRemove, ",") != "decayed-old" {
		t.Errorf("remove = %v, want [decayed-old]", gotRemove)
	}
}

func TestFirstActionLine(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{"## Action\nRun the migration after schema changes\n", "Run the migration after schema changes"},
		{"# Title\n\nJust prose here\n", "Just prose here"},
		{"## Action\n\n  Indented action line  ", "Indented action line"},
		{"", ""},
	}
	for _, c := range cases {
		if got := firstActionLine(c.body); got != c.want {
			t.Errorf("firstActionLine(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}

func TestRenderClaudemdProposals(t *testing.T) {
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	p := claudemdProposals{
		add:    []*homunculus.Instinct{{ID: "a1", Confidence: 0.85, Domain: "workflow", Trigger: "when X", Body: "## Action\ndo X"}},
		remove: []*homunculus.Instinct{{ID: "r1", Confidence: 0.30, FirstSeen: now.AddDate(0, 0, -40)}},
	}
	doc := renderClaudemdProposals(p, now)
	for _, want := range []string{
		"# CLAUDE.md Evolution Proposals",
		"## Proposed Additions",
		"### ADD — a1",
		"- rule: do X",
		"## Proposed Removals",
		"### REMOVE — r1",
		"bough never edits CLAUDE.md automatically",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered doc missing %q\n---\n%s", want, doc)
		}
	}
}

// TestRunEvolveClaudeMD_PreviewAndWrite exercises the command body end to
// end against a temp homunculus: preview prints to stdout without writing,
// --write saves the proposals file under <root>/.claude/.
func TestRunEvolveClaudeMD_PreviewAndWrite(t *testing.T) {
	t.Setenv("BOUGH_HOMUNCULUS_DIR", t.TempDir())
	repo := t.TempDir()
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)

	ident, err := homunculus.DetectIdentity(repo)
	if err != nil {
		t.Fatalf("DetectIdentity: %v", err)
	}
	layout := homunculus.NewLayout()
	writeProjInstinct(t, layout, ident.ID, "high-conf-rule", 0.85)

	proposalsPath := filepath.Join(repo, ".claude", claudemdProposalsRelName)

	// preview: prints, writes nothing
	var preview bytes.Buffer
	if err := runEvolveClaudeMD(&preview, repo, "", false, now); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.String(), "### ADD — high-conf-rule") {
		t.Errorf("preview missing ADD proposal:\n%s", preview.String())
	}
	if _, err := os.Stat(proposalsPath); !os.IsNotExist(err) {
		t.Errorf("preview wrote a file (stat err=%v)", err)
	}

	// --write: saves the proposals file
	var written bytes.Buffer
	if err := runEvolveClaudeMD(&written, repo, "", true, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := os.ReadFile(proposalsPath)
	if err != nil {
		t.Fatalf("proposals file not written: %v", err)
	}
	if !strings.Contains(string(body), "### ADD — high-conf-rule") {
		t.Errorf("proposals file missing ADD:\n%s", body)
	}
}

func ids(in []*homunculus.Instinct) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = x.ID
	}
	return out
}
