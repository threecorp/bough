package claudecli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/prompts"
)

// fakeCLI writes a stand-in `claude` that behaves like the real one in the
// single respect this test is about: it refuses any argument that looks like
// an unknown flag, and it reads the prompt from stdin.
//
// FakeExec cannot cover this — it is handed the prompt as a Go string and
// never sees how the process would actually have been spawned. That is
// precisely how the defect below survived a green suite.
func fakeCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in CLI is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
# Reject anything flag-shaped that is not a flag we know, the way the real
# CLI's argument parser does.
for a in "$@"; do
  case "$a" in
    -p|--model|--max-turns|--output-format|--permission-mode|--allowedTools|--json-schema) ;;
    -*) printf "error: unknown option '%s'\n" "$a" >&2; exit 1 ;;
    *) ;;
  esac
done
prompt=$(cat)
if [ -z "$prompt" ]; then
  printf 'error: no prompt on stdin\n' >&2
  exit 1
fi
printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"ok\\":true}"}\n'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func providerWithBin(bin string) *Provider {
	p := NewProvider()
	p.Bin = bin
	p.Timeout = 10 * time.Second
	return p
}

// TestInvokePassesPromptOnStdin is the regression for the defect that made
// the generation gate's LLM layer inert in every real run.
//
// The prompt used to be the value of -p. The gate judge template opens with
// YAML frontmatter, so its first line is a literal `---`, and the CLI read
// that as an unknown option and exited 1. Every judge call failed; the gate
// fails open by design, so unjudged instincts were promoted while the run
// printed `unreviewed=N (cleared — fail-open)`. Nothing in the suite caught
// it, because FakeExec never spawns a process.
//
// Asserting on the frontmatter alone would fix the instance. The assertion
// is on the class: a prompt beginning with any flag-shaped text must still
// reach the model.
func TestInvokePassesPromptOnStdin(t *testing.T) {
	bin := fakeCLI(t)
	for _, tc := range []struct{ name, body string }{
		{"yaml_frontmatter", "---\nversion: 1\n---\nJudge this instinct.\n"},
		{"leading_long_flag", "--help me reason about this instinct\n"},
		{"leading_short_flag", "-p is not a flag here, it is prose\n"},
		{"ordinary_prose", "IMPORTANT: return JSON.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := providerWithBin(bin)
			out, _, err := p.GenerateRaw(context.Background(), tc.body)
			if err != nil {
				t.Fatalf("prompt starting %q was not delivered: %v", firstLine(tc.body), err)
			}
			if !strings.Contains(string(out), `"result"`) {
				t.Errorf("expected the result envelope, got %q", string(out))
			}
		})
	}
}

// TestInvokeKeepsPromptOutOfArgv pins the other half: the body must not be
// passed as an argument at all, so no future prompt content can be re-read
// as one.
func TestInvokeKeepsPromptOutOfArgv(t *testing.T) {
	p := NewProvider()
	var gotArgs []string
	p.FakeExec = func(_ context.Context, args, _ []string, _ io.Reader, _ string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{"type":"result","result":"{\"ok\":true}"}`), nil
	}
	if _, _, err := p.GenerateRaw(context.Background(), "SENTINEL-PROMPT-BODY"); err != nil {
		t.Fatal(err)
	}
	for _, a := range gotArgs {
		if strings.Contains(a, "SENTINEL-PROMPT-BODY") {
			t.Fatalf("the prompt body reached argv as %q", a)
		}
	}
}

// TestPromptTemplatesSurviveTheRealArgumentParser drives every template bough
// ships through the stand-in CLI. A template is added by editing a .md file,
// which does not look like a change to how the process is spawned — this is
// the check that connects the two, so the next template that opens with
// frontmatter fails here rather than in a user's fail-open gate.
func TestPromptTemplatesSurviveTheRealArgumentParser(t *testing.T) {
	bin := fakeCLI(t)
	res := prompts.Resolver{} // no override roots: exercise the embedded defaults
	names := []string{
		prompts.TemplateObserver,
		prompts.TemplateJudge,
		prompts.TemplateLabel,
		prompts.TemplateAgent,
		prompts.TemplateCommand,
		prompts.TemplateInstinctGate,
	}
	for _, name := range names {
		tpl, err := res.Get(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		t.Run(name, func(t *testing.T) {
			p := providerWithBin(bin)
			if _, _, err := p.GenerateRaw(context.Background(), tpl.Body); err != nil {
				t.Errorf("template %s cannot be delivered to the CLI: %v", name, err)
			}
		})
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
