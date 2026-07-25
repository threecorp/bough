package instinctgate

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSidecar(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "denylist.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDenylistAbsentIsInert pins the default posture: most operators
// never need a denylist, and a guard that failed (or nagged) on a
// missing optional sidecar would be turned off, taking the cases that
// DO need it with it.
func TestDenylistAbsentIsInert(t *testing.T) {
	d, err := LoadDenylist(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil {
		t.Fatalf("missing sidecar must not error: %v", err)
	}
	if d.Active() {
		t.Error("missing sidecar should be inert")
	}
	if _, hit := d.Match("anything at all"); hit {
		t.Error("inert denylist must not match")
	}
	// An empty path (unconfigured) behaves the same way.
	if d2, err := LoadDenylist(""); err != nil || d2.Active() {
		t.Errorf("empty path: err=%v active=%v, want nil/false", err, d2.Active())
	}
}

// TestDenylistUnreadableIsAnError pins the distinction that matters: an
// ABSENT sidecar is "not configured", but one that exists and cannot be
// read is a broken configuration. Treating the latter as empty would
// silently disable a guard the operator believes is running.
func TestDenylistUnreadableIsAnError(t *testing.T) {
	p := writeSidecar(t, "term\n")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Skipf("cannot chmod in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	if _, err := LoadDenylist(p); err == nil {
		if os.Geteuid() == 0 {
			t.Skip("running as root: permissions are not enforced")
		}
		t.Error("an unreadable sidecar must be an error, not a silent empty list")
	}
}

// TestDenylistMatching covers the parsing and the matching rule with
// probes phrased differently from the list itself — the term embedded in
// a longer identifier, in different case, and as part of a sentence.
func TestDenylistMatching(t *testing.T) {
	d, err := LoadDenylist(writeSidecar(t, "# comment line\n\nAcme\nprivate-host\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Active() {
		t.Fatal("loaded sidecar should be active")
	}
	cases := []struct {
		name string
		text string
		want string // "" = must not match
	}{
		{"exact term", "deploy to acme", "acme"},
		{"different case", "deploy to ACME", "acme"},
		{"embedded in identifier", "the host acme-prod-01 needs a restart", "acme"},
		{"second term", "ssh into private-host before the migration", "private-host"},
		{"comment not loaded", "add a # comment line to the file", ""},
		{"unrelated text", "re-run the flaky suite with a fixed seed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, hit := d.Match(tc.text)
			if tc.want == "" {
				if hit {
					t.Errorf("unexpected match on %q (term=%q)", tc.text, term)
				}
				return
			}
			if !hit || term != tc.want {
				t.Errorf("Match(%q) = (%q, %v), want (%q, true)", tc.text, term, hit, tc.want)
			}
		})
	}
}

// TestDenylistHoldsCandidateInGate pins the wiring: a denied term in the
// propagating surface holds the instinct, while the same term appearing
// only in the evidence body does NOT — a note may record what it saw
// without recommending it onward.
func TestDenylistHoldsCandidateInGate(t *testing.T) {
	d, err := LoadDenylist(writeSidecar(t, "acme\n"))
	if err != nil {
		t.Fatal(err)
	}
	g := New(Config{Enabled: true, Denylist: d})

	held := g.Screen([]Candidate{cand("c1", "when deploying", "push the build to acme first")})
	if len(held.Held) != 1 || held.Held[0].Rule != "denylisted-term:acme" {
		t.Errorf("surface hit: held=%+v, want denylisted-term:acme", held.Held)
	}

	bodyOnly := g.Screen([]Candidate{{
		ID: "c2", Trigger: "when reviewing history", Action: "read the reflog first",
		Body: "context: the failure happened on acme",
	}})
	if len(bodyOnly.Held) != 0 {
		t.Errorf("evidence-only mention must not be held: %+v", bodyOnly.Held)
	}
}
