// Package session implements the SessionEnd + PreCompact hook
// handlers: per-session summary, instinct confidence evaluation, the
// CLAUDE.md evolution proposal, and the pre-compaction instinct
// snapshot.
//
// The summary + evaluate + preserve handlers are pure filesystem (no
// LLM): SessionEnd fires once per session, but reinforcing it with a
// claude --print call on every session close would add cost the
// operator did not ask for. Only the CLAUDE.md evolution proposal
// touches the LLM, and it is opt-in + dry-run by default.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/observe"
)

// Confidence band ladder. An instinct the session reinforced (= its
// trigger tokens appeared in the observations) climbs one band; an
// instinct exercised during a session that showed a correction marker
// drops one. The low bands (0.30 / 0.40) sit BELOW inject's
// MinConfidence (0.50), so a repeatedly-contradicted instinct decays
// out of the injected set entirely.
//
// Evaluate is ADVISORY: it reports what it would have done and writes
// nothing. This algorithm cannot assign per-instinct credit — the
// correction signal is one flag for the whole session, and "exercised"
// is a token overlap, so one occurrence of a correction word demotes
// every instinct the session brushed against. Measured on this
// project's live corpus (2026-08-07): 109 of 144 sessions (76%) carry a
// correction word somewhere in their observations, and 407 of 409
// instincts had been driven to the 0.30 floor — below the injection
// gate, so a corpus that cost hundreds of LLM mints delivered nothing.
// The reference implementation this ports hit the same failure and
// deliberately keeps its own version of this loop inert for the same
// reason. Until observations record WHICH instinct influenced an
// action, not writing is the correct behaviour, and this comment is
// load-bearing: reconnecting the write path without attribution
// re-sinks the corpus within days.
var confidenceBands = []float64{0.30, 0.40, 0.50, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85}

// correctionMarkerRE matches a USER correction of the assistant as a whole
// word (case-insensitive). It deliberately DIVERGES from ECC's set
// (error|mistake|wrong|fix|correct|undo): "error", "fix" and "correct"
// dominate ordinary TASK prompts ("fix the bug", "there's an error", "correct
// the value") rather than corrections of the assistant, so they are dropped;
// the kept set leans to words that, in a prompt, mean "you (the assistant) did
// something wrong". Whole-word anchoring also stops sub-word false positives
// ("prefix"/"fixture"/"correctly"/"undone").
var correctionMarkerRE = regexp.MustCompile(`(?i)\b(wrong|mistake|incorrect|undo|revert|broke|broken)\b`)

// ReinforceDelta / ContradictDelta are how many bands an instinct
// moves on a reinforcement / contradiction. ECC moves one band each
// way; bough keeps that so the evaluation is gentle (= a single
// session never swings an instinct from 0.85 to 0.50).
const (
	reinforceSteps  = 1
	contradictSteps = 1
)

// EvalResult records what one session evaluation did, for the CLI
// summary + the eval/scores.jsonl audit.
type EvalResult struct {
	SessionID    string    `json:"session_id"`
	EvaluatedAt  time.Time `json:"evaluated_at"`
	Observations int       `json:"observations"`
	Reinforced   int       `json:"reinforced"`
	Contradicted int       `json:"contradicted"`
	Unchanged    int       `json:"unchanged"`
}

// Evaluate reinforces / demotes each project instinct based on the
// session's observations, then rewrites the instinct files with the
// adjusted confidence + bumped LastSeen. The heuristic is token
// overlap: if an instinct's trigger/action tokens appear in the
// observation stream, the session exercised it (= reinforce). The
// ECC reference uses a richer signal (explicit success/failure
// markers); bough's token-overlap proxy is deterministic + LLM-free.
//
// now is injected for deterministic audit timestamps. Returns the
// EvalResult; the caller appends it to eval/scores.jsonl.
func Evaluate(layout homunculus.Layout, projectID, sessionID string, observations []observe.Observation, now time.Time) (EvalResult, error) {
	res := EvalResult{
		SessionID:    sessionID,
		EvaluatedAt:  now.UTC(),
		Observations: len(observations),
	}
	instincts, _ := homunculus.ScanInstincts(layout.InstinctsDir(projectID))
	if len(instincts) == 0 || len(observations) == 0 {
		return res, nil
	}

	obsTokens := tokenizeObservations(observations)
	correction := sessionHadCorrection(observations)

	for _, in := range instincts {
		if instinctOverlap(in, obsTokens) < 0.15 {
			res.Unchanged++ // not exercised this session
			continue
		}
		// Exercised. Reinforce by default; demote if the session showed
		// a correction marker (the instinct was active while something
		// went wrong) — ECC's hurt/helped split, targeted to the
		// instincts the session actually used.
		steps := reinforceSteps
		if correction {
			steps = -contradictSteps
		}
		newConf := stepBand(in.Confidence, steps)
		if newConf == in.Confidence {
			res.Unchanged++
			continue
		}
		// ADVISORY ONLY — count what would have happened, write nothing.
		// See the confidenceBands comment: without per-instinct
		// attribution the session-wide correction flag demotes everything
		// a busy session touched, and 76% of real sessions carry a
		// correction word. The counts still land in eval/scores.jsonl so
		// the false-positive rate stays measurable for whoever builds the
		// attribution this needs.
		if correction {
			res.Contradicted++
		} else {
			res.Reinforced++
		}
	}
	return res, nil
}

// AppendScore appends one EvalResult as a JSONL line to
// eval/scores.jsonl. The dir is created lazily.
func AppendScore(layout homunculus.Layout, projectID string, res EvalResult) error {
	dir := layout.EvalDir(projectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("session.AppendScore: mkdir: %w", err)
	}
	path := filepath.Join(dir, "scores.jsonl")
	line, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("session.AppendScore: marshal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("session.AppendScore: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("session.AppendScore: write: %w", err)
	}
	return nil
}

// stepBand moves a confidence value up (positive steps) or down
// (negative) the discrete band ladder. Values off the ladder snap to
// the nearest band before stepping so a hand-edited 0.73 still moves
// predictably.
func stepBand(conf float64, steps int) float64 {
	idx := nearestBandIndex(conf)
	idx += steps
	if idx < 0 {
		idx = 0
	}
	if idx >= len(confidenceBands) {
		idx = len(confidenceBands) - 1
	}
	return confidenceBands[idx]
}

func nearestBandIndex(conf float64) int {
	best := 0
	bestDist := 1e9
	for i, b := range confidenceBands {
		d := conf - b
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	return best
}

func tokenizeObservations(obs []observe.Observation) map[string]struct{} {
	out := map[string]struct{}{}
	for _, o := range obs {
		addTokens(out, o.Tool)
		addTokens(out, string(o.ToolInput))
		addTokens(out, string(o.ToolOutput))
	}
	return out
}

func instinctOverlap(in *homunculus.Instinct, obsTokens map[string]struct{}) float64 {
	itoks := map[string]struct{}{}
	addTokens(itoks, in.Trigger)
	addTokens(itoks, in.Body)
	if len(itoks) == 0 {
		return 0
	}
	hit := 0
	for t := range itoks {
		if _, ok := obsTokens[t]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(itoks))
}

// sessionHadCorrection reports whether the USER corrected bough during the
// session — ECC's signal to demote (rather than reinforce) the instincts the
// session exercised. It scans the user's PROMPTS only (a whole-word marker
// match), never tool outputs.
//
// Scanning tool outputs (the pre-v0.9.18 behavior) was the bug: a build log
// "0 errors", a file read containing "prefix", a lint summary saying "fix
// by …", or JSON like `{"error":null}` are not corrections, yet they appear
// in essentially every session — so the demotion branch fired constantly and
// good instincts decayed out of the injected set. A correction is something
// the user TYPED, which lives in the prompt.
//
// Marker precision: the marker set (correctionMarkerRE) drops the task-dominant
// "error"/"fix"/"correct", so an ordinary TASK prompt ("fix the login bug",
// "there's an error in X") no longer demotes — only a prompt that reads as a
// correction of the assistant ("that's wrong", "undo that", "you broke it")
// does. It is still a keyword heuristic, not perfect; demotion is one band,
// token-overlap-gated, and re-reinforced on the next clean exercise, so any
// residual over-fire is bounded.
func sessionHadCorrection(obs []observe.Observation) bool {
	for _, o := range obs {
		if o.Prompt == "" {
			continue
		}
		if correctionMarkerRE.MatchString(o.Prompt) {
			return true
		}
	}
	return false
}

// addTokens lower-cases + splits on non-alphanumeric, dropping tokens
// under 3 runes (= shorter tokens are mostly noise in tool I/O JSON).
// unicode.IsLetter/IsDigit (not a bare a-z/0-9 range check) so
// instinct/observation text in Japanese or any other non-Latin script
// still tokenizes — an ASCII-only check silently zeroed
// instinctOverlap for such text, permanently disabling confidence
// reinforcement/demotion for it.
func addTokens(set map[string]struct{}, s string) {
	cur := make([]rune, 0, 16)
	flush := func() {
		if len(cur) >= 3 {
			set[string(cur)] = struct{}{}
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
}

// Summary renders a short human-readable session summary from the
// observation stream + the eval result. The CLI prints it to stdout
// on `bough session-end`.
func Summary(res EvalResult, observations []observe.Observation) string {
	byEvent := map[string]int{}
	for _, o := range observations {
		byEvent[o.Event]++
	}
	events := make([]string, 0, len(byEvent))
	for e := range byEvent {
		events = append(events, e)
	}
	sort.Strings(events)

	var b strings.Builder
	fmt.Fprintf(&b, "session %s: %d observations\n", res.SessionID, res.Observations)
	for _, e := range events {
		fmt.Fprintf(&b, "  %-16s %d\n", e, byEvent[e])
	}
	fmt.Fprintf(&b, "instincts: reinforced=%d contradicted=%d unchanged=%d\n", res.Reinforced, res.Contradicted, res.Unchanged)
	return b.String()
}
