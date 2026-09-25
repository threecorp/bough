package api

import (
	"regexp"
	"strings"
	"testing"
)

// TestDockerImage_Resolve pins the precedence every engine plugin
// duplicated before this type existed (docker.image > version > default)
// plus the tag check that is new: a registry publishing only full x.y.z
// tags used to accept `version: "7"` and fail much later at the pull,
// with an error naming neither the YAML key nor the escape hatch.
func TestDockerImage_Resolve(t *testing.T) {
	es := DockerImage{
		Image:      "docker.elastic.co/elasticsearch/elasticsearch:%s",
		Default:    "9.5.3",
		TagPattern: regexp.MustCompile(`^\d+\.\d+\.\d+$`),
		TagHint:    "a full x.y.z tag, e.g. 9.5.3",
	}

	cases := []struct {
		name    string
		extras  map[string]string
		want    string
		wantErr bool
	}{
		{"docker.image wins over version", map[string]string{"docker.image": "my.registry/es:custom", "version": "9"}, "my.registry/es:custom", false},
		{"version fills the template", map[string]string{"version": "9.4.1"}, "docker.elastic.co/elasticsearch/elasticsearch:9.4.1", false},
		{"older line still resolves", map[string]string{"version": "7.17.29"}, "docker.elastic.co/elasticsearch/elasticsearch:7.17.29", false},
		{"empty version falls back to the default", map[string]string{"version": ""}, "docker.elastic.co/elasticsearch/elasticsearch:9.5.3", false},
		{"nil extras falls back to the default", nil, "docker.elastic.co/elasticsearch/elasticsearch:9.5.3", false},
		{"major only is rejected", map[string]string{"version": "9"}, "", true},
		{"major.minor is rejected", map[string]string{"version": "9.4"}, "", true},
		{"shell metachars are rejected", map[string]string{"version": "9.4.1; rm -rf /"}, "", true},
	}
	for _, c := range cases {
		got, err := es.Resolve(c.extras)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("%s: Resolve = %q, want an error", c.name, got)
		case !c.wantErr && err != nil:
			t.Errorf("%s: Resolve returned %v, want %q", c.name, err, c.want)
		case !c.wantErr && got != c.want:
			t.Errorf("%s: Resolve = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDockerImage_ResolveErrorIsActionable guards the wording: an
// operator reading it must learn which value was rejected, what shape is
// expected, and that extras.docker.image accepts anything.
func TestDockerImage_ResolveErrorIsActionable(t *testing.T) {
	es := DockerImage{
		Image:      "docker.elastic.co/elasticsearch/elasticsearch:%s",
		Default:    "9.5.3",
		TagPattern: regexp.MustCompile(`^\d+\.\d+\.\d+$`),
		TagHint:    "a full x.y.z tag, e.g. 9.5.3",
	}
	_, err := es.Resolve(map[string]string{"version": "7"})
	if err == nil {
		t.Fatal("Resolve accepted a major-only version")
	}
	for _, want := range []string{`"7"`, "x.y.z", "extras.docker.image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
