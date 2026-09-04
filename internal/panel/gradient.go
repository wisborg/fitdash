package panel

import (
	"fmt"
	"math"

	"github.com/wisborg/fitactivity"
)

// GradientPanel draws the terrain's current steepness as a single line that
// tilts with it -- rising for a climb, level for flat ground, falling for a
// descent -- beside the signed percentage reading.
//
// One responsibility: this panel answers "how steep is it right now", nothing
// else. It shares its data with ElevationPanel and ClimbPanel (all three read
// ctx.Elevation) but draws none of their trace, playhead or cumulative bars --
// a panel folding all three into one would be unable to be placed, or
// selected by a future --panels flag, independently of the others.
//
// # Why a tilting line, not a dial
//
// The user was shown a needle-on-a-labelled-dial design first and asked
// instead for a plain line that leans with the ground: "/" for a climb, "---"
// for level, "\" for a descent. No dial, no limit rays, no legend -- the
// shape alone is the gist, and the signed number beside it is the
// measurement. See "The division of labour" below for why splitting it this
// way is what makes an exaggerated line honest rather than misleading.
//
// # The angle is amplified, on purpose, by a FIXED factor
//
// A line drawn at grade's own true angle is invisible at every grade a person
// actually rides or runs: atan(0.10) is 5.7 degrees, atan(0.20) is 11.3 --
// both read as "flat" on a strip a few dozen pixels tall. So the drawn angle
// is the true angle (atan(grade), the honest geometric tilt that grade's
// rise/run actually has) multiplied by gradientAngleAmplification and
// clamped to +/- gradientMaxAngleRadians. Proportionality survives the
// amplification -- double the grade is double the drawn tilt, right up to
// the clamp -- which is what keeps the exaggeration a single stated constant
// rather than a curve nobody could invert by eye. See gradientAngleFor.
//
// # Fixed, never derived from the activity
//
// A per-activity scale -- amplifying so THIS activity's own steepest section
// reaches the clamp -- was offered and rejected. It was honest for the dial
// design ONLY because the dial drew labelled limit rays a viewer could read
// the scale off; a bare line has no such marks, so a derived range would mean
// the identical drawn tilt represented a different real grade on a different
// activity, with nothing on screen saying which activity's scale is in play.
// The user chose comparability: the same tilt means the same grade on every
// render, which is only true if the amplification never moves. See
// gradientAngleAmplification's own doc comment for the specific number and
// why 4 was picked over deriving one.
//
// # The division of labour: the line carries the gist, the number the truth
//
// Exactly because the line is a deliberately exaggerated qualitative signal
// -- climbing, level, or descending, and roughly how hard -- the percentage
// beside it has to be the plain, unexaggerated measurement, or the whole
// panel would be dressing up a guess as a reading. Format is `%+.1f%%`,
// videofx's own format string for the identical figure, verbatim -- so the
// same instant of the same activity reads identically in both programs (see
// formatGrade).
//
// # A faint horizontal reference
//
// Static draws a thin, dimmed horizontal stroke across the same span the
// tilting line occupies. A slight tilt against nothing to compare it to
// barely reads as a tilt at all; against a level reference it does, at the
// gentlest grades the amplification does the least for -- checked at the
// gate on a shallow-grade frame, not assumed.
//
// # No caption
//
// This panel used to draw a "GRADIENT" caption beside the line. It is
// deleted, not merely stopped being drawn, for the same reason MarkerPanel's
// own "MARKERS" caption was (see highlight_panel.go's Static comment): the
// word named the PANEL, not the thing on screen, and a tilting line beside a
// signed percentage is legible without a word to say "this is a gradient" in
// a way the caption never made it more so.
//
// The structural half of the argument is what makes this safe rather than
// merely tidy: this panel's Accepts is the IDENTICAL elevationIsPlottable
// predicate ClimbPanel uses (see "Absence" below), so the gradient line is
// never drawn without ClimbPanel's gain/loss bars sitting beside it in the
// same strip (layouts.go). The context a viewer relies on to read the bare
// percentage -- "this is next to a climb/loss readout, so it's a grade" --
// is guaranteed by the accept predicate the two panels share, not by luck or
// by this file's own placement choice. Anyone later giving the two panels
// different accept conditions would quietly remove the thing that makes the
// missing caption defensible, which is why that dependency is recorded here
// rather than left to be rediscovered.
//
// Name() still returns "gradient": the render summary and the future
// --panels flag (docs/architecture.md) select and report on panels by that
// name regardless of what they draw on screen. Only the drawn caption is
// gone.
//
// # Static and dynamic
//
// The horizontal reference does not depend on anything but the box this
// panel was placed in, so it is Static, drawn once. The tilt and the reading
// are the only things that change frame to frame, so both are Dynamic -- and
// Dynamic never eases toward either: see the "no easing" section below for
// why that would be both wrong and unnecessary here.
//
// # No easing between frames, and this is not merely a style choice
//
// Easing a value toward a target needs the LAST frame's value kept somewhere,
// and Dynamic must never mutate the Painter (see the Painter contract) --
// there is nowhere honest to keep it. It is also unnecessary: gradeWindowFor
// (elevation.go) sizes the window this panel's reading is measured over at a
// floor of 30 m (widened only once a render is compressed enough that one
// frame's own stride exceeds it), and at running pace a 30 fps frame advances
// on the order of a tenth of a metre against that 60 m span -- a fraction of
// one percent of the window's own local variation. The reading this panel
// draws is already smooth from one frame to the next by construction, so
// there is nothing left to ease toward. This is the one place a reviewer will
// reach for the forbidden thing, which is why it is written here rather than
// left to be rediscovered.
//
// # Absence
//
// Accepts is elevationIsPlottable(ctx), the identical predicate
// ElevationPanel and ClimbPanel use (see elevation.go) -- this panel must
// never draw on a model either of those two declined, or a viewer sees three
// panels disagree about whether the same instant's data exists at all. Where
// the activity never carried elevation, or elevation without distance, or a
// flat/zero-span profile, this panel declines outright and the layout closes
// up around it.
//
// Per frame, absence is a placeholder: no line is drawn (a line parked level
// would be a confident "0% grade", which is a real, drawable answer this
// panel must not invent when it does not have one) and the reading shows
// ReadoutPlaceholder in Theme.Absent. This fires on an ordinary distance
// dropout and on THE PRE-DATA TRAP: fitactivity.ElevationModel.GradeAtDistance
// clamps to the profile's own ends, so asked for a distance before the
// model's own StartDistance it answers a confident 0.0 grade, no error.
// ElevationPanel is washing that same stretch absent and ClimbPanel is
// washing its own tracks absent at the identical instant; drawing "+0.0%"
// here would disagree with both. So Dynamic checks f.Sample.Distance against
// the model's own StartDistance BEFORE ever calling GradeAtDistance, exactly
// mirroring ClimbPanel's identical guard against the identical trap.
//
// Past the model's own TotalDistance the clamp is the opposite case and is
// left alone: the grade GradeAtDistance reports there is the terrain's own
// final measured slope, which is honestly known once the activity has
// actually finished, matching ClimbPanel's and ElevationPanel's identical
// choice to keep showing a live reading (not wash absent) past their own axis
// ends.
//
// # Scaling
//
// Every measure on this Painter is a fraction of this panel's own box or of
// unit (min(box.W, box.H)) -- never a pixel constant, and (now that the
// caption is gone) never ctx.BasePx() either -- so it has to read at 1080p,
// at 4K, and in the portrait tree's narrower box without a layout-specific
// branch in this file. The earlier draft of this panel sized its caption off
// ctx.BasePx() instead of unit, for the reason ClimbPanel's own caption
// still does (see climbCaptionFraction's doc comment): that departure went
// away with the caption itself, and the reading -- the only text this panel
// draws now -- is sized off unit like ClimbPanel's reading, not off
// BasePx like ClimbPanel's caption.
type GradientPanel struct{}

// Name identifies the panel, and is the key a future --panels flag would
// select it by (see docs/architecture.md) -- short, lowercase, hyphen-free,
// naming the thing on screen.
func (GradientPanel) Name() string { return "gradient" }

// Accepts is elevationIsPlottable(ctx) -- see the type doc comment's
// "Absence" section for why this must be the SAME predicate ElevationPanel
// and ClimbPanel use rather than a second copy that could quietly stop
// agreeing with them.
func (GradientPanel) Accepts(ctx *Context) bool { return elevationIsPlottable(ctx) }

// Prepare lays out the line's geometry and the reading's font size once, and
// resolves the grade window this render's own compression implies
// (gradeWindowFor, elevation.go).
func (GradientPanel) Prepare(ctx *Context, box Box) Painter {
	p := &gradientPainter{box: box}
	if !elevationIsPlottable(ctx) {
		// Accepts should have prevented this. Drawing nothing is the
		// least-wrong option left, mirroring ClimbPanel's and
		// ElevationPanel's own identical defensive guard in their own
		// Prepare methods -- not a policy this panel chooses for itself.
		return p
	}
	m := ctx.Elevation
	p.model = m
	p.profileStart, p.axisEnd = m.StartDistance(), m.TotalDistance()

	activitySeconds := ctx.Timeline.ActivityDuration().Seconds()
	p.window = gradeWindowFor(p.axisEnd-p.profileStart, activitySeconds, ctx.Timeline.MaxSpeedup(), ctx.Timeline.FPS())

	unit := math.Min(box.W, box.H)

	readingW := box.W * gradientReadingWidthFraction
	gap := box.W * gradientGapFraction

	// lineX0 sits flush at the box's own left edge, now that there is no
	// caption column ahead of it to leave room for -- the line is the
	// leftmost thing this panel draws, the way ClimbPanel's "GAIN"/"LOSS"
	// caption is the leftmost thing ITS Painter draws.
	lineX0 := box.X

	// halfLen is a fraction of unit -- so the line's own length tracks the
	// row's height in the ordinary case this panel is placed in (a wide,
	// short strip row) -- clamped to whatever the reading column actually
	// leaves it, the same defensive clamp ClimbPanel's own trackW applies
	// for the identical reason (see its comment in climb.go).
	halfLen := unit * gradientLineLengthFraction
	if maxHalf := (box.W - gap - readingW) / 2; halfLen > maxHalf {
		halfLen = maxHalf
	}
	if halfLen < 0 {
		halfLen = 0
	}
	p.halfLen = halfLen
	p.lineWidth = math.Max(2, unit*gradientLineWidthFraction)

	// centerX sits a fixed halfLen past lineX0, not mid-way between lineX0
	// and the reading column the way an earlier version placed it -- that
	// earlier rule let the line float in whatever daylight the box happened
	// to leave once the reading column was pinned to the box's far edge,
	// which read as separate columns spread across the whole box rather
	// than one group. Anchoring the line at the box's own edge, and the
	// reading immediately after the line (below), is the same shape
	// ClimbPanel's own trackX/readingX already settled on for its gain/loss
	// bars -- see climb.go's trackW comment for why the earlier,
	// edge-anchored rule was rejected there too. The daylight it frees is left empty at the box's own far
	// edge rather than handed to any one column.
	p.centerX = lineX0 + halfLen
	p.centerY = box.Y + box.H/2

	// readingX is lineX0 plus the line's own full length, one gap further --
	// a fixed column, not an anchor off the tilted line's own current end
	// (which moves with the grade; anchoring there would make the reading
	// jitter as the terrain changes), mirroring climbReadingWidthFraction's
	// identical fixed-column choice for its own reading beside a bar whose
	// own fill also varies.
	p.readingX = lineX0 + 2*halfLen + gap

	p.readingPx = unit * gradientReadingFraction

	// Sized once against a fixed worst-case template, never against a value
	// sampled from this render -- distanceTemplate (readout.go) is this
	// project's own precedent for that choice: a grade's own worst case is
	// not knowable in Prepare the way ClimbPanel's totals are (those are
	// fixed once the activity ends; a grade can spike anywhere along it), so
	// a template judged generous at the gate is what stands in for it here.
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize(gradientReadingTemplate, readingW*0.92, p.readingPx); err == nil {
			p.readingPx = px
		}
	}
	return p
}

// gradientReadingWidthFraction is the reading column's own reserved width, a
// fraction of box.W -- sized for a signed one-decimal percentage, which runs
// to six characters ("-99.9%").
const gradientReadingWidthFraction = 0.30

// gradientGapFraction is the daylight between the line area and the reading
// column, a fraction of box.W -- mirroring climbGapFraction for the
// identical reason: the two must never touch, even on the narrowest half-box
// this panel is placed in.
const gradientGapFraction = 0.03

// gradientLineLengthFraction is the tilting line's own HALF-length, as a
// fraction of unit (min(box.W, box.H)) -- so the line's total length tracks
// the box's smaller dimension, ordinarily its height on the wide, short strip
// row this panel is placed in. A judgement call: long enough that a tilt is
// visible as a tilt rather than a dot, short enough that it does not crowd
// the reading beside it.
const gradientLineLengthFraction = 1.15

// gradientLineWidthFraction is the line's own stroke thickness, as a
// fraction of unit -- floored at 2px in Prepare so it never vanishes to a
// hairline on a small render. A judgement call: thick enough to read as a
// deliberate stroke, restrained enough that it does not read as a filled bar.
const gradientLineWidthFraction = 0.05

// gradientReadingFraction is the reading's own nominal font size, as a
// fraction of unit -- the same fraction climbReadingFraction and Readout's
// own value use, so a gradient reading beside either in the same render
// reads at a comparable size.
const gradientReadingFraction = 0.34

// gradientReadingTemplate is the widest string this panel's reading is ever
// expected to print, reserved once in Prepare (see FitSize call above) rather
// than derived from a sampled worst case -- see Prepare's own comment on why
// a grade's true worst case is not knowable there the way ClimbPanel's
// totals are. Two integer digits and one decimal, signed: a grade beyond
// +/-99.9% is not a real road or trail, so this is generous rather than
// exact, the same judgement distanceTemplate (readout.go) makes for
// distance's own worst case.
const gradientReadingTemplate = "-99.9%"

// gradientAngleAmplification is the fixed factor the true grade angle
// (atan(grade)) is multiplied by before it is drawn -- see the type doc
// comment's "The angle is amplified" section for why a factor is needed at
// all, and "Fixed, never derived" for why it must be a constant rather than
// scaled per activity.
//
// 4 is the value chosen, not merely stated: solving atan(x)*4 =
// gradientMaxAngleRadians gives x = tan(gradientMaxAngleRadians/4) = tan(pi/16)
// ~= 0.199, so the clamp lands at very close to 20% grade -- around the
// steepest sustained gradient a paved road or a maintained trail actually
// reaches. That is the reason 4 is the number, in the same voice this
// package's other judgement constants use: checked against a real-world
// bound, not picked to look right on one screenshot.
const gradientAngleAmplification = 4.0

// gradientMaxAngleRadians is the drawn line's own limit, 45 degrees -- a
// line steeper than that reads as "nearly vertical" regardless of how much
// further the true grade climbs, and 45 degrees is the angle a viewer reads
// as "as steep as this shape can show" without needing a labelled limit ray
// to say so (which the dial design this replaced needed -- see the type doc
// comment's "Why a tilting line, not a dial").
const gradientMaxAngleRadians = math.Pi / 4

// gradientAngleFor converts a true grade (rise/run, e.g. 0.061 for 6.1%
// climbing, negative for a descent) into the radians the drawn line tilts by:
// the true angle atan(grade) -- the honest geometric tilt that grade
// actually has -- multiplied by gradientAngleAmplification and clamped to
// +/- gradientMaxAngleRadians.
//
// Extracted as its own pure function, with no Canvas or Painter in scope,
// because it is the one decision in this panel that has a correct answer a
// test can check directly -- see gradient_test.go's table test, which
// derives its expected values from this exact formula rather than pinning
// whatever the code happened to return.
//
// atan, not the grade itself: grade IS tan(angle) by definition (rise over
// run), so the geometrically honest tilt for a given grade is the angle
// whose tangent is that grade, not the grade treated as if it already were
// one. Using the raw fraction in place of atan would over-tilt small grades
// and under-tilt steep ones relative to what the terrain's own slope
// triangle actually looks like.
func gradientAngleFor(grade float64) float64 {
	angle := math.Atan(grade) * gradientAngleAmplification
	if angle > gradientMaxAngleRadians {
		return gradientMaxAngleRadians
	}
	if angle < -gradientMaxAngleRadians {
		return -gradientMaxAngleRadians
	}
	return angle
}

// formatGrade renders a grade fraction as a signed percentage, one decimal --
// `%+.1f%%`, videofx's own format string for the identical figure, verbatim,
// so the same instant of the same activity reads identically in both
// programs (see the type doc comment's "division of labour" section).
func formatGrade(grade float64) string { return fmt.Sprintf("%+.1f%%", grade*100) }

type gradientPainter struct {
	box Box

	// model is read-only from here on: Prepare sets it once from
	// ctx.Elevation and Dynamic only ever calls GradeAtDistance on it, never
	// mutating the Painter -- see the Painter contract's own rule on why
	// Dynamic must not.
	model *fitactivity.ElevationModel

	profileStart, axisEnd float64
	window                float64

	centerX, centerY float64
	halfLen          float64
	lineWidth        float64

	readingX  float64
	readingPx float64
}

// Static draws the faint horizontal reference -- fixed for the whole
// render, so it is the "invariant" half of the static/dynamic split
// described on the type doc comment. There is no caption to draw here any
// more -- see the type doc comment's "No caption" section.
func (p *gradientPainter) Static(c *Canvas) {
	if p.model == nil {
		// Prepare's own defensive guard (Accepts should already have
		// prevented this -- see Prepare's comment) left nothing built to
		// draw from.
		return
	}
	if p.halfLen > 0 {
		c.Polyline(
			[]float64{p.centerX - p.halfLen, p.centerX + p.halfLen},
			[]float64{p.centerY, p.centerY},
			math.Max(1, p.lineWidth*0.3),
			Fade(c.Theme.Dim, 0.5),
		)
	}
}

// Dynamic draws the tilted line and the signed reading, or the placeholder
// (no line, "--" in Theme.Absent) when the current instant's grade is not
// known. See the type doc comment's "Absence" section for the policy and,
// in particular, for why the pre-data region is refused rather than read as
// a confident 0.0% -- exactly ClimbPanel's identical guard against
// fitactivity.ElevationModel's own clamping behaviour.
func (p *gradientPainter) Dynamic(c *Canvas, f Frame) {
	if p.model == nil {
		return
	}

	if !f.Sample.HasDistance || f.Sample.Distance < p.profileStart {
		// The ordinary dropout and the pre-data trap take the identical
		// branch: neither is a real "0% grade", both are "unknown", so no
		// line is drawn at all -- a line parked level would be exactly the
		// confident lie CLAUDE.md forbids, spread across a shape instead of
		// a number.
		_ = c.Text(ReadoutPlaceholder, p.readingX, p.centerY, 0, 0.5, p.readingPx, c.Theme.Absent)
		return
	}

	d := f.Sample.Distance
	if d > p.axisEnd {
		// Past the recorded end: the model's own clamp is the RIGHT answer
		// here, not a trap -- see the type doc comment's closing paragraph.
		d = p.axisEnd
	}
	grade := p.model.GradeAtDistance(d, p.window)
	angle := gradientAngleFor(grade)

	if p.halfLen > 0 {
		x0 := p.centerX - p.halfLen*math.Cos(angle)
		x1 := p.centerX + p.halfLen*math.Cos(angle)
		// Image y grows downward, so a positive (climbing) angle must raise
		// the right-hand end -- a SMALLER y -- not a larger one.
		y0 := p.centerY + p.halfLen*math.Sin(angle)
		y1 := p.centerY - p.halfLen*math.Sin(angle)
		c.Polyline([]float64{x0, x1}, []float64{y0, y1}, p.lineWidth, c.Theme.Foreground)
	}
	_ = c.Text(formatGrade(grade), p.readingX, p.centerY, 0, 0.5, p.readingPx, c.Theme.Foreground)
}
