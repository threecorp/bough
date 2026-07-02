package conformance

import (
	"net"
	"net/url"
	"strings"
	"time"
)

const dialableTimeout = 3 * time.Second

// Reporter is the subset of *testing.T the invariant helpers need.
// Plugin authors call AssertReachable / AssertShellSafe / AssertNonEmpty
// with a plain *testing.T (which satisfies Reporter); the suite's own
// self-tests pass a recording double to assert that the helpers
// flag the v0.2.5 / v0.2.6 failure modes without polluting the
// parent test's pass/fail state.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

// AssertReachable verifies that every host:port pair the plugin
// advertised through EnvVars is actually reachable from the host the
// suite is running on. This is the v0.2.6 guard: a plugin that
// advertises its container's bridge IP (e.g. 172.17.0.4) will appear
// to "work" in unit tests but crash any client that tries to dial
// the published address from outside the container.
//
// Pair detection looks for two conventions plugins use in the wild:
//
//   - paired keys: a `*_HOST` and a `*_PORT` with the same prefix.
//     This is the bough convention (BOUGH_MYSQL_HOST + BOUGH_MYSQL_PORT).
//   - URL keys: a `*_URL` value that parses as a URL with an
//     explicit port (e.g. redis://127.0.0.1:6379/8).
//
// If neither convention is present in env the assertion is a no-op
// (logged, not failed) — there is no sensible default address to
// guess and forcing the convention would lock out plugins that
// emit only an opaque DSN.
func AssertReachable(t Reporter, env map[string]string) {
	t.Helper()
	addrs := extractDialableAddrs(env)
	if len(addrs) == 0 {
		t.Logf("AssertReachable: no host/port pair found in env; nothing to verify")
		return
	}
	for _, addr := range addrs {
		conn, err := net.DialTimeout("tcp", addr, dialableTimeout)
		if err != nil {
			t.Errorf("EnvVars advertises %s but the host cannot reach it: %v "+
				"(see v0.2.6 — likely a container bridge IP or a publish-port not "+
				"pinned to the bough host port)", addr, err)
			continue
		}
		_ = conn.Close()
	}
}

// AssertShellSafe verifies that every EnvVars value is safe to drop
// into a bash `source` line. This is the v0.2.5 guard: a value
// containing `(`, `&`, `;`, etc. aborts `source .env.local` on the
// first such byte and silently leaves every subsequent variable
// unset, which is how the empty-port redis URL crashed auba-api at
// boot.
//
// Plugins whose values legitimately contain shell metachars (the
// historical mysql DSN format is the canonical example —
// `root:@tcp(127.0.0.1:3306)/db?parseTime=true&loc=UTC`) must set
// Config.AllowShellMetachars=true to declare that they accept the
// downstream cost of post-processing.
func AssertShellSafe(t Reporter, env map[string]string, allow bool) {
	t.Helper()
	if allow {
		return
	}
	for k, v := range env {
		if strings.ContainsAny(v, "()&;<>|`$'\"\\ \t") {
			t.Errorf("env %s=%q contains a shell metacharacter — "+
				"declare Config.AllowShellMetachars=true if this is intentional "+
				"(see v0.2.5 — bash `source .env.local` aborts on the first "+
				"metachar and silently empties every later ${VAR})", k, v)
		}
	}
}

// AssertNonEmpty verifies that no value the plugin returned is the
// empty string. An empty value renders into the host's .env.local as
// `KEY=`, which `source` then sets to the empty string — a silent
// data-loss path most callers will not notice until production.
func AssertNonEmpty(t Reporter, env map[string]string) {
	t.Helper()
	if len(env) == 0 {
		t.Errorf("EnvVars returned an empty map; the host would render nothing to .env.local")
		return
	}
	for k, v := range env {
		if strings.TrimSpace(v) == "" {
			t.Errorf("EnvVars %s is empty — host would render `%s=` into .env.local", k, k)
		}
	}
}

// extractDialableAddrs walks env and pulls out every "this is a
// dialable host:port" it can recognise. Order in the returned slice
// is unspecified; the suite asserts every entry independently.
//
// Pairing handles three shapes:
//
//   - Single-port engines: `BOUGH_<KIND>_HOST` + `BOUGH_<KIND>_PORT`
//     (mysql / postgres / redis / elasticsearch).
//   - Multi-port engines: a single `BOUGH_<KIND>_HOST` shared across
//     every `BOUGH_<KIND>_<ROLE>_PORT` (rabbitmq amqp+management,
//     kafka broker+controller, nats client+monitor+cluster).
//   - URL keys: any `*_URL` value that parses as a URL with an
//     explicit port, so plugins emitting a per-role
//     `BOUGH_<KIND>_<ROLE>_URL` get dialed without a host lookup.
//
// If a `*_PORT` key has no matching `*_HOST` family (not even an
// ancestor prefix), it is skipped — there is no sensible address to
// guess and forcing the convention would lock out opaque-DSN plugins.
func extractDialableAddrs(env map[string]string) []string {
	var out []string

	// hostFor returns the most-specific `*_HOST` value whose key is an
	// ancestor of stem (`stem` = a `*_PORT` key with `_PORT` stripped).
	// Walk back one `_<segment>` at a time so a key like
	// `BOUGH_RABBITMQ_AMQP_PORT` matches the shared `BOUGH_RABBITMQ_HOST`
	// without manual role enumeration.
	hostFor := func(stem string) (string, bool) {
		for cur := stem; cur != ""; {
			if host, ok := env[cur+"_HOST"]; ok {
				return host, true
			}
			i := strings.LastIndexByte(cur, '_')
			if i < 0 {
				return "", false
			}
			cur = cur[:i]
		}
		return "", false
	}

	for k, v := range env {
		if !strings.HasSuffix(k, "_PORT") || v == "" {
			continue
		}
		stem := strings.TrimSuffix(k, "_PORT")
		host, ok := hostFor(stem)
		if !ok {
			continue
		}
		if host == "" {
			host = "127.0.0.1"
		}
		out = append(out, net.JoinHostPort(host, v))
	}
	for k, v := range env {
		if !strings.HasSuffix(k, "_URL") {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || u.Port() == "" {
			continue
		}
		out = append(out, u.Host)
	}
	return out
}
