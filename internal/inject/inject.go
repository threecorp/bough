// Package inject builds the context block bough's UserPromptSubmit
// hook prints to stdout. Claude Code folds that stdout into the next
// turn's context, so it is billed as input tokens at the operator's
// subscription rate. The block is therefore capped (~9.5KB default) and
// ordered by RELEVANCE to the current prompt, so the instincts that
// actually bear on this turn land in the window before the cap
// truncates. Without a prompt there is nothing to be relevant to, and
// the confidence order is the fallback.
//
// This package is intentionally LLM-free: the UserPromptSubmit hook
// fires on every prompt, so anything that spawned `claude --print`
// here would add latency + cost to every keystroke-to-response cycle.
// Selection is pure filesystem plus lexical retrieval (internal/retrieve)
// — no embeddings, no network, no model call on the prompt hot path.
package inject

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/retrieve"
)

// DefaultMaxBytes is the ECC-derived cap on the injected block. The
// UserPromptSubmit stdout is billed as input tokens, so the block is
// bounded to balance "useful context" against "cost per prompt".
// ~9.5 KB ≈ a few thousand tokens.
const DefaultMaxBytes = 9500

// DefaultMaxInstincts bounds how many instincts we even consider for
// the block before the byte cap kicks in. Keeps the render loop cheap
// on a 1k-instinct corpus.
const DefaultMaxInstincts = 40

// MinConfidence drops low-confidence instincts from injection
// entirely — a 0.30-confidence guess is more likely to mislead than
// help, and it competes for the byte budget with reliable ones.
const MinConfidence = 0.50

// DefaultClusterCap bounds how many instincts from ONE discovered family
// may take the block. Restatements cluster together, so without a cap a
// prompt that brushes a well-covered subject spends the whole budget
// hearing the same advice five times while every other subject the
// prompt touched gets nothing.
//
// It binds only where the corpus carries a cluster stamp (written by
// `bough evolve --generate`, read via Options.ClusterOf). Unstamped is
// UNCAPPED, not capped-at-one — and `bough doctor` prints the stamped
// population so an unstamped corpus is loud instead of quietly making
// this mechanism inert.
const DefaultClusterCap = 2

// Options tunes the block. Zero values for MaxBytes/MaxInstincts fall
// back to the Default* constants so callers can pass Options{} for
// the standard block. MinConfidence is a pointer specifically because
// 0.0 is a legitimate, meaningful value ("no floor, include
// everything") that a bare float64 could not distinguish from "not
// set" — nil means "not set", any non-nil value (including a pointer
// to 0.0) is used as-is.
type Options struct {
	MaxBytes      int
	MaxInstincts  int
	MinConfidence *float64
	// Prompt is the user's submitted text. When non-empty, selection is
	// RELEVANCE-ranked against it (see internal/retrieve) instead of
	// confidence-ranked. Empty keeps the confidence-ordered fallback,
	// which is all a caller without a prompt can do.
	Prompt string
	// ContextTokens describe where the session is working (sub-project /
	// package names below the project root). See retrieve.ContextTokens
	// for why the repo name itself is deliberately not among them.
	ContextTokens []string
	// ExcludeIDs are instinct ids an evolved skill already delivers.
	// Supplying them drops those instincts from the pushed block so the
	// same knowledge is not both pushed and pullable.
	//
	// The CALLER decides whether to supply this, and by default does not:
	// suppressing the push for knowledge whose pull path has not
	// demonstrably fired removes it from BOTH paths at once. The registry
	// is recorded first, acted on only once there is evidence the pull
	// path works.
	ExcludeIDs map[string]struct{}
	// ClusterOf maps instinct id → the family it clustered into, as
	// stamped by the last evolve pass (evolve.ClusterAssignments). An id
	// absent from the map is UNSTAMPED and therefore uncapped: a missing
	// stamp means "we do not know its family", and guessing it is alone
	// would be a different claim than the data supports.
	ClusterOf map[string]int
	// ClusterCap bounds how many members of one family may be rendered.
	// Zero falls back to DefaultClusterCap.
	ClusterCap int
	// SelfLimit bounds the selection step of the prompt hook. The host
	// gives the whole hook 5 seconds; the default leaves room for the
	// blocks that must still print after selection. Overrun is fail-open:
	// the prompt loses the instinct block, never the turn — deliberately
	// no fallback ranking, because a fallback slower than the thing that
	// timed out blows the same budget twice. A field, not a package
	// const, so a test can exercise the overrun without waiting 3s.
	SelfLimit time.Duration
}

// WithDefaults returns a copy with every unset knob at its default.
// Exported because the cli layer needs the resolved SelfLimit BEFORE
// calling Build — the deadline covers the corpus scan that happens
// first — and re-deriving the default there would put the value in two
// places.
func (o Options) WithDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxInstincts <= 0 {
		o.MaxInstincts = DefaultMaxInstincts
	}
	if o.MinConfidence == nil {
		def := MinConfidence
		o.MinConfidence = &def
	}
	if o.SelfLimit <= 0 {
		o.SelfLimit = 3 * time.Second // hook budget is 5s; the rest must still print
	}
	if o.ClusterCap <= 0 {
		o.ClusterCap = DefaultClusterCap
	}
	return o
}

// ranked is one candidate instinct plus the scope it came from. It is
// package scope rather than local to Build because the relevance path
// hands the pool to a helper and gets it back reordered.
type ranked struct {
	in        *homunculus.Instinct
	isProject bool
}

// Build assembles the injection block from the project + global
// instinct corpora. Selection order:
//
//  1. Drop instincts below MinConfidence.
//  2. Merge by ID — an above-floor project instinct shadows a global
//     one with the same ID (= ECC's project-overrides-global
//     precedence: the local repo's version is the more-specific one,
//     so the global copy — a promoted sibling — must not be injected
//     as a second line). A project instinct that itself falls below
//     the floor does NOT shadow its global twin, since the global
//     copy may have since diverged into independently-valid,
//     cross-project-validated knowledge (session evaluation only ever
//     adjusts the project-scope copy's confidence).
//  3. Order the survivors. With a prompt, that is relevance (see
//     internal/retrieve): three channels fused by rank, and a candidate
//     matching in none of them is DROPPED, so an off-topic prompt
//     correctly yields nothing. Without a prompt there is no relevance
//     to compute, so confidence order is the fallback.
//  4. Take the top MaxInstincts.
//  5. Render one line per instinct, stopping before the byte cap so
//     the block never exceeds MaxBytes mid-line.
//
// Confidence still gates entry (step 1) and is still displayed, but it
// no longer decides ORDER when a prompt is available: measured on this
// project's live corpus every instinct carries the same confidence, so
// ordering by it was really ordering by the id tiebreak. Returns the
// rendered block + the ids actually included, in the order they were
// rendered. Callers that only need the count take len(ids).
func Build(project, global []*homunculus.Instinct, opts Options) (string, []string) {
	opts = opts.WithDefaults()

	pool := make([]ranked, 0, len(project)+len(global))
	// A project ID shadows the same-ID global one ONLY when the project
	// copy itself clears the confidence floor (= is actually being
	// injected). Session evaluation (internal/session/evaluate.go) only
	// ever adjusts confidence for the project-scope instinct file; the
	// promoted global twin (internal/cli/instinct_promote.go) is never
	// touched by it. So a project instinct that independently decays
	// below the floor after promotion must NOT suppress its still-valid,
	// cross-project-validated global twin — recording the shadow only
	// for above-floor project instincts lets that promoted knowledge keep
	// surfacing instead of silently disappearing once the local copy
	// decays.
	projectIDs := make(map[string]bool, len(project))
	for _, in := range project {
		if _, covered := opts.ExcludeIDs[in.ID]; covered {
			continue // already delivered by an evolved skill
		}
		if in.Confidence >= *opts.MinConfidence {
			projectIDs[in.ID] = true
			pool = append(pool, ranked{in: in, isProject: true})
		}
	}
	for _, in := range global {
		if projectIDs[in.ID] {
			continue // project overrides global on ID collision (ECC precedence)
		}
		if _, covered := opts.ExcludeIDs[in.ID]; covered {
			continue
		}
		if in.Confidence >= *opts.MinConfidence {
			pool = append(pool, ranked{in: in, isProject: false})
		}
	}
	if opts.Prompt != "" {
		pool = rankByRelevance(pool, opts)
	} else {
		// No prompt: there is no relevance to compute, so fall back to
		// the confidence order. Measured on this project's live corpus
		// every instinct carries the SAME confidence, which makes this
		// order effectively alphabetical — a fallback, not a ranking.
		sort.SliceStable(pool, func(i, j int) bool {
			if pool[i].in.Confidence != pool[j].in.Confidence {
				return pool[i].in.Confidence > pool[j].in.Confidence
			}
			// project before global at equal confidence
			if pool[i].isProject != pool[j].isProject {
				return pool[i].isProject
			}
			return pool[i].in.ID < pool[j].in.ID
		})
	}
	if len(pool) > opts.MaxInstincts {
		pool = pool[:opts.MaxInstincts]
	}

	var b strings.Builder
	b.WriteString("# bough — learned instincts for this project\n\n")
	header := b.Len()
	// The ids are returned, not just their count: telemetry records what
	// a prompt actually received, and "how many" cannot answer whether
	// retrieval is reaching the tail of the corpus or cycling the same
	// few notes. The count callers used to take is len(ids).
	var ids []string
	perCluster := map[int]int{}
	for _, r := range pool {
		// One family may contribute at most ClusterCap lines. Counted on
		// what was KEPT, not on what was considered, so a candidate dropped
		// by the byte budget does not consume its family's allowance.
		cluster, stamped := opts.ClusterOf[r.in.ID]
		if stamped && perCluster[cluster] >= opts.ClusterCap {
			continue
		}
		line := renderInstinctLine(r.in)
		if b.Len()+len(line) > opts.MaxBytes && len(ids) > 0 {
			break
		}
		b.WriteString(line)
		ids = append(ids, r.in.ID)
		if stamped {
			perCluster[cluster]++
		}
	}
	if len(ids) == 0 {
		// nothing cleared the confidence floor; emit an empty block
		// so the hook is a clean no-op rather than a dangling header.
		return "", nil
	}
	_ = header
	return b.String(), ids
}

// rankByRelevance reorders the pool by how well each instinct matches
// the prompt, dropping the ones that match it in no channel. That drop
// is the point: an off-topic prompt should surface nothing rather than
// fill the block with whatever ranked first on a content-independent
// order.
func rankByRelevance(pool []ranked, opts Options) []ranked {
	docs := make([]retrieve.Doc, 0, len(pool))
	byID := make(map[string]ranked, len(pool))
	for _, r := range pool {
		key := scopedKey(r)
		byID[key] = r
		docs = append(docs, retrieve.Doc{
			ID:         key,
			Text:       r.in.ID + " " + r.in.Trigger + " " + r.in.Body,
			ModTime:    r.in.LastSeen,
			Confidence: r.in.Confidence,
			IsProject:  r.isProject,
		})
	}
	ranker := retrieve.NewRanker()
	ranker.MaxResults = opts.MaxInstincts
	out := make([]ranked, 0, opts.MaxInstincts)
	for _, res := range ranker.Rank(docs, opts.Prompt, opts.ContextTokens) {
		if r, ok := byID[res.Doc.ID]; ok {
			out = append(out, r)
		}
	}
	return out
}

// scopedKey distinguishes a project instinct from a global one carrying
// the same id. Build already resolved that collision in favour of the
// project copy, but the retrieval index is keyed by id, so the scope has
// to travel with it or one entry would shadow the other on lookup.
func scopedKey(r ranked) string {
	if r.isProject {
		return "project/" + r.in.ID
	}
	return "global/" + r.in.ID
}

// renderInstinctLine is the one-instinct rendering used inside the
// byte budget. Format: "- [conf] trigger → action" on a single line
// so the cap arithmetic is per-instinct and the block stays scannable.
func renderInstinctLine(in *homunculus.Instinct) string {
	trigger := oneLine(in.Trigger)
	action := firstActionLine(in.Body)
	return fmt.Sprintf("- [%.0f%%] %s → %s\n", in.Confidence*100, trigger, action)
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// firstActionLine extracts the first non-empty line under "## Action".
// Mirrors evolve.firstActionLine but kept local so inject has no
// import edge to evolve.
func firstActionLine(body string) string {
	lines := strings.Split(body, "\n")
	inAction := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.EqualFold(t, "## Action") {
			inAction = true
			continue
		}
		if inAction {
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "## ") {
				break
			}
			return oneLine(t)
		}
	}
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return oneLine(t)
	}
	return "(no action)"
}
