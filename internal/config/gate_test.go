package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGateEnabled_TriState pins the policy-gate default: an absent
// `gate:` block (nil Enabled) defaults ON, because the guard is
// reversible and zero-cost — while an explicit `enabled: false` turns it
// off. This is the one place the safety-on default is decided, so it is
// worth a direct test rather than inferring it from the observer path.
func TestGateEnabled_TriState(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "absent gate block defaults on",
			yaml: "enabled: true\n",
			want: true,
		},
		{
			name: "explicit enabled false turns it off",
			yaml: "enabled: true\ngate:\n  enabled: false\n",
			want: false,
		},
		{
			name: "explicit enabled true stays on",
			yaml: "enabled: true\ngate:\n  enabled: true\n",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ic InstinctConfig
			if err := yaml.Unmarshal([]byte(tc.yaml), &ic); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := ic.GateEnabled(); got != tc.want {
				t.Errorf("GateEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGateAllowIDsParse pins the YAML wiring for the exemption list so a
// rule-citing instinct can be allowlisted from .bough.yaml.
func TestGateAllowIDsParse(t *testing.T) {
	var ic InstinctConfig
	y := "gate:\n  enabled: true\n  allow_ids:\n    - never-force-push-rule\n    - never-merge-rule\n"
	if err := yaml.Unmarshal([]byte(y), &ic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ic.Gate.AllowIDs) != 2 || ic.Gate.AllowIDs[0] != "never-force-push-rule" {
		t.Errorf("AllowIDs = %v, want [never-force-push-rule never-merge-rule]", ic.Gate.AllowIDs)
	}
}
