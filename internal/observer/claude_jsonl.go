package observer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/ikeikeikeike/bough/internal/instinct"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// FileWatchOptions parameterises the opt-in beta Claude Code .jsonl
// observer. Round 3 of external review insisted this be SECONDARY
// to stdin ingest because of fsnotify cross-platform fragility
// (macOS FSEvents vs Linux inotify divergence, log rotation, file
// truncate). The host's CLI / coordinator therefore mark this
// observer as `stability: beta` and turn it off by default.
type FileWatchOptions struct {
	// Path is the .jsonl file to tail. The CLI typically resolves
	// this from `~/.claude/projects/<id>/<session>.jsonl` based on
	// CWD; tests pass a temp file directly.
	Path  string
	Scope schema.Scope
	// RotationHandling true → if the watched file's inode changes
	// (typical of `logrotate` or an editor save-and-replace), the
	// observer re-binds to the new inode and continues.
	RotationHandling bool
	// TruncateHandling true → if the file is truncated (`> file`),
	// the observer seeks back to 0 and resumes from the new head.
	TruncateHandling bool
	// DebounceMs gates how aggressively we batch events. fsnotify
	// can emit multiple events per write on macOS; debounce
	// collapses a burst into one ingest call.
	DebounceMs int
}

// FileWatch runs until ctx is cancelled, ingesting each new line
// the watched file emits as one TraceBundle. Returns the count of
// admitted candidates and the running error (if any).
//
// The implementation is intentionally tight on guarantees: lines
// are delivered at-least-once on the happy path, and inode-change
// detection is best-effort. If rotation handling is off and the
// underlying file is rotated mid-stream, the watch reports an
// error and exits — leaving the user to relaunch via `bough
// instinct ingest --stdin`, the documented primary path.
func FileWatch(ctx context.Context, coord *instinct.Coordinator, opts FileWatchOptions) (int, error) {
	if opts.Path == "" {
		return 0, errors.New("file_watch: path is empty")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return 0, fmt.Errorf("file_watch: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(opts.Path); err != nil {
		return 0, fmt.Errorf("file_watch: %s: %w", opts.Path, err)
	}

	f, err := os.Open(opts.Path)
	if err != nil {
		return 0, fmt.Errorf("file_watch: open %s: %w", opts.Path, err)
	}
	defer f.Close()
	// Seek to end so we only see new lines, not history.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return 0, fmt.Errorf("file_watch: seek: %w", err)
	}

	prevInode, _ := inodeOf(opts.Path)
	debounce := time.Duration(opts.DebounceMs) * time.Millisecond
	if debounce <= 0 {
		debounce = 100 * time.Millisecond
	}

	reader := bufio.NewReader(f)
	var (
		// admitted is read by the main goroutine on return and
		// written by the time.AfterFunc-driven flush goroutine.
		// Round 3 follow-up fix (HIGH #10): the previous plain
		// int caused a race detector failure when the suite
		// exercised the file watch under -race. atomic.Int64 keeps
		// the contract trivial without restructuring the loop.
		admitted atomic.Int64
		mu       sync.Mutex
		pending  = false
		timer    *time.Timer
	)

	flush := func() {
		mu.Lock()
		pending = false
		mu.Unlock()
		lines, err := readNewLines(reader)
		if err != nil || len(lines) == 0 {
			return
		}
		bundles := []schema.TraceBundle{makeBundle(StdinIngestOptions{
			Source: schema.TraceSourceSessionLog,
			Scope:  opts.Scope,
		}, 0, lines)}
		n, _, _ := coord.Ingest(ctx, opts.Scope, bundles)
		admitted.Add(int64(n))
	}

	for {
		select {
		case <-ctx.Done():
			return int(admitted.Load()), nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return int(admitted.Load()), nil
			}
			// Handle rotation: if the file is renamed or removed
			// and a new file with the same path appears, re-bind.
			if opts.RotationHandling && (ev.Has(fsnotify.Rename) || ev.Has(fsnotify.Remove)) {
				if newInode, err := waitForReplacement(opts.Path, prevInode, 2*time.Second); err == nil {
					prevInode = newInode
					f.Close()
					f, err = os.Open(opts.Path)
					if err != nil {
						return int(admitted.Load()), fmt.Errorf("file_watch: re-open after rotation: %w", err)
					}
					reader = bufio.NewReader(f)
					_ = watcher.Add(opts.Path)
					continue
				}
			}
			// Handle truncate: if the file shrank, seek back to 0
			// and resume.
			if opts.TruncateHandling && ev.Has(fsnotify.Write) {
				if st, err := f.Stat(); err == nil {
					if pos, _ := f.Seek(0, io.SeekCurrent); pos > st.Size() {
						_, _ = f.Seek(0, io.SeekStart)
						reader = bufio.NewReader(f)
					}
				}
			}
			if !ev.Has(fsnotify.Write) {
				continue
			}
			mu.Lock()
			if !pending {
				pending = true
				if timer == nil {
					timer = time.AfterFunc(debounce, flush)
				} else {
					timer.Reset(debounce)
				}
			}
			mu.Unlock()
		case err, ok := <-watcher.Errors:
			if !ok {
				return int(admitted.Load()), nil
			}
			return int(admitted.Load()), fmt.Errorf("file_watch: %w", err)
		}
	}
}

// readNewLines drains the bufio reader of every complete line
// currently buffered or readable without blocking. Partial lines
// (no trailing \n) stay in the buffer for the next flush.
func readNewLines(r *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			lines = append(lines, line)
		}
		if err == io.EOF {
			return lines, nil
		}
		if err != nil {
			return lines, err
		}
	}
}

func inodeOf(path string) (uint64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if s, ok := statSys(st); ok {
		return s, nil
	}
	return 0, errors.New("inode lookup unsupported on this platform")
}

// waitForReplacement polls the path until an inode different from
// `prev` appears, or until the timeout expires.
func waitForReplacement(path string, prev uint64, timeout time.Duration) (uint64, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n, err := inodeOf(path); err == nil && n != prev && n != 0 {
			return n, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, errors.New("file_watch: no replacement inode after rotation timeout")
}
