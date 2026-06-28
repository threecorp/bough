// Package prompts owns the LLM prompt templates bough hands to the
// Claude CLI subprocess (= internal/provider/claudecli). Every
// template ships embedded in the binary so a fresh `go install
// bough` works without any side-by-side config; operators that want
// to tune the prompt can override per-template via:
//
//  1. <repo>/.bough/prompts/<name>.md  — repo-local override
//  2. $XDG_CONFIG_HOME/bough/prompts/<name>.md  (default: ~/.config/bough/prompts/)
//  3. embedded default
//
// The first hit wins. The resolver also returns a PromptVersion key
// the judge cache (= v0.9.1 GATE 5) keys against, so override-vs-
// embedded calls never share a cache entry by accident.
package prompts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed defaults/*.md
var defaultsFS embed.FS

// Template names the bough host knows about. Adding a new template
// here keeps the lookup table in one place; the constants are also
// used to build the override search paths.
const (
	TemplateObserver = "observer"
	TemplateJudge    = "evolve_judge"
	TemplateLabel    = "evolve_label"
	TemplateAgent    = "evolve_agent"
	TemplateCommand  = "evolve_command"
)

// Template holds one resolved prompt body plus the metadata bough
// needs to thread the version through the audit log.
type Template struct {
	Name    string // canonical name (= one of the constants above)
	Source  string // "embedded" | "user-config" | "repo-local"
	Path    string // on-disk path when Source != "embedded"
	Body    string // resolved Markdown body
	Version string // sha256 first 12 hex of Body — pinned per call
}

// Resolver finds the right template body across the three layers.
// SearchRoots can be customised in tests so a fixture directory
// stands in for ~/.config and the repo root.
type Resolver struct {
	UserConfigRoot string // default: $XDG_CONFIG_HOME/bough or ~/.config/bough
	RepoLocalRoot  string // default: <cwd>/.bough
}

// NewResolver returns a Resolver wired with the canonical search
// roots derived from the host environment. UserConfigRoot honours
// $XDG_CONFIG_HOME when set; falls back to $HOME/.config.
// RepoLocalRoot is anchored at the current working directory.
func NewResolver() Resolver {
	return Resolver{
		UserConfigRoot: userConfigRoot(),
		RepoLocalRoot:  repoLocalRoot(),
	}
}

// Get returns the template named name. Returns an error only when
// name is not a known template constant; missing override files
// silently fall through to the embedded default.
func (r Resolver) Get(name string) (Template, error) {
	if !isKnownTemplate(name) {
		return Template{}, fmt.Errorf("prompts.Resolver.Get: unknown template %q", name)
	}
	// 1. repo-local
	if body, path, ok := readFile(filepath.Join(r.RepoLocalRoot, "prompts", name+".md")); ok {
		return mkTemplate(name, "repo-local", path, body), nil
	}
	// 2. user-config
	if body, path, ok := readFile(filepath.Join(r.UserConfigRoot, "prompts", name+".md")); ok {
		return mkTemplate(name, "user-config", path, body), nil
	}
	// 3. embedded default
	body, err := defaultsFS.ReadFile("defaults/" + name + ".md")
	if err != nil {
		return Template{}, fmt.Errorf("prompts.Resolver.Get: embedded %q missing: %w", name, err)
	}
	return mkTemplate(name, "embedded", "", string(body)), nil
}

func mkTemplate(name, source, path, body string) Template {
	return Template{
		Name:    name,
		Source:  source,
		Path:    path,
		Body:    body,
		Version: versionOf(body),
	}
}

func versionOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])[:12]
}

func readFile(path string) (string, string, bool) {
	if path == "" {
		return "", "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	return string(raw), path, true
}

func isKnownTemplate(name string) bool {
	switch name {
	case TemplateObserver, TemplateJudge, TemplateLabel,
		TemplateAgent, TemplateCommand:
		return true
	}
	return false
}

func userConfigRoot() string {
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" {
		return filepath.Join(v, "bough")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "bough")
	}
	return ""
}

func repoLocalRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(cwd, ".bough")
}
