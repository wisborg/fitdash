package panel

import (
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// Panel is one dashboard element, in three phases.
//
// The phases are not decoration. A HUD that composites over footage can get
// away with one Draw method plus an optional DrawStatic taking the same
// per-frame type, and this project's sibling does exactly that -- and it is the
// arrangement that failed there. The static call receives a per-frame value
// with everything but the dimensions left at zero, which makes "do not read
// anything else from it" a convention held up by a doc comment. When it was
// broken, an axis was labelled against one origin while the playhead drawn on
// top of it used another; both layers drew, they disagreed, and nothing errored.
//
// Splitting Prepare out makes that a compile error instead of a rule:
//
//   - Accepts decides whether the panel is placed at all, before any box
//     exists. This is where an activity-level absence is answered -- a pool
//     swim has no GPS, an indoor ride has no elevation -- and a panel that
//     declines is pruned from the layout so its siblings grow into its space.
//   - Prepare binds the panel to one render and one box, returning a Painter
//     that holds everything fixed for the whole video: the box, a projection,
//     an axis range, resolved font sizes.
//   - The Painter draws, in two passes, from that one set of fields.
type Panel interface {
	// Name identifies the panel in diagnostics and in the render summary that
	// reports which panels drew and which declined.
	Name() string

	// Accepts reports whether this panel has anything to show for this
	// activity. Returning false is a decision, not a failure: the layout
	// closes up around it.
	//
	// This is ALSO the panel's declaration of its absent-data policy, and
	// deliberately the only one. A panel returning true unconditionally has
	// chosen to draw a placeholder when a reading is missing; one that
	// inspects ctx has chosen to decline. A separate Policy method would be a
	// second declaration free to disagree with the behaviour.
	Accepts(ctx *Context) bool

	// Prepare binds the panel to one render. Everything invariant across the
	// video is computed here, once, and kept on the returned Painter.
	Prepare(ctx *Context, box Box) Painter
}

// Painter is one panel bound to one render.
type Painter interface {
	// Static draws what does not change: chrome, axes, labels, the dim
	// outline of a whole route. It is called ONCE per render, and it has no
	// Frame in scope -- not a zeroed one, none -- so there is no per-frame
	// value that could be silently absent here.
	Static(c *Canvas)

	// Dynamic draws what changes, over the static layer, once per frame.
	//
	// It must NOT mutate the Painter. Nothing today depends on that, and it
	// is what keeps parallel frame rendering possible later; a memo cache
	// written during drawing is why this project's sibling needs a mutex
	// around one.
	Dynamic(c *Canvas, f Frame)
}

// NoStatic is embedded by a panel with nothing invariant to draw.
//
// Embedding it is a deliberate, greppable declaration that the question was
// asked and the answer was none, where simply omitting the method would be
// indistinguishable from not having considered it. The panel contract asks
// which part of a panel is invariant across the whole render; making the
// method required means that question cannot be skipped.
type NoStatic struct{}

// Static draws nothing.
func (NoStatic) Static(*Canvas) {}

// Context is everything computed once and identical on every frame of one
// render.
//
// Panels read it in Accepts and Prepare and then let it go: a Painter keeps
// what it needs as its own fields. Frame deliberately holds no pointer back to
// here, so there is no route by which per-render state can arrive during the
// dynamic pass pretending to be per-frame, or the reverse.
type Context struct {
	// Track is the decoded activity.
	Track *fitactivity.Track

	// Report is what the activity actually contains, metric by metric, with
	// coverage rather than mere presence.
	//
	// It is here so fitdash has exactly ONE rule for "does this activity carry
	// power", shared by the inspect command and by every panel's Accepts. A
	// panel deriving it by walking the samples would be free to disagree with
	// the report the user just printed -- same file, two answers, and the
	// disagreement shows up only as a panel missing when inspect said it
	// should be there.
	Report inspect.Report

	// Timer resolves the activity's window and its elapsed-versus-active time.
	Timer *fitactivity.TimerModel

	// Timeline maps a frame index to an instant. See Timeline.
	Timeline Timeline

	// Width, Height are the output frame's pixel dimensions.
	Width, Height int

	// FontScale is the layout's base text size as a fraction of the frame's
	// smaller dimension, carried here so a Painter can resolve its own sizes
	// during Prepare rather than during drawing.
	FontScale float64
}

// BasePx is the layout's base text size in pixels for this frame size.
func (c *Context) BasePx() float64 {
	unit := float64(c.Width)
	if c.Height < c.Width {
		unit = float64(c.Height)
	}
	return c.FontScale * unit
}

// Frame is the per-frame state, and carries nothing that is constant across
// the render.
type Frame struct {
	// Index is the 0-based frame number.
	Index int

	// At is the instant in the activity this frame shows, on the activity's
	// own clock.
	At time.Time

	// Elapsed and Active are the two clocks at At: wall-clock time since the
	// activity began, and moving time with pauses subtracted.
	//
	// Both, always, even when they are equal. A dashboard has to choose which
	// one its timeline runs on, and a reader should see the pair and the
	// choice rather than one number that answered the question for them.
	// Computed once here rather than N times in N panels.
	//
	// When the file carried no timer events, Active equals Elapsed because
	// there were no pauses to find -- which is NOT the same as an activity
	// that never stopped. A panel showing active time must consult
	// Context.Timer.HasTimerEvents and say so, rather than printing a number
	// that merely looks like a measurement.
	Elapsed, Active time.Duration

	// Sample is the activity interpolated to At.
	//
	// INVARIANT: when HasSample is false, this is the zero Sample. That is
	// what lets a panel write one presence check --
	// `if f.Sample.HasHeartRate { ... } else { placeholder }` -- and be
	// correct inside a dropout without a second condition, because every
	// presence flag on a zero Sample is false.
	//
	// The tempting change is to hold the last good sample so the dashboard
	// does not flicker. That is the confident lie in its purest form: a heart
	// rate frozen at 148 through a two-minute dropout is indistinguishable
	// from a live reading.
	Sample    fitactivity.Sample
	HasSample bool

	// Paused reports whether At falls inside one of the activity's paused
	// intervals. What a panel does with it -- dim a readout, freeze a pace,
	// print a marker -- is the panel's decision; reporting the fact is the
	// renderer's.
	Paused bool
}
