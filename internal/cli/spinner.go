package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// spinnerFrames is the braille progress cycle painted during a long
// silent step.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const spinnerInterval = 100 * time.Millisecond

// spinner renders a single-line "<frame> <msg>" progress indicator to
// an interactive terminal so a long silent step (engine boot, git
// clone) visibly shows bough is alive rather than hung. When the writer
// is not a TTY — the WorktreeCreate hook pipes stderr, CI captures it, a
// shell redirect — the spinner is INERT: no goroutine, no bytes written,
// so the plain [bough] lines remain the entire, greppable log. That
// inertness is the contract the hook path relies on.
type spinner struct {
	w        io.Writer
	tty      bool
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// startSpinner begins animating msg on w and returns a handle whose
// Stop() halts the animation and clears the line. Stop() is safe to call
// on a non-TTY (inert) spinner.
func startSpinner(w io.Writer, msg string) *spinner {
	s := &spinner{w: w, tty: isInteractive(w)}
	if !s.tty {
		return s
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run(msg)
	return s
}

func (s *spinner) run(msg string) {
	defer close(s.done)
	t := time.NewTicker(spinnerInterval)
	defer t.Stop()
	// Paint frame 0 immediately so there is no interval-long blank before
	// the first tick.
	fmt.Fprintf(s.w, "\r%c %s", spinnerFrames[0], msg)
	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			i = (i + 1) % len(spinnerFrames)
			fmt.Fprintf(s.w, "\r%c %s", spinnerFrames[i], msg)
		}
	}
}

// Stop halts the animation and erases the spinner line (CR + clear-to-
// end-of-line) so the caller's next [bough] line starts on a clean row.
// No-op for an inert (non-TTY) spinner.
func (s *spinner) Stop() {
	if !s.tty {
		return
	}
	// sync.Once so a second Stop() (a future extra call site, or an
	// error path that also defers Stop) cannot panic on a double
	// `close(s.stop)` and crash `bough create`.
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done
		fmt.Fprint(s.w, "\r\033[K")
	})
}

// isInteractive reports whether w is a terminal bough may animate on. A
// non-*os.File writer (a bytes.Buffer in tests, a pipe from the hook)
// can never be a TTY, so it is treated as non-interactive.
func isInteractive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
