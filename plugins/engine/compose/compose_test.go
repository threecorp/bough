//go:build darwin || linux

package compose

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProvider_PortRangeDefault(t *testing.T) {
	p := New()
	ranges, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	mainRange, ok := ranges["main"]
	if !ok {
		t.Fatalf("PortRangeDefault did not declare role 'main' (got %v)", ranges)
	}
	if mainRange.Low != defaultPortLow || mainRange.High != defaultPortHigh {
		t.Errorf("defaults: got [%d, %d], want [%d, %d]", mainRange.Low, mainRange.High, defaultPortLow, defaultPortHigh)
	}
}

func TestProvider_PortRangeDefault_overrides(t *testing.T) {
	p := &Provider{PortLow: 60000, PortHigh: 61000}
	ranges, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	mainRange := ranges["main"]
	if mainRange.Low != 60000 || mainRange.High != 61000 {
		t.Errorf("override: got [%d, %d], want [60000, 61000]", mainRange.Low, mainRange.High)
	}
}

func TestComposeProjectName(t *testing.T) {
	cases := []struct {
		name         string
		worktreeName string
		file         string
		want         string
	}{
		{
			name:         "simple lowercase inputs",
			worktreeName: "myworktree",
			file:         "compose.yml",
			want:         "bough-myworktree-compose-yml",
		},
		{
			name:         "mixed case worktree name and nested path",
			worktreeName: "F-Feature",
			file:         "demo-api/compose.yml",
			want:         "bough-f-feature-demo-api-compose-yml",
		},
		{
			name:         "underscores and dots collapse to single dashes",
			worktreeName: "F_Feature.Name",
			file:         "svc/docker-compose.dev.yml",
			want:         "bough-f-feature-name-svc-docker-compose-dev-yml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := composeProjectName(tc.worktreeName, tc.file)
			if got != tc.want {
				t.Errorf("composeProjectName(%q, %q) = %q, want %q", tc.worktreeName, tc.file, got, tc.want)
			}
			// Compose project names must match [a-z0-9][a-z0-9_-]*.
			if got == "" || !isLowerAlnum(rune(got[0])) {
				t.Errorf("composeProjectName(%q, %q) = %q, must start with [a-z0-9]", tc.worktreeName, tc.file, got)
			}
			for _, r := range got {
				if !isLowerAlnum(r) && r != '-' && r != '_' {
					t.Errorf("composeProjectName(%q, %q) = %q, contains invalid rune %q", tc.worktreeName, tc.file, got, r)
				}
			}
		})
	}
}

func isLowerAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// TestComposeProjectName_Deterministic guards the property Up/Down
// both rely on: the SAME (worktreeName, file) pair must always derive
// the SAME project name, so a Down call can locate what Up started
// without any extra state beyond the sidecar file's own File field.
func TestComposeProjectName_Deterministic(t *testing.T) {
	a := composeProjectName("F-Feature", "demo-api/compose.yml")
	b := composeProjectName("F-Feature", "demo-api/compose.yml")
	if a != b {
		t.Errorf("composeProjectName is not deterministic: %q != %q", a, b)
	}
}

// TestComposeProjectName_DifferentWorktreesDiffer guards the core
// worktree-isolation claim: two worktrees referencing the textually
// identical compose file must never derive the same project name,
// or docker compose's own up-or-reuse would silently hand worktree B
// a container actually owned by worktree A.
func TestComposeProjectName_DifferentWorktreesDiffer(t *testing.T) {
	a := composeProjectName("F-WorktreeA", "demo-api/compose.yml")
	b := composeProjectName("F-WorktreeB", "demo-api/compose.yml")
	if a == b {
		t.Errorf("two different worktrees derived the same project name %q — isolation is broken", a)
	}
}

func TestRenderOverride(t *testing.T) {
	out, err := renderOverride(overrideSpec{
		Service:    "redis",
		TargetPort: 6379,
		HostPort:   56123,
	})
	if err != nil {
		t.Fatalf("renderOverride: %v", err)
	}

	var doc struct {
		Services map[string]struct {
			ContainerName string `yaml:"container_name"`
			Ports         []struct {
				Target    int    `yaml:"target"`
				Published string `yaml:"published"`
				Protocol  string `yaml:"protocol"`
			} `yaml:"ports"`
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("generated override is not valid YAML: %v\n%s", err, out)
	}

	svc, ok := doc.Services["redis"]
	if !ok {
		t.Fatalf("override has no 'redis' service:\n%s", out)
	}
	if want := "bough-compose-56123"; svc.ContainerName != want {
		t.Errorf("container_name = %q, want %q", svc.ContainerName, want)
	}
	if len(svc.Ports) != 1 {
		t.Fatalf("ports = %v, want exactly 1 entry", svc.Ports)
	}
	if svc.Ports[0].Target != 6379 {
		t.Errorf("ports[0].target = %d, want 6379", svc.Ports[0].Target)
	}
	if svc.Ports[0].Published != "56123" {
		t.Errorf("ports[0].published = %q, want %q", svc.Ports[0].Published, "56123")
	}
	if svc.Ports[0].Protocol != "tcp" {
		t.Errorf("ports[0].protocol = %q, want %q", svc.Ports[0].Protocol, "tcp")
	}
	wantLabels := map[string]string{
		"com.bough.managed":         "true",
		"com.bough.engine":          "compose",
		"com.bough.compose-service": "redis",
		"com.bough.host-port":       "56123",
	}
	for k, want := range wantLabels {
		if got := svc.Labels[k]; got != want {
			t.Errorf("labels[%q] = %q, want %q", k, got, want)
		}
	}
}

// TestRenderOverride_DifferentServicesDoNotCollide is a light
// regression guard: the override must key on the caller's Service
// name, not a hardcoded string, since a compose file's target service
// name is operator-chosen.
func TestRenderOverride_DifferentServicesDoNotCollide(t *testing.T) {
	out, err := renderOverride(overrideSpec{Service: "cache", TargetPort: 11211, HostPort: 57000})
	if err != nil {
		t.Fatalf("renderOverride: %v", err)
	}
	if !strings.Contains(string(out), "cache:") {
		t.Errorf("override does not key on the given service name 'cache':\n%s", out)
	}
}
