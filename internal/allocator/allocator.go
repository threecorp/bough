// Package allocator deterministically picks a free port in [base, base+rangeSize)
// for a (name, kind) pair, falling back to linear probing on collision.
//
// The strategy mirrors a Python helper used by an earlier Bash-based
// worktree-create hook that this package replaces. That predecessor
// has been running with ~16+ concurrent worktrees and zero observed
// collision at rangeSize=3000. The deterministic seed guarantees that
// re-running `bough create F-X` after a worktree was removed and
// re-added returns the same port — peer worktrees never get
// reshuffled, which is the property that makes shareable `.env.local`
// files stable across sessions.
package allocator

import (
	"errors"
	"fmt"
	"hash/crc32"
)

// ErrRangeExhausted is returned when every port in [base, base+rangeSize)
// is already held in `taken`. The caller is expected to either widen the
// range in the monorepo's .worktree-isolation.yaml or to free unused
// worktree entries by running `bough remove`.
var ErrRangeExhausted = errors.New("allocator: all ports in range are taken")

// Allocate returns a port in [base, base+rangeSize) that is not present in
// `taken`. The first candidate is `base + crc32(name) % rangeSize` (IEEE
// polynomial, matching Python's zlib.crc32 so the predecessor registry stays
// portable to bough), so the same `name` deterministically maps to the
// same candidate across runs. On collision, candidates step forward by one
// modulo rangeSize until an open slot is found or the range is exhausted.
//
// `taken` should be the set of ports already held by other worktrees for
// the same kind — the registry layer loads it from .worktree-ports.json
// before invoking Allocate. Pass nil for "no ports taken yet".
func Allocate(name string, base, rangeSize int, taken map[int]bool) (int, error) {
	if rangeSize <= 0 {
		return 0, fmt.Errorf("allocator: rangeSize must be positive, got %d", rangeSize)
	}
	if base <= 0 {
		return 0, fmt.Errorf("allocator: base must be positive, got %d", base)
	}
	// Cast to uint32 before %; int(uint32) on 32-bit Go can land negative.
	// uint32 % uint32(rangeSize) keeps the candidate in [0, rangeSize) on
	// every platform without sign quirks.
	// IEEE polynomial — same as Python's zlib.crc32 so a registry
	// produced by the predecessor Bash hook stays portable.
	seed := crc32.ChecksumIEEE([]byte(name))
	candidate := base + int(seed%uint32(rangeSize))
	for i := 0; i < rangeSize; i++ {
		if !taken[candidate] {
			return candidate, nil
		}
		candidate = base + ((candidate-base+1)%rangeSize)
	}
	return 0, ErrRangeExhausted
}
