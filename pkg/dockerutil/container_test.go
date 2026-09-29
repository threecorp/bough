//go:build darwin || linux

package dockerutil

import (
	"errors"
	"testing"
)

// TestImageSwapped is the guard for the post-v0.27.0 review finding:
// UpOrReuse reused any running container answering to the name, so
// changing engines[].version and running create again left the old
// version serving while .env.local and `bough status` named the new
// one. An unknown on either side must stay a reuse, or a daemon that
// reports no Config would break an Up that used to work.
func TestImageSwapped(t *testing.T) {
	cases := []struct {
		name string
		want string
		have string
		swap bool
	}{
		{"same ref reuses", "mysql:8.4", "mysql:8.4", false},
		{"different tag is a swap", "mysql:9", "mysql:8.4", true},
		{"different image is a swap", "postgres:17-alpine", "postgres:17", true},
		{"no wanted ref reuses (compose owns the image)", "", "nginx:1.27", false},
		{"daemon reported no image reuses", "mysql:8.4", "", false},
		{"neither known reuses", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageSwapped(tc.want, tc.have); got != tc.swap {
				t.Errorf("imageSwapped(%q, %q) = %v, want %v", tc.want, tc.have, got, tc.swap)
			}
		})
	}
}

// TestIsPortConflictError_RecognizesBothDaemonErrorShapes is the
// regression guard for the #7-review finding: StartOrCleanup's
// port-conflict detection matched only "port is already allocated"
// (another Docker container holds the port). A plain host-level
// listener holding the same port produces a different daemon message
// ("...bind: address already in use") that the bare substring check
// missed, silently falling through to a generic, non-actionable
// error for exactly the case the check exists to catch.
func TestIsPortConflictError_RecognizesBothDaemonErrorShapes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "docker container holds the port",
			err:  errors.New("Bind for 127.0.0.1:42001 failed: port is already allocated"),
			want: true,
		},
		{
			name: "host-level listener holds the port",
			err:  errors.New("Error response from daemon: ports are not available: exposing port TCP 127.0.0.1:42001 -> 127.0.0.1:0: listen tcp4 127.0.0.1:42001: bind: address already in use"),
			want: true,
		},
		{
			name: "unrelated daemon error",
			err:  errors.New("Error response from daemon: No such image: mysql:8.4"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPortConflictError(c.err); got != c.want {
				t.Errorf("isPortConflictError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
