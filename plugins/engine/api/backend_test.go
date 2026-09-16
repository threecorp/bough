package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeBackend records how often Running was consulted so the
// single-backend short-circuit is observable.
type fakeBackend struct {
	name    string
	running bool
	probes  *int
}

func (f fakeBackend) Up(context.Context, *UpReq) error                   { return nil }
func (f fakeBackend) ReadyCheck(context.Context, int, int) (bool, error) { return true, nil }
func (f fakeBackend) Down(context.Context, *DownReq) error               { return nil }
func (f fakeBackend) Running(context.Context, int) bool {
	if f.probes != nil {
		*f.probes++
	}
	return f.running
}

// TestBackends_ForUp_DefaultsToDocker pins the requirement that a
// .bough.yaml which omits `backend:` still runs somewhere: the host
// sends no token, and Up must land on DefaultBackend rather than error.
func TestBackends_ForUp_DefaultsToDocker(t *testing.T) {
	want := fakeBackend{name: "docker"}
	b := Backends{DefaultBackend: want}

	for _, extras := range []map[string]string{nil, {}, {"backend": ""}} {
		got, err := b.ForUp(extras)
		if err != nil {
			t.Fatalf("ForUp(%v) returned %v, want the default backend", extras, err)
		}
		if got.(fakeBackend).name != want.name {
			t.Errorf("ForUp(%v) = %q, want %q", extras, got.(fakeBackend).name, want.name)
		}
	}
}

// TestBackends_ForUp_UnknownNameIsActionable guards the message an
// operator sees after the nix backend was removed: a stale
// `backend: nix` must name both the rejected token and what the plugin
// actually provides, not fall through to whatever is registered.
func TestBackends_ForUp_UnknownNameIsActionable(t *testing.T) {
	b := Backends{DefaultBackend: fakeBackend{name: "docker"}}

	_, err := b.ForUp(map[string]string{"backend": "nix"})
	if err == nil {
		t.Fatal("ForUp accepted an unregistered backend token")
	}
	var unknown *UnknownBackendError
	if !errors.As(err, &unknown) {
		t.Fatalf("ForUp error = %T, want *UnknownBackendError", err)
	}
	for _, want := range []string{`"nix"`, "docker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestBackends_EmptySetIsAnError guards the zero-value Provider: a
// plugin built as a struct literal instead of via New() has no table,
// and both resolvers must say so rather than nil-deref inside a gRPC
// handler.
func TestBackends_EmptySetIsAnError(t *testing.T) {
	var b Backends

	if _, err := b.ForUp(nil); err == nil || !strings.Contains(err.Error(), "New()") {
		t.Errorf("ForUp on an empty set = %v, want an error naming New()", err)
	}
	if _, err := b.ForPort(context.Background(), 42345); err == nil || !strings.Contains(err.Error(), "New()") {
		t.Errorf("ForPort on an empty set = %v, want an error naming New()", err)
	}
}

// TestBackends_ForPort_SingleBackendShortCircuits is the cost guard:
// Down and ReadyCheck carry no backend token, but with one backend
// registered there is nothing to disambiguate, so no Running probe (a
// Docker daemon round trip) may be paid.
func TestBackends_ForPort_SingleBackendShortCircuits(t *testing.T) {
	probes := 0
	b := Backends{DefaultBackend: fakeBackend{name: "docker", running: false, probes: &probes}}

	got, err := b.ForPort(context.Background(), 42345)
	if err != nil {
		t.Fatalf("ForPort returned %v", err)
	}
	if got.(fakeBackend).name != "docker" {
		t.Errorf("ForPort = %q, want docker", got.(fakeBackend).name)
	}
	if probes != 0 {
		t.Errorf("Running was probed %d times for a single registered backend, want 0", probes)
	}
}

// TestBackends_ForPort_PicksTheRunningBackend is what makes adding a
// second backend a local change: with two registered, the one actually
// holding the port wins, and when neither does the default is returned
// so an idempotent Down still runs.
func TestBackends_ForPort_PicksTheRunningBackend(t *testing.T) {
	ctx := context.Background()

	b := Backends{
		DefaultBackend: fakeBackend{name: "docker", running: false},
		"podman":       fakeBackend{name: "podman", running: true},
	}
	got, err := b.ForPort(ctx, 42345)
	if err != nil {
		t.Fatalf("ForPort returned %v", err)
	}
	if got.(fakeBackend).name != "podman" {
		t.Errorf("ForPort = %q, want the running backend podman", got.(fakeBackend).name)
	}

	idle := Backends{
		DefaultBackend: fakeBackend{name: "docker", running: false},
		"podman":       fakeBackend{name: "podman", running: false},
	}
	got, err = idle.ForPort(ctx, 42345)
	if err != nil {
		t.Fatalf("ForPort returned %v", err)
	}
	if got.(fakeBackend).name != "docker" {
		t.Errorf("ForPort with nothing running = %q, want the default docker", got.(fakeBackend).name)
	}
}
