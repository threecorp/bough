package instinctgate

import (
	"bufio"
	"os"
	"strings"
)

// The denylist is the one layer whose CONTENT cannot live in this
// repository. A team's list names the things that must never propagate
// out of a session — client names, internal hostnames, unreleased
// product codenames. Committing it would publish exactly the strings it
// exists to contain, so bough ships a template with placeholders and
// loads the real list from a sidecar file that is never tracked.
//
// A missing sidecar is not an error and not a warning: most operators
// never need one, and a guard that nagged on every mint would be turned
// off. Absent means the layer is simply not active, and `bough doctor`
// reports which state you are in so "off" is visible rather than
// assumed.

// Denylist is a set of literal terms that must not appear in an
// instinct. Matching is case-insensitive and substring-based on purpose:
// the terms are proper nouns, and a word-boundary rule would miss
// `acme-prod-01` when the list says `acme`.
type Denylist struct {
	terms []string
}

// LoadDenylist reads a sidecar denylist. One term per line; blank lines
// and `#` comments are ignored. A missing file yields an empty (inert)
// list and no error — see the package note above for why absence is not
// a fault. A file that exists but cannot be read IS returned as an
// error, because that is a broken configuration rather than an absent
// one, and silently treating it as empty would disable the guard the
// operator thinks is running.
func LoadDenylist(path string) (*Denylist, error) {
	if path == "" {
		return &Denylist{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Denylist{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	d := &Denylist{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		d.terms = append(d.terms, strings.ToLower(line))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// Active reports whether any term is loaded. Callers use it to say
// "denylist: off (no sidecar)" rather than implying a clean pass.
func (d *Denylist) Active() bool { return d != nil && len(d.terms) > 0 }

// Match returns the first denied term contained in text, if any. The
// TERM is returned rather than a bare bool so a quarantine report can
// name what tripped — but note that the report then contains the term,
// which is why the report lives beside the corpus and not in the repo.
func (d *Denylist) Match(text string) (string, bool) {
	if !d.Active() {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, term := range d.terms {
		if strings.Contains(lower, term) {
			return term, true
		}
	}
	return "", false
}
