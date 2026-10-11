//go:build darwin || linux

package dockerutil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/client"
)

// fakeDaemon answers the three Engine API calls UpOrReuse and
// CheckWorktree make — list by name, inspect, remove — for one container,
// so the reuse and refusal decisions run without a docker daemon.
type fakeDaemon struct {
	name          string
	inspectStatus int
	labels        map[string]string
	binds         []string
	running       bool

	mu      sync.Mutex
	removed bool
}

const fakeID = "c0ffee"

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": fakeID, "Names": []string{"/" + f.name}}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/"+fakeID+"/json"):
		if f.inspectStatus != 0 {
			w.WriteHeader(f.inspectStatus)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "inspect failed"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":         fakeID,
			"Config":     map[string]any{"Image": "mysql:8.4", "Labels": f.labels},
			"HostConfig": map[string]any{"Binds": f.binds},
			"State":      map[string]any{"Running": f.running},
		})
	case r.Method == http.MethodDelete && strings.HasSuffix(path, "/containers/"+fakeID):
		f.mu.Lock()
		f.removed = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "unexpected " + r.Method + " " + path})
	}
}

func (f *fakeDaemon) wasRemoved() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed
}

func newFakeClient(t *testing.T, f *fakeDaemon) *client.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")),
		client.WithHTTPClient(srv.Client()),
		client.WithVersion("1.47"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func worktreeLabels(root string) map[string]string {
	return map[string]string{LabelManaged: "true", LabelWorktreeRoot: root}
}

// TestUpOrReuse_LeavesAnotherWorktreesContainerAlone: container names are
// bough-<engine>-<port> and the port is a hash of the worktree name, so a
// second clone of the monorepo with a same-named worktree finds the first
// clone's container. Reusing it pointed the second worktree at the first
// one's database; removing a stopped one deleted it.
func TestUpOrReuse_LeavesAnotherWorktreesContainerAlone(t *testing.T) {
	owner, other := t.TempDir(), t.TempDir()
	for _, running := range []bool{true, false} {
		f := &fakeDaemon{name: "bough-mysql-36000", labels: worktreeLabels(owner), running: running}
		_, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", other, "")
		if err == nil {
			t.Fatalf("running=%v: UpOrReuse accepted a container another worktree created", running)
		}
		for _, want := range []string{f.name, owner, other} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("running=%v: refusal %q does not name %q", running, err, want)
			}
		}
		if f.wasRemoved() {
			t.Errorf("running=%v: UpOrReuse removed another worktree's container", running)
		}
	}
}

func TestUpOrReuse_ReusesItsOwnWorktreesContainer(t *testing.T) {
	owner := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(owner, link); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "deleted-worktree")
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"same path", worktreeLabels(owner), owner},
		{"same dir through a symlink", worktreeLabels(owner), link},
		{"no label (created before the label existed)", map[string]string{LabelManaged: "true"}, owner},
		{"labelled worktree no longer exists", worktreeLabels(gone), owner},
		{"caller names no worktree (compose)", worktreeLabels(owner), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDaemon{name: "bough-mysql-36000", labels: tc.labels, running: true}
			skip, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", tc.want, "")
			if err != nil || !skip {
				t.Errorf("UpOrReuse = (%v, %v), want (true, nil)", skip, err)
			}
		})
	}
}

// TestUpOrReuse_InspectFailureKeepsTheContainer: a failed inspect used to
// fall through to a force remove, deleting a running container whose
// owner and image were never checked. Only "already gone" may proceed.
func TestUpOrReuse_InspectFailureKeepsTheContainer(t *testing.T) {
	f := &fakeDaemon{name: "bough-mysql-36000", inspectStatus: http.StatusInternalServerError}
	if _, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", t.TempDir(), ""); err == nil {
		t.Error("UpOrReuse succeeded although inspect failed")
	}
	if f.wasRemoved() {
		t.Error("UpOrReuse removed a container it could not inspect")
	}

	gone := &fakeDaemon{name: "bough-mysql-36000", inspectStatus: http.StatusNotFound}
	skip, err := UpOrReuse(context.Background(), newFakeClient(t, gone), gone.name, "mysql:8.4", t.TempDir(), "")
	if err != nil || skip {
		t.Errorf("container vanished before inspect: UpOrReuse = (%v, %v), want (false, nil)", skip, err)
	}
	if gone.wasRemoved() {
		t.Error("UpOrReuse removed a container the daemon reported gone")
	}
}

// TestCheckWorktree is Down's half: stopping another worktree's container
// takes that worktree's database down.
func TestCheckWorktree(t *testing.T) {
	owner, other := t.TempDir(), t.TempDir()
	f := &fakeDaemon{name: "bough-mysql-36000", labels: worktreeLabels(owner), running: true}
	cli := newFakeClient(t, f)
	if err := CheckWorktree(context.Background(), cli, fakeID, f.name, other); err == nil {
		t.Error("CheckWorktree accepted another worktree's container")
	}
	if err := CheckWorktree(context.Background(), cli, fakeID, f.name, owner); err != nil {
		t.Errorf("CheckWorktree refused its own container: %v", err)
	}
	gone := &fakeDaemon{name: "bough-mysql-36000", inspectStatus: http.StatusNotFound}
	if err := CheckWorktree(context.Background(), newFakeClient(t, gone), fakeID, gone.name, other); err != nil {
		t.Errorf("CheckWorktree on a vanished container: %v, want nil", err)
	}
}

func TestLabels_WorktreeRoot(t *testing.T) {
	if got := Labels("mysql", "mysql:8.4", 36000, "/w/F-Auth/api")[LabelWorktreeRoot]; got != "/w/F-Auth/api" {
		t.Errorf("LabelWorktreeRoot = %q, want /w/F-Auth/api", got)
	}
	if _, ok := Labels("mysql", "mysql:8.4", 36000, "")[LabelWorktreeRoot]; ok {
		t.Error("an empty worktree root must not be stamped")
	}
}

// TestUpOrReuse_UnlabelledContainerOfAnotherDatadir: a container made
// before the worktree label existed still names its datadir in its bind
// mount, so another checkout's legacy container is refused too.
func TestUpOrReuse_UnlabelledContainerOfAnotherDatadir(t *testing.T) {
	own, other := t.TempDir(), t.TempDir()
	cases := []struct {
		name    string
		bind    string
		refused bool
	}{
		{"bind of another live datadir", other + ":/var/lib/mysql", true},
		{"bind of this datadir", own + ":/var/lib/mysql", false},
		{"bind of a datadir that no longer exists", filepath.Join(other, "gone") + ":/var/lib/mysql", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDaemon{name: "bough-mysql-36000", labels: map[string]string{LabelManaged: "true"}, binds: []string{tc.bind}, running: true}
			skip, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", t.TempDir(), own)
			if tc.refused && err == nil {
				t.Error("UpOrReuse reused a legacy container bound to another datadir")
			}
			if !tc.refused && (err != nil || !skip) {
				t.Errorf("UpOrReuse = (%v, %v), want (true, nil)", skip, err)
			}
			if f.wasRemoved() {
				t.Error("UpOrReuse removed the container")
			}
		})
	}
}

// TestOtherWorktree_CaseDifferentSpelling: a cwd typed in another case on a
// case-insensitive filesystem is the same worktree, not a foreign one.
func TestOtherWorktree_CaseDifferentSpelling(t *testing.T) {
	base := t.TempDir()
	owner := filepath.Join(base, "Owner")
	if err := os.Mkdir(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "owner")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	if got := OtherWorktree(owner, alias); got != "" {
		t.Errorf("OtherWorktree(%q, %q) = %q, want \"\" (same directory)", owner, alias, got)
	}
}
