package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
)

// importScreen applies the policy gate to instincts arriving from a
// foreign corpus.
//
// Import is the least-trusted write path bough has: the text was minted
// by somebody else's observer, against somebody else's sessions, and it
// lands in instincts/personal where injection reads it. The mint path
// has been screened since the gate existed; this one was not, so a
// corpus carrying "merge as soon as CI is green" imported it straight
// into the prompt.
//
// Fail-CLOSED, which inverts the generation path's rule. There, a gate
// that cannot run must promote and log, because it runs constantly and
// blocking it stops learning. Import runs rarely, by hand, and what it
// writes is injected into every later prompt — so "the screen could not
// run" must never read as "clean".
//
// Held notes are not dropped: they land in the destination project's
// quarantine with a REPORT, which makes them exactly as reviewable as a
// locally-minted hold (`bough instinct verdict keep|retire`). The source
// corpus is never modified either way.
type importScreen struct {
	gate   *instinctgate.Gate
	layout homunculus.Layout
	now    time.Time
	// enabled mirrors the resolved gate config so the report can say
	// "off" instead of "held 0". A gate the operator switched off reads
	// exactly like a clean corpus otherwise, which is the confusion the
	// printed-even-when-zero line exists to prevent.
	enabled bool

	// projectID is the destination project the current copyProject call
	// is filling. Set per project so one import of many projects writes
	// one quarantine batch each, not one shared batch.
	projectID string
	held      []movedRecord
	scanned   int
}

// screensInstinct reports whether a file copied to dst would become an
// injectable instinct: a non-catalog .md under the project's instincts
// tree. The extension and the directory are BOTH part of the coverage —
// a sweep that checks one and not the other is how the widest-scope
// writer went unscanned for months.
func screensInstinct(dstRel string) bool {
	if filepath.Ext(dstRel) != ".md" {
		return false
	}
	base := filepath.Base(dstRel)
	if base == "INSTINCTS.md" || base == "MEMORY.md" || base == "README.md" {
		return false
	}
	// filepath.ToSlash so the prefix test holds on any separator.
	return strings.HasPrefix(filepath.ToSlash(dstRel), "instincts/")
}

// screen decides one incoming instinct. A held note is copied into the
// quarantine batch instead of into the corpus and recorded for the
// REPORT; the caller skips its normal write.
//
// Reading the SOURCE file is deliberate: the surface screened here is
// the same trigger + whole action block the mint path screens, so a
// note cannot be judged by one standard on the way in and another on
// the way over.
func (s *importScreen) screen(src, dst string) (held bool, err error) {
	if s == nil || s.gate == nil {
		return false, nil
	}
	s.scanned++
	in, rerr := homunculus.ReadInstinctFile(src)
	if rerr != nil || in == nil {
		// Unparseable here means unparseable for ScanInstincts too, so it
		// could never be injected. Let it copy: the import's job is to
		// move the corpus, and a file bough cannot read is inert rather
		// than dangerous.
		return false, nil
	}
	res := s.gate.Screen([]instinctgate.Candidate{{
		ID:      in.ID,
		Trigger: in.Trigger,
		Action:  actionBlock(in.Body),
		Body:    in.Body,
		Path:    src,
	}})
	if len(res.Held) != 1 {
		return false, nil
	}
	batch := s.batchDir()
	if err := os.MkdirAll(batch, 0o755); err != nil {
		return false, fmt.Errorf("ecc import: quarantine dir %s: %w", batch, err)
	}
	// The batch dir is flat while the corpus is not: instincts/personal
	// and instincts/inherited legitimately hold the same id, so a name
	// already taken gets a suffix rather than overwriting the earlier
	// hold and leaving two REPORT rows pointing at one file.
	qdst := uniquePath(filepath.Join(batch, filepath.Base(dst)))
	if err := copyInstinctFile(src, qdst); err != nil {
		return false, fmt.Errorf("ecc import: quarantine %s: %w", in.ID, err)
	}
	s.held = append(s.held, movedRecord{
		id:         in.ID,
		reason:     res.Held[0].Rule,
		path:       qdst,
		restoreDir: s.layout.StagingDir(s.projectID),
	})
	return true, nil
}

// uniquePath returns path, or the first free "<base>-N.md" beside it.
func uniquePath(path string) string {
	if _, err := os.Stat(path); err != nil {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
}

// reset drops the accumulator without writing a REPORT. The caller uses
// it when a project's copy FAILED: its counts and held records must not
// leak into the next project's report, which would name files under one
// project's quarantine with another project's restore dir.
func (s *importScreen) reset() {
	if s == nil {
		return
	}
	s.scanned, s.held = 0, nil
}

func (s *importScreen) batchDir() string {
	return filepath.Join(s.layout.QuarantineDir(s.projectID), s.now.Format("20060102-150405"))
}

// flush writes the batch REPORT for whatever this project's copy held,
// and resets the accumulator for the next project. Returns the counts so
// the caller can print them EVEN WHEN ZERO — an unmeasured 0 and an
// unswept directory read identically in a report.
func (s *importScreen) flush() (scanned, held int, batch string, err error) {
	if s == nil || s.gate == nil {
		return 0, 0, "", nil
	}
	scanned, held = s.scanned, len(s.held)
	s.scanned = 0
	if held == 0 {
		s.held = nil
		return scanned, 0, "", nil
	}
	batch = s.batchDir()
	spec := quarantineReportSpec(s.layout.StagingDir(s.projectID))
	spec.title = "Policy quarantine — imported corpus"
	werr := writeMoveReport(batch, spec, s.held, s.now)
	s.held = nil
	return scanned, held, batch, werr
}
