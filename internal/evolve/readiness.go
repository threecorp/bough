package evolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The exclusion switch — stop pushing what a skill already delivers —
// cannot be judged from the code. It depends on whether the PULL path is
// actually firing in this operator's setup, which is a fact about the
// world, not about the implementation. Flipping it early removes the
// knowledge from both paths at once: the skill exists, nothing loads it,
// and the instincts are no longer injected. The operator sees the loop
// go quiet and has no way to tell which half broke.
//
// So the decision is a mechanical readiness check, not a judgement call,
// and it reports WAIT with the specific thing that is missing rather
// than a score. A gate that says "not ready (0.62)" tells you nothing
// about what to go do.

// ReadinessCheck is one precondition with its current state. Blocking
// separates "this must hold" from advisory context, so the caller can
// render both without deciding which is which.
type ReadinessCheck struct {
	Name     string
	Passed   bool
	Detail   string
	Blocking bool
}

// Readiness is the verdict for the exclusion switch.
type Readiness struct {
	Checks []ReadinessCheck
	// Coverage is the registry the checks parsed. It travels with the
	// verdict so a caller acting on Ready() does not read and parse the
	// same file again — the injector runs on EVERY prompt, and that second
	// read is paid on the interactive hot path. It is nil exactly when the
	// registry could not be read, which is already a failed blocking check,
	// so a Ready() verdict always carries a usable registry.
	Coverage *SkillCoverage
}

// Ready reports whether every BLOCKING check passed.
func (r Readiness) Ready() bool {
	for _, c := range r.Checks {
		if c.Blocking && !c.Passed {
			return false
		}
	}
	return true
}

// Blockers returns the checks standing in the way, so the caller can
// print what to go do rather than a bare refusal.
func (r Readiness) Blockers() []ReadinessCheck {
	var out []ReadinessCheck
	for _, c := range r.Checks {
		if c.Blocking && !c.Passed {
			out = append(out, c)
		}
	}
	return out
}

// minDeployedSkills is how many skills must exist before suppressing
// pushed instincts is even a question. Below this the corpus has not
// produced a portfolio worth relying on, and the exclusion would trade
// working delivery for a nearly empty one.
const minDeployedSkills = 3

// ExclusionReadiness evaluates whether the skill-covered instincts may
// stop being pushed. It answers from artifacts on disk — deployed
// skills, the coverage registry, the project's rules directory — because
// every one of those is something the operator can go look at when the
// gate says WAIT.
//
// The one check this deliberately does NOT make is "does the corpus look
// big enough": size is not evidence that delivery works.
func ExclusionReadiness(skillsDir, deployedSkillsDir, coveragePath string) Readiness {
	var r Readiness

	deployed := countSkillDirs(deployedSkillsDir)
	r.Checks = append(r.Checks, ReadinessCheck{
		Name:     "skills deployed where the host can load them",
		Passed:   deployed >= minDeployedSkills,
		Blocking: true,
		Detail: fmt.Sprintf("%d skill(s) under %s, need %d — below this the portfolio is not worth trading working delivery for",
			deployed, deployedSkillsDir, minDeployedSkills),
	})

	cov, err := LoadSkillCoverage(coveragePath)
	switch {
	case err != nil:
		r.Checks = append(r.Checks, ReadinessCheck{
			Name: "coverage registry readable", Passed: false, Blocking: true,
			Detail: fmt.Sprintf("%v — without it there is no record of what a skill delivers", err),
		})
		return r
	default:
		n := len(cov.CoveredIDs())
		r.Checks = append(r.Checks, ReadinessCheck{
			Name:     "coverage registry records what skills deliver",
			Passed:   n > 0,
			Blocking: true,
			Detail: fmt.Sprintf("%d instinct id(s) recorded as covered in %s — an empty registry would suppress nothing, or everything, depending on how it is read",
				n, coveragePath),
		})
		// Advisory: a coverage entry naming ids that no longer exist as
		// skills means the registry has drifted from the portfolio.
		stale := staleCoverageSlugs(cov, skillsDir)
		r.Checks = append(r.Checks, ReadinessCheck{
			Name:     "coverage registry matches the skills on disk",
			Passed:   len(stale) == 0,
			Blocking: false,
			Detail:   staleDetail(stale),
		})
	}
	r.Coverage = cov
	return r
}

// countSkillDirs counts the deployed skill entries. Both a directory
// (bough's own layout) and a symlink to one count: the deploy step
// symlinks, and a check that only saw real directories would report zero
// on a correctly deployed portfolio.
func countSkillDirs(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, serr := os.Stat(filepath.Join(dir, e.Name()))
		if serr == nil && info.IsDir() {
			n++
		}
	}
	return n
}

// staleCoverageSlugs lists registry entries whose skill is gone from
// disk. Their ids would stay suppressed while nothing delivers them.
func staleCoverageSlugs(cov *SkillCoverage, skillsDir string) []string {
	var stale []string
	for slug := range cov.BySkill {
		if _, err := os.Stat(filepath.Join(skillsDir, slug, "SKILL.md")); err != nil {
			stale = append(stale, slug)
		}
	}
	return stale
}

func staleDetail(stale []string) string {
	if len(stale) == 0 {
		return "every recorded skill still exists on disk"
	}
	return fmt.Sprintf("%d registry entr(y/ies) name a skill that is gone (%s) — their ids would stay suppressed while nothing delivers them",
		len(stale), strings.Join(stale, ", "))
}
