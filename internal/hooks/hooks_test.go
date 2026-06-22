package hooks

import (
	"context"
	"errors"
	"testing"
)

// TestAllEvents_StableOrder pins the canonical event list so a
// future patch adding a new event has to update the test in
// lockstep — keeping the install / uninstall / doctor diff order
// reproducible across runs.
func TestAllEvents_StableOrder(t *testing.T) {
	got := AllEvents()
	want := []HookEvent{
		EventPreToolUse,
		EventPostToolUse,
		EventUserPromptSubmit,
		EventStop,
		EventSessionEnd,
		EventPreCompact,
		EventWorktreeCreate,
		EventWorktreeRemove,
	}
	if len(got) != len(want) {
		t.Fatalf("AllEvents length: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllEvents[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestManager_SkeletonMethodsReturnSentinel locks the v0.7.0 first
// commit's "skeleton only" contract: every Manager method returns
// hooks.ErrNotYetWired until the subsequent v0.7.0 sub-phases fill
// the bodies in. The sentinel lets `bough hook ...` print a
// useful diagnostic instead of silently no-op'ing.
func TestManager_SkeletonMethodsReturnSentinel(t *testing.T) {
	m := New("/tmp/.claude/settings.json")
	ctx := context.Background()
	if err := m.Install(ctx, "bough hook handle"); !errors.Is(err, ErrNotYetWired) {
		t.Errorf("Install: want ErrNotYetWired, got %v", err)
	}
	if err := m.Uninstall(ctx); !errors.Is(err, ErrNotYetWired) {
		t.Errorf("Uninstall: want ErrNotYetWired, got %v", err)
	}
	if _, err := m.List(ctx); !errors.Is(err, ErrNotYetWired) {
		t.Errorf("List: want ErrNotYetWired, got %v", err)
	}
	if _, err := m.Replay(ctx, EventPreToolUse, []byte("{}")); !errors.Is(err, ErrNotYetWired) {
		t.Errorf("Replay: want ErrNotYetWired, got %v", err)
	}
}
