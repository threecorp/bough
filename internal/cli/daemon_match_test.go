package cli

import "testing"

// TestDaemonLineMatches is the v0.9.17 regression for the recycled-pid
// kill: daemonRunning/findDaemonByRoot must only treat a pid as the
// observer daemon when its command line actually is one — so a stale pid
// file whose pid the OS recycled cannot make `observer stop` signal an
// unrelated process.
func TestDaemonLineMatches(t *testing.T) {
	root := "/Users/x/repo"
	cases := []struct {
		line string
		want bool
	}{
		{"123 bough observer _run-daemon --root /Users/x/repo --interval 600", true},
		{"123 /usr/bin/vim main.go", false},                                               // recycled pid → unrelated process
		{"9 bough observer _run-daemon --root /Users/x/repo-other --interval 600", false}, // prefix root must not match
		{"9 bough observer run-once --root /Users/x/repo ", false},                        // run-once is not the daemon
		{"", false},
	}
	for _, c := range cases {
		if got := daemonLineMatches(c.line, root); got != c.want {
			t.Errorf("daemonLineMatches(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}
