package allocator

import (
	"errors"
	"hash/crc32"
	"testing"
)

func TestAllocate_deterministic(t *testing.T) {
	// Allocating the same name twice (with the previous result removed from
	// `taken`) must return the same port — this is the property that makes
	// `bough remove` followed by `bough create` idempotent.
	port1, err := Allocate("F-Auth", 33000, 3000, nil)
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	port2, err := Allocate("F-Auth", 33000, 3000, nil)
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if port1 != port2 {
		t.Errorf("deterministic re-run: got %d then %d, want equal", port1, port2)
	}
}

func TestAllocate_seedMatchesCrc32(t *testing.T) {
	// The seed must match Python's zlib.crc32 (IEEE polynomial) so a
	// .worktree-ports.json registry produced by the predecessor Python
	// helper can be consumed by bough without any rewriting.
	const name = "F-Auth"
	const base, size = 33000, 3000
	want := base + int(crc32.ChecksumIEEE([]byte(name))%uint32(size))
	got, err := Allocate(name, base, size, nil)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != want {
		t.Errorf("seed: got %d want %d", got, want)
	}
}

func TestAllocate_probesPastCollision(t *testing.T) {
	// Pin the first candidate as taken; the allocator must walk forward by
	// one modulo rangeSize and return the next slot. This documents the
	// linear-probe contract — anything more sophisticated (e.g. hash perturb
	// of Python's dict) would break parity with the predecessor registry.
	first, err := Allocate("F-Probe", 33000, 3000, nil)
	if err != nil {
		t.Fatalf("uncontested: %v", err)
	}
	taken := map[int]bool{first: true}
	next, err := Allocate("F-Probe", 33000, 3000, taken)
	if err != nil {
		t.Fatalf("contested: %v", err)
	}
	if next != first+1 {
		t.Errorf("probe: got %d want %d (one-step linear probe)", next, first+1)
	}
}

func TestAllocate_wrapsAroundRange(t *testing.T) {
	// Force the initial candidate to the last port in the range, then mark
	// it taken. The probe must wrap back to `base` (modulo rangeSize) and
	// return the first port. The range_size matters: 1 means everything is
	// the same slot — used by the next subtest.
	base, size := 100, 5
	// Construct `taken` so that only base+0 is free; everything else taken.
	taken := map[int]bool{base + 1: true, base + 2: true, base + 3: true, base + 4: true}
	// The crc-seeded candidate may land anywhere in [100, 104]; whatever it
	// is, the probe must terminate at 100 (the only free slot).
	got, err := Allocate("any", base, size, taken)
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
	_, err := Allocate("any", base, size, taken)
	if !errors.Is(err, ErrRangeExhausted) {
		t.Errorf("got %v want ErrRangeExhausted", err)
	}
}

func TestAllocate_invalidInput(t *testing.T) {
	if _, err := Allocate("a", 33000, 0, nil); err == nil {
		t.Errorf("rangeSize=0: want error, got nil")
	}
	if _, err := Allocate("a", 33000, -1, nil); err == nil {
		t.Errorf("rangeSize=-1: want error, got nil")
	}
	if _, err := Allocate("a", 0, 3000, nil); err == nil {
		t.Errorf("base=0: want error, got nil")
	}
	if _, err := Allocate("a", -10, 3000, nil); err == nil {
		t.Errorf("base=-10: want error, got nil")
	}
}

func TestAllocate_noDuplicatesInDenseRange(t *testing.T) {
	// Allocate every slot in a small range one by one, threading `taken`
	// forward. Every returned port must be distinct and in-range — this is
	// the property the host code relies on when it allocates db / api /
	// gateway sequentially against a half-full registry.
	const base, size = 100, 10
	taken := map[int]bool{}
	got := map[int]bool{}
	for i := 0; i < size; i++ {
		name := []byte{byte('A' + i)}
		p, err := Allocate(string(name), base, size, taken)
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
