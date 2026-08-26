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

// ElapsedPanel shows the activity's two clocks: wall-clock time since it began,
// and moving time with pauses subtracted.
//
// Both, always, even when they are equal. A dashboard has to choose which one
// its timeline runs on -- fitdash runs on elapsed, so the readout freezes
// through a pause -- and a viewer should see the pair and the choice rather
// than one number that answered the question for them. A stopped activity then
// reads AS stopped: elapsed keeps counting, active does not.
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
	e := &elapsedPainter{box: box}

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
		if px, err := ctx.Fonts.FitSize("ACTIVE "+clockTemplate, box.W*0.92, e.subPx); err == nil {
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
	e.labelY = centerY - unit*0.34
	e.ruleY = centerY - unit*0.26
	e.subY = centerY + unit*0.30
	e.ruleH = unit * 0.012
	if e.ruleH < 1 {
		e.ruleH = 1
	}
	e.centerX = box.X + box.W/2
	e.ruleW = box.W * 0.72
	return e
}

type elapsedPainter struct {
	box     Box
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
	_ = c.Text("ELAPSED", e.centerX, e.labelY, 0.5, 0.5, e.labelPx, c.Theme.Dim)
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
	_ = c.Text(FormatClock(f.Elapsed), e.centerX, e.clockY, 0.5, 0.5, e.clockPx, c.Theme.Foreground)

	if f.HasTimerEvents {
		_ = c.Text("ACTIVE "+FormatClock(f.Active), e.centerX, e.subY, 0.5, 0.5, e.subPx, c.Theme.Dim)
		return
	}
	_ = c.Text("ACTIVE "+ClockPlaceholder, e.centerX, e.subY, 0.5, 0.5, e.subPx, c.Theme.Absent)
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
