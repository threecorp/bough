// Package dockertest provides Docker daemon support for tests only.
package dockertest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// StartDaemon serves list and inspect requests; an empty name means no container.
func StartDaemon(t *testing.T, name, image string, running bool, labels map[string]string, binds ...string) {
	t.Helper()
	startDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			list := []map[string]any{}
			if name != "" {
				list = append(list, map[string]any{"Id": "old", "Names": []string{"/" + name}})
			}
			_ = json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/old/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Image":      "sha256:old",
				"Config":     map[string]any{"Image": image, "Labels": labels},
				"State":      map[string]any{"Running": running},
				"HostConfig": map[string]any{"Binds": binds},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/"):
			if strings.HasSuffix(r.URL.Path, "/images/"+image+"/json") && image != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:old"})
				return
			}
			http.Error(w, "image missing", http.StatusNotFound)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

// StartCreateDaemon serves an empty container list, image inspects and one
// create+start; onCreate runs when the create request arrives.
func StartCreateDaemon(t *testing.T, onCreate func()) {
	t.Helper()
	startDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:writer"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			onCreate()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "created"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/created/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func startDaemon(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "1.47")
	// The fake serves plain HTTP regardless of the caller's Docker settings.
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_CONTEXT", "")
}
