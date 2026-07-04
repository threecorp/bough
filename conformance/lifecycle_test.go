//go:build darwin || linux

package conformance

import (
	"net"
	"strconv"
	"testing"

	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// seedListener grabs a kernel-assigned free port and returns it along
// with a closer. Mirrors pkg/dockerutil/network_test.go's pattern.
func seedListener(t *testing.T) (port int, close func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("seed listen: %v", err)
	}
	return l.Addr().(*net.TCPAddr).Port, func() { _ = l.Close() }
}

func TestPickFreePort_DegenerateRangeReturnsLow(t *testing.T) {
	if got, want := pickFreePort(5000, 5000, nil), 5000; got != want {
		t.Errorf("pickFreePort(5000, 5000) = %d, want %d (high<=low degenerate case)", got, want)
	}
	if got, want := pickFreePort(5000, 4999, nil), 5000; got != want {
		t.Errorf("pickFreePort(5000, 4999) = %d, want %d (high<low degenerate case)", got, want)
	}
}

// TestPickFreePort_SkipsOccupiedPortAndBindsNext is the regression
// guard for PR #27's own motivation (a stray process bound to Low):
// pickFreePort must scan past an occupied low port to the next free
// one instead of returning the occupied port.
func TestPickFreePort_SkipsOccupiedPortAndBindsNext(t *testing.T) {
	occupied, closeOccupied := seedListener(t)
	defer closeOccupied()

	got := pickFreePort(occupied, occupied+portScanAttempts, nil)
	if got == occupied {
		t.Fatalf("pickFreePort returned the occupied port %d", got)
	}
	if !isPortFreeForTest(t, got) {
		t.Errorf("pickFreePort returned %d, which is not actually free", got)
	}
}

func TestPickFreePort_ExhaustionFallsBackToLow(t *testing.T) {
	// Occupy every port in a narrow range so the scan exhausts without
	// finding a free one; low itself is left occupied too, matching
	// the real "stray process on Low" scenario the fallback exists for.
	const rangeWidth = 3
	low, closeLow := seedListener(t)
	defer closeLow()
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	// Occupy low+1..low+rangeWidth so the whole [low, low+rangeWidth]
	// window is busy; pickFreePort's fallback must still return low
	// rather than a busy port or a port outside the requested range.
	for p := low + 1; p <= low+rangeWidth; p++ {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			t.Skipf("could not seed port %d for exhaustion test: %v", p, err)
		}
		closers = append(closers, func() { _ = l.Close() })
	}
	if got, want := pickFreePort(low, low+rangeWidth, nil), low; got != want {
		t.Errorf("pickFreePort exhaustion fallback = %d, want %d (low)", got, want)
	}
}

func TestPickFreePort_SkipsClaimedPorts(t *testing.T) {
	free1, close1 := seedListener(t)
	close1() // release immediately so it is free again, but still claimed
	free2, close2 := seedListener(t)
	defer close2()

	claimed := map[int]bool{free1: true}
	low, high := min(free1, free2), max(free1, free2)+portScanAttempts
	got := pickFreePort(low, high, claimed)
	if got == free1 {
		t.Errorf("pickFreePort returned a claimed port %d", free1)
	}
}

// TestAllocateRoles_OverlappingRangesGetDistinctPorts is the
// regression guard for the wave-4 review finding: allocateRoles used
// to call pickFreePort per role with no shared claimed-port state, so
// two roles whose PortRangeDefault ranges overlap (a real shape for
// multi-port engines like rabbitmq AMQP+Management) could be handed
// the identical port — Up() would then fail to publish the same host
// port twice for an otherwise perfectly contract-conformant plugin.
func TestAllocateRoles_OverlappingRangesGetDistinctPorts(t *testing.T) {
	base, closeBase := seedListener(t)
	defer closeBase()
	low := base
	high := base + portScanAttempts

	ranges := map[string]engineapi.PortRange{
		"main":       {Low: low, High: high},
		"management": {Low: low, High: high}, // fully overlapping range
	}
	ports, portInts, mainPort := allocateRoles(ranges, "main")

	if len(ports) != 2 {
		t.Fatalf("allocateRoles returned %d ports, want 2", len(ports))
	}
	if portInts[0] == portInts[1] {
		t.Errorf("allocateRoles gave both roles the identical port %d for fully overlapping ranges", portInts[0])
	}
	if mainPort == 0 {
		t.Errorf("mainPort was never set")
	}
}

func isPortFreeForTest(t *testing.T, port int) bool {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}
