// Package allocator deterministically picks a free port in
// [base, base+rangeSize) for a (name, role) pair, falling back to
// linear probing on collision.
//
// The strategy mirrors a Python helper used by an earlier Bash-based
// worktree-create hook that this package replaces. That predecessor
// has been running with ~16+ concurrent worktrees and zero observed
// collision at rangeSize=3000. The deterministic seed guarantees that
// re-running `bough create F-X` after a worktree was removed and
// re-added returns the same port — peer worktrees never get
// reshuffled, which is the property that makes shareable `.env.local`
// files stable across sessions.
//
// v0.4.0 added the `role` argument so multi-port engines (rabbitmq
// AMQP + Management, kafka broker + controller, NATS client +
// monitor + cluster) can allocate one port per role with a stable
// per-(name, role) seed. Single-port engines (mysql / postgres /
// redis / elasticsearch) pass role="main" or "" (treated identically)
// and the seed reduces to `crc32(name)` — bit-for-bit the v0.3 result,
// so an auba deployment migrated from v0.3 to v0.4 sees zero port
// drift on its existing engines.
package allocator

import (
	"errors"
	"fmt"
	"hash/crc32"
)

// ErrRangeExhausted is returned when every port in
// [base, base+rangeSize) is already present in `taken`. The caller is
// expected to either widen the range in the monorepo's .bough.yaml or
// to free unused worktree entries by running `bough remove`.
var ErrRangeExhausted = errors.New("allocator: all ports in range are taken")

// Allocate returns a port in [base, base+rangeSize) that is not
// present in `taken`. The first candidate is
// `base + crc32(seedKey(name, role)) % rangeSize` (IEEE polynomial,
// matching Python's zlib.crc32 so the predecessor registry stays
// portable to bough). For role="" or role="main" the seed key is
// just `name`, preserving v0.3 port assignments byte-for-byte across
// the v0.4 upgrade. For other roles the seed key is `name|role`, so
// a single worktree spawning rabbitmq with roles "amqp" and
// "management" gets two stable but distinct ports.
//
// On collision, candidates step forward by one modulo rangeSize until
// an open slot is found or the range is exhausted.
//
// `taken` should be the set of ports already held by other worktrees
// for the same (kind, role) — the registry layer loads it from
// .bough-ports.json before invoking Allocate. Pass nil for "no ports
// taken yet".
func Allocate(name, role string, base, rangeSize int, taken map[int]bool) (int, error) {
	if rangeSize <= 0 {
		return 0, fmt.Errorf("allocator: rangeSize must be positive, got %d", rangeSize)
	}
	if base <= 0 {
		return 0, fmt.Errorf("allocator: base must be positive, got %d", base)
	}
	seed := crc32.ChecksumIEEE([]byte(seedKey(name, role)))
	// Cast to uint32 before %; int(uint32) on 32-bit Go can land
	// negative. uint32 % uint32(rangeSize) keeps the candidate in
	// [0, rangeSize) on every platform without sign quirks.
	candidate := base + int(seed%uint32(rangeSize))
	for i := 0; i < rangeSize; i++ {
		if !taken[candidate] {
			return candidate, nil
		}
		candidate = base + ((candidate-base+1)%rangeSize)
	}
	return 0, ErrRangeExhausted
}

// seedKey returns the byte sequence the crc32 IEEE polynomial hashes.
// For single-port engines the role is empty (or "main"); we collapse
// both to just `name` so a v0.3-era port allocation transferred into
// v0.4's allocator returns the same port. Multi-role keys join with
// "|" — a delimiter that cannot appear in a worktree name (which is
// always a feature-branch slug) and would otherwise confuse a
// future tokeniser.
func seedKey(name, role string) string {
	if role == "" || role == "main" {
		return name
	}
	return name + "|" + role
}
