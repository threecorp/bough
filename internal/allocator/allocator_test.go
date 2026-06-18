package allocator

import (
	"errors"
	"hash/crc32"
	"testing"
)

func TestAllocate_deterministic(t *testing.T) {
	// Allocating the same name twice (with the previous result removed
	// from `taken`) must return the same port — this is the property
	// that makes `bough remove` followed by `bough create` idempotent.
	port1, err := Allocate("F-Auth", "", 33000, 3000, nil)
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	port2, err := Allocate("F-Auth", "", 33000, 3000, nil)
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if port1 != port2 {
		t.Errorf("deterministic re-run: got %d then %d, want equal", port1, port2)
	}
}

func TestAllocate_seedMatchesCrc32(t *testing.T) {
	// The seed must match Python's zlib.crc32 (IEEE polynomial) so a
	// `.worktree-ports.json` registry produced by the predecessor
	// Python helper can be consumed by bough without any rewriting.
	const name = "F-Auth"
	const base, size = 33000, 3000
	want := base + int(crc32.ChecksumIEEE([]byte(name))%uint32(size))
	got, err := Allocate(name, "", base, size, nil)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != want {
		t.Errorf("seed: got %d want %d", got, want)
	}
}

// TestAllocate_seedMatchesV03_roleMainEqualsEmpty is the v0.4
// compatibility guard: role="" and role="main" MUST produce the same
// seed so an auba deployment migrated from v0.3 sees zero port drift
// on its existing engines.
func TestAllocate_seedMatchesV03_roleMainEqualsEmpty(t *testing.T) {
	const name = "F-Migrate"
	const base, size = 33000, 3000
	pEmpty, err := Allocate(name, "", base, size, nil)
	if err != nil {
		t.Fatalf("role='': %v", err)
	}
	pMain, err := Allocate(name, "main", base, size, nil)
	if err != nil {
		t.Fatalf("role=main: %v", err)
	}
	if pEmpty != pMain {
		t.Errorf("role='' and role='main' must match: got %d vs %d", pEmpty, pMain)
	}
}

// TestAllocate_distinctPortsForDistinctRoles is the multi-port engine
// invariant: rabbitmq with roles amqp + management on the same
// worktree must get two distinct candidate ports out of the same
// (base, rangeSize).
func TestAllocate_distinctPortsForDistinctRoles(t *testing.T) {
	const name = "F-Rabbit"
	const base, size = 60000, 1000
	pAmqp, err := Allocate(name, "amqp", base, size, nil)
	if err != nil {
		t.Fatalf("role=amqp: %v", err)
	}
	pMgmt, err := Allocate(name, "management", base, size, nil)
	if err != nil {
		t.Fatalf("role=management: %v", err)
	}
	// Astronomically unlikely to collide on the very first crc32 seed
	// for a 1000-wide range; if it ever does, the test will need a
	// `taken: {pAmqp: true}` argument on the management call.
	if pAmqp == pMgmt {
		t.Errorf("distinct roles produced the same candidate seed (%d); test fixtures need a wider range", pAmqp)
	}
}

func TestAllocate_probesPastCollision(t *testing.T) {
	// Pin the first candidate as taken; the allocator must walk
	// forward by one modulo rangeSize and return the next slot.
	first, err := Allocate("F-Probe", "", 33000, 3000, nil)
	if err != nil {
		t.Fatalf("uncontested: %v", err)
	}
	taken := map[int]bool{first: true}
	next, err := Allocate("F-Probe", "", 33000, 3000, taken)
	if err != nil {
		t.Fatalf("contested: %v", err)
	}
	if next != first+1 {
		t.Errorf("probe: got %d want %d (one-step linear probe)", next, first+1)
	}
}

func TestAllocate_wrapsAroundRange(t *testing.T) {
	base, size := 100, 5
	taken := map[int]bool{base + 1: true, base + 2: true, base + 3: true, base + 4: true}
	got, err := Allocate("any", "", base, size, taken)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != base {
		t.Errorf("wrap-around: got %d want %d (only free slot)", got, base)
	}
}

func TestAllocate_exhausted(t *testing.T) {
	base, size := 100, 3
	taken := map[int]bool{100: true, 101: true, 102: true}
	_, err := Allocate("any", "", base, size, taken)
	if !errors.Is(err, ErrRangeExhausted) {
		t.Errorf("got %v want ErrRangeExhausted", err)
	}
}

func TestAllocate_invalidInput(t *testing.T) {
	if _, err := Allocate("a", "", 33000, 0, nil); err == nil {
		t.Errorf("rangeSize=0: want error, got nil")
	}
	if _, err := Allocate("a", "", 33000, -1, nil); err == nil {
		t.Errorf("rangeSize=-1: want error, got nil")
	}
	if _, err := Allocate("a", "", 0, 3000, nil); err == nil {
		t.Errorf("base=0: want error, got nil")
	}
	if _, err := Allocate("a", "", -10, 3000, nil); err == nil {
		t.Errorf("base=-10: want error, got nil")
	}
}

func TestAllocate_noDuplicatesInDenseRange(t *testing.T) {
	const base, size = 100, 10
	taken := map[int]bool{}
	got := map[int]bool{}
	for i := 0; i < size; i++ {
		name := []byte{byte('A' + i)}
		p, err := Allocate(string(name), "", base, size, taken)
		if err != nil {
			t.Fatalf("Allocate #%d: %v", i, err)
		}
		if got[p] {
			t.Errorf("duplicate port %d on iteration %d", p, i)
		}
		if p < base || p >= base+size {
			t.Errorf("out of range: got %d, want in [%d,%d)", p, base, base+size)
		}
		got[p] = true
		taken[p] = true
	}
	if len(got) != size {
		t.Errorf("filled %d unique ports, want %d", len(got), size)
	}
}
