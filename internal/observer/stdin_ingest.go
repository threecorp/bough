// Package observer is the v0.5 trace ingestion pipeline. Round 3
// of external review made the stdin path the PRIMARY for fsnotify
// portability reasons (macOS FSEvents vs Linux inotify divergence,
// log rotation, truncate handling); the Claude `.jsonl` file watch
// is an opt-in beta that lands in Μ-1.11.
//
// The stdin path is simple by design: read lines, group them into
// TraceBundles using a few host-known framing rules, and hand the
// batch to the coordinator. The same path serves `bough instinct
// ingest --stdin --source test_failure` from a `make test 2>&1 |`
// pipe, a CI failure log, and an interactive paste.
package observer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/instinct"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// StdinIngestOptions parameterises a single ingest run. Source is
// the `--source` flag value (mapped onto the TraceSource enum);
// SourceEventID is the optional idempotency token the caller can
// pass via `--source-event-id` for retry-safe ingest from CI.
type StdinIngestOptions struct {
	Source        schema.TraceSource
	Scope         schema.Scope
	SourceEventID string
	// MaxLines caps how many lines coalesce into one TraceBundle.
	// Default: 200. Set higher for paste-friendly UX, lower for
	// fast-tick ingest from a `tail -f` pipe.
	MaxLines int
}

// Ingest reads from r until EOF, coalesces the input into one or
// more TraceBundles, and hands them to coord.Ingest. Returns the
// counts the coordinator reported plus a wrapping error if any
// stage failed.
func Ingest(ctx context.Context, coord *instinct.Coordinator, r io.Reader, opts StdinIngestOptions) (admitted, reinforced int, err error) {
	if opts.MaxLines <= 0 {
		opts.MaxLines = 200
	}
	if opts.Source == "" {
		opts.Source = schema.TraceSourceStdin
	}

	scanner := bufio.NewScanner(r)
	// 1 MiB max line — go's default 64KiB is sometimes too small
	// for stack trace blobs from `make integration-test`.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1<<20)

	var (
		bundles []schema.TraceBundle
		group   []string
		groupID int
	)
	flush := func() {
		if len(group) == 0 {
			return
		}
		groupID++
		bundles = append(bundles, makeBundle(opts, groupID, group))
		group = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		group = append(group, line)
		if len(group) >= opts.MaxLines || isFrameBoundary(line) {
			flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("stdin ingest scan: %w", err)
	}
	flush()
	if len(bundles) == 0 {
		return 0, 0, nil
	}
	return coord.Ingest(ctx, opts.Scope, bundles)
}

// makeBundle stamps a TraceBundle from a chunk of lines. ID is the
// sha256 of the chunk content + the run's SourceEventID so a
// retry-ingest produces the same ID and the backend's dedupe path
// fires.
func makeBundle(opts StdinIngestOptions, groupID int, lines []string) schema.TraceBundle {
	content := strings.Join(lines, "\n")
	h := sha256.New()
	h.Write([]byte(opts.SourceEventID))
	h.Write([]byte(content))
	id := hex.EncodeToString(h.Sum(nil))[:16]
	suffix := ""
	if groupID > 0 {
		suffix = fmt.Sprintf(":%d", groupID)
	}
	return schema.TraceBundle{
		ID:            id,
		Source:        opts.Source,
		Scope:         opts.Scope,
		CapturedAt:    time.Now().UTC(),
		Content:       content,
		EvidenceRef:   opts.SourceEventID + suffix,
		SourceEventID: opts.SourceEventID + suffix,
	}
}

// isFrameBoundary returns true on lines that conventionally end a
// trace frame (blank line, "--- FAIL" / "--- PASS" markers, etc.).
// Cheap heuristics that turn a 1000-line CI log into a handful of
// meaningful bundles instead of one giant blob.
func isFrameBoundary(line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	switch {
	case strings.HasPrefix(line, "--- FAIL"),
		strings.HasPrefix(line, "--- PASS"),
		strings.HasPrefix(line, "FAIL\t"),
		strings.HasPrefix(line, "ok  \t"):
		return true
	}
	return false
}
