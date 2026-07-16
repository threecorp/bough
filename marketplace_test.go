package bough

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The repo publishes three Claude Code plugins from one tree, and every one of
// them is a set of paths a human typed into .claude-plugin/marketplace.json.
// Nothing at build time reads that file, so a renamed directory or a typo'd
// source ships a marketplace whose `/plugin install` fails for users while the
// Go build stays green. These tests are the only thing standing between that
// edit and a broken install, so they check the packaging claims the manifest
// makes rather than re-testing the JSON round-trip.

type marketplace struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name        string `json:"name"`
		Source      string `json:"source"`
		Description string `json:"description"`
	} `json:"plugins"`
}

func readMarketplace(t *testing.T) marketplace {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("read marketplace.json: %v", err)
	}
	var mp marketplace
	if err := json.Unmarshal(data, &mp); err != nil {
		t.Fatalf("parse marketplace.json: %v", err)
	}
	return mp
}

// TestMarketplaceVariants pins the three-plugin split itself. The names are the
// user-facing contract (`/plugin install bough-all@bough`), so a rename here is
// a breaking change that should fail loudly rather than silently orphan anyone
// who already installed the old name.
func TestMarketplaceVariants(t *testing.T) {
	mp := readMarketplace(t)

	want := map[string]bool{"bough": false, "bough-hooks": false, "bough-all": false}
	for _, p := range mp.Plugins {
		seen, known := want[p.Name]
		if !known {
			t.Errorf("plugin %q is not one of the three published variants; add it here on purpose or drop it", p.Name)
			continue
		}
		if seen {
			t.Errorf("plugin %q declared twice", p.Name)
		}
		want[p.Name] = true

		if p.Description == "" {
			t.Errorf("plugin %q has no description; it is the only text a user sees when choosing a variant", p.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("plugin %q missing from marketplace.json", name)
		}
	}
}

// TestMarketplaceSourcesResolve walks each declared source the way Claude Code
// does: the directory must exist and carry a .claude-plugin/plugin.json whose
// name matches the marketplace entry. A mismatch installs a plugin under a name
// nobody advertised.
func TestMarketplaceSourcesResolve(t *testing.T) {
	for _, p := range readMarketplace(t).Plugins {
		t.Run(p.Name, func(t *testing.T) {
			if _, err := os.Stat(p.Source); err != nil {
				t.Fatalf("source %q does not resolve: %v", p.Source, err)
			}
			data, err := os.ReadFile(filepath.Join(p.Source, ".claude-plugin", "plugin.json"))
			if err != nil {
				t.Fatalf("source %q has no plugin.json: %v", p.Source, err)
			}
			var manifest struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatalf("parse %s/plugin.json: %v", p.Source, err)
			}
			if manifest.Name != p.Name {
				t.Fatalf("plugin.json name %q != marketplace name %q", manifest.Name, p.Name)
			}
		})
	}
}

// TestVariantContents pins what each variant is FOR, which is the whole reason
// there are three. The split only protects anyone if `bough` stays hook-free
// (safe at any scope because it cannot fire on its own) and bough-hooks stays
// command-free; a stray directory silently collapses that distinction.
func TestVariantContents(t *testing.T) {
	cases := []struct {
		plugin  string
		dir     string
		want    []string
		wantNot []string
	}{
		{plugin: "bough", dir: ".", want: []string{"commands", "skills"}, wantNot: []string{"hooks"}},
		{plugin: "bough-hooks", dir: "claude-plugins/bough-hooks", want: []string{"hooks"}, wantNot: []string{"commands", "skills"}},
		{plugin: "bough-all", dir: "claude-plugins/bough-all", want: []string{"commands", "skills", "hooks"}},
	}
	for _, tc := range cases {
		t.Run(tc.plugin, func(t *testing.T) {
			for _, d := range tc.want {
				if _, err := os.Stat(filepath.Join(tc.dir, d)); err != nil {
					t.Errorf("%s must ship %s/: %v", tc.plugin, d, err)
				}
			}
			for _, d := range tc.wantNot {
				if _, err := os.Stat(filepath.Join(tc.dir, d)); err == nil {
					t.Errorf("%s must NOT ship %s/ — that is what a separate variant is for", tc.plugin, d)
				}
			}
		})
	}
}

// TestBoughAllSharesRootTrees is the counterpart of the hooks-symlink guard in
// internal/hooks: bough-all's commands/ and skills/ must resolve to the same
// trees the root `bough` plugin and the //go:embed in assets.go publish. Copies
// would let the two variants ship different commands under one version.
func TestBoughAllSharesRootTrees(t *testing.T) {
	for _, tree := range []string{"commands", "skills"} {
		t.Run(tree, func(t *testing.T) {
			got, err := filepath.EvalSymlinks(filepath.Join("claude-plugins", "bough-all", tree))
			if err != nil {
				t.Fatalf("resolve bough-all/%s: %v", tree, err)
			}
			want, err := filepath.EvalSymlinks(tree)
			if err != nil {
				t.Fatalf("resolve root %s: %v", tree, err)
			}
			if got != want {
				t.Fatalf("bough-all/%s is a copy, not the shared tree\n got: %s\nwant: %s", tree, got, want)
			}
		})
	}
}
