package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
	"github.com/ikeikeikeike/bough/internal/observe"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
	"github.com/ikeikeikeike/bough/internal/termio"
)

// newDoctorCmd wires the top-level `bough doctor` alias for
// `bough hook doctor`. Round 5 review insisted the transparency
// check (= "what is bough actually running on my behalf, and how
// much is it costing me") be reachable without remembering the
// `hook` namespace — the doctor is the operator's first stop when
// the automation surface starts to feel surprising.
//
// Both spellings render the exact same report. The v0.9 continuous-
// learning posture (= Claude CLI on PATH, Anthropic env scrub,
// LLM limiter defaults, homunculus root) is appended after the
// v0.7 hook + observer block.
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report bough's hook wiring + observer + cost posture (alias for `bough claude hook doctor`)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runDoctor(c)
		},
	}
}

// renderContinuousLearningPosture appends the v0.9 surface to the
// doctor output. The order is:
//
//  1. Continuous-learning header
//  2. claude CLI presence (= bough observer can only call out when
//     `claude` is on PATH)
//  3. Anthropic API env scrub (= WARN when ANTHROPIC_API_KEY etc.
//     are exported in the operator's shell, even though bough
//     spawns the subprocess with them stripped — exported keys
//     still affect the operator's interactive Claude Code session)
//  4. Self-DoS cap defaults (= reminds the operator what bough will
//     refuse to do on their behalf)
//  5. Homunculus root (= where the corpus lives on disk)
func renderContinuousLearningPosture(w io.Writer) {
	fmt.Fprintln(w) // blank line between the hook/observer/cost block and this one
	st := termio.NewStyler(w)

	// Each line reports its own status; the section header takes the worst so
	// a missing claude CLI (the one hard error here) paints the whole block
	// [✗], while an exported API key paints it [!].
	claudeStatus, claudeLine := claudeCLILine()
	envStatus, envLines := anthropicEnvLines()
	autostart, running := observerAutostartState()
	daemonStatus, daemonLine := observerAutostartLine(autostart, running)

	fmt.Fprintf(w, "%s Continuous learning (v0.9):\n",
		st.Section(termio.Worst(claudeStatus, envStatus, daemonStatus)))

	fmt.Fprintf(w, "    %s claude CLI        %s\n", st.Mark(claudeStatus), claudeLine)
	for i, line := range envLines {
		// The first env line carries the mark; continuation lines (the named
		// vars, the unset hint) are indented plainly under it.
		if i == 0 {
			fmt.Fprintf(w, "    %s Anthropic env     %s\n", st.Mark(envStatus), line)
			continue
		}
		fmt.Fprintf(w, "        %s\n", line)
	}
	fmt.Fprintf(w, "    %s Self-DoS caps     %d calls/session, %d calls/hour, %d failure breaker, %s cooldown\n",
		st.Mark(termio.StatusNeutral),
		claudecli.DefaultMaxCallsPerSession,
		claudecli.DefaultMaxCallsPerHour,
		claudecli.DefaultCircuitBreakerN,
		claudecli.DefaultCircuitCooldown,
	)
	rootStatus, rootLine := homunculusRootLine()
	fmt.Fprintf(w, "    %s homunculus root   %s\n", st.Mark(rootStatus), rootLine)
	fmt.Fprintf(w, "    %s observer daemon   %s\n", st.Mark(daemonStatus), daemonLine)

	// Every gate line below is computed from the same three facts — the
	// monorepo root, the project identity, and the config. Resolve them
	// once here: derived four times they were four chances to disagree
	// about which project doctor is reporting on.
	env := newGateEnv()

	// Policy gate posture + how many minted instincts are currently held.
	// Surfacing the held count is deliberate (invariant: a silent hold is
	// indistinguishable from data loss); the count in the message IS the
	// signal that a command-shaped instinct was withheld for review.
	gateEnabled, gateHeld, gateBatches := policyGateState(env)
	gateStatus, gateLine := policyGateLine(gateEnabled, gateHeld, gateBatches)
	fmt.Fprintf(w, "    %s policy gate       %s\n", st.Mark(gateStatus), gateLine)

	// The optional layers report their own posture. Both are inert
	// without local files, and an operator who assumes a guard is running
	// when it is not is worse off than one who knows it is off.
	denyStatus, denyLine := denylistLine(denylistActive(env))
	fmt.Fprintf(w, "    %s   denylist        %s\n", st.Mark(denyStatus), denyLine)
	groundStatus, groundLine := groundingLine(env)
	fmt.Fprintf(w, "    %s   rule grounding  %s\n", st.Mark(groundStatus), groundLine)

	// The exclusion switch is decided by evidence, not by the config flag
	// alone. Printing WAIT with the specific missing precondition is the
	// point: "not ready (0.62)" tells an operator nothing about what to
	// go do.
	exStatus, exLines := exclusionReadinessLines(env)
	for i, line := range exLines {
		if i == 0 {
			fmt.Fprintf(w, "    %s   skill exclusion %s\n", st.Mark(exStatus), line)
			continue
		}
		fmt.Fprintf(w, "          %s\n", line)
	}
}

// gateEnv is the environment every continuous-learning posture line is
// computed from: which monorepo, which project, and what the config
// says. Resolved once per doctor run and passed down, so the lines
// cannot disagree about their subject and the same files are not walked
// four times.
//
// A zero value is the honest "could not resolve" state; hasRoot /
// hasIdent report which parts are usable so each line degrades on its
// own rather than the whole block failing.
type gateEnv struct {
	// root is empty exactly when the cwd could not be resolved, and
	// ident.ID is empty exactly when identity detection failed — so the
	// two are their own presence flags. Separate booleans would have to
	// be kept in lockstep by hand at every assignment, and a flag that
	// drifts from its value produces a posture line describing a project
	// that was never resolved: the disagreement this type prevents.
	root  string
	ident homunculus.ProjectIdentity
	cfg   *config.Config
}

func (e gateEnv) hasRoot() bool  { return e.root != "" }
func (e gateEnv) hasIdent() bool { return e.ident.ID != "" }

// newGateEnv is the constructor for the type above, not a shared helper:
// it exists so the resolution happens exactly once per doctor run.
func newGateEnv() gateEnv {
	var env gateEnv
	cwd, err := os.Getwd()
	if err != nil {
		return env
	}
	env.root = resolveMonorepoRoot(cwd)
	if cfg, cerr := loadConfigQuiet(resolveConfigPath(&cobra.Command{}, env.root)); cerr == nil {
		env.cfg = cfg
	}
	if ident, ierr := homunculus.DetectIdentity(env.root); ierr == nil {
		env.ident = ident
	}
	return env
}

// exclusionReadinessLines renders the readiness verdict for suppressing
// skill-covered instincts, naming each unmet precondition.
func exclusionReadinessLines(env gateEnv) (termio.Status, []string) {
	if !env.hasRoot() {
		return termio.StatusNeutral, []string{"WAIT — cannot resolve the project root"}
	}
	if !env.hasIdent() {
		return termio.StatusNeutral, []string{"WAIT — no project identity here"}
	}
	layout := homunculus.NewLayout()
	requested := env.cfg != nil && env.cfg.Instinct.ExcludeSkillCovered
	// doctor RENDERS the advisory drift line, so it asks for it here. The
	// injector deliberately does not.
	r := evolve.ExclusionReadiness(
		layout.EvolvedSkillsDir(env.ident.ID),
		layout.TelemetryFile(env.ident.ID),
		layout.SkillCoverageFile(env.ident.ID),
		time.Now(),
		evolve.DefaultExclusionWindow(),
	).WithAdvisory()
	switch {
	case r.Ready() && requested:
		return termio.StatusOK, append([]string{"ON — skill-covered instincts are not also pushed"}, advisoryNotes(r)...)
	case r.Ready():
		return termio.StatusNeutral, append([]string{"READY but not requested (set instinct.exclude_skill_covered: true)"}, advisoryNotes(r)...)
	}
	lines := []string{"WAIT — not safe to stop pushing skill-covered instincts:"}
	for _, b := range r.Blockers() {
		lines = append(lines, "- "+b.Name+": "+b.Detail)
	}
	lines = append(lines, advisoryNotes(r)...)
	if requested {
		lines = append(lines, "(requested in .bough.yaml, held by the gate — nothing is being suppressed)")
	}
	return termio.StatusNeutral, lines
}

// advisoryNotes renders the non-blocking checks that FAILED. Both
// branches above call it, because the advisory was previously computed
// on every run — one os.Stat per registered skill — and then dropped:
// the Ready branch never looked at Checks and the WAIT branch printed
// only Blockers(). A diagnostic nobody can see is worse than none,
// since the cost is paid and the comment claims it is rendered.
func advisoryNotes(r evolve.Readiness) []string {
	var out []string
	for _, c := range r.Checks {
		if c.Blocking || c.Passed {
			continue
		}
		out = append(out, "note: "+c.Name+": "+c.Detail)
	}
	return out
}

// denylistActive reports whether the denylist sidecar is loaded for the
// resolved monorepo.
func denylistActive(env gateEnv) bool {
	if !env.hasRoot() {
		return false
	}
	configured := ""
	if env.cfg != nil {
		configured = env.cfg.Instinct.Gate.DenylistPath
	}
	return loadDenylistQuiet(env.root, configured).Active()
}

// denylistLine renders the denylist posture. Inert is neutral, never an
// error: the layer is opt-in by having a file, and most projects never
// need one.
func denylistLine(active bool) (termio.Status, string) {
	if !active {
		return termio.StatusNeutral, "OFF — no " + DefaultDenylistPath + " (optional; see docs/denylist.template.txt)"
	}
	return termio.StatusOK, "ON — terms loaded from the untracked sidecar"
}

// groundingLine reports whether rule grounding has governance to work
// with, and names the documents it read: "we found no rule" is only
// actionable if the operator knows which files were consulted.
func groundingLine(env gateEnv) (termio.Status, string) {
	if !env.hasRoot() {
		return termio.StatusNeutral, "OFF — cannot resolve the project root"
	}
	root := env.root
	var configured []string
	if env.cfg != nil {
		configured = env.cfg.Instinct.Gate.GovernancePaths
	}
	g := instinctgate.LoadGovernance(governancePaths(root, configured))
	if !g.Active() {
		return termio.StatusNeutral, "OFF — no rule documents found (checked " + strings.Join(defaultGovernancePaths, ", ") + ")"
	}
	names := make([]string, 0, len(g.Sources))
	for _, s := range g.Sources {
		if rel, rerr := filepath.Rel(root, s); rerr == nil {
			names = append(names, rel)
			continue
		}
		names = append(names, s)
	}
	return termio.StatusOK, "ON — grounded against " + strings.Join(names, ", ")
}

// policyGateState resolves the deterministic policy-gate posture for the
// monorepo at the current cwd: whether the gate is enabled, and how many
// minted instincts sit in quarantine right now (held whole, never
// deleted). Best-effort — no config / no project reports the default-on
// posture with a zero held count.
func policyGateState(env gateEnv) (enabled bool, held, batches int) {
	enabled = true
	if env.cfg != nil {
		enabled = env.cfg.Instinct.GateEnabled()
	}
	if !env.hasIdent() {
		return enabled, 0, 0
	}
	held, batches = countQuarantined(homunculus.NewLayout().QuarantineDir(env.ident.ID))
	return enabled, held, batches
}

// countQuarantined tallies held instincts across the dated batch
// subdirectories of a quarantine dir. The per-batch REPORT.md is not an
// instinct, so it is not counted; only <id>.md files are.
func countQuarantined(dir string) (held, batches int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		batchEntries, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		n := 0
		for _, be := range batchEntries {
			if filepath.Ext(be.Name()) == ".md" && be.Name() != "REPORT.md" {
				n++
			}
		}
		if n > 0 {
			batches++
			held += n
		}
	}
	return held, batches
}

// policyGateLine renders the gate posture as a status + message. Every
// state is neutral or OK, never an error: an OFF gate is a deliberate
// operator choice, and held instincts are the guard functioning, not a
// fault — so the section header is never escalated by the gate.
func policyGateLine(enabled bool, held, batches int) (termio.Status, string) {
	if !enabled {
		return termio.StatusNeutral, "OFF — minted instincts are not screened (set instinct.gate.enabled: true to turn on)"
	}
	if held == 0 {
		return termio.StatusOK, "ON — 0 held (screens command-shaped forbidden actions only; not a completeness claim)"
	}
	return termio.StatusNeutral, fmt.Sprintf(
		"ON — %d held across %d batch(es); review .policy-quarantine/*/REPORT.md, restore is a reversible mv",
		held, batches)
}

// claudeCLILine reports whether the claude binary bough shells out to is on
// PATH. Absent is a hard error — the observer cannot mint without it.
func claudeCLILine() (termio.Status, string) {
	if bin, err := exec.LookPath("claude"); err == nil {
		return termio.StatusOK, bin
	}
	return termio.StatusError, "not on PATH — `bough observer run-once` will refuse to spawn until you install Claude Code"
}

// anthropicEnvLines returns the API-key-scrub posture. A clean env is one OK
// line; exported vars are a warning plus one line per offending var and the
// unset hint. bough strips these from the subprocess, so it is a warning about
// the operator's OWN interactive session, not a bough fault.
func anthropicEnvLines() (termio.Status, []string) {
	apiVars := observe.DetectAnthropicAPIVars(os.Environ())
	if len(apiVars) == 0 {
		return termio.StatusOK, []string{"no API key vars exported (subscription auth path is clean)"}
	}
	lines := []string{"exported API key vars detected — bough strips these from the subprocess env, but the operator's interactive `claude` session may still flip to API billing:"}
	for _, v := range apiVars {
		lines = append(lines, "• "+v)
	}
	return termio.StatusWarn, append(lines, "run `unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN` to clear")
}

// homunculusRootLine reports whether the corpus root exists yet. Missing is
// neutral, not an error — it is created on the first mint.
func homunculusRootLine() (termio.Status, string) {
	layout := homunculus.NewLayout()
	if _, err := os.Stat(layout.Root); err == nil {
		return termio.StatusOK, layout.Root
	}
	return termio.StatusNeutral, layout.Root + " (will be created on first `bough observer run-once`)"
}

// observerAutostartState resolves whether this monorepo opts into the minting
// daemon and whether one is up right now. Best-effort: no config / no project
// reports (false, false). Resolves the config via resolveConfigPath (the same
// canonical .bough.yaml → legacy .worktree-isolation.yaml fallback every other
// bough command uses) so a monorepo that has not renamed its legacy config
// still gets an accurate answer rather than a false OFF.
func observerAutostartState() (autostart, running bool) {
	if cwd, err := os.Getwd(); err == nil {
		root := resolveMonorepoRoot(cwd)
		if cfg, err := loadConfigQuiet(resolveConfigPath(&cobra.Command{}, root)); err == nil {
			autostart = cfg.Instinct.Observer.Autostart
		}
		running = observerDaemonRunning(root)
	}
	return autostart, running
}

// observerAutostartLine renders the observer-autostart posture (off /
// on+running / on+idle) as a status + message, unit-testable without a real
// daemon or an on-disk config. An OFF autostart never claims the daemon is
// running even if one happens to be up from a manual start. On+running is the
// only OK state; both other states are neutral (a correct, deliberate choice
// either way, not a fault).
func observerAutostartLine(autostart, running bool) (termio.Status, string) {
	switch {
	case autostart && running:
		return termio.StatusOK, "autostart ON — daemon running (minting instincts each interval via claude --print)"
	case autostart:
		return termio.StatusNeutral, "autostart ON — not running yet (starts on the next UserPromptSubmit)"
	default:
		return termio.StatusNeutral, "autostart OFF — LLM minting is manual (`bough observer start` or .bough.yaml instinct.observer.autostart)"
	}
}
