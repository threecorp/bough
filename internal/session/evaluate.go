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

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/observe"
)

// Confidence band ladder. An instinct the session reinforced (= its
// trigger tokens appeared in the observations) climbs one band; an
// instinct exercised during a session that showed a correction marker
// drops one. The low bands (0.30 / 0.40) sit BELOW inject's
// MinConfidence (0.50), so a repeatedly-contradicted instinct decays
// out of the injected set entirely — bough's analogue of ECC's
// demotion toward removal (ECC clamps to [0.1, 0.95]).
var confidenceBands = []float64{0.30, 0.40, 0.50, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85}

// correctionMarkerRE matches ECC's correction markers as WHOLE WORDS
// (case-insensitive). Whole-word anchoring is essential: a naked substring
// scan flags "0 errors" (error), "prefix"/"suffix"/"fixture"/"fixed" (fix),
// and "correctly"/"incorrect" (correct) — tokens that appear in nearly every
// session — which demoted good instincts every session (v0.9.18 review).
var correctionMarkerRE = regexp.MustCompile(`(?i)\b(error|mistake|wrong|fix|correct|undo)\b`)

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
	dir := layout.InstinctsDir(projectID)

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
		in.Confidence = newConf
		in.LastSeen = now.UTC()
		in.Observed++
		if _, err := homunculus.WriteInstinctFile(dir, in); err != nil {
			return res, err
		}
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
// the user TYPED ("no, that's wrong" / "undo that" / "fix this"), which lives
// in the prompt.
//
// Residual (documented, not yet addressed): a marker can still sit in an
// opening TASK prompt ("fix the login bug") rather than a correction, so this
// is conservative-but-imperfect. A precise "user is correcting the assistant"
// signal (correction phrases, or markers only in 2nd+ prompts) is a candidate
// refinement; demotion is one band, token-overlap-gated, and re-reinforced on
// the next clean exercise, so the residual is bounded.
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
// under 3 chars (= shorter tokens are mostly noise in tool I/O JSON).
func addTokens(set map[string]struct{}, s string) {
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() >= 3 {
			set[cur.String()] = struct{}{}
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
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
