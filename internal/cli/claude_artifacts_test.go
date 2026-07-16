package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bough "github.com/ikeikeikeike/bough"
)

// runArtifact drives one `bough claude <kind> <verb>` through cobra rather than
// calling the RunE body, so the flag defaults and wiring under test are the ones
// an operator actually gets.
func runArtifact(t *testing.T, k artifactKind, args ...string) string {
	t.Helper()
	cmd := newArtifactCmd(k)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%s %v: %v\noutput: %s", k.name, args, err, out.String())
	}
	return out.String()
}

// TestArtifactInstallDeploysEmbedded is the normal path: every name bough
// embeds must land at the scope's .claude/<subdir>. Asserting against the
// embedded FS rather than a hardcoded list means adding a skill cannot leave
// this test passing on a stale expectation.
func TestArtifactInstallDeploysEmbedded(t *testing.T) {
	for _, k := range []artifactKind{skillKind, commandKind} {
		t.Run(k.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			out := runArtifact(t, k, "install")

			names, err := embeddedArtifactNames(k)
			if err != nil {
				t.Fatal(err)
			}
			if len(names) == 0 {
				t.Fatalf("bough embeds no %ss; the install path cannot be meaningfully tested", k.entryIs)
			}
			for _, n := range names {
				if _, err := os.Stat(filepath.Join(".claude", k.subdir, n)); err != nil {
					t.Errorf("%s not installed: %v", n, err)
				}
				if !strings.Contains(out, n) {
					t.Errorf("install output does not name %q; the operator cannot see what landed", n)
				}
			}
		})
	}
}

// TestArtifactInstallIsIdempotent covers the double-install path: re-running
// must overwrite in place, not error and not duplicate, because that is how an
// operator picks up a new bough version's content.
func TestArtifactInstallIsIdempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	dst := filepath.Join(".claude", "skills")

	runArtifact(t, skillKind, "install")
	before, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a stale copy from an older bough: the re-install must correct it.
	stale := filepath.Join(dst, "using-bough", "SKILL.md")
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("expected the shipped skill at %s: %v", stale, err)
	}
	if err := os.WriteFile(stale, []byte("stale content from an older version"), 0o644); err != nil {
		t.Fatal(err)
	}

	runArtifact(t, skillKind, "install")

	after, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("entry count changed across re-install: %d -> %d", len(before), len(after))
	}
	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	want, err := bough.Assets.ReadFile("skills/using-bough/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("re-install did not overwrite stale content; an upgrade would leave the old skill in place")
	}
}

// TestArtifactUninstallPreservesHandAuthored is the contract that makes
// uninstall safe to run: it removes only what bough ships. An operator's own
// skill sitting in the same directory is not bough's to delete.
func TestArtifactUninstallPreservesHandAuthored(t *testing.T) {
	t.Chdir(t.TempDir())
	dst := filepath.Join(".claude", "skills")

	runArtifact(t, skillKind, "install")

	mine := filepath.Join(dst, "my-own")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	runArtifact(t, skillKind, "uninstall")

	if _, err := os.Stat(mine); err != nil {
		t.Errorf("uninstall deleted a hand-authored skill: %v", err)
	}
	names, err := embeddedArtifactNames(skillKind)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dst, n)); err == nil {
			t.Errorf("bough's own %q survived uninstall", n)
		}
	}
}

// TestArtifactUninstallWithoutInstall is the not-installed path: uninstall at a
// scope where nothing landed must be a quiet no-op, not an error — an operator
// clearing several scopes should not have to know which ones were used.
func TestArtifactUninstallWithoutInstall(t *testing.T) {
	t.Chdir(t.TempDir())

	out := runArtifact(t, skillKind, "uninstall")

	if !strings.Contains(out, "removed 0") {
		t.Errorf("expected a 0-removed report, got: %s", out)
	}
}

// TestArtifactDirRejectsUnknownScope is the bad-input path. A typo'd --scope
// must fail loudly: the alternative is silently resolving to some default
// directory and writing files where the operator never looked.
func TestArtifactDirRejectsUnknownScope(t *testing.T) {
	if _, err := artifactDir(skillKind, "global"); err == nil {
		t.Fatal("expected an unknown scope to be rejected")
	} else if !strings.Contains(err.Error(), "global") {
		t.Errorf("error should name the rejected scope, got: %v", err)
	}

	// The two valid scopes must resolve to their documented directories.
	got, err := artifactDir(skillKind, HookScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".claude", "skills"); got != want {
		t.Errorf("user scope = %q, want %q", got, want)
	}
}

// TestEmbeddedArtifactNamesFiltersByShape guards the one place skills and
// commands genuinely differ: a skill is a directory, a command is a .md file.
// Get the filter backwards and install silently ships nothing.
func TestEmbeddedArtifactNamesFiltersByShape(t *testing.T) {
	skills, err := embeddedArtifactNames(skillKind)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range skills {
		if strings.HasSuffix(n, ".md") {
			t.Errorf("skill %q looks like a file; skills are directories", n)
		}
	}

	commands, err := embeddedArtifactNames(commandKind)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range commands {
		if !strings.HasSuffix(n, ".md") {
			t.Errorf("command %q is not a .md file", n)
		}
	}
	if len(skills) == 0 || len(commands) == 0 {
		t.Fatalf("expected both kinds to be embedded, got %d skills / %d commands", len(skills), len(commands))
	}
}

// TestRenderArtifactList checks the report an operator reads to decide whether
// to install: present entries must be marked installed and absent ones not.
func TestRenderArtifactList(t *testing.T) {
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dst, "here"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := renderArtifactList(&out, skillKind, dst, []string{"here", "absent"}); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{dst, "here", "absent"} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q:\n%s", want, got)
		}
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 { // header + one line per name
		t.Fatalf("expected a header and 2 entries, got:\n%s", got)
	}
	if !strings.Contains(lines[1], "✓ installed") {
		t.Errorf("present entry not marked installed: %q", lines[1])
	}
	if !strings.Contains(lines[2], "not installed") {
		t.Errorf("absent entry not marked missing: %q", lines[2])
	}
}
