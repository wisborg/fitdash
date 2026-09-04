package panel

import (
	"image/color"
	"math"

	"github.com/wisborg/fitactivity"
)

// ClimbPanel draws the activity's cumulative elevation gain and loss as two
// horizontal tracks, gain above and loss below -- up is up, so no legend is
// needed to say which row is which.
//
// One responsibility: this panel answers "how much climbing has the
// activity done so far", nothing else. It shares its data with
// ElevationPanel (both read ctx.Elevation) but draws none of its own trace
// or playhead, and ElevationPanel draws no gain/loss bars -- a panel that
// drew both a route and a profile would be two panels unable to be placed
// independently, which is exactly what this split avoids.
//
// # The shared scale
//
// Both tracks are drawn against ONE scale: the larger of the activity's own
// total gain and total loss (maxTotal, resolved once in Prepare). This is
// the misreading the design exists to prevent -- a viewer compares the two
// bars on sight, and normalising each to its OWN total would make two bars
// of equal length mean two different numbers of metres. Under one shared
// scale the shorter track's ghost simply ends short of the box, which states
// the ratio between the two for free, without a legend and without a second
// number to read.
//
// No colour distinguishes gain from loss: Theme has no such role (see
// canvas.go's Theme doc comment on why a panel never invents one), and sign,
// caption and vertical position already carry it unambiguously.
//
// # Static and dynamic
//
// Each track has a dim ghost, at the fraction of the shared scale its OWN
// total represents, drawn ONCE in Static because an activity's total gain
// and total loss are fixed for the whole render. The live fill -- the
// fraction reached at the current instant -- is drawn on top in Dynamic,
// every frame. This is the cleanest instance of the static/dynamic split in
// the project: the invariant pass draws the destination, the per-frame pass
// draws the progress, and it is also what makes a single exported frame of
// this panel legible on its own -- exactly the property the marker strip's
// own comment names as a real weakness of a design that only reads in
// motion.
//
// # Absence
//
// Accepts is elevationIsPlottable(ctx), the identical predicate
// ElevationPanel uses (see elevation.go) -- one panel drawing on a model the
// other declined is the disagreement that function exists to prevent. Where
// the activity never carried elevation at all, this panel declines outright
// and the layout closes up around it (see layouts.go).
//
// Per frame, absence is a placeholder: both tracks wash their full length in
// Fade(Theme.Absent, elevationAbsentFillAlpha(theme)) -- reusing
// ElevationPanel's own derivation rather than a second one that could drift
// from it -- and both readings show ReadoutPlaceholder. This fires on an
// ordinary sensor dropout (f.Sample.HasDistance false) and on THE PRE-DATA
// TRAP: fitactivity.ElevationModel.AtDistance clamps to its own ends, so
// asked for a distance before the model's own StartDistance it answers gain
// 0, loss 0 -- confidently, with no error. ElevationPanel is washing that
// same stretch absent and refusing its dot at the same instant; printing
// "0 m" here would put the two panels in disagreement about the same frame,
// one saying unknown and one saying zero. So Dynamic checks
// f.Sample.Distance against the model's own StartDistance BEFORE ever
// calling AtDistance, and takes the placeholder branch rather than trust the
// clamp to be the answer -- the clamp makes the wrong behaviour the default,
// so this has to be written rather than left to fall out on its own.
//
// Past the model's own TotalDistance the clamp is the opposite case and is
// left alone: gain and loss there are the activity's own final totals, which
// is the honestly known answer once the activity has actually finished,
// matching ElevationPanel's identical choice to keep filling (not wash) past
// its own axis end.
//
// # Scaling
//
// Most of what this Painter draws is a fraction of this panel's own box or of
// unit (min(box.W, the box.H each row gets)), never a pixel constant -- see
// climbLabelWidthFraction and its neighbours. The panel is placed as a
// full-width strip in both layout trees (layouts.go) and has to read at
// 1080p, at 4K, and in the much narrower portrait tree without a
// layout-specific branch in this file.
//
// The caption ("GAIN"/"LOSS") is the one exception, and deliberately: this
// strip is one row of eight in the landscape tree and one of sixteen in the
// portrait tree (see layouts.go), split again into a gain row and a loss row,
// so unit here is a fraction of a fraction of the frame -- far smaller than
// the box a gauge readout gets for the SAME caption role ("HEART RATE",
// "ELAPSED", "DISTANCE"). Sizing the caption off unit ties its legibility to
// how much weight this strip happens to be given, which is a layout decision
// with nothing to do with whether a viewer can read the word "GAIN". This is
// the identical departure ElevationPanel's own axis labels already made, for
// the identical reason (see elevAxisLabelFraction's doc comment): chrome that
// must read consistently alongside a frame's OTHER captions is sized from
// ctx.BasePx(), the layout's base text size for this frame, not from a box
// this panel's own row height happens to resolve to. See
// climbCaptionFraction.
type ClimbPanel struct{}

// Name identifies the panel. This is also the key a future --panels flag
// would select it by (see docs/architecture.md), so it stays short,
// lowercase and names the thing on screen.
func (ClimbPanel) Name() string { return "climb" }

// Accepts is elevationIsPlottable(ctx) -- see the type doc comment's
// "Absence" section for why this must be the SAME predicate
// ElevationPanel.Accepts uses rather than a second copy of it.
func (ClimbPanel) Accepts(ctx *Context) bool { return elevationIsPlottable(ctx) }

// Prepare lays out the two tracks once, from the activity's own totals.
func (ClimbPanel) Prepare(ctx *Context, box Box) Painter {
	p := &climbPainter{box: box}
	if !elevationIsPlottable(ctx) {
		// Accepts should have prevented this. Drawing nothing is the
		// least-wrong option left, mirroring ElevationPanel's own identical
		// defensive guard in its Prepare -- not a policy this panel chooses
		// for itself.
		return p
	}
	m := ctx.Elevation
	p.model = m
	p.profileStart, p.axisEnd = m.StartDistance(), m.TotalDistance()
	p.totalGain, p.totalLoss = m.TotalGain(), m.TotalLoss()
	p.maxTotal = math.Max(p.totalGain, p.totalLoss)
	if p.maxTotal <= 0 {
		// Both totals are zero. elevationIsPlottable already requires a real
		// elevation RANGE (max > min), so this should not be reachable in
		// practice -- but a zero scale can only ever divide by itself, and
		// refusing to draw is safer than trusting that invariant to hold
		// forever.
		return p
	}

	rowH := box.H / 2
	unit := rowH
	if box.W < unit {
		unit = box.W
	}

	labelW := box.W * climbLabelWidthFraction
	readingW := box.W * climbReadingWidthFraction
	gap := box.W * climbGapFraction

	p.trackX = box.X + labelW + gap

	// trackW is WHATEVER WIDTH REMAINS after the caption column, the two
	// gaps and the reading column -- not a fixed fraction of the box the way
	// an earlier version of this Painter (climbTrackWidthFraction, since
	// removed) computed it. That fixed fraction was itself the fix for an
	// even earlier version that stretched the track to the box's far edge
	// and then placed the reading past IT, at the box's own far edge --
	// which stranded the reading a whole gap away from the shorter of the
	// two bars. Reserving the track a deliberately narrower share solved
	// that, but at the cost of leaving the freed width sitting as slack
	// INSIDE this panel's own box, between the reading and the box's far
	// edge, which grows or shrinks with the box rather than disappearing --
	// exactly the gap this version removes.
	//
	// The reading is placed immediately after the track (readingX, below),
	// not at the box's far edge, so there is no longer a second rule
	// competing with this one: the track can fill its box because nothing
	// downstream of it depends on where the box's edge is, only on where the
	// track's OWN edge is. That is also what keeps the reading adjacent to
	// its own bar without a separate case for the shorter bar: whichever
	// total is smaller, its ghost and fill still measure against the SAME
	// trackW, so the reading sitting at trackX+trackW+gap is never more than
	// one gap from the LARGER bar's own end, which is the fixed reference
	// both rows share (see climbReadingWidthFraction's doc comment).
	trackW := box.W - labelW - gap - gap - readingW
	if trackW < 0 {
		trackW = 0
	}
	p.trackW = trackW
	p.barH = math.Max(2, rowH*climbBarHeightFraction)

	p.gainY = box.Y + rowH/2
	p.lossY = box.Y + rowH + rowH/2
	p.labelX = box.X
	// readingX sits one gap past where the TRACK's own column ends, not at
	// the box's far edge -- see climbReadingWidthFraction's doc comment for
	// why this is a fixed column rather than an anchor off either bar's own
	// current fill. Now that trackW fills the box (above) rather than
	// reserving a fixed share of it, this is also the column immediately
	// after the last pixel any bar could ever reach, so the whole group --
	// caption, tracks, reading -- occupies the box with no structural slack
	// between the reading and the box's own far edge.
	p.readingX = p.trackX + p.trackW + gap

	// labelPx departs from unit -- see the type doc comment's "Scaling"
	// section and climbCaptionFraction for why the caption alone is sized
	// off the frame rather than off this row's own (usually tiny) height.
	p.labelPx = ctx.BasePx() * climbCaptionFraction
	p.readingPx = unit * climbReadingFraction

	// Sized once against the labels and the widest reading EACH ROW will
	// ever actually show -- the activity's own total, since a cumulative
	// figure never exceeds it (see the type doc comment's "past the model's
	// own TotalDistance" case) -- never against a guessed worst case. Only
	// ever shrinks (FitSize's own contract), and chained across both calls
	// the same way ElevationPanel's own distPx is (elevation.go's Prepare),
	// so "GAIN" and "LOSS" share one size and both readings share another,
	// rather than each row picking its own and the two reading columns
	// disagreeing in size for no reason a viewer could point to.
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize("GAIN", labelW*0.92, p.labelPx); err == nil {
			p.labelPx = px
		}
		if px, err := ctx.Fonts.FitSize("LOSS", labelW*0.92, p.labelPx); err == nil {
			p.labelPx = px
		}
		if px, err := ctx.Fonts.FitSize(formatElevation(p.totalGain), readingW*0.92, p.readingPx); err == nil {
			p.readingPx = px
		}
		if px, err := ctx.Fonts.FitSize(formatElevation(p.totalLoss), readingW*0.92, p.readingPx); err == nil {
			p.readingPx = px
		}
	}
	return p
}

// climbLabelWidthFraction is how much of the box's own width the "GAIN"/
// "LOSS" caption column reserves, a fraction of box.W rather than of unit --
// this is a HORIZONTAL column width, not a size answering off the box's
// smaller dimension the way font sizes below do. A judgement call to check
// at the gate: wide enough that "GAIN" and "LOSS" never fight FitSize down
// to an illegible size in the portrait tree's narrower box, tight enough
// that it does not eat into the track the two bars actually draw on.
const climbLabelWidthFraction = 0.10

// climbReadingWidthFraction is the reading column's own reserved width, a
// fraction of box.W, wider than the caption's because a reading can run to
// several digits ("1234 m") where a caption never grows past four letters.
//
// It sizes the column; it does not place it at the box's far edge. Prepare
// anchors readingX one climbGapFraction past where the TRACK's own column
// ends (trackX+trackW), so the column this fraction reserves sits close to
// the bars it describes. That has to be a FIXED x, the same for the gain row
// and the loss row, rather than each row's own reading following its OWN
// bar's current fill: the two bars end at different lengths by design (the
// shared scale IS the point, see the type doc comment), so anchoring each
// reading to its own bar's end would put the two numbers at two different x
// positions that also drift as the activity progresses. A fixed column is
// what a viewer can read as "the number that goes with this row" without it
// jittering.
//
// The two rows sharing one x falls out for free now that trackW (Prepare)
// is the SAME width for both rows -- gain and loss are drawn against one
// shared track column, not two independently sized ones, so there is
// nothing left to reconcile between them: whichever row's bar is shorter,
// its reading still sits at the one column both rows' bars end at.
const climbReadingWidthFraction = 0.18

// climbGapFraction is the daylight between the caption column and the
// track, and between the track and the reading column, each as a fraction
// of box.W -- so the columns never touch even on the narrowest box this
// panel is placed in.
const climbGapFraction = 0.02

// climbBarHeightFraction is a track's own thickness, as a fraction of the
// ROW's height (box.H/2, since the box splits into a gain row and a loss
// row) -- never of unit, because unit already folds in box.W, and a track
// this wide should not grow with the box's WIDTH the way a font legibly can.
// A judgement call: thick enough to read as a bar rather than a hairline,
// restrained enough that the two rows do not touch.
const climbBarHeightFraction = 0.42

// climbCaptionFraction is the caption's own nominal font size, as a fraction
// of ctx.BasePx() -- see the type doc comment's "Scaling" section for why
// the caption departs from unit, the basis every other measure on this
// Painter uses. It is the starting point FitSize then only ever shrinks from
// (see Prepare), the same role elevAxisLabelFraction plays for
// ElevationPanel's own axis chrome.
//
// 0.30 is not a fraction chosen to reproduce Readout's own caption size
// exactly -- Readout sizes ITS caption off its own box's unit
// (readout.go's captionOffset neighbourhood), and that box's shape differs
// by row and by layout tree, so "HEART RATE" and "ELAPSED" do not even agree
// with EACH OTHER on an exact pixel size today. 0.30 is a judgement call,
// checked at 1080p, 4K and in the portrait tree beside those same captions:
// legible at a comparable size to its neighbours on the frame, not
// recessive chrome the way elevAxisLabelFraction's 0.45-of-BasePx is for an
// axis label sitting beside a plot.
const climbCaptionFraction = 0.30

// climbReadingFraction is the reading's own nominal font size, as a fraction
// of unit (min(box.W, the row's own height)) -- the starting point FitSize
// then only ever shrinks from (see Prepare). The same fraction Readout uses
// for its own value (readout.go), so a climb reading beside an ordinary
// readout in the same render reads at a comparable size rather than looking
// like a different program's chrome.
const climbReadingFraction = 0.34

type climbPainter struct {
	box Box

	// model is read-only from here on: Prepare sets it once from
	// ctx.Elevation and Dynamic only ever calls AtDistance on it, never
	// mutating the Painter -- see the Painter contract's own rule on why
	// Dynamic must not.
	model *fitactivity.ElevationModel

	profileStart, axisEnd float64
	totalGain, totalLoss  float64
	maxTotal              float64
	trackX, trackW        float64
	barH                  float64
	gainY, lossY          float64
	labelX, readingX      float64
	labelPx, readingPx    float64
}

// drawBar fills a track of width w (already clamped by the caller to
// [0, trackW]) at row centre y, in col. Shared by the ghost (Static) and the
// live fill and absence wash (Dynamic) so all three are pixel-identical
// bars differing only in width and colour, never independently drawn shapes
// that could disagree about the row's own geometry.
func (p *climbPainter) drawBar(c *Canvas, y, w float64, col color.Color) {
	if w <= 0 {
		return
	}
	c.Rect(Box{X: p.trackX, Y: y - p.barH/2, W: w, H: p.barH}, col)
}

// Static draws the captions and each track's dim ghost -- the activity's own
// total gain and total loss, fixed for the whole render, so this is the
// "destination" half of the static/dynamic split described on the type doc
// comment.
func (p *climbPainter) Static(c *Canvas) {
	if p.trackW <= 0 || p.maxTotal <= 0 {
		return
	}
	_ = c.Text("GAIN", p.labelX, p.gainY, 0, 0.5, p.labelPx, c.Theme.Dim)
	_ = c.Text("LOSS", p.labelX, p.lossY, 0, 0.5, p.labelPx, c.Theme.Dim)

	gainGhost := p.trackW * (p.totalGain / p.maxTotal)
	lossGhost := p.trackW * (p.totalLoss / p.maxTotal)
	p.drawBar(c, p.gainY, gainGhost, c.Theme.Dim)
	p.drawBar(c, p.lossY, lossGhost, c.Theme.Dim)
}

// Dynamic draws the live fill -- the "progress" half of the static/dynamic
// split -- and the two numeric readings, or the placeholder wash and
// readings when the current instant's climbing figure is not known. See the
// type doc comment's "Absence" section for the policy and, in particular,
// for why the pre-data region is refused rather than read as a confident
// zero.
func (p *climbPainter) Dynamic(c *Canvas, f Frame) {
	if p.trackW <= 0 || p.maxTotal <= 0 {
		return
	}
	absent := Fade(c.Theme.Absent, elevationAbsentFillAlpha(c.Theme))

	if !f.Sample.HasDistance || f.Sample.Distance < p.profileStart {
		// The ordinary dropout (no distance at all) and the pre-data trap
		// (a known distance, but before the model's own StartDistance, where
		// AtDistance would clamp to a confident zero) take the identical
		// branch: neither is a real "no climbing yet", both are "unknown",
		// and washing the whole track is what says so without inventing an
		// extent -- exactly ElevationPanel's own choice for the identical
		// two cases.
		p.drawBar(c, p.gainY, p.trackW, absent)
		p.drawBar(c, p.lossY, p.trackW, absent)
		// Left-aligned (0, not 1) at readingX -- see climbReadingWidthFraction's
		// doc comment for why this is a fixed column just past the track's own
		// end rather than the box's far edge.
		_ = c.Text(ReadoutPlaceholder, p.readingX, p.gainY, 0, 0.5, p.readingPx, c.Theme.Absent)
		_ = c.Text(ReadoutPlaceholder, p.readingX, p.lossY, 0, 0.5, p.readingPx, c.Theme.Absent)
		return
	}

	d := f.Sample.Distance
	if d > p.axisEnd {
		// Past the recorded end: the model's own clamp is the RIGHT answer
		// here, not a trap -- see the type doc comment's closing paragraph.
		d = p.axisEnd
	}
	_, gain, loss := p.model.AtDistance(d)

	gainFill := math.Min(p.trackW, math.Max(0, p.trackW*(gain/p.maxTotal)))
	lossFill := math.Min(p.trackW, math.Max(0, p.trackW*(loss/p.maxTotal)))
	p.drawBar(c, p.gainY, gainFill, c.Theme.Foreground)
	p.drawBar(c, p.lossY, lossFill, c.Theme.Foreground)

	// Left-aligned at readingX for the identical reason the placeholder branch
	// above is: a fixed column beside the track, not the box's far edge.
	_ = c.Text(formatElevation(gain), p.readingX, p.gainY, 0, 0.5, p.readingPx, c.Theme.Foreground)
	_ = c.Text(formatElevation(loss), p.readingX, p.lossY, 0, 0.5, p.readingPx, c.Theme.Foreground)
}
