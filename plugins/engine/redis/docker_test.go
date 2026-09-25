//go:build darwin || linux

package redis

import "testing"

// TestDockerImageTable pins this plugin's version→image mapping, which
// no test covered while it lived in pickDockerImage: an accidental drop
// of the -alpine suffix or a typo in the repository name would have
// stayed green until someone ran a real create.
func TestDockerImageTable(t *testing.T) {
	cases := []struct {
		name    string
		extras  map[string]string
		want    string
		wantErr bool
	}{
		{"version fills the template", map[string]string{"version": "8"}, "redis:8-alpine", false},
		{"patch level is accepted", map[string]string{"version": "7.4.1"}, "redis:7.4.1-alpine", false},
		{"docker.image wins", map[string]string{"docker.image": "redis:7", "version": "8"}, "redis:7", false},
		{"nil extras falls back to the default", nil, "redis:7-alpine", false},
		{"a non-numeric tag needs docker.image", map[string]string{"version": "7-bookworm"}, "", true},
	}
	for _, c := range cases {
		got, err := dockerImage.Resolve(c.extras)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("%s: Resolve = %q, want an error", c.name, got)
		case !c.wantErr && err != nil:
			t.Errorf("%s: Resolve returned %v, want %q", c.name, err, c.want)
		case !c.wantErr && got != c.want:
			t.Errorf("%s: Resolve = %q, want %q", c.name, got, c.want)
		}
	}
}
