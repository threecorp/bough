// Package telemetry is the append-only record of what the continuous
// learning loop actually DID — not what it produced. Three questions
// live here, and they are the three the completion gate, the operator,
// and a post-mortem each need:
//
//   - was a skill PULLED (KindSkillPull), i.e. did the delivery path
//     that a deployed portfolio is supposed to open actually fire;
//   - what did a prompt SELECT (KindSelection), i.e. is retrieval
//     reaching the tail of the corpus or cycling the same few notes;
//   - what did the generation gate DECIDE (KindJudge), and in
//     particular did anything get promoted WITHOUT being judged.
//
// # Why an event log and not a snapshot
//
// The obvious shape for usage data is a periodic snapshot of counters
// per slug, and a trend read as the difference between two snapshots.
// That shape has two failure modes that are not bugs in any one line of
// code but consequences of the shape itself:
//
//   - Consolidating N skills into fewer removes slugs from the snapshot,
//     so the total DROPS and the trend reports lost usage where real new
//     usage happened.
//   - Picking the baseline as "the newest snapshot older than the
//     window" makes the latest snapshot its own baseline once every
//     snapshot is older than the window, so the delta is zero by
//     construction.
//
// Counting events inside a time window has neither: there is no
// subtraction and no baseline to select. A slug that disappears simply
// stops contributing new events; it can never subtract. This package
// therefore stores events and never stores a derived total.
//
// # What is still possible to get wrong
//
// A reader that cannot parse its writer reports the same thing as a
// reader that parsed correctly and found nothing. That failure is
// silent and plausible, which is why Log.Drift exists and why every
// count in this package goes through exactly one decode path
// (parseLine) rather than each caller deciding what a line means.
package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kind names what an event records. It is a closed set: a line whose
// kind is not one of these is not silently ignored, it is reported by
// Drift — an unknown kind means the writer is ahead of the reader,
// which is exactly the drift that must not read as "nothing happened".
type Kind string

const (
	// KindSkillPull is one successful load of an evolved skill by the
	// host. This is the pull path firing, and the completion gate's
	// central question.
	KindSkillPull Kind = "skill_pull"
	// KindSelection is one injection: the instinct ids a single prompt
	// actually received. Distinct ids over a window answer whether
	// retrieval reaches the tail.
	KindSelection Kind = "selection"
	// KindJudge is one generation-gate pass. Counts carries the
	// reviewed / unreviewed / held tallies; Unreviewed > 0 is the
	// promoted-without-being-judged event, the one worth grepping for.
	KindJudge Kind = "judge"
)

// Event is one line of the log. Every field beyond TS and Kind is
// optional and kind-specific; the decode is uniform so that adding a
// kind cannot fork the parse.
type Event struct {
	TS   time.Time `json:"ts"`
	Kind Kind      `json:"kind"`

	// Slug is the evolved skill that was pulled (KindSkillPull).
	Slug string `json:"slug,omitempty"`

	// IDs are the instinct ids injected for one prompt (KindSelection).
	IDs []string `json:"ids,omitempty"`

	// Counts carries a kind's tallies (KindJudge: reviewed, unreviewed,
	// held). A map rather than named fields because the gate's shape has
	// changed twice already and a new tally must not require a schema
	// migration of historical lines.
	Counts map[string]int `json:"counts,omitempty"`

	// Session correlates events within one host session.
	Session string `json:"session,omitempty"`

	// Raw holds a payload the writer could not attribute — e.g. a skill
	// pull whose slug was not where the writer expected. It is the input
	// to Drift: keeping the bytes is what makes the failure legible
	// later instead of arriving as a zero.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Writer appends events to a project's telemetry log. It mirrors
// observe.Writer deliberately — same O_APPEND-per-line contract, same
// lazy mkdir, same injectable clock — so there is one append idiom in
// the tree rather than two that drift.
type Writer struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

// NewWriter pins a Writer to path. The parent directory is created on
// first Append so constructing a Writer has no side effects.
func NewWriter(path string) *Writer { return &Writer{path: path, now: time.Now} }

// SetClock overrides the time source used when an event arrives
// unstamped. Tests pin it for deterministic windows.
func (w *Writer) SetClock(now func() time.Time) { w.now = now }

// Path returns the file the writer appends to.
func (w *Writer) Path() string { return w.path }

// Append writes one event as a single JSONL line.
func (w *Writer) Append(e Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e.Kind == "" {
		return fmt.Errorf("telemetry.Writer.Append: Kind is required")
	}
	if e.TS.IsZero() {
		e.TS = w.now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("telemetry.Writer.Append: mkdir: %w", err)
	}
	buf, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("telemetry.Writer.Append: marshal: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("telemetry.Writer.Append: open %s: %w", w.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(buf, '\n')); err != nil {
		return fmt.Errorf("telemetry.Writer.Append: write: %w", err)
	}
	return nil
}

// AppendBestEffort writes e and swallows the error. Telemetry is
// observation, never a precondition: a full disk must not break the
// hook that records the pull, the prompt that records its selection, or
// the gate that records its verdict. The callers are all on paths where
// failing loudly would cost the operator the actual work.
func (w *Writer) AppendBestEffort(e Event) { _ = w.Append(e) }

// A Log is the parsed contents of one telemetry file.
type Log struct {
	// Events are every line that decoded, in file order.
	Events []Event
	// Unparsed counts lines that were not JSON at all (a torn write, a
	// truncated tail). Distinct from Drift: this is damage, not
	// disagreement.
	Unparsed int
}

// Load reads the whole log. A missing file is an empty log, not an
// error — a project that has never recorded anything is the normal
// starting state, and callers must not have to special-case it.
func Load(path string) (*Log, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Log{}, nil
		}
		return nil, fmt.Errorf("telemetry.Load: read %s: %w", path, err)
	}
	lg := &Log{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e, ok := parseLine([]byte(line))
		if !ok {
			lg.Unparsed++
			continue
		}
		lg.Events = append(lg.Events, e)
	}
	return lg, nil
}

// parseLine is the ONE decode path in this package. Every count, every
// window, and the drift check all read through it, so no two callers
// can disagree about what a line means — which is the whole failure
// this package is built to avoid.
func parseLine(b []byte) (Event, bool) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, false
	}
	return e, true
}

// Window returns the events in [from, to]. Both bounds are inclusive so
// a caller asking for "the last 14 days" from a clock reading gets the
// event that landed on the boundary rather than losing it to a
// half-open interval nobody documented.
func (l *Log) Window(from, to time.Time) []Event {
	var out []Event
	for _, e := range l.Events {
		if e.TS.Before(from) || e.TS.After(to) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// PullsBySlug counts successful skill pulls per slug within a window.
// An event with no slug is NOT counted — it is drift, and Drift reports
// it. Counting it under an empty key would turn a parse failure into a
// number the gate would believe.
func PullsBySlug(events []Event) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		if e.Kind != KindSkillPull || e.Slug == "" {
			continue
		}
		out[e.Slug]++
	}
	return out
}

// DistinctIDs counts how many distinct instinct ids were injected
// across the given events. This is the breadth measure for retrieval:
// a number that stays flat while prompts vary means the selection is
// cycling the same few notes.
func DistinctIDs(events []Event) int {
	seen := map[string]struct{}{}
	for _, e := range events {
		if e.Kind != KindSelection {
			continue
		}
		for _, id := range e.IDs {
			seen[id] = struct{}{}
		}
	}
	return len(seen)
}

// CountUnjudgedPromoted and CountUnjudgedStaged are the two tallies a
// judge event carries for candidates that got no verdict. They are
// SEPARATE because the gate does two different things with them, and
// only one of them is alarming:
//
//   - promoted: the model could not answer, the gate failed open, and
//     the candidate is in the corpus unscreened;
//   - staged: the run was interrupted or the operator's own call budget
//     ran out, so the candidate was left in staging for the next pass.
//
// Folding both into one "unreviewed" number made `bough ops` report
// promotions that had explicitly not happened — the observer prints
// "left STAGED, not promoted" for the same run.
const (
	CountUnjudgedPromoted = "unjudged_promoted"
	CountUnjudgedStaged   = "unjudged_staged"
)

// UnjudgedPromotions totals the candidates that reached the corpus
// without a verdict. The gate fails open on purpose, so this number is
// how an operator finds out that it did. Candidates left staged are NOT
// counted here — see UnjudgedStaged.
func UnjudgedPromotions(events []Event) int { return sumCount(events, CountUnjudgedPromoted) }

// UnjudgedStaged totals the candidates a run left in staging without a
// verdict. Not alarming on its own: the next pass adopts and judges
// them. It is reported so the two numbers are never read as one.
func UnjudgedStaged(events []Event) int { return sumCount(events, CountUnjudgedStaged) }

func sumCount(events []Event, key string) int {
	n := 0
	for _, e := range events {
		if e.Kind != KindJudge {
			continue
		}
		n += e.Counts[key]
	}
	return n
}

// Oldest returns the timestamp of the earliest event, and false when
// the log is empty. Callers use it to state how much history actually
// backs a verdict rather than naming the window they wished for.
func (l *Log) Oldest() (time.Time, bool) {
	if len(l.Events) == 0 {
		return time.Time{}, false
	}
	oldest := l.Events[0].TS
	for _, e := range l.Events[1:] {
		if e.TS.Before(oldest) {
			oldest = e.TS
		}
	}
	return oldest, true
}

// Newest returns the timestamp of the latest event, and false when the
// log is empty.
func (l *Log) Newest() (time.Time, bool) {
	if len(l.Events) == 0 {
		return time.Time{}, false
	}
	newest := l.Events[0].TS
	for _, e := range l.Events[1:] {
		if e.TS.After(newest) {
			newest = e.TS
		}
	}
	return newest, true
}

// DriftIn reports lines that decoded but that this reader could not
// make sense of — the writer and the reader disagreeing, rather than
// nothing having happened.
//
// The predicate is deliberately ASYMMETRIC to the parser. The parser is
// strict: an unattributed pull contributes zero. The tripwire asks the
// opposite question — "does this line look like it was meant to carry
// something?" — so a zero that should have been a number becomes a loud
// row instead of a believable one. Healthy lines are silent, so a
// correct log produces no noise.
//
// It takes a WINDOW, and the window must be the one the caller's counts
// come from. An earlier version read the whole log, which made the
// check unsatisfiable: the log is append-only with no rotation, so a
// payload shape that moved once, was fixed, and has long since aged out
// of the window went on blocking forever — a false WAIT with no end,
// which is the incident this whole gate exists to prevent, not a
// stricter version of it. A drift row must mean "the numbers beside it
// are unreliable", and only in-window lines feed those numbers.
//
// Unparsed lines are the exception and are always reported: a line that
// is not JSON carries no timestamp, so it cannot be placed inside or
// outside any window, and it is damage rather than disagreement.
func (l *Log) DriftIn(from, to time.Time) []string {
	out := driftRows(l.Window(from, to))
	if l.Unparsed > 0 {
		out = append(out, fmt.Sprintf("%d line(s) are not valid JSON — a torn or truncated write", l.Unparsed))
	}
	return out
}

func driftRows(events []Event) []string {
	var out []string
	unknown := map[string]int{}
	for _, e := range events {
		switch e.Kind {
		case KindSkillPull:
			// A pull the writer could not attribute kept its payload.
			// That is the shape of "the slug moved", and it must not be
			// indistinguishable from "no pull happened".
			if e.Slug == "" && len(e.Raw) > 0 {
				out = append(out, fmt.Sprintf("a skill_pull carries an unattributed payload (%s) — the reader did not find a slug in it",
					preview(e.Raw)))
			}
		case KindSelection:
			if len(e.IDs) == 0 && len(e.Raw) > 0 {
				out = append(out, fmt.Sprintf("a selection carries a payload but no ids (%s)", preview(e.Raw)))
			}
		case KindJudge:
			if len(e.Counts) == 0 && len(e.Raw) > 0 {
				out = append(out, fmt.Sprintf("a judge event carries a payload but no counts (%s)", preview(e.Raw)))
			}
		default:
			unknown[string(e.Kind)]++
		}
	}
	for _, k := range sortedKeys(unknown) {
		out = append(out, fmt.Sprintf("%d line(s) carry an unknown kind %q — the writer is ahead of this reader", unknown[k], k))
	}
	return out
}

// maxPreviewBytes bounds a drift row's quoted payload. The row exists
// to be read in a terminal beside other rows; a whole tool payload
// would bury the rows around it.
const maxPreviewBytes = 80

func preview(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) <= maxPreviewBytes {
		return s
	}
	// Cut on a rune boundary. A byte index lands mid-rune on any payload
	// carrying a non-ASCII path or prompt fragment, and emitting invalid
	// UTF-8 into the row that exists to be read is its own small version
	// of the failure this package is about.
	cut := maxPreviewBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	// Say that it was cut. A silent truncation in a diagnostic row is
	// the same class of lie the row exists to expose.
	return fmt.Sprintf("%s… (%d bytes total)", s[:cut], len(raw))
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
