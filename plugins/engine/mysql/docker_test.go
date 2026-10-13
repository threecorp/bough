//go:build darwin || linux

package mysql

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"
	"github.com/ikeikeikeike/bough/pkg/dockerutil/dockertest"
	"github.com/ikeikeikeike/bough/plugins/engine/api"
)

// TestBuildDockerEnv_ReservedKeysAreNotDuplicated is the regression
// guard for the wave-3 review finding: the extras passthrough loop
// excluded only "docker."-prefixed/"version"/"backend" keys, not
// "database" or "allow_empty_password" — both of which already have
// a hardcoded MYSQL_* entry. An extras key colliding with either used
// to append a second, conflicting entry for the same env name instead
// of being dropped.
func TestBuildDockerEnv_ReservedKeysAreNotDuplicated(t *testing.T) {
	got := buildDockerEnv("bough", map[string]string{
		"database":             "custom_name",
		"allow_empty_password": "no",
		"character_set_server": "utf8mb4",
	})

	for _, want := range []string{"MYSQL_DATABASE=bough", "MYSQL_ALLOW_EMPTY_PASSWORD=yes"} {
		if !slices.Contains(got, want) {
			t.Errorf("buildDockerEnv missing hardcoded default %q\nfull env:\n%s", want, strings.Join(got, "\n  "))
		}
	}
	for _, badPrefix := range []string{"MYSQL_DATABASE=custom_name", "MYSQL_ALLOW_EMPTY_PASSWORD=no"} {
		if slices.Contains(got, badPrefix) {
			t.Errorf("buildDockerEnv let a reserved extras key override the hardcoded default: %q leaked into env\nfull env:\n%s", badPrefix, strings.Join(got, "\n  "))
		}
	}
	if !slices.Contains(got, "MYSQL_CHARACTER_SET_SERVER=utf8mb4") {
		t.Errorf("buildDockerEnv dropped a legitimate, non-reserved extras key\nfull env:\n%s", strings.Join(got, "\n  "))
	}
	if n, want := len(got), 3; n != want {
		t.Errorf("buildDockerEnv: got %d env entries, want %d (no duplicate MYSQL_DATABASE/MYSQL_ALLOW_EMPTY_PASSWORD entries)", n, want)
	}
}

func TestBuildDockerEnv_DockerVersionBackendExtrasExcluded(t *testing.T) {
	got := buildDockerEnv("bough", map[string]string{
		"docker.image": "mysql:8.0",
		"version":      "8.0",
		"backend":      "docker",
	})
	if n, want := len(got), 2; n != want {
		t.Errorf("buildDockerEnv: got %d env entries, want %d (docker./version/backend must not leak into MYSQL_* env)\nfull env:\n%s", n, want, strings.Join(got, "\n  "))
	}
}

// TestDockerImageTable pins this plugin's version→image mapping, which
// no test covered while it lived in pickDockerImage: a typo in the
// repository name or a lost default would have stayed green until
// someone ran a real create.
func TestDockerImageTable(t *testing.T) {
	cases := []struct {
		name    string
		extras  map[string]string
		want    string
		wantErr bool
	}{
		{"version fills the template", map[string]string{"version": "9"}, "mysql:9", false},
		{"patch level is accepted", map[string]string{"version": "8.4.5"}, "mysql:8.4.5", false},
		{"docker.image wins", map[string]string{"docker.image": "mysql:8.4-oracle", "version": "9"}, "mysql:8.4-oracle", false},
		{"nil extras falls back to the default", nil, "mysql:8.4", false},
		{"a variant tag needs docker.image", map[string]string{"version": "8.4-oracle"}, "", true},
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
