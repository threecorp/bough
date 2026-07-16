package cli

import (
	"strings"
	"testing"
)

// TestNewPluginsCmd_ShortMatchesWiredSubcommands is the regression
// guard for the merged-PR-#23 review finding: PR #23 (commit
// 86ebdef, "Ν-1.8 signing scaffolding") added a `bough plugins
// verify` subcommand and changed newPluginsCmd's Short text to
// "List and verify bough plugin binaries", but the v0.9.0 reset
// (commit eee8a3d) deleted internal/cli/plugin_verify.go and the
// AddCommand(..., newPluginsVerifyCmd()) call while leaving the
// Short string untouched — so `bough plugins --help` kept
// advertising a "verify" capability that no longer exists, even
// though docs/SIGNING.md was correctly updated to say "There is no
// `bough plugins verify` subcommand today". This test fails if a
// future change reintroduces the same drift: it asserts the Short
// text only claims "verify" when a "verify" subcommand is actually
// wired.
func TestNewPluginsCmd_ShortMatchesWiredSubcommands(t *testing.T) {
	cmd := newPluginsCmd()

	hasVerify := false
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
		if sub.Name() == "verify" {
			hasVerify = true
		}
	}

	claimsVerify := strings.Contains(strings.ToLower(cmd.Short), "verify")

	if claimsVerify && !hasVerify {
		t.Errorf("newPluginsCmd Short claims verify support (%q) but no `verify` subcommand is wired (have: %v)", cmd.Short, names)
	}
	if hasVerify && !claimsVerify {
		t.Errorf("newPluginsCmd wires a `verify` subcommand but Short text does not mention it: %q", cmd.Short)
	}
}
