// Package dockertest provides Docker daemon support for tests only.
package dockertest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// StartDaemon serves list and inspect requests for one container.
func StartDaemon(t *testing.T, name, image string, running bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "old", "Names": []string{"/" + name}}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/old/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Config": map[string]any{"Image": image},
				"State":  map[string]any{"Running": running},
			})
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "1.47")
	// The fake serves plain HTTP regardless of the caller's Docker settings.
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_CONTEXT", "")
}
