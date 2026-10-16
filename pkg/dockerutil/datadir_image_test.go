//go:build darwin || linux

package dockerutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
		unknown    bool
		recorded   string // record expected after the check when none was stamped
	}{
		{name: "missing ID refuses swapped refs", stamp: "redis:7-alpine", data: true, want: "docker.io/library/redis:7-alpine", mismatch: true},
		{name: "retagged recorded ref cannot authorize new ID", stamp: "redis:7-alpine\nsha256:old\n", data: true, want: "retagged:latest", mismatch: true},
		{name: "missing record without container refuses an unknown writer", data: true, want: "redis:8-alpine", mismatch: true, unknown: true},
		{name: "missing record with a matching container records it", data: true, want: "redis:7-alpine", container: true, recorded: "redis:7-alpine\nsha256:running\n"},
		{name: "missing record with a mismatched container leaves the refusal to UpOrReuse", data: true, want: "redis:8-alpine", container: true},
		{name: "missing record and empty directory", want: "redis:8-alpine"},
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
			cli := datadirImageTestClient(t, name, tc.container, &datadirImageTestContainer{binds: []string{dir + ":/data"}})
			err := CheckDatadirImage(context.Background(), cli, name, dir, tc.want)
			var mismatch *ImageMismatchError
			if tc.mismatch {
				if !errors.As(err, &mismatch) {
					t.Fatalf("CheckDatadirImage = %v, want ImageMismatchError", err)
				}
				wantName := ""
				wantHave := "redis:7-alpine"
				if tc.unknown {
					wantHave = ""
				}
				if mismatch.HaveImage != wantHave || mismatch.WantImage != tc.want || mismatch.ContainerName != wantName {
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
			if tc.stamp == "" {
				data, err := os.ReadFile(dir + ".image")
				if tc.recorded == "" && !os.IsNotExist(err) {
					t.Fatalf("check wrote a record: %q, %v", data, err)
				}
				if tc.recorded != "" && (err != nil || string(data) != tc.recorded) {
					t.Fatalf("container's writer not recorded: %q, %v", data, err)
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

// The remedy records the wanted ref because the host cannot list a 0700 dir.
func TestCheckDatadirImage_UnreadableDatadirPassesWithWantedRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory mode")
	}
	dir := filepath.Join(t.TempDir(), "postgres-data")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.WriteFile(dir+".image", []byte("postgres:17-alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := datadirImageTestClient(t, "bough-postgres-42001", false)
	if err := CheckDatadirImage(context.Background(), cli, "bough-postgres-42001", dir, "postgres:17-alpine"); err != nil {
		t.Fatalf("recorded wanted ref was refused: %v", err)
	}
}

type datadirImageTestContainer struct {
	binds          []string
	inspectStatus  int
	inspectCalls   atomic.Int32
	omitHostConfig bool
	missingImageID bool
}

func datadirImageTestClient(t *testing.T, name string, exists bool, containers ...*datadirImageTestContainer) *client.Client {
	t.Helper()
	info := &datadirImageTestContainer{}
	if len(containers) != 0 {
		info = containers[0]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			list := []map[string]any{}
			if exists {
				list = append(list, map[string]any{"Id": "old", "Names": []string{"/" + name}})
			}
			_ = json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/old/json"):
			info.inspectCalls.Add(1)
			if info.inspectStatus != 0 {
				http.Error(w, "inspect failed", info.inspectStatus)
				return
			}
			imageID := "sha256:running"
			if info.missingImageID {
				imageID = ""
			}
			var hostConfig any = map[string]any{"Binds": info.binds}
			if info.omitHostConfig {
				hostConfig = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Image": imageID, "Config": map[string]any{"Image": "redis:7-alpine"},
				"HostConfig": hostConfig,
			})
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

func TestCheckDatadirImage_ContainerBinds(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stamp          string
		bind           string
		inspectStatus  int
		wantHave       string
		mismatch       bool
		inspectError   bool
		recorded       string
		missingImageID bool
	}{
		{name: "other datadir checks the record", stamp: "redis:8-alpine\nsha256:new\n", bind: "other", mismatch: true, wantHave: "redis:8-alpine"},
		{name: "other datadir cannot record a legacy writer", bind: "other", mismatch: true},
		{name: "same datadir leaves the image check to UpOrReuse", stamp: "redis:8-alpine\nsha256:new\n", bind: "same"},
		{name: "same datadir records its legacy writer", bind: "same", recorded: "redis:7-alpine\nsha256:running\n"},
		{name: "a matching bind after another bind records its writer", bind: "multiple", recorded: "redis:7-alpine\nsha256:running\n"},
		{name: "a symlink to the same datadir records its writer", bind: "symlink", recorded: "redis:7-alpine\nsha256:running\n"},
		{name: "same datadir resolves a missing container image ID", bind: "same", missingImageID: true, recorded: "redis:7-alpine\nsha256:retagged\n"},
		{name: "no host config checks the record", stamp: "redis:8-alpine\nsha256:new\n", bind: "nil", mismatch: true, wantHave: "redis:8-alpine"},
		{name: "no host config cannot record a legacy writer", bind: "nil", mismatch: true},
		{name: "no binds checks the record", stamp: "redis:8-alpine\nsha256:new\n", mismatch: true, wantHave: "redis:8-alpine"},
		{name: "no binds cannot record a legacy writer", mismatch: true},
		{name: "gone container checks the record", stamp: "redis:8-alpine\nsha256:new\n", inspectStatus: http.StatusNotFound, mismatch: true, wantHave: "redis:8-alpine"},
		{name: "gone container leaves an unknown writer", inspectStatus: http.StatusNotFound, mismatch: true},
		{name: "inspect failure preserves the record", stamp: "redis:8-alpine\nsha256:new\n", inspectStatus: http.StatusInternalServerError, inspectError: true},
		{name: "inspect failure cannot record a legacy writer", inspectStatus: http.StatusInternalServerError, inspectError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "redis-data")
			if err := EnsureDatadir(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "data"), []byte("old data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.stamp != "" {
				if err := os.WriteFile(dir+".image", []byte(tc.stamp), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			info := &datadirImageTestContainer{inspectStatus: tc.inspectStatus, missingImageID: tc.missingImageID}
			switch tc.bind {
			case "nil":
				info.omitHostConfig = true
			case "same":
				info.binds = []string{dir + ":/data:rw"}
			case "other":
				info.binds = []string{filepath.Join(t.TempDir(), "other-data") + ":/data"}
			case "multiple":
				info.binds = []string{t.TempDir() + ":/config:ro", dir + "/:/data:rw"}
			case "symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				info.binds = []string{alias + ":/data"}
			}
			cli := datadirImageTestClient(t, "bough-redis-42001", true, info)
			err := CheckDatadirImage(context.Background(), cli, "bough-redis-42001", dir, "redis:7-alpine")
			var mismatch *ImageMismatchError
			switch {
			case tc.mismatch:
				if !errors.As(err, &mismatch) || mismatch.HaveImage != tc.wantHave || mismatch.WantImage != "redis:7-alpine" || mismatch.ContainerName != "" {
					t.Errorf("CheckDatadirImage = %v, want mismatch with HaveImage %q", err, tc.wantHave)
				}
			case tc.inspectError:
				if err == nil || !strings.Contains(err.Error(), "inspect") || errors.As(err, &mismatch) {
					t.Errorf("CheckDatadirImage = %v, want inspect error", err)
				}
			case err != nil:
				t.Errorf("CheckDatadirImage = %v, want nil", err)
			}
			data, readErr := os.ReadFile(dir + ".image")
			wantRecord := tc.stamp + tc.recorded
			if wantRecord == "" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Errorf("check wrote a record: %q, %v", data, readErr)
				}
			} else if readErr != nil || string(data) != wantRecord {
				t.Errorf("record = %q, %v, want %q", data, readErr, wantRecord)
			}
			if calls := info.inspectCalls.Load(); calls != 1 {
				t.Errorf("container inspect calls = %d, want 1", calls)
			}
		})
	}
}

// TestCheckDatadirImage_AnonymousVolumeIsNotWriter: a bind entry with one
// path and no ":" is an anonymous volume at that container path, so it
// writes no host dir even when the path spells this datadir.
func TestCheckDatadirImage_AnonymousVolumeIsNotWriter(t *testing.T) {
	for _, stamp := range []string{"", "redis:8-alpine\nsha256:new\n"} {
		t.Run(fmt.Sprintf("record=%v", stamp != ""), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "redis-data")
			checkNonWriterBind(t, dir, dir, stamp)
		})
	}
}

func TestCheckDatadirImage_NamedVolumeIsNotWriter(t *testing.T) {
	t.Chdir(t.TempDir())
	dir, err := filepath.Abs("redis-data")
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{"", "redis:8-alpine\nsha256:new\n"} {
		t.Run(fmt.Sprintf("record=%v", stamp != ""), func(t *testing.T) {
			checkNonWriterBind(t, dir, "redis-data:/data", stamp)
		})
	}
}

func TestCheckDatadirImage_ReadOnlyBindIsNotWriter(t *testing.T) {
	for _, mode := range []string{"ro", "ro,z", "z,ro", "rprivate,ro"} {
		for _, stamp := range []string{"", "redis:8-alpine\nsha256:new\n"} {
			t.Run(fmt.Sprintf("mode=%s/record=%v", mode, stamp != ""), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "redis-data")
				checkNonWriterBind(t, dir, dir+":/data:"+mode, stamp)
			})
		}
	}
}

func checkNonWriterBind(t *testing.T, dir, bind, stamp string) {
	t.Helper()
	if err := EnsureDatadir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("old data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stamp != "" {
		if err := os.WriteFile(dir+".image", []byte(stamp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info := &datadirImageTestContainer{binds: []string{bind}}
	cli := datadirImageTestClient(t, "bough-redis-42001", true, info)
	err := CheckDatadirImage(context.Background(), cli, "bough-redis-42001", dir, "redis:7-alpine")
	var mismatch *ImageMismatchError
	wantHave := strings.Split(stamp, "\n")[0]
	if !errors.As(err, &mismatch) || mismatch.ContainerName != "" || mismatch.HaveImage != wantHave || mismatch.WantImage != "redis:7-alpine" {
		t.Errorf("CheckDatadirImage = %v, want record mismatch with HaveImage %q", err, wantHave)
	}
	data, readErr := os.ReadFile(dir + ".image")
	if stamp == "" {
		if !errors.Is(readErr, os.ErrNotExist) {
			t.Errorf("non-writer recorded as legacy writer: %q, %v", data, readErr)
		}
	} else if readErr != nil || string(data) != stamp {
		t.Errorf("record = %q, %v, want unchanged %q", data, readErr, stamp)
	}
}
