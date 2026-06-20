package schema

import "time"

// TraceSource enumerates the observer pipelines that feed
// TraceBundle into the host. The values are the strings the host
// uses in `.bough.yaml`'s `instinct.mint.sources` list — adding a
// new source means adding a new constant here and teaching
// internal/observer how to populate it.
type TraceSource string

const (
	TraceSourceStdin           TraceSource = "stdin"            // bough instinct ingest --stdin (v0.5 primary)
	TraceSourceSessionLog      TraceSource = "session_log"      // claude .jsonl tail (opt-in beta)
	TraceSourceTestFailure     TraceSource = "test_failure"     // make test 2>&1 | ingest --source test_failure
	TraceSourceLintOutput      TraceSource = "lint_output"      // golangci-lint output piped to ingest
	TraceSourceCommitMessage   TraceSource = "commit_message"   // git hook ingest
	TraceSourcePostCreateHook  TraceSource = "post_create_hook" // bough create's post_create stdout
	TraceSourceExplicitFeedback TraceSource = "explicit_user_feedback" // bough instinct mint --rule '...'
	TraceSourceLLMOnly         TraceSource = "llm_only"         // LLM-backed minter plugin (v0.6+)
)

// TraceBundle is one normalised observation as it flows through
// observer → redaction → poisoning_guard → InstinctMinter. The
// observer pipeline produces a TraceBundle per atomic input (one
// line of stdin, one JSONL row, one test failure block); redaction
// rewrites Content in place if any pii_pattern matches.
//
// SourceEventID is the idempotency token observers use to suppress
// duplicate-on-retry. Stdin ingest accepts `--source-event-id` on
// the command line; the JSONL tail observer derives it from the
// session ID + JSONL row number. The coordinator's poisoning_guard
// hashes (DedupeKey, SourceEventID) so CI retries and JSONL re-
// reads never produce a duplicate Store call.
type TraceBundle struct {
	ID            string
	Source        TraceSource
	Scope         Scope
	CapturedAt    time.Time
	Content       string // redacted raw text
	EvidenceRef   string // session id / commit hash / test id / etc.
	SourceEventID string // observer-supplied idempotency token
}
