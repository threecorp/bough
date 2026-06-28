package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolver_EmbeddedFallback(t *testing.T) {
	r := Resolver{} // no override roots
	got, err := r.Get(TemplateObserver)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != "embedded" {
		t.Errorf("Source = %q, want embedded", got.Source)
	}
	if !strings.Contains(got.Body, "bough's observer daemon") {
		t.Errorf("embedded observer.md missing canonical phrase")
	}
	if got.Version == "" || len(got.Version) != 12 {
		t.Errorf("Version = %q, want 12 hex chars", got.Version)
	}
}

func TestResolver_RepoLocalOverride(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "prompts", "observer.md")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "REPO-LOCAL OVERRIDE for observer.md\n"
	if err := os.WriteFile(override, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := Resolver{RepoLocalRoot: dir}
	got, err := r.Get(TemplateObserver)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != "repo-local" {
		t.Errorf("Source = %q, want repo-local", got.Source)
	}
	if got.Body != body {
		t.Errorf("Body mismatch:\nGOT:\n%s\nWANT:\n%s", got.Body, body)
	}
	if got.Path != override {
		t.Errorf("Path = %q, want %q", got.Path, override)
	}
}

func TestResolver_UserConfigOverride(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "prompts", "observer.md")
	_ = os.MkdirAll(filepath.Dir(override), 0o755)
	_ = os.WriteFile(override, []byte("user-config wins"), 0o644)

	r := Resolver{UserConfigRoot: dir}
	got, err := r.Get(TemplateObserver)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != "user-config" {
		t.Errorf("Source = %q, want user-config", got.Source)
	}
}

func TestResolver_RepoBeatsUserConfig(t *testing.T) {
	userDir := t.TempDir()
	repoDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(userDir, "prompts"), 0o755)
	_ = os.MkdirAll(filepath.Join(repoDir, "prompts"), 0o755)
	_ = os.WriteFile(filepath.Join(userDir, "prompts", "observer.md"), []byte("user"), 0o644)
	_ = os.WriteFile(filepath.Join(repoDir, "prompts", "observer.md"), []byte("repo"), 0o644)

	r := Resolver{UserConfigRoot: userDir, RepoLocalRoot: repoDir}
	got, err := r.Get(TemplateObserver)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != "repo-local" {
		t.Errorf("Source = %q, want repo-local (repo > user)", got.Source)
	}
}

func TestResolver_RejectsUnknownTemplate(t *testing.T) {
	r := Resolver{}
	_, err := r.Get("nope")
	if err == nil {
		t.Errorf("expected error for unknown template")
	}
}

func TestResolver_AllKnownTemplatesEmbeddedExist(t *testing.T) {
	r := Resolver{}
	for _, name := range []string{
		TemplateObserver, TemplateJudge, TemplateLabel,
		TemplateAgent, TemplateCommand,
	} {
		if _, err := r.Get(name); err != nil {
			t.Errorf("known template %q missing embedded default: %v", name, err)
		}
	}
}

func TestResolver_VersionVariesByBody(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "prompts", "observer.md"), []byte("v1"), 0o644)

	r := Resolver{RepoLocalRoot: dir}
	v1, _ := r.Get(TemplateObserver)

	_ = os.WriteFile(filepath.Join(dir, "prompts", "observer.md"), []byte("v2-different"), 0o644)
	v2, _ := r.Get(TemplateObserver)

	if v1.Version == v2.Version {
		t.Errorf("Version did not change when Body changed (%q vs %q)", v1.Body, v2.Body)
	}
}
