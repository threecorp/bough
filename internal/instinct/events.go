package instinct

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one append-only audit record of a coordinator action.
// The events file is the durable, replay-able log of what bough did:
// when an external backend disagrees with the local mirror, the
// events file is the source of truth for "what should have
// happened".
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`             // mint | approve | store | query | forget | promote | export | import | decay
	Scope     string    `json:"scope,omitempty"`  // "<level>/<worktree_or_repo>"
	ID        string    `json:"id,omitempty"`     // affected Instinct ID, if any
	Detail    string    `json:"detail,omitempty"` // freeform context
}

// EventWriter is a thread-safe JSON-lines appender. The coordinator
// holds one EventWriter per scope (or shares one global writer);
// the lock keeps line interleavings sane when stdin ingest and a
// `bough memory query` race.
type EventWriter struct {
	mu   sync.Mutex
	file *os.File
}

// NewEventWriter opens or creates the events.jsonl at path. The
// parent directory is created if missing. Caller invokes Close().
func NewEventWriter(path string) (*EventWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir events dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open events: %w", err)
	}
	return &EventWriter{file: f}, nil
}

// Append writes one event line and fsync's if it is a write that
// affects durable state (mint / approve / store / forget / promote).
// Read-only events (query) skip fsync — they are useful for
// observability but not load-bearing.
func (w *EventWriter) Append(ev Event) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	enc := json.NewEncoder(w.file)
	if err := enc.Encode(ev); err != nil {
		return fmt.Errorf("events encode: %w", err)
	}
	switch ev.Kind {
	case "mint", "approve", "store", "forget", "promote", "decay", "import":
		_ = w.file.Sync()
	}
	return nil
}

// Close releases the underlying file handle. Subsequent Append
// calls return an error.
func (w *EventWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.file.Close()
	w.file = nil
	return err
}
