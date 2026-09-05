package api

import (
	"fmt"
	"regexp"
)

// DockerImage resolves the image an engine plugin runs from the two
// channels the host supplies in UpReq.Extras: `docker.image` (a verbatim
// ref, the escape hatch for anything this template cannot express) and
// `version` (the tag fragment `engines[].version` carries).
//
// TagPattern is what makes a wrong version fail at Up instead of at the
// pull: a registry that publishes only full x.y.z tags turns
// `version: "9"` into a ref that has never existed, and the pull error
// ("manifest unknown") names neither the YAML key nor the fix.
//
// Shared by the four engine plugins, which differ only in the template,
// the default tag and the tag shapes their registry publishes.
type DockerImage struct {
	// Image is the image ref with exactly one %s, filled with the tag.
	Image string
	// Default is the tag used when extras carries no version. The host
	// always sends one (engines[].version is required), so this governs
	// the conformance suite, the smoke tool and direct API callers.
	Default string
	// TagPattern accepts the tag shapes the registry actually publishes.
	TagPattern *regexp.Regexp
	// TagHint completes "expects …" in the rejection message.
	TagHint string
}

// Resolve returns the image ref to pull, or an error naming both the
// offending version and the two ways out. Callers wrap it with their own
// engine prefix.
func (d DockerImage) Resolve(extras map[string]string) (string, error) {
	if ref := extras["docker.image"]; ref != "" {
		return ref, nil
	}
	tag := extras["version"]
	if tag == "" {
		tag = d.Default
	}
	if d.TagPattern != nil && !d.TagPattern.MatchString(tag) {
		return "", fmt.Errorf("version %q is not a tag the docker backend can pull (expects %s); set version accordingly or set extras.docker.image to an explicit image ref", tag, d.TagHint)
	}
	return fmt.Sprintf(d.Image, tag), nil
}
