//go:build darwin || linux

package dockerutil

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

func TestRecordDatadirImage_WritesBesideDatadir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".local", "postgres-data")
	if err := EnsureDatadir(dir); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"postgres:16-alpine", "postgres:17-alpine"} {
		if err := RecordDatadirImage(context.Background(), datadirImageTestClient(t, "unused", false), "unused", dir+"/", ref); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(dir + ".image")
		if err != nil || string(data) != ref+"\nsha256:old\n" {
			t.Fatalf("image record = %q, err=%v", data, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("record made initdb's directory nonempty: %v, %v", entries, err)
		}
		entries, err = os.ReadDir(filepath.Dir(dir))
		if err != nil || len(entries) != 2 {
			t.Fatalf("record left extra files: %v, %v", entries, err)
		}
	}
}

func TestCheckDatadirImage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stamp      string
		data       bool
		missingDir bool
		want       string
		container  bool
		mismatch   bool
	}{
		{name: "missing ID refuses swapped refs", stamp: "redis:7-alpine", data: true, want: "docker.io/library/redis:7-alpine", mismatch: true},
		{name: "retagged recorded ref cannot authorize new ID", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "retagged:latest", mismatch: true},
		{name: "missing record allows legacy data", data: true, want: "redis:8-alpine"},
		{name: "same reference", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "redis:7-alpine"},
		{name: "empty wanted reference", stamp: "redis:7-alpine\nsha256:old\n", data: true},
		{name: "missing directory ignores stale record", stamp: "redis:7-alpine\nsha256:old\n", missingDir: true, want: "redis:8-alpine"},
		{name: "empty directory ignores stale record", stamp: "redis:7-alpine\nsha256:old\n", want: "redis:8-alpine"},
		{name: "different image without container", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "redis:8-alpine", mismatch: true},
		{name: "wanted image missing from local store", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "redis:9-alpine", mismatch: true},
		{name: "different image with container", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "redis:8-alpine", container: true},
		{name: "same recorded image ID under another reference despite retagging", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "docker.io/library/redis:7-alpine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "redis-data")
			if !tc.missingDir {
				if err := EnsureDatadir(dir); err != nil {
					t.Fatal(err)
				}
			}
			if tc.data {
				if err := os.WriteFile(filepath.Join(dir, ".old-data"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stamp != "" {
				if err := os.WriteFile(dir+".image", []byte(tc.stamp), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			name := "bough-redis-42001"
			cli := datadirImageTestClient(t, name, tc.container)
			err := CheckDatadirImage(context.Background(), cli, name, dir, tc.want)
			var mismatch *ImageMismatchError
			if tc.mismatch {
				if !errors.As(err, &mismatch) {
					t.Fatalf("CheckDatadirImage = %v, want ImageMismatchError", err)
				}
				wantName := ""
				if mismatch.HaveImage != "redis:7-alpine" || mismatch.WantImage != tc.want || mismatch.ContainerName != wantName {
					t.Fatalf("mismatch = %+v", mismatch)
				}
			} else if err != nil {
				t.Fatalf("CheckDatadirImage: %v", err)
			}
			if tc.stamp != "" {
				data, err := os.ReadFile(dir + ".image")
				if err != nil || string(data) != tc.stamp {
					t.Fatalf("check changed record: %q, %v", data, err)
				}
			}
		})
	}
}

func TestCheckDatadirImage_ReportsUnreadableRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "redis-data")
	if err := EnsureDatadir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir+".image", 0o755); err != nil {
		t.Fatal(err)
	}
	cli := datadirImageTestClient(t, "unused", false)
	if err := CheckDatadirImage(context.Background(), cli, "unused", dir, "redis:8-alpine"); err != nil {
		t.Fatalf("empty datadir blocked by unreadable record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDatadirImage(context.Background(), cli, "unused", dir, "redis:8-alpine"); err == nil {
		t.Fatal("check ignored a record that could not be read")
	}
}

func TestCheckDatadirImage_UnreadableDatadirStillRefusesMismatch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory mode")
	}
	dir := filepath.Join(t.TempDir(), "postgres-data")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.WriteFile(dir+".image", []byte("postgres:16-alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := datadirImageTestClient(t, "bough-postgres-42001", false)
	var mismatch *ImageMismatchError
	if err := CheckDatadirImage(context.Background(), cli, "bough-postgres-42001", dir, "postgres:17-alpine"); !errors.As(err, &mismatch) {
		t.Fatalf("unreadable directory bypassed mismatch: %v", err)
	}
}

func datadirImageTestClient(t *testing.T, name string, exists bool) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			list := []map[string]any{}
			if exists {
				list = append(list, map[string]any{"Id": "old", "Names": []string{"/" + name}})
			}
			_ = json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/old/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Image": "sha256:running", "Config": map[string]any{"Image": "redis:7-alpine"}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/"):
			ref := strings.TrimSuffix(strings.SplitN(r.URL.Path, "/images/", 2)[1], "/json")
			ids := map[string]string{
				"redis:7-alpine":                   "sha256:retagged",
				"redis:8-alpine":                   "sha256:new",
				"retagged:latest":                  "sha256:retagged",
				"postgres:16-alpine":               "sha256:old",
				"postgres:17-alpine":               "sha256:old",
				"docker.io/library/redis:7-alpine": "sha256:old",
			}
			id, ok := ids[ref]
			if !ok {
				http.Error(w, "image missing", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})
		default:
			t.Errorf("check mutated Docker or made an unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestRecordDatadirImage_ReportsWriteErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "redis-data")
	if err := RecordDatadirImage(context.Background(), datadirImageTestClient(t, "unused", false), "unused", dir, "redis:7-alpine"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent error = %v", err)
	}
	dir = filepath.Join(t.TempDir(), "redis-data")
	if err := os.Mkdir(dir+".image", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RecordDatadirImage(context.Background(), datadirImageTestClient(t, "unused", false), "unused", dir, "redis:7-alpine"); err == nil {
		t.Fatal("write replaced a directory with the image record")
	}
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed write left temporary files: %v, %v", entries, err)
	}
}

func TestCheckDatadirImage_ReportsEmptyRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "redis-data")
	if err := EnsureDatadir(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "data"), dir + ".image"} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckDatadirImage(context.Background(), datadirImageTestClient(t, "unused", false), "unused", dir, "redis:8-alpine"); err == nil {
		t.Fatal("empty record silently bypassed the image check")
	}
}

func TestCheckDatadirImage_ReportsInvalidDatadir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "redis-data")
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+".image", []byte("redis:7-alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckDatadirImage(context.Background(), nil, "unused", dir, "redis:8-alpine"); err == nil || !strings.Contains(err.Error(), "read data directory") {
		t.Fatalf("invalid datadir error = %v", err)
	}
}

func TestRecordDatadirImage_ReportsHostAndRootWriteErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory mode")
	}
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	err := RecordDatadirImage(context.Background(), datadirImageTestClient(t, "unused", false), "unused", filepath.Join(parent, "redis-data"), "redis:7-alpine")
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "writing it from a root container also failed") {
		t.Fatalf("write did not preserve both causes: %v", err)
	}
}

func TestRecordDatadirImage_ReuseKeepsTheRunningContainersID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "redis-data")
	if err := EnsureDatadir(dir); err != nil {
		t.Fatal(err)
	}
	// The ref now resolves to sha256:retagged; the container still runs sha256:running.
	cli := datadirImageTestClient(t, "bough-redis-42001", true)
	if err := RecordDatadirImage(context.Background(), cli, "bough-redis-42001", dir, "redis:7-alpine"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dir + ".image")
	if err != nil || string(data) != "redis:7-alpine\nsha256:running\n" {
		t.Fatalf("record = %q, err=%v", data, err)
	}
}

func TestRecordDatadirImage_InspectFailurePreservesRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "redis-data")
	const oldRecord = "redis:7-alpine\nsha256:old\n"
	if err := os.WriteFile(dir+".image", []byte(oldRecord), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := datadirImageTestClient(t, "unused", false)
	if err := RecordDatadirImage(context.Background(), cli, "unused", dir, "missing:1"); err == nil || !strings.Contains(err.Error(), "inspect image missing:1") {
		t.Fatalf("inspect failure = %v", err)
	}
	data, err := os.ReadFile(dir + ".image")
	if err != nil || string(data) != oldRecord {
		t.Fatalf("failed inspect changed record: %q, %v", data, err)
	}
}
