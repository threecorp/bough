// Package inject builds the context block bough's UserPromptSubmit
// hook prints to stdout. Claude Code folds that stdout into the next
// turn's context, so it is billed as input tokens at the operator's
// subscription rate. The block is therefore capped (= ECC's ~9.5KB
// default) and confidence-sorted so the most-reliable instincts land
// in the window before the cap truncates.
//
// This package is intentionally LLM-free: the UserPromptSubmit hook
// fires on every prompt, so anything that spawned `claude --print`
// here would add latency + cost to every keystroke-to-response cycle.
// Instinct selection is pure filesystem + sort.
package inject

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ikeikeikeike/bough/internal/homunculus"
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

// Options tunes the block. Zero values fall back to the Default*
// constants so callers can pass Options{} for the standard block.
type Options struct {
	MaxBytes     int
	MaxInstincts int
	MinConfidence float64
}

func (o Options) withDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxInstincts <= 0 {
		o.MaxInstincts = DefaultMaxInstincts
	}
	if o.MinConfidence <= 0 {
		o.MinConfidence = MinConfidence
	}
	return o
}

// Build assembles the injection block from the project + global
// instinct corpora. Selection order:
//
//  1. Drop instincts below MinConfidence.
//  2. Sort by confidence descending (= ECC's confidence-sort, the
//     threecorp improvement over the original alphabetical order
//     that truncated mid-corpus by filename).
//  3. Take the top MaxInstincts.
//  4. Render one line per instinct, stopping before the byte cap so
//     the block never exceeds MaxBytes mid-line.
//
// project instincts rank above global ones at equal confidence (=
// the local repo's learned patterns are more specific). Returns the
// rendered block + the count actually included.
func Build(project, global []*homunculus.Instinct, opts Options) (string, int) {
	opts = opts.withDefaults()

	type ranked struct {
		in        *homunculus.Instinct
		isProject bool
	}
	pool := make([]ranked, 0, len(project)+len(global))
	for _, in := range project {
		if in.Confidence >= opts.MinConfidence {
			pool = append(pool, ranked{in: in, isProject: true})
		}
	}
	for _, in := range global {
		if in.Confidence >= opts.MinConfidence {
			pool = append(pool, ranked{in: in, isProject: false})
		}
	}
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
	if len(pool) > opts.MaxInstincts {
		pool = pool[:opts.MaxInstincts]
	}

	var b strings.Builder
	b.WriteString("# bough — learned instincts for this project\n\n")
	header := b.Len()
	included := 0
	for _, r := range pool {
		line := renderInstinctLine(r.in)
		if b.Len()+len(line) > opts.MaxBytes && included > 0 {
			break
		}
		b.WriteString(line)
		included++
	}
	if included == 0 {
		// nothing cleared the confidence floor; emit an empty block
		// so the hook is a clean no-op rather than a dangling header.
		return "", 0
	}
	_ = header
	return b.String(), included
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
