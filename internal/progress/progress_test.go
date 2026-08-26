package progress

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeClock lets the throttling be tested without sleeping. A test that slept
// for real would take seconds and would still be timing-dependent.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestReporter(w *bytes.Buffer, total int, inline bool) (*Reporter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	r := New(w, total, inline)
	r.now = clock.now
	return r, clock
}

// TestReporter_ThrottlesToTheInterval is the property that makes this usable
// at all: Update is called once per frame, up to 46,000 times, and must not
// produce 46,000 lines.
func TestReporter_ThrottlesToTheInterval(t *testing.T) {
	var buf bytes.Buffer
	r, clock := newTestReporter(&buf, 1000, false)

	// A thousand updates inside one interval must produce nothing.
	for i := 1; i <= 1000; i++ {
		r.Update(i)
	}
	if got := buf.String(); got != "" {
		t.Errorf("1000 updates inside one interval printed:\n%s", got)
	}

	clock.advance(DefaultInterval)
	r.Update(500)
	first := strings.Count(buf.String(), "500/1000")
	if first != 1 {
		t.Errorf("after one interval, got %d lines mentioning 500/1000, want 1", first)
	}

	// And more updates before the next interval are still suppressed.
	for i := 501; i <= 600; i++ {
		r.Update(i)
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Errorf("got %d lines, want 1 -- updates within an interval must be suppressed", n)
	}

	clock.advance(DefaultInterval)
	r.Update(900)
	if !strings.Contains(buf.String(), "900/1000") {
		t.Error("the next interval did not print")
	}
}

// TestReporter_FirstUpdateIsSilent pins a deliberate choice.
//
// A render that fails in its first second should print the error, not leave a
// stale "0%" line above it; a render that finishes inside one interval should
// print nothing rather than flashing a progress line at the user.
func TestReporter_FirstUpdateIsSilent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := newTestReporter(&buf, 100, false)
	r.Update(1)
	if got := buf.String(); got != "" {
		t.Errorf("the first update printed %q, want nothing", got)
	}
}

// TestReporter_ReportsCountAndPercentage checks the line says what it should,
// with values derived by hand.
func TestReporter_ReportsCountAndPercentage(t *testing.T) {
	var buf bytes.Buffer
	r, clock := newTestReporter(&buf, 200, false)
	r.Update(0)
	clock.advance(DefaultInterval)
	r.Update(50)

	got := buf.String()
	for _, want := range []string{"50/200", "25%"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line %q is missing %q", strings.TrimSpace(got), want)
		}
	}
}

// TestReporter_EstimatesRemainingTime derives the expectation from the fake
// clock: a quarter done after four seconds means about twelve seconds left.
func TestReporter_EstimatesRemainingTime(t *testing.T) {
	var buf bytes.Buffer
	r, clock := newTestReporter(&buf, 400, false)
	r.Update(0) // starts the clock
	clock.advance(4 * time.Second)
	r.Update(100)

	if got := buf.String(); !strings.Contains(got, "12s left") {
		t.Errorf("progress line %q should estimate about 12s left (a quarter done in 4s)", strings.TrimSpace(got))
	}
}

// TestReporter_WithholdsAnEstimateItCannotMake pins that an estimate from
// almost no work is not shown. Two frames in is noise presented as
// information, and a wildly wrong "about 3h left" is worse than none.
func TestReporter_WithholdsAnEstimateItCannotMake(t *testing.T) {
	var buf bytes.Buffer
	r, clock := newTestReporter(&buf, 100000, false)
	r.Update(0)
	clock.advance(DefaultInterval)
	// Under a second of measured work.
	r.start = clock.t.Add(-500 * time.Millisecond)
	r.Update(2)
	if strings.Contains(buf.String(), "left") {
		t.Errorf("an estimate was offered from half a second of work: %q", strings.TrimSpace(buf.String()))
	}
}

// TestReporter_InlineOverwritesAndDoneClears covers the terminal path.
//
// Done matters: a progress line left in place is overwritten only as far as
// the next message is long, so a short summary printed after a long progress
// line leaves the tail of the progress line visible after it.
func TestReporter_InlineOverwritesAndDoneClears(t *testing.T) {
	var buf bytes.Buffer
	r, clock := newTestReporter(&buf, 100, true)
	r.Update(0)
	clock.advance(DefaultInterval)
	r.Update(50)

	if !strings.Contains(buf.String(), "\r") {
		t.Error("inline progress did not use a carriage return")
	}
	if strings.Contains(buf.String(), "\n") {
		t.Error("inline progress emitted a newline; it would scroll instead of overwriting")
	}

	before := buf.Len()
	r.Done()
	after := buf.String()[before:]
	if !strings.HasPrefix(after, "\r") || !strings.HasSuffix(after, "\r") {
		t.Errorf("Done should blank the line and return to its start; wrote %q", after)
	}
	if !strings.Contains(after, "    ") {
		t.Error("Done did not blank the line, so a shorter message would leave the tail visible")
	}
}

// TestReporter_NotInlineUsesLines pins the redirected case: a log file
// collecting one enormous line full of carriage returns is unreadable.
func TestReporter_NotInlineUsesLines(t *testing.T) {
	var buf bytes.Buffer
	r, clock := newTestReporter(&buf, 100, false)
	r.Update(0)
	clock.advance(DefaultInterval)
	r.Update(50)
	if strings.Contains(buf.String(), "\r") {
		t.Error("non-inline progress used a carriage return")
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Error("non-inline progress did not end its line")
	}
}

// TestReporter_NilIsSilentAndSafe is what lets --quiet pass nil rather than
// making every call site guard.
func TestReporter_NilIsSilentAndSafe(t *testing.T) {
	var r *Reporter
	r.Update(1)
	r.Done()
}

// TestIsTerminal_SaysNoForAPipe checks the inline decision is made on
// something real rather than assumed.
func TestIsTerminal_SaysNoForAPipe(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("a regular file was reported as a terminal; progress would fill it with carriage returns")
	}
}
