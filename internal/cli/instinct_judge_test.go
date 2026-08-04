package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/prompts"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// TestJudgeBudgetRaisesBothCaps is the regression for a budget the
// operator set and the limiter ignored: only the per-session cap moved,
// so a value above the hourly default stopped short at a number nobody
// chose — and the run then told them to re-run with the very value that
// had just been overridden.
func TestJudgeBudgetRaisesBothCaps(t *testing.T) {
	_, prov, err := newGateReviewer("", 45, nil)
	if err != nil {
		t.Fatalf("build reviewer: %v", err)
	}
	if prov.Limiter.MaxCallsPerSession != 45 {
		t.Errorf("session cap = %d, want 45", prov.Limiter.MaxCallsPerSession)
	}
	if prov.Limiter.MaxCallsPerHour < 45 {
		t.Errorf("hourly cap = %d, want at least the budget (45) — otherwise the budget is silently clamped",
			prov.Limiter.MaxCallsPerHour)
	}
}

// TestSmallBudgetLeavesTheHourlyCapAlone: the hourly cap protects the
// operator's interactive session, so a budget below it must not lower it.
func TestSmallBudgetLeavesTheHourlyCapAlone(t *testing.T) {
	_, prov, err := newGateReviewer("", 6, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prov.Limiter.MaxCallsPerHour != claudecli.DefaultMaxCallsPerHour {
		t.Errorf("hourly cap = %d, want the default %d untouched",
			prov.Limiter.MaxCallsPerHour, claudecli.DefaultMaxCallsPerHour)
	}
}

// TestForbiddenCategoriesReachThePrompt: the judge can only weigh a
// category it was told about, so a project rule outside bough's defaults
// has to be renderable. Hardcoding the list capped the judge at whatever
// bough's author had thought of, and it cleared violations of everything
// else while reporting a clean review.
func TestForbiddenCategoriesReachThePrompt(t *testing.T) {
	tpl, err := prompts.Resolver{}.Get(prompts.TemplateInstinctGate)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderGatePrompt(tpl.Body, "when a spec is late", "move the items out of the sprint",
		[]string{"deferring agreed scope out of a sprint without asking"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "deferring agreed scope out of a sprint") {
		t.Errorf("a project's own category must appear in the prompt:\n%s", body)
	}
	if strings.Contains(body, "force-pushing") {
		t.Error("a configured list REPLACES the defaults — leaving both would put the real set in two places")
	}
}

// TestDefaultCategoriesRenderWhenNoneConfigured keeps the common case
// working, and pins that the defaults are the VCS-safety families.
func TestDefaultCategoriesRenderWhenNoneConfigured(t *testing.T) {
	tpl, err := prompts.Resolver{}.Get(prompts.TemplateInstinctGate)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderGatePrompt(tpl.Body, "t", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"without being asked", "force-pushing", "deleting a branch"} {
		if !strings.Contains(body, want) {
			t.Errorf("default category %q missing from the rendered prompt", want)
		}
	}
}

// TestConfiguredCategoriesAreReadFromTheSameFileAsTheGate: the
// deterministic layer and the judge configured from different places is
// how a checker ends up covering a subset of the governance it claims to
// enforce.
func TestConfiguredCategoriesAreReadFromTheSameFileAsTheGate(t *testing.T) {
	root := t.TempDir()
	yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: app\n" +
		"registry:\n  path: .bough-ports.json\n" +
		"instinct:\n  enabled: true\n  gate:\n    forbidden_actions:\n      - never defer agreed scope\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	got := gateForbiddenActions(&cobra.Command{}, root)
	if len(got) != 1 || got[0] != "never defer agreed scope" {
		t.Errorf("configured categories = %v, want the project's own", got)
	}
	// A project without the key must fall back to the defaults, never to
	// an empty list: a judge with no categories clears everything while
	// reporting a full review.
	if fallback := gateForbiddenActions(&cobra.Command{}, t.TempDir()); len(fallback) == 0 {
		t.Error("a project without the key must fall back to the defaults, not to nothing")
	}
}
