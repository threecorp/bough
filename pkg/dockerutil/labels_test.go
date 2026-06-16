//go:build darwin || linux

package dockerutil

import "testing"

func TestLabels_Schema(t *testing.T) {
	got := Labels("mysql", "mysql:8.4", 42345)
	want := map[string]string{
		"com.bough.managed":   "true",
		"com.bough.engine":    "mysql",
		"com.bough.image":     "mysql:8.4",
		"com.bough.host-port": "42345",
	}
	if len(got) != len(want) {
		t.Fatalf("label count: got %d, want %d (got=%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Labels()[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestLabels_EngineDifferentiation(t *testing.T) {
	// Each plugin must serialise its own engine value so `bough remove`
	// can filter by engine when listing leaked containers.
	cases := []struct {
		engine string
		image  string
	}{
		{"mysql", "mysql:8.4"},
		{"postgres", "postgres:16-alpine"},
		{"redis", "redis:7-alpine"},
		{"elasticsearch", "docker.elastic.co/elasticsearch/elasticsearch:7.17.29"},
	}
	for _, c := range cases {
		labels := Labels(c.engine, c.image, 12345)
		if labels[LabelEngine] != c.engine {
			t.Errorf("engine %s: LabelEngine = %q", c.engine, labels[LabelEngine])
		}
		if labels[LabelImage] != c.image {
			t.Errorf("engine %s: LabelImage = %q", c.engine, labels[LabelImage])
		}
		if labels[LabelHostPort] != "12345" {
			t.Errorf("engine %s: LabelHostPort = %q, want 12345", c.engine, labels[LabelHostPort])
		}
		if labels[LabelManaged] != "true" {
			t.Errorf("engine %s: LabelManaged = %q, want true", c.engine, labels[LabelManaged])
		}
	}
}

func TestLabels_KeyConstants(t *testing.T) {
	// The constants are part of the package's public API — pinned by
	// schema-stability comment in labels.go. If a refactor changes a
	// key string, this test fails so the breaking change is loud.
	cases := map[string]string{
		LabelManaged:  "com.bough.managed",
		LabelEngine:   "com.bough.engine",
		LabelImage:    "com.bough.image",
		LabelHostPort: "com.bough.host-port",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("label key constant drift: got %q, want %q", got, want)
		}
	}
}
