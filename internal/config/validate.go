package config

import (
	"errors"
	"fmt"
)

// validateSemantic enforces cross-field rules that go-playground/
// validator struct tags cannot express:
//
//  1. Exactly one repository must carry role:"engine-provider" (or the
//     v0.3 alias "db-provider") when at least one `engines:` entry is
//     present. The bough host cd's into that repo to issue
//     `nix run --impure '.#mysql' -- up`, so an ambiguous (>1) or
//     absent (0) provider produces an undefined launch site.
//  2. Each Engine.PortRanges entry must be a [low, high] pair with
//     low < high so the allocator never traps in an infinite probe.
//     Every engine must have at least one entry.
//  3. Each Ports[<kind>].Range must satisfy the same low<high
//     constraint.
//  4. Engine.Kind values must be unique — spawning two
//     `bough-plugin-mysql` instances for the same worktree would
//     clash on /tmp socket path.
//
// All semantic errors are accumulated and returned as a single joined
// error so a config-file author sees every problem at once instead of
// fixing them one-by-one across multiple runs.
func (c *Config) validateSemantic() error {
	var errs []error

	if len(c.Engines) > 0 {
		nProvider := 0
		for _, r := range c.Repositories {
			if r.Role == "engine-provider" || r.Role == "db-provider" {
				nProvider++
			}
		}
		switch nProvider {
		case 0:
			errs = append(errs, errors.New("config: at least one repository must have role: engine-provider when `engines:` is non-empty"))
		case 1:
			// happy path
		default:
			errs = append(errs, fmt.Errorf("config: exactly one repository may have role: engine-provider, found %d", nProvider))
		}
	}

	seenKind := map[string]bool{}
	for i, eng := range c.Engines {
		if seenKind[eng.Kind] {
			errs = append(errs, fmt.Errorf("config: engines[%d].kind=%q is duplicated", i, eng.Kind))
		}
		seenKind[eng.Kind] = true
		if len(eng.PortRanges) == 0 {
			errs = append(errs, fmt.Errorf("config: engines[%d].port_ranges is empty; declare at least {main: [low, high]}", i))
		}
		for role, pr := range eng.PortRanges {
			if pr[0] <= 0 || pr[1] <= pr[0] {
				errs = append(errs, fmt.Errorf("config: engines[%d].port_ranges[%s]=%v must be [low,high] with 0<low<high", i, role, pr))
			}
		}
	}

	for kind, pr := range c.Ports {
		if pr.Range[0] <= 0 || pr.Range[1] <= pr.Range[0] {
			errs = append(errs, fmt.Errorf("config: ports[%s].range=%v must be [low,high] with 0<low<high", kind, pr.Range))
		}
	}

	seenRepo := map[string]bool{}
	for i, r := range c.Repositories {
		if seenRepo[r.Name] {
			errs = append(errs, fmt.Errorf("config: repositories[%d].name=%q is duplicated", i, r.Name))
		}
		seenRepo[r.Name] = true
	}

	return errors.Join(errs...)
}
