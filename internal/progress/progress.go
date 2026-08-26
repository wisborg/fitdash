// Package progress reports how far a long render has got.
//
// A 46,000-frame render takes minutes and, without this, prints nothing at all
// while it does -- which is indistinguishable from a hang, and is a bug report
// waiting to happen.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultInterval is how often progress is printed.
//
// Two seconds rather than every frame: at 300 frames a second a per-frame line
// would produce more output than work, and even a per-frame carriage return
// costs a syscall per frame. It is also slow enough that the remaining-time
// estimate has settled between updates and does not visibly jitter.
const DefaultInterval = 2 * time.Second

// Reporter prints periodic progress to a writer.
//
// The zero value is not useful; build one with New. A nil *Reporter is safe to
// call, and reports nothing -- so a caller with --quiet passes nil rather than
// guarding every call site.
type Reporter struct {
	w        io.Writer
	total    int
	interval time.Duration
	inline   bool

	mu    sync.Mutex
	start time.Time
	last  time.Time
	shown bool

	// now is swappable so the throttling can be tested without sleeping.
	now func() time.Time
}

// New returns a Reporter writing to w.
//
// inline uses a carriage return to overwrite one line in place, which is right
// for a terminal and wrong for a log file -- a redirected stream would collect
// a single enormous line. The caller decides, because only it knows where the
// output is going.
func New(w io.Writer, total int, inline bool) *Reporter {
	return &Reporter{
		w: w, total: total, interval: DefaultInterval, inline: inline,
		now: time.Now,
	}
}

// Update reports that i of the total items are done.
//
// Safe on a nil Reporter, and throttled: at most one line per interval,
// regardless of how often it is called.
func (r *Reporter) Update(i int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if r.start.IsZero() {
		// last starts WITH start, not at the zero time. Leaving it zero makes
		// the first call's elapsed time enormous, so the throttle passes and
		// the very first update prints -- which is the opposite of what is
		// wanted. A render that fails in its first second should show the
		// error, not leave a stale "0%" line above it, and one that finishes
		// inside a single interval should print nothing rather than flashing
		// a progress line at the user.
		r.start, r.last = now, now
		return
	}
	if now.Sub(r.last) < r.interval {
		return
	}
	r.last = now
	r.write(i, now)
}

// Done clears the progress line, so whatever is printed next starts clean.
//
// Only meaningful when inline: a line left half-overwritten by a shorter
// message is how progress output ends up embedded in a summary.
func (r *Reporter) Done() {
	if r == nil || !r.inline || !r.shown {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "\r%s\r", strings.Repeat(" ", 60))
	r.shown = false
}

func (r *Reporter) write(i int, now time.Time) {
	pct := 0.0
	if r.total > 0 {
		pct = float64(i) / float64(r.total) * 100
	}
	line := fmt.Sprintf("%d/%d frames (%.0f%%)%s", i, r.total, pct, remaining(r.start, now, i, r.total))
	if r.inline {
		fmt.Fprintf(r.w, "\r%-60s", line)
	} else {
		fmt.Fprintln(r.w, line)
	}
	r.shown = true
}

// remaining estimates the time left, or returns "" when it cannot yet.
//
// Linear extrapolation from the average rate so far, which is honest for this
// workload: every frame costs about the same, since each one draws the same
// panels over the same layout. It is deliberately not shown before any
// meaningful work has happened -- an estimate from two frames is noise
// presented as information.
func remaining(start, now time.Time, done, total int) string {
	if done <= 0 || total <= 0 || done >= total {
		return ""
	}
	elapsed := now.Sub(start)
	if elapsed < time.Second {
		return ""
	}
	perItem := elapsed / time.Duration(done)
	left := perItem * time.Duration(total-done)
	return fmt.Sprintf(", about %s left", left.Round(time.Second))
}

// IsTerminal reports whether f is a character device, i.e. whether inline
// progress makes sense.
//
// A redirected stream is not, so `fitdash ... 2>log` collects one line per
// interval instead of one very long line full of carriage returns.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
