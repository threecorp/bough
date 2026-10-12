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

// providerRoot returns <worktree>/api, the shape of UpReq.WorktreeRoot: the
// engine-provider repo inside a worktree dir that exists. The repo dir
// itself is not created; the owner is the worktree, not the repo.
func providerRoot(t *testing.T) string {
	return filepath.Join(t.TempDir(), "api")
}

// TestUpOrReuse_LeavesAnotherWorktreesContainerAlone: container names are
// bough-<engine>-<port> and the port is a hash of the worktree name, so a
// second clone of the monorepo with a same-named worktree finds the first
// clone's container. Reusing it pointed the second worktree at the first
// one's database; removing a stopped one deleted it.
func TestUpOrReuse_LeavesAnotherWorktreesContainerAlone(t *testing.T) {
	owner, other := providerRoot(t), providerRoot(t)
	for _, running := range []bool{true, false} {
		f := &fakeDaemon{name: "bough-mysql-36000", labels: worktreeLabels(owner), running: running}
		_, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", other)
		if err == nil {
			t.Fatalf("running=%v: UpOrReuse accepted a container another worktree created", running)
		}
		for _, want := range []string{f.name, filepath.Dir(owner), filepath.Dir(other)} {
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
	owner := providerRoot(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(owner), link); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "deleted-worktree", "api")
	cases := []struct {
		name   string
		labels map[string]string
		binds  []string
		want   string
	}{
		{"same path", worktreeLabels(owner), nil, owner},
		{"same worktree through a symlink", worktreeLabels(owner), nil, filepath.Join(link, "api")},
		{"no label and no datadir bind", map[string]string{LabelManaged: "true"}, []string{"/etc/hosts:/etc/hosts:ro"}, owner},
		{"labelled worktree no longer exists", worktreeLabels(gone), nil, owner},
		{"caller names no worktree (compose)", worktreeLabels(owner), nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDaemon{name: "bough-mysql-36000", labels: tc.labels, binds: tc.binds, running: true}
			skip, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", tc.want)
			if err != nil || !skip {
				t.Errorf("UpOrReuse = (%v, %v), want (true, nil)", skip, err)
			}
		})
	}
}

// TestUpOrReuse_ProviderRepoGoneButWorktreeAlive: the label names the
// engine-provider repo, but the datadir lives in the worktree's .local, so
// a deleted repo dir did not free the database for another checkout.
func TestUpOrReuse_ProviderRepoGoneButWorktreeAlive(t *testing.T) {
	owner, other := providerRoot(t), providerRoot(t)
	f := &fakeDaemon{name: "bough-mysql-36000", labels: worktreeLabels(owner), running: true}
	if _, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", other); err == nil {
		t.Error("UpOrReuse reused a container whose worktree is alive although its provider repo dir is gone")
	}
}

// TestUpOrReuse_InspectFailureKeepsTheContainer: a failed inspect used to
// fall through to a force remove, deleting a running container whose
// owner and image were never checked. Only "already gone" may proceed.
func TestUpOrReuse_InspectFailureKeepsTheContainer(t *testing.T) {
	f := &fakeDaemon{name: "bough-mysql-36000", inspectStatus: http.StatusInternalServerError}
	if _, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", providerRoot(t)); err == nil {
		t.Error("UpOrReuse succeeded although inspect failed")
	}
	if f.wasRemoved() {
		t.Error("UpOrReuse removed a container it could not inspect")
	}

	gone := &fakeDaemon{name: "bough-mysql-36000", inspectStatus: http.StatusNotFound}
	skip, err := UpOrReuse(context.Background(), newFakeClient(t, gone), gone.name, "mysql:8.4", providerRoot(t))
	if err != nil || skip {
		t.Errorf("container vanished before inspect: UpOrReuse = (%v, %v), want (false, nil)", skip, err)
	}
	if gone.wasRemoved() {
		t.Error("UpOrReuse removed a container the daemon reported gone")
	}
}

// legacyBinds is the bind mount a pre-label engine container carries:
// <worktree>/.local/<kind>-data at the engine's data dir.
func legacyBinds(provider string) []string {
	return []string{filepath.Join(filepath.Dir(provider), ".local", "mysql-data") + ":/var/lib/mysql"}
}

// TestUpOrReuse_UnlabelledContainerOfAnotherWorktree: a container made
// before the worktree label existed still names its datadir in its bind
// mount, and the datadir's worktree is its owner.
func TestUpOrReuse_UnlabelledContainerOfAnotherWorktree(t *testing.T) {
	own, other := providerRoot(t), providerRoot(t)
	gone := filepath.Join(t.TempDir(), "deleted-worktree", "api")
	cases := []struct {
		name    string
		binds   []string
		refused bool
	}{
		{"datadir of another live worktree", legacyBinds(other), true},
		{"datadir of this worktree", legacyBinds(own), false},
		{"datadir of a worktree that no longer exists", legacyBinds(gone), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDaemon{name: "bough-mysql-36000", labels: map[string]string{LabelManaged: "true"}, binds: tc.binds, running: true}
			skip, err := UpOrReuse(context.Background(), newFakeClient(t, f), f.name, "mysql:8.4", own)
			if tc.refused && err == nil {
				t.Error("UpOrReuse reused a legacy container of another worktree")
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

// TestCheckWorktree is Down's half: stopping another worktree's container
// takes that worktree's database down, labelled or not.
func TestCheckWorktree(t *testing.T) {
	owner, other := providerRoot(t), providerRoot(t)
	labelled := &fakeDaemon{name: "bough-mysql-36000", labels: worktreeLabels(owner), running: true}
	legacy := &fakeDaemon{name: "bough-mysql-36000", labels: map[string]string{LabelManaged: "true"}, binds: legacyBinds(owner), running: true}
	for name, f := range map[string]*fakeDaemon{"labelled": labelled, "legacy": legacy} {
		cli := newFakeClient(t, f)
		if err := CheckWorktree(context.Background(), cli, fakeID, f.name, other); err == nil {
			t.Errorf("%s: CheckWorktree accepted another worktree's container", name)
		}
		if err := CheckWorktree(context.Background(), cli, fakeID, f.name, owner); err != nil {
			t.Errorf("%s: CheckWorktree refused its own container: %v", name, err)
		}
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
	if got := OtherWorktree(filepath.Join(owner, "api"), filepath.Join(alias, "api")); got != "" {
		t.Errorf("OtherWorktree = %q, want \"\" (same worktree)", got)
	}
}
