package panel

import (
	"fmt"
	"time"
)

// clockTemplate is the widest a clock readout gets before it needs a second
// hours digit, and it is what the panel measures against.
//
// Sizing to the TEMPLATE rather than to the current value is the whole trick.
// A size derived from "0:00:07" would grow the text as the activity ran, and
// then shrink it again at every rollover -- and because the size is resolved
// in Prepare, it would also be a per-render decision made from one frame's
// data. The template makes the size a property of the layout instead.
const clockTemplate = "8:88:88"

// --clock's legal values -- which of the two clocks this panel draws LARGE,
// with the other kept beneath it. Exported so the CLI's own validation and
// Context.Clock compare against one pair of strings rather than each
// defining its own, the same reason --bottom-band's and --gauges' values
// live beside the panels that read them.
const (
	// ClockElapsed is the default and the zero value's behaviour, so an
	// unset Context draws exactly what it always has: elapsed large,
	// active beneath it.
	ClockElapsed = "elapsed"

	// ClockActive puts moving time on top instead. It is the natural
	// pairing for --pauses skip, where video time advances only while the
	// timer was running and the elapsed clock is the one that jumps -- but
	// the two flags are deliberately independent, and neither implies the
	// other (see Context.Pauses).
	ClockActive = "active"
)

// ElapsedPanel shows the activity's two clocks: wall-clock time since it began,
// and moving time with pauses subtracted.
//
// Both, always, even when they are equal, and in either order (see
// Context.Clock). A dashboard has to choose which one its timeline runs on,
// and a viewer should see the pair and the choice rather than one number
// that answered the question for them. Under the default --pauses freeze the
// render runs on elapsed, so a stopped activity reads AS stopped: elapsed
// keeps counting, active does not. Under --pauses skip it runs on active,
// and elapsed is the clock that jumps at each cut.
//
// Name() stays "elapsed" under either order, deliberately, and this is not
// the small lie MarkerPanel's own rename was made to avoid: that panel would
// have printed "highlight" on a render where no highlight was configured at
// all, naming a thing that did not exist. This panel draws the elapsed clock
// in every configuration -- --clock chooses which of its two rows is the
// large one, not whether elapsed is shown -- so the name is true whatever
// the flag says.
type ElapsedPanel struct{}

// Name identifies the panel.
func (ElapsedPanel) Name() string { return "elapsed" }

// Accepts is unconditionally true, which is this panel's declaration that it
// draws a PLACEHOLDER rather than declining.
//
// Every activity has a duration, so there is no activity-level absence for it
// to decline over. What it can lack is active time -- a file carrying no timer
// events has no pauses to find -- and that is an instant-level absence handled
// in Dynamic, by the same rule any missing reading gets.
func (ElapsedPanel) Accepts(*Context) bool { return true }

// Prepare resolves every size and position once.
//
// Nothing here is recomputed per frame, and that is not an optimisation: a
// size resolved during drawing would be per-frame state deciding where the
// static chrome went, so the rule under the label could end up laid out
// against one size while the clock drawn over it used another.
func (p ElapsedPanel) Prepare(ctx *Context, box Box) Painter {
	e := &elapsedPainter{box: box, activeFirst: ctx.Clock == ClockActive}

	// Sizes are fractions of the box, not of the frame: this panel must fit
	// the rectangle it was given, whatever shape the layout made it.
	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	e.labelPx = unit * 0.13
	e.clockPx = unit * 0.34
	e.subPx = unit * 0.11

	// Then shrink whatever does not fit the box's WIDTH. A box that is wide
	// and short sizes from its height above and would overflow horizontally.
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize(clockTemplate, box.W*0.92, e.clockPx); err == nil {
			e.clockPx = px
		}
		// Measured against the prefix this render will actually draw, not
		// against the wider of the two: fitting "ELAPSED" every time would
		// shrink the sub-line under the DEFAULT order, where the prefix is
		// the shorter "ACTIVE", and a flag nobody passed must not change
		// the pixels of a render that does not use it.
		if px, err := ctx.Fonts.FitSize(e.subCaption()+" "+clockTemplate, box.W*0.92, e.subPx); err == nil {
			e.subPx = px
		}
	}

	// Rows are spaced against `unit` and centred in the box rather than placed
	// at fractions of its height, so the group keeps its shape whatever
	// rectangle the layout hands over. A box that grows -- because a
	// neighbouring panel declined and the layout closed up -- would otherwise
	// pull the label and the sub-line away from the clock they belong to.
	centerY := box.Y + box.H/2
	e.clockY = centerY
	e.subY = centerY + unit*0.30
	e.centerX = box.X + box.W/2

	// The CAPTION alone is the exception: it is positioned off box.H, not
	// off `unit` above, because this panel always sits beside Distance() in
	// a Row (layouts.go) and Row siblings are guaranteed the same box
	// HEIGHT -- never the same width, which is what `unit` collapses to
	// the moment this panel's own box is narrower than it is tall. See
	// captionOffset's own doc comment for the mismatch that fell out of
	// multiplying the shared fraction by each panel's own `unit` instead.
	//
	// This is safe rather than merely convenient: box.H >= unit always (unit
	// IS min(box.W, box.H)), so this can only move the caption FURTHER from
	// centre than `unit` would have, never closer -- and TestReadout_
	// RowsClearOneAnother's whole point is that a caption too CLOSE to the
	// value is the failure mode, never one with room to spare. In the
	// ordinary case this panel is placed in, box.W comfortably exceeds
	// box.H (a clock reads wide, not tall), so unit already equals box.H and
	// this changes nothing; it only diverges in the narrow case the pairing
	// exists to fix.
	e.labelY = centerY - box.H*captionOffset
	// The rule beneath the label shares box.H the same way, and shares its
	// own geometry with Readout's rule via captionRuleGeometry -- see that
	// function's doc comment for why the gap and the thickness take the
	// same basis as the caption while the width does not.
	e.ruleY, e.ruleW, e.ruleH = captionRuleGeometry(box.H, box, e.labelY)
	return e
}

type elapsedPainter struct {
	box Box

	// activeFirst puts moving time in the large row and elapsed beneath it
	// -- Context.Clock resolved once in Prepare rather than compared per
	// frame, so which clock is where is a property of the layout the same
	// way every size in this painter is.
	activeFirst bool

	labelPx float64
	clockPx float64
	subPx   float64
	labelY  float64
	ruleY   float64
	ruleH   float64
	ruleW   float64
	clockY  float64
	subY    float64
	centerX float64
}

// Static draws the chrome: the label and the rule beneath it. Neither changes
// across the render, so both are rasterized once.
func (e *elapsedPainter) Static(c *Canvas) {
	_ = c.Text(e.mainCaption(), e.centerX, e.labelY, 0.5, 0.5, e.labelPx, c.Theme.Dim)
	c.Rect(Box{X: e.centerX - e.ruleW/2, Y: e.ruleY, W: e.ruleW, H: e.ruleH}, c.Theme.Dim)
}

// Dynamic draws the two clocks.
//
// Active is shown as a placeholder when the file carried no timer events,
// rather than as a number equal to elapsed. That equality is not a
// measurement, it is the absence of one: without timer events there are no
// pauses to locate, and printing a figure that merely looks measured is the
// same confident lie as rendering a missing heart rate as zero. It is an
// absent-data policy in exactly the sense every other panel's is.
func (e *elapsedPainter) Dynamic(c *Canvas, f Frame) {
	// The absence belongs to ACTIVE wherever active is drawn, which is the
	// whole reason this reads as "which row is the active one" rather than
	// as two independent rows. Under --clock active the placeholder is the
	// LARGE readout -- an unmeasurable number shown at the size the user
	// asked to see it at, in Theme.Absent, rather than quietly demoted to
	// the small row or replaced by the elapsed figure standing in for it.
	main, sub := FormatClock(f.Elapsed), FormatClock(f.Active)
	mainCol, subCol := c.Theme.Foreground, c.Theme.Dim
	if e.activeFirst {
		main, sub = sub, main
	}
	if !f.HasTimerEvents {
		if e.activeFirst {
			main, mainCol = ClockPlaceholder, c.Theme.Absent
		} else {
			sub, subCol = ClockPlaceholder, c.Theme.Absent
		}
	}

	_ = c.Text(main, e.centerX, e.clockY, 0.5, 0.5, e.clockPx, mainCol)
	_ = c.Text(e.subCaption()+" "+sub, e.centerX, e.subY, 0.5, 0.5, e.subPx, subCol)
}

// mainCaption and subCaption name the two rows. One pair of expressions, not
// a caption stored per row: the two must always be the OTHER of each other,
// and two independently assigned strings could say "ELAPSED" twice.
func (e *elapsedPainter) mainCaption() string {
	if e.activeFirst {
		return "ACTIVE"
	}
	return "ELAPSED"
}

func (e *elapsedPainter) subCaption() string {
	if e.activeFirst {
		return "ELAPSED"
	}
	return "ACTIVE"
}

// ClockPlaceholder is what a clock reads when there is no measurement behind
// it. Shaped like the readout it replaces so the layout does not shift, and
// drawn in Theme.Absent so it cannot be mistaken for one.
const ClockPlaceholder = "-:--:--"

// FormatClock renders d as H:MM:SS.
//
// Truncated rather than rounded, because this is a stopwatch: a clock that
// showed 0:00:01 half a second in would be ahead of the activity it is
// describing, and every frame of the first half-second would disagree with the
// video's own position.
//
// Hours are not zero-padded, so an activity under ten hours reads 1:23:45
// rather than 01:23:45. The face is monospace, so the readout's width changes
// only at ten hours, which no run this targets reaches.
//
// A negative duration reads as zero. Elapsed and Active both clamp at the
// activity's start, so this should be unreachable; showing a negative clock
// would be a stranger failure than showing none.
func FormatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	return fmt.Sprintf("%d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}
