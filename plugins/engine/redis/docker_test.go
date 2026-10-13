//go:build darwin || linux

package redis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"
	"github.com/ikeikeikeike/bough/pkg/dockerutil/dockertest"
	"github.com/ikeikeikeike/bough/plugins/engine/api"
)

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

func TestDockerUp_ImageMismatchRemedy(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			name := dockerContainerName(42001)
			datadir := filepath.Join(t.TempDir(), "engine data")
			dockertest.StartDaemon(t, name, "old:1", running)
			err := (dockerBackend{}).Up(context.Background(), &api.UpReq{
				Ports:   []api.PortSpec{{Role: "main", Port: 42001}},
				Datadir: datadir,
				Extras:  map[string]string{"docker.image": "wanted:2"},
			})
			if err == nil {
				t.Fatal("Up returned nil for an image mismatch")
			}
			var mismatch *dockerutil.ImageMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("Up error lost ImageMismatchError: %v", err)
			}
			for _, want := range []string{"docker rm -f " + name, "empty the data directory " + fmt.Sprintf("%q", datadir), "all data in that directory is lost", "on Linux its files belong to the container's user, so empty it as root", "keep the directory itself", "keeping the worktree", "WorktreeCreate hook", "`bough create` with the same worktree name", "bough remove", "discard the whole worktree"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("remedy missing %q: %v", want, err)
				}
			}
		})
	}
}
