package instinctgate

import "slices"

// Candidate is one minted instinct under review. Only Trigger and Action —
// the "propagating surface", i.e. the text a future prompt would actually
// inject — are scanned; Body/Path are carried so the caller can quarantine
// the file and the judge (a later unit) can read the whole note. Scanning
// only the surface is deliberate: a note may legitimately CITE a forbidden
// command in its provenance/evidence without RECOMMENDING it.
type Candidate struct {
	ID      string
	Trigger string
	Action  string
	Body    string
	Path    string
}

// Decision records why a candidate was held. Rule is the tripwire name
// (not the regex), so a quarantine REPORT reads as the governance it
// enforces.
type Decision struct {
	ID   string
	Rule string
}

// Result is one gate pass. Cleared may be promoted out of staging; Held
// must be quarantined (moved, never deleted). The split is the whole
// contract — the caller does the filesystem moves, this package only decides.
type Result struct {
	Cleared []Candidate
	Held    []Decision
}

// DefaultForbiddenActions are the categories the LLM layer judges an
// instinct against when a project has not named its own. They are the
// universal VCS-safety families the deterministic tripwires also cover,
// stated as intent rather than as commands — the judge exists for the
// phrasings a pattern cannot reach.
//
// A project with rules outside these families must say so
// (instinct.gate.forbidden_actions): a judge cannot weigh a category
// nobody told it about, and it will clear violations of it while
// reporting a clean review — which is a checker covering a subset of the
// governance it claims to enforce.
var DefaultForbiddenActions = []string{
	"merging, landing, or closing someone's change without being asked",
	"discarding uncommitted or in-progress work (resetting, cleaning, stashing away, \"starting from a clean slate\", \"tidying up\" a working tree)",
	"rewriting authorship or history the operator owns",
	"force-pushing, or pushing directly to a protected branch",
	"deleting a branch, tag, or remote ref",
}

// Config is value-typed so the CLI can build it from .bough.yaml without
// this package importing config. Later units add the denylist + judge
// fields; Unit A is the deterministic tripwire layer plus its exemptions.
type Config struct {
	// Enabled gates the whole deterministic layer. Off ⇒ every candidate
	// clears (byte-for-byte the pre-gate behaviour), so the gate is a safe
	// default-off flip if an operator wants it.
	Enabled bool
	// AllowIDs exempts instincts whose own action IS the rule forbidding a
	// command (an instinct "never run `git push --force`" names the command
	// it forbids; quarantining it would be backwards). Exemption is by id
	// rather than a negation heuristic because a false hold is reversible
	// (quarantine is a move) but a wrong heuristic is silent.
	AllowIDs []string
	// Tripwires is the pattern set. Defaults to DefaultTripwires when nil so
	// a zero Config with Enabled=true still guards the built-in rules.
	Tripwires []Tripwire
	// Denylist holds terms that must never propagate (client names,
	// internal hostnames). Loaded from an untracked sidecar; nil or empty
	// means the layer is inert. See denylist.go.
	Denylist *Denylist
	// Governance is the project's rule text. An instinct that CLAIMS to
	// cite a rule must share a contiguous run of words with it, which is
	// what catches a confidently-invented policy. nil or empty means the
	// layer is inert — with no governance loaded every citation would
	// look unfounded. See grounding.go.
	Governance *Governance
}

// Gate applies the deterministic layer.
type Gate struct {
	cfg       Config
	tripwires []Tripwire
	allow     map[string]bool
}

// New builds a Gate from a Config, filling the default tripwire set when the
// caller supplied none.
func New(cfg Config) *Gate {
	tw := cfg.Tripwires
	if tw == nil {
		tw = DefaultTripwires()
	}
	allow := make(map[string]bool, len(cfg.AllowIDs))
	for _, id := range cfg.AllowIDs {
		allow[id] = true
	}
	return &Gate{cfg: cfg, tripwires: tw, allow: allow}
}

// Screen partitions candidates into Cleared and Held. A disabled gate
// clears everything. An allowlisted id always clears. Otherwise a candidate
// is held by the first tripwire that matches its propagating surface
// (Trigger + Action); Body is intentionally not scanned, so a note that
// merely cites a forbidden command in its evidence is not quarantined.
func (g *Gate) Screen(cands []Candidate) Result {
	if !g.cfg.Enabled {
		return Result{Cleared: slices.Clone(cands)}
	}
	res := Result{}
	for _, c := range cands {
		if g.allow[c.ID] {
			res.Cleared = append(res.Cleared, c)
			continue
		}
		if d, held := g.screenOne(c); held {
			res.Held = append(res.Held, d)
			continue
		}
		res.Cleared = append(res.Cleared, c)
	}
	return res
}

// screenOne runs the deterministic layers over one candidate, cheapest
// and most-certain first: command-shaped tripwires, then the denylist,
// then rule grounding. The first hit wins and names itself, so a
// quarantine report cites ONE reason rather than a list the operator has
// to rank.
//
// Only the propagating surface (trigger + action) is scanned — a note
// may legitimately cite a forbidden command or a sensitive term in its
// evidence without recommending it. Grounding reads the same surface for
// the same reason: it judges what the instinct ASSERTS, not what it
// recorded as context.
func (g *Gate) screenOne(c Candidate) (Decision, bool) {
	surface := c.Trigger + "\n" + c.Action
	for _, tw := range g.tripwires {
		if tw.Re.MatchString(surface) {
			return Decision{ID: c.ID, Rule: tw.Rule}, true
		}
	}
	if term, hit := g.cfg.Denylist.Match(surface); hit {
		return Decision{ID: c.ID, Rule: "denylisted-term:" + term}, true
	}
	// Grounding applies only to instincts asserting governance; an
	// ordinary practice note has no rule to be grounded against.
	if ClaimsRule(surface) && !g.cfg.Governance.Grounded(surface) {
		return Decision{ID: c.ID, Rule: "ungrounded-rule-claim"}, true
	}
	return Decision{}, false
}
