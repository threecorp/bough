package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
	"github.com/ikeikeikeike/bough/internal/retrieve"
	"github.com/ikeikeikeike/bough/internal/telemetry"
)

// runInjectContext resolves the current project's instinct pools and
// writes the confidence-ranked injection block to out. Shared by
// dispatchInjectContext (hook.go's UserPromptSubmit hook path) and
// newInjectContextCmd's RunE (the manual preview command below) so
// the two paths cannot drift apart — before this helper existed they
// already needed an identical fix hand-applied twice (switching
// DetectIdentity(cwd) to DetectIdentity(resolveMonorepoRoot(cwd)) in
// both places).
func runInjectContext(out io.Writer, root string, opts inject.Options) error {
	// The hook's whole budget is 5s; selection gets opts.SelfLimit of it
	// so the lessons block can still print if the corpus scan runs long.
	// Checked between phases rather than mid-scan: the scan is the only
	// unbounded step, and a check after it converts "the hook timed out
	// and the prompt lost every block" into "the prompt lost the
	// instinct block only".
	start := time.Now()
	deadline := start.Add(opts.WithDefaults().SelfLimit)
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("inject-context: getwd: %w", err)
		}
		cwd = w
	}
	// Resolve identity from the MONOREPO ROOT, not the raw cwd: the
	// observation writer (resolveHomunculusObsPath), the observer
	// daemon that mints the instinct files, session-end, and preserve
	// all pool to DetectIdentity(resolveMonorepoRoot(cwd)). In a
	// multi-repo monorepo / worktree (the .bough.yaml root is not a
	// git repo; each sub-repo has its own origin) the raw-cwd id would
	// differ from the writer's id, so this injector would read an
	// empty project and surface nothing — the loop's whole payoff.
	monoRoot := resolveMonorepoRoot(cwd)
	ident, err := homunculus.DetectIdentity(monoRoot)
	if err != nil {
		// A non-git directory is not an error for a hook — just
		// emit nothing so the prompt is unaffected.
		return nil
	}
	layout := homunculus.NewLayout()
	project, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	// Where the session is working is part of the query. Derived
	// relative to the monorepo root, so the repo name itself — which
	// matches a large share of the corpus and would drown short prompts
	// — contributes nothing.
	cfg := injectConfig(monoRoot)
	if len(opts.ContextTokens) == 0 {
		opts.ContextTokens = retrieve.ContextTokens(monoRoot, cwd)
	}
	// The files the session just opened are the strongest statement about
	// what it is working on, and the prompt often does not contain them:
	// "why is this failing?" names nothing. They join as context tokens, so
	// they feed the exact channel (paths are identifier-shaped), the
	// lexical channel and the relevance floor — the same three places the
	// cwd's own segments feed.
	opts.ContextTokens = append(opts.ContextTokens, opts.RecentFiles...)
	// A non-English prompt reaches an English corpus through the operator's
	// alias file or not at all: nothing tokenizes 予約 into "booking", so
	// without this the lexical channel scores a Japanese prompt against
	// vocabulary it cannot contain.
	if cfg != nil {
		opts.ContextTokens = append(opts.ContextTokens,
			aliasExpansions(resolveUnderRoot(monoRoot, cfg.Instinct.Select.AliasPath), opts.Prompt)...)
	}
	// Suppressing skill-covered instincts is gated on evidence, not on a
	// config flag alone: an operator who turns it on before the pull path
	// works loses the knowledge from both paths at once. The readiness
	// check is the authority, so a premature `exclude_skill_covered: true`
	// cannot take effect.
	// One exclusion set with one consumer — this injector. The operator's
	// manual register and the skill-covered set answer different questions
	// ("I have heard this enough" vs "a skill already delivers this") but
	// they are the same decision at the point of delivery, and a second
	// consumer with its own idea of "covered" is how the two paths drift
	// into disagreeing about what was pushed.
	if opts.ExcludeIDs == nil {
		opts.ExcludeIDs = skillCoveredExclusions(monoRoot, ident.ID, layout)
		if cfg != nil {
			for id := range manualExclusions(resolveUnderRoot(monoRoot, cfg.Instinct.Select.ExclusionsPath)) {
				if opts.ExcludeIDs == nil {
					opts.ExcludeIDs = map[string]struct{}{}
				}
				opts.ExcludeIDs[id] = struct{}{}
			}
		}
	}
	// Which family each instinct belongs to, so one family of restatements
	// cannot take the whole block. Stamped by the offline evolve pass
	// because clustering is O(N²) — unaffordable here. Unreadable or
	// missing leaves every instinct unstamped, which means UNCAPPED: the
	// cap exists to trim redundancy, so failing to read it must not start
	// dropping instincts on a guess. `bough doctor` reports the stamped
	// population precisely so that state is visible.
	assignments, aerr := evolve.LoadClusterAssignments(layout.ClusterAssignmentsFile(ident.ID))
	if aerr != nil {
		assignments = nil
	}
	if opts.ClusterOf == nil && assignments != nil {
		opts.ClusterOf = assignments.ByInstinct
	}
	var block string
	var ids []string
	timedOut := time.Now().After(deadline)
	if !timedOut {
		block, ids = inject.Build(project, global, opts)
	}
	// Human-authored corrections outrank minted instincts and are not
	// scored, so they are prepended rather than merged into the ranking
	// — and they are emitted even when nothing cleared the confidence
	// floor, since ground truth does not depend on the corpus having
	// anything to say.
	// Both the config lookup and the file lookup anchor on the SAME
	// monorepo root the identity resolved from. Passing the raw `root`
	// parameter here would read a different (possibly empty) directory,
	// so an operator's configured path would be silently ignored when the
	// hook fires from a sub-repo.
	// Zero = the lessons block's own default budget. It is deliberately
	// NOT derived from opts.MaxBytes: the two blocks have separate
	// allowances that sum under the total, so tuning the instinct block
	// must not silently shrink the operator's corrections.
	var lessonPaths []string
	if cfg != nil {
		lessonPaths = cfg.Instinct.Lessons.Paths
	}
	lessons := inject.LessonsBlock(monoRoot, lessonPaths, 0)
	// The selection is recorded even when it chose NOTHING. A prompt that
	// correctly selected zero instincts is a data point — the share of
	// empty selections is a selector-health signal, and skipping the
	// write made that rate structurally unmeasurable: absence of a line
	// and an empty selection looked identical. Best-effort: the hook is
	// on the prompt path and telemetry must never cost a turn. The ids
	// (not just a count) are what answers whether retrieval reaches the
	// tail of the corpus or keeps cycling the same few notes.
	telemetry.NewWriter(homunculus.NewLayout().TelemetryFile(ident.ID)).
		AppendBestEffort(telemetry.Event{
			Kind: telemetry.KindSelection,
			IDs:  ids,
			N:    len(ids),
			MS:   float64(time.Since(start).Microseconds()) / 1000.0,
		})
	// The quarantine notice is PREPENDED, ahead of everything: the gate's
	// SessionStart/observer output goes nowhere an operator reads, so the
	// prompt context is the one place a hold is guaranteed to be seen —
	// and a silent hold is indistinguishable from data loss. It clears
	// when the batch gains a REVIEWED marker, because a notice that never
	// clears is a notice that gets ignored.
	notice := quarantineNotice(layout, ident.ID)
	// Routing new arrivals into rules / skills / the tail is the only
	// manual step left, so it is the only one that can silently stop
	// happening — and a corpus filling with restatements is invisible from
	// inside a session. Appended after the block rather than prepended: it
	// is about the corpus's upkeep, not about this turn.
	backlog := arrivalBacklogNotice(project, assignments)
	if notice == "" && lessons == "" && backlog == "" && len(ids) == 0 {
		return nil // nothing to say → clean no-op
	}
	fmt.Fprint(out, notice)
	fmt.Fprint(out, lessons)
	if len(ids) > 0 {
		fmt.Fprint(out, block)
	}
	fmt.Fprint(out, backlog)
	return nil
}

// quarantineNotice announces quarantine batches that still hold notes
// and lack a REVIEWED marker. Empty when there is nothing to review, so
// a healthy corpus adds zero bytes to the prompt.
func quarantineNotice(layout homunculus.Layout, projectID string) string {
	root := layout.QuarantineDir(projectID)
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	batches, held := 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "REVIEWED")); err == nil {
			continue
		}
		n := 0
		files, ferr := os.ReadDir(dir)
		if ferr != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && filepath.Ext(f.Name()) == ".md" && f.Name() != "REPORT.md" {
				n++
			}
		}
		if n == 0 {
			continue
		}
		batches++
		held += n
	}
	if batches == 0 {
		return ""
	}
	return fmt.Sprintf("[bough policy] %d held instinct(s) in %d unreviewed batch(es) — read REPORT.md under %s, restore what belongs (into .staging; the next pass re-judges it), then `touch <batch>/REVIEWED`.\n\n",
		held, batches, root)
}

// arrivalBacklogNotice announces that enough instincts have arrived since
// the last clustering pass to be worth routing. Empty below the threshold,
// so a corpus keeping up adds zero bytes to the prompt.
func arrivalBacklogNotice(project []*homunculus.Instinct, assignments *evolve.ClusterAssignments) string {
	n, overdue := evolve.DefaultArrivalBacklog().Count(project, assignments)
	if !overdue {
		return ""
	}
	return fmt.Sprintf("\n[bough] %d instinct(s) have arrived since the last clustering pass — run `bough evolve` to see what has grown into a family, then `bough evolve --generate` to fold the restatements and route the rest.\n",
		n)
}

// injectConfig loads .bough.yaml for the injector's optional inputs
// (lessons paths, the manual exclusion register, the alias file). A
// missing or unreadable config is not an error: the hook fires on every
// prompt, so it degrades to the conventions and defaults rather than
// failing the turn. nil means "nothing configured".
func injectConfig(root string) *config.Config {
	cfg, err := loadConfigQuiet(resolveConfigPath(&cobra.Command{}, root))
	if err != nil {
		return nil
	}
	return cfg
}

// resolveUnderRoot interprets a configured path relative to the monorepo
// root, the way every other path in .bough.yaml is read. Absolute paths
// are left alone, and empty stays empty so the caller can tell
// "unconfigured" from "configured to the root".
func resolveUnderRoot(root, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// newInjectContextCmd wires `bough inject-context` — the
// UserPromptSubmit hook handler. Claude Code calls it on every user
// prompt; whatever it writes to stdout is folded into the next turn's
// context (= billed as input tokens at the operator's subscription
// rate). The handler selects the highest-confidence instincts for the
// current project + global scope, caps the block at ~9.5 KB, and
// prints it. No LLM call — the hook fires on every keystroke-to-
// response cycle, so it stays pure filesystem.
//
// Wired into .claude/settings.json via `bough hook install`; can also
// be run by hand to preview the block an operator's next prompt would
// receive.
func newInjectContextCmd() *cobra.Command {
	var (
		root     string
		maxBytes int
		maxN     int
		minConf  float64
		prompt   string
	)
	cmd := &cobra.Command{
		Use:   "inject-context",
		Short: "Print the confidence-ranked instinct block for the UserPromptSubmit hook",
		Long: `bough inject-context is the UserPromptSubmit hook handler. It
prints the instincts most relevant to the prompt, for the current
project (+ global scope), so Claude Code folds them into the next
turn's context.

Ranking is by relevance to --prompt, which the hook always passes:
three channels (exact identifier/path hits, BM25 over the corpus, and
recency) are fused, and a candidate no lexical channel found is
dropped even when it is recent — so an off-topic prompt correctly gets
nothing. Confidence is audit data, not a ranking key. Run by hand
WITHOUT --prompt and there is nothing to rank against, so it falls
back to confidence order.

The block is byte-capped (default ~9.5 KB) because the stdout is
billed as input tokens. No claude --print call is made — selection is
pure filesystem.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := inject.Options{
				MaxBytes:     maxBytes,
				MaxInstincts: maxN,
				Prompt:       prompt,
			}
			// Only override MinConfidence when the operator actually
			// passed the flag: --min-confidence 0 is a legitimate "no
			// floor" request, and inject.Options.MinConfidence must see
			// that as an explicit 0.0, not as "unset" (which would
			// silently substitute the 0.50 default and drop every
			// instinct in the real, reachable 0.30-0.49 band).
			if cmd.Flags().Changed("min-confidence") {
				opts.MinConfidence = &minConf
			}
			return runInjectContext(cmd.OutOrStdout(), root, opts)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", 0, "byte cap on the instinct block (default 5000; the lessons block has its own 3000)")
	cmd.Flags().IntVar(&maxN, "max-instincts", 0, "max instincts to render (default 12)")
	cmd.Flags().Float64Var(&minConf, "min-confidence", 0, "drop instincts below this confidence (default 0.50)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "rank against this prompt (the hook passes the real one; empty falls back to confidence order)")
	return cmd
}

// skillCoveredExclusions returns the instinct ids to drop from the
// pushed block — but ONLY when the readiness gate says the pull path can
// carry them. The gate is the authority, not the config flag: an
// operator who sets `exclude_skill_covered: true` before the portfolio
// is deployed would lose that knowledge from both paths at once, and the
// symptom (the loop goes quiet) does not point at the cause.
//
// Returns nil in every uncertain case. Pushing knowledge that is also
// pullable costs prompt budget; NOT pushing knowledge that turns out not
// to be pullable costs the knowledge.
func skillCoveredExclusions(monoRoot, projectID string, layout homunculus.Layout) map[string]struct{} {
	cfg, err := loadConfigQuiet(resolveConfigPath(&cobra.Command{}, monoRoot))
	if err != nil || !cfg.Instinct.ExcludeSkillCovered {
		return nil
	}
	skillsDir := layout.EvolvedSkillsDir(projectID)
	coveragePath := layout.SkillCoverageFile(projectID)
	// The gate reads the coverage registry to judge it, so take the copy
	// it already parsed rather than reading the same file a second time
	// on the prompt hot path. A non-Ready verdict already covers the
	// unreadable case — that check is Blocking — so there is no separate
	// nil test to make here.
	ready := evolve.ExclusionReadiness(skillsDir, layout.TelemetryFile(projectID), coveragePath,
		time.Now(), evolve.DefaultExclusionWindow())
	if !ready.Ready() {
		return nil
	}
	return ready.Coverage.CoveredIDs()
}
