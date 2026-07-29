package evolve

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/telemetry"
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
//
// # Existence is not evidence
//
// An earlier version of this gate counted skills on disk and called that
// readiness. A deployed skill nothing ever loads is indistinguishable,
// on disk, from one the model reaches for daily — so that gate could
// pass while the pull path had never fired even once, which is precisely
// the state it exists to prevent. It now counts PULLS, recorded by the
// host as they happen (internal/telemetry).
//
// # A gate that cannot see its input is worse than no gate
//
// The failure this design guards against is not a wrong threshold, it is
// a reader that cannot parse its writer: it reports "nothing happened",
// which is a believable answer, and so it is believed. Two things keep
// that from being silent here. Every count goes through one decode path
// (telemetry.parseLine), so no two checks can disagree; and Drift is
// rendered as its own BLOCKING row, so a parse failure refuses to pass
// the gate instead of quietly reading as an absence of usage.

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
	// advisory computes the non-blocking drift check on demand. It is
	// never needed to decide Ready(), so the work is not done unless a
	// caller is going to show it.
	advisory func() ReadinessCheck
}

// WithAdvisory materialises the deferred non-blocking checks. Call it
// only where the result is rendered — the verdict itself never needs them.
func (r Readiness) WithAdvisory() Readiness {
	if r.advisory != nil {
		r.Checks = append(r.Checks, r.advisory())
		r.advisory = nil
	}
	return r
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

// ExclusionWindow is how far back pulls are counted. Two weeks is long
// enough that a quiet week does not reset the verdict and short enough
// that a portfolio abandoned months ago stops qualifying.
type ExclusionWindow struct {
	// History is the span the gate reads.
	History time.Duration
	// Recent is the sub-span the "still being used" check reads. It is a
	// COUNT inside a window, never a difference between two snapshots:
	// a difference reports a consolidation — slugs leaving the set — as
	// lost usage, and can be zero by construction when the baseline it
	// picks is the reading it is compared against.
	Recent time.Duration
	// MinPulledSlugs is how many distinct skills must have been pulled.
	// Below this the portfolio has not shown it can carry delivery.
	MinPulledSlugs int
}

// DefaultExclusionWindow is seeded here rather than as package consts so
// a test can ask for a 1-day history without waiting one, and so an
// operator-facing knob has one place to appear if it is ever needed.
func DefaultExclusionWindow() ExclusionWindow {
	return ExclusionWindow{
		History:        14 * 24 * time.Hour,
		Recent:         7 * 24 * time.Hour,
		MinPulledSlugs: 3,
	}
}

// maxNamed bounds how many slugs a detail line lists. Anything cut is
// SAID to be cut: a bounded list that reads as the whole set is how a
// "no silent caps" rule gets broken by the code that states it.
const maxNamed = 5

// ExclusionReadiness evaluates whether the skill-covered instincts may
// stop being pushed. It answers from two artifacts: the telemetry log —
// what the host actually did — and the coverage registry, which says
// what a skill delivers. Only pulls of slugs the registry knows about
// count: three pulls of skills that cover no instincts are not evidence
// that suppressing instincts is safe.
func ExclusionReadiness(skillsDir, telemetryPath, coveragePath string, now time.Time, win ExclusionWindow) Readiness {
	var r Readiness

	cov, cerr := LoadSkillCoverage(coveragePath)
	if cerr != nil {
		r.Checks = append(r.Checks, ReadinessCheck{
			Name: "coverage registry readable", Passed: false, Blocking: true,
			Detail: fmt.Sprintf("%v — without it there is no record of what a skill delivers", cerr),
		})
		return r
	}
	covered := len(cov.CoveredIDs())
	r.Checks = append(r.Checks, ReadinessCheck{
		Name:     "coverage registry records what skills deliver",
		Passed:   covered > 0,
		Blocking: true,
		Detail: fmt.Sprintf("%d instinct id(s) recorded as covered in %s — an empty registry would suppress nothing, or everything, depending on how it is read",
			covered, coveragePath),
	})

	log, lerr := telemetry.Load(telemetryPath)
	if lerr != nil {
		r.Checks = append(r.Checks, ReadinessCheck{
			Name: "usage telemetry readable", Passed: false, Blocking: true,
			Detail: fmt.Sprintf("%v — with no record of what the host did, the pull path cannot be shown to work", lerr),
		})
		return r
	}

	// The self-check comes FIRST among the telemetry rows. If the reader
	// cannot make sense of what the writer produced, every number below
	// it is a zero that means "I could not tell", and presenting those
	// first is how a broken parser reads as a finding.
	drift := log.Drift()
	r.Checks = append(r.Checks, ReadinessCheck{
		Name:     "gate self-check",
		Passed:   len(drift) == 0,
		Blocking: true,
		Detail:   driftDetail(drift),
	})

	pulls := telemetry.PullsBySlug(log.Window(now.Add(-win.History), now))
	inPortfolio := pulledSlugsCoveredBy(pulls, cov)
	r.Checks = append(r.Checks, ReadinessCheck{
		Name:     "the pull path is firing",
		Passed:   len(inPortfolio) >= win.MinPulledSlugs,
		Blocking: true,
		Detail: fmt.Sprintf("%d of the registry's skill(s) were pulled in the last %s%s, need %d — a deployed skill nothing loads is not delivery%s",
			len(inPortfolio), roundDays(win.History), namedList(inPortfolio), win.MinPulledSlugs,
			unregisteredNote(pulls, inPortfolio)),
	})

	recent := telemetry.PullsBySlug(log.Window(now.Add(-win.Recent), now))
	recentN := totalPulls(pulledSlugsCoveredBy(recent, cov), recent)
	r.Checks = append(r.Checks, ReadinessCheck{
		Name:     "and still firing",
		Passed:   recentN > 0,
		Blocking: true,
		Detail:   fmt.Sprintf("%d pull(s) in the last %s%s", recentN, roundDays(win.Recent), staleSuffix(log, now)),
	})

	r.Checks = append(r.Checks, historyCheck(log, now, win))

	// Advisory: a coverage entry naming a skill that is gone from disk
	// means the registry has drifted from the portfolio.
	//
	// Deferred, because this costs one os.Stat PER REGISTERED SKILL and
	// the injector calls ExclusionReadiness on every prompt while reading
	// only Ready() and Coverage. Paying that walk on the interactive path
	// for a line nobody renders there is latency spent on nothing; doctor
	// calls WithAdvisory when it is about to print it.
	r.advisory = func() ReadinessCheck {
		stale := staleCoverageSlugs(cov, skillsDir)
		return ReadinessCheck{
			Name:     "coverage registry matches the skills on disk",
			Passed:   len(stale) == 0,
			Blocking: false,
			Detail:   staleDetail(stale),
		}
	}
	r.Coverage = cov
	return r
}

// historyCheck states the age it actually measured, not the age it was
// asked about. "+0 in the last 14d" printed against 8 days of history
// names the intent rather than the measurement, and the two are only
// the same once enough time has passed — exactly when the row stops
// mattering.
func historyCheck(log *telemetry.Log, now time.Time, win ExclusionWindow) ReadinessCheck {
	oldest, ok := log.Oldest()
	if !ok {
		return ReadinessCheck{
			Name: "enough history to judge by", Passed: false, Blocking: true,
			Detail: fmt.Sprintf("no telemetry recorded yet — %s of history is needed before a quiet week can be told apart from an unused portfolio",
				roundDays(win.History)),
		}
	}
	have := now.Sub(oldest)
	return ReadinessCheck{
		Name:     "enough history to judge by",
		Passed:   have >= win.History,
		Blocking: true,
		Detail: fmt.Sprintf("the oldest event is %s old, need %s before a quiet week can be told apart from an unused portfolio",
			roundDays(have), roundDays(win.History)),
	}
}

// pulledSlugsCoveredBy narrows the pulled set to skills the coverage
// registry knows about, sorted so a rendered list is stable.
func pulledSlugsCoveredBy(pulls map[string]int, cov *SkillCoverage) []string {
	var out []string
	for slug := range pulls {
		if _, ok := cov.BySkill[slug]; ok {
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out
}

// unregisteredNote exists because two true numbers can read as a
// contradiction. This row counts pulls of skills the coverage registry
// knows about; the usage summary counts every pull. A project whose
// registry is empty therefore sees "0 pulled" here and "3 pulls" there,
// and an operator reconciling those two by hand is exactly the position
// the gate is supposed to spare them. So when pulls happened but none of
// them are registered, the row says which fact it is reporting.
func unregisteredNote(pulls map[string]int, inPortfolio []string) string {
	other := len(pulls) - len(inPortfolio)
	if other <= 0 {
		return ""
	}
	return fmt.Sprintf(". %d other skill(s) WERE pulled but deliver no recorded instincts, so they do not count here — run the evolve pass to record what they cover", other)
}

func totalPulls(slugs []string, pulls map[string]int) int {
	n := 0
	for _, s := range slugs {
		n += pulls[s]
	}
	return n
}

// staleSuffix says when the newest reading is itself old. Without it a
// zero cannot be told apart from "nothing has been recorded lately",
// and those two call for opposite actions.
func staleSuffix(log *telemetry.Log, now time.Time) string {
	newest, ok := log.Newest()
	if !ok {
		return ""
	}
	age := now.Sub(newest)
	if age < 48*time.Hour {
		return ""
	}
	return fmt.Sprintf("; the newest event is %s old — check that the hooks are still installed", roundDays(age))
}

func namedList(slugs []string) string {
	if len(slugs) == 0 {
		return ""
	}
	if len(slugs) <= maxNamed {
		return " (" + strings.Join(slugs, ", ") + ")"
	}
	return fmt.Sprintf(" (%s — showing %d of %d)", strings.Join(slugs[:maxNamed], ", "), maxNamed, len(slugs))
}

func driftDetail(drift []string) string {
	if len(drift) == 0 {
		return "the telemetry reads cleanly — the numbers below mean what they say"
	}
	shown := drift
	suffix := ""
	if len(shown) > maxNamed {
		shown = shown[:maxNamed]
		suffix = fmt.Sprintf(" (showing %d of %d)", maxNamed, len(drift))
	}
	return fmt.Sprintf("SCHEMA DRIFT — the reader could not make sense of the log%s: %s. Fix the parser before trusting any row above; a count it cannot read looks exactly like a count of zero",
		suffix, strings.Join(shown, "; "))
}

func roundDays(d time.Duration) string {
	days := d.Hours() / 24
	if days < 1 {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", days)
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
	sort.Strings(stale)
	return stale
}

func staleDetail(stale []string) string {
	if len(stale) == 0 {
		return "every recorded skill still exists on disk"
	}
	return fmt.Sprintf("%d registry entr(y/ies) name a skill that is gone%s — their ids would stay suppressed while nothing delivers them",
		len(stale), namedList(stale))
}
