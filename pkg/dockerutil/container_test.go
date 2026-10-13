//go:build darwin || linux

package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/pkg/dockerutil/dockertest"
)

// TestImageSwapped is the guard for the post-v0.27.0 review finding:
// UpOrReuse reused any running container answering to the name, so
// changing engines[].version and running create again left the old
// version serving while .env.local and `bough status` named the new
// one. An unknown on either side must stay a reuse, or a daemon that
// reports no Config would break an Up that used to work.
func TestImageSwapped(t *testing.T) {
	cases := []struct {
		name string
		want string
		have string
		swap bool
	}{
		{"same ref reuses", "mysql:8.4", "mysql:8.4", false},
		{"different tag is a swap", "mysql:9", "mysql:8.4", true},
		{"different image is a swap", "postgres:17-alpine", "postgres:17", true},
		{"no wanted ref reuses (compose owns the image)", "", "nginx:1.27", false},
		{"daemon reported no image reuses", "mysql:8.4", "", false},
		{"neither known reuses", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageSwapped(tc.want, tc.have); got != tc.swap {
				t.Errorf("imageSwapped(%q, %q) = %v, want %v", tc.want, tc.have, got, tc.swap)
			}
		})
	}
}

// TestIsPortConflictError_RecognizesBothDaemonErrorShapes is the
// regression guard for the #7-review finding: StartOrCleanup's
// port-conflict detection matched only "port is already allocated"
// (another Docker container holds the port). A plain host-level
// listener holding the same port produces a different daemon message
// ("...bind: address already in use") that the bare substring check
// missed, silently falling through to a generic, non-actionable
// error for exactly the case the check exists to catch.
func TestIsPortConflictError_RecognizesBothDaemonErrorShapes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "docker container holds the port",
			err:  errors.New("Bind for 127.0.0.1:42001 failed: port is already allocated"),
			want: true,
		},
		{
			name: "host-level listener holds the port",
			err:  errors.New("Error response from daemon: ports are not available: exposing port TCP 127.0.0.1:42001 -> 127.0.0.1:0: listen tcp4 127.0.0.1:42001: bind: address already in use"),
			want: true,
		},
		{
			name: "unrelated daemon error",
			err:  errors.New("Error response from daemon: No such image: mysql:8.4"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPortConflictError(c.err); got != c.want {
				t.Errorf("isPortConflictError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestUpOrReuse_ImageMismatch(t *testing.T) {
	// Shell TLS settings must not reach the plain HTTP fake daemon.
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", filepath.Join(t.TempDir(), "missing-certs"))
	t.Setenv("DOCKER_CONTEXT", "unrelated-context")
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			name := "bough-mysql-42001"
			dockertest.StartDaemon(t, name, "mysql:8.4", running)
			cli, err := NewClient()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cli.Close() })
			skip, err := UpOrReuse(context.Background(), cli, name, "mysql:9")
			var mismatch *ImageMismatchError
			if !errors.As(fmt.Errorf("reuse: %w", err), &mismatch) {
				t.Fatalf("UpOrReuse error = %v, want ImageMismatchError", err)
			}
			if skip {
				t.Fatal("image mismatch reused or removed the container")
			}
			if mismatch.ContainerName != name || mismatch.HaveImage != "mysql:8.4" || mismatch.WantImage != "mysql:9" {
				t.Fatalf("mismatch = %+v", mismatch)
			}
			want := "container bough-mysql-42001 was created from mysql:8.4, not the mysql:9 this config asks for; bough does not start another image on the data directory the first one wrote"
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestImageMismatchError_WithDatadir(t *testing.T) {
	mismatch := &ImageMismatchError{ContainerName: "bough-elasticsearch-42001", HaveImage: "es:7", WantImage: "es:9"}
	for _, datadir := range []string{filepath.Join(t.TempDir(), "es data"), "relative-es-data"} {
		err := mismatch.WithDatadir(datadir)
		var got *ImageMismatchError
		if !errors.As(err, &got) || got != mismatch {
			t.Fatalf("WithDatadir lost the original error: %v", err)
		}
		absolute, absErr := filepath.Abs(datadir)
		if absErr != nil {
			t.Fatal(absErr)
		}
		for _, want := range []string{mismatch.Error(), "first run `docker rm -fv " + mismatch.ContainerName + "`, then empty the data directory " + fmt.Sprintf("%q", absolute), "all data in that directory is lost", "on Linux its files belong to the container's user, so empty it as root", "keep the directory itself", "resume the same worktree the same way it was created", "WorktreeCreate hook", "`bough create` with the same worktree name", "`bough remove` to discard the whole worktree"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("WithDatadir missing %q: %v", want, err)
			}
		}
	}
}

func TestImageMismatchError_WithDatadirWithoutContainer(t *testing.T) {
	mismatch := &ImageMismatchError{HaveImage: "redis:7-alpine", WantImage: "redis:8-alpine"}
	datadir := filepath.Join(t.TempDir(), "redis data")
	err := mismatch.WithDatadir(datadir)
	var got *ImageMismatchError
	if !errors.As(err, &got) || got != mismatch {
		t.Fatalf("WithDatadir lost the original error: %v", err)
	}
	if strings.Contains(err.Error(), "docker rm") || strings.Contains(err.Error(), "container  was") {
		t.Fatalf("remedy names a nonexistent container: %v", err)
	}
	for _, want := range []string{"redis:7-alpine", "redis:8-alpine", "empty the data directory", datadir, "keep the directory itself", "record the wanted image with", datadir + ".image", "WorktreeCreate hook", "`bough create` with the same worktree name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("remedy missing %q: %v", want, err)
		}
	}
}

func TestShellQuote_RoundTripsThroughSh(t *testing.T) {
	for _, s := range []string{"plain", "a b", "it's", "$HOME", "`id`", "$(id)", "line\nbreak", `back\slash`} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shellQuote(%q) via sh = %q, err=%v", s, out, err)
		}
	}
}

func TestImageMismatchError_WithDatadirUnknownWriter(t *testing.T) {
	mismatch := &ImageMismatchError{WantImage: "redis:8-alpine"}
	datadir := filepath.Join(t.TempDir(), "redis data")
	err := mismatch.WithDatadir(datadir)
	if strings.Contains(err.Error(), "docker rm") || strings.Contains(err.Error(), "written by ,") {
		t.Fatalf("unknown writer remedy names a container or an empty image: %v", err)
	}
	for _, want := range []string{
		"no image record and no container", "if redis:8-alpine wrote this data",
		"in a root shell if the data directory's parent is root-owned",
		"printf '%s\\n' 'redis:8-alpine' > '" + datadir + ".image'",
		"otherwise to start over", "empty the data directory " + fmt.Sprintf("%q", datadir),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("remedy missing %q: %v", want, err)
		}
	}
}
