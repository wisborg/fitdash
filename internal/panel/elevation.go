package panel

import (
	"fmt"
	"image/color"
	"math"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// ElevationPanel draws the activity's elevation profile against distance, with
// a playhead marking where on it the activity currently is.
//
// The x axis spans zero to the model's TotalDistance, NOT
// ElevationModel.StartDistance()..TotalDistance() the way an early version of
// this panel had it. That earlier choice existed to dodge a real bug in the
// sibling HUD project: a profile built from a clip cut out of the middle of an
// activity keeps the activity's own distance numbering, so a plot that divided
// by TotalDistance alone would squeeze a profile starting at 10.2 km into the
// right-hand fifth of its box. StartDistance..TotalDistance is the right span
// for that caller.
//
// It is the wrong span here because **fitdash never renders a clip** -- see
// CLAUDE.md and docs/architecture.md, this program renders a whole activity,
// always. The only way StartDistance is ever non-zero for a whole-activity
// track is BuildElevationModel's own rule: it keeps only samples carrying
// BOTH distance and elevation, and the barometer commonly takes a few metres
// to settle after the GPS distance stream has already started, so the model's
// first point can land a few metres in, whatever the warm-up cost. That is missing
// data, not a scope boundary, and shrinking the axis to hide it would be the
// same dishonesty CLAUDE.md forbids in a single reading, spread across an
// axis instead of a number: the plot would silently claim the activity began
// wherever the barometer happened to wake up.
//
// So the axis now starts at the activity's own zero, and profileStart (the
// model's own StartDistance) survives as a second field marking where the
// recorded trace itself begins on that axis -- see the "pre-data region"
// documented on Dynamic for what happens in the gap between the two.
//
// Here the axis is two fields on the Painter (axisEnd and, for the trace's own
// extent, profileStart), and the SAME method -- xForDistance -- places the
// profile, the labels, the fill and the dot. They cannot be scaled against
// different origins because there is only one origin to scale against; that
// principle is what this design is still built to preserve, only the origin
// itself has moved.
type ElevationPanel struct{}

// Name identifies the panel.
func (ElevationPanel) Name() string { return "elevation" }

// Accepts requires BOTH elevation and distance, and requires them on the same
// samples.
//
// Distance is not incidental: it is the profile's x axis. An activity with
// barometric elevation but no distance -- a treadmill session, a rowing
// machine -- has readings with nothing to plot them against, and a profile
// drawn against sample index would be a different graph wearing this one's
// labels.
//
// Asking the coverage report about each independently is NOT enough, and the
// gap was real. The report counts them per metric, so a file whose elevation
// and distance land on disjoint samples satisfies both questions while
// BuildElevationModel -- which keeps only samples carrying both -- comes back
// empty. The panel was then placed, took the full-width strip at the bottom of
// the landscape layout, and drew nothing: an unexplained band of background,
// indistinguishable from a panel that crashed, and absent from the "declined"
// summary because it had not declined. That is precisely the third option
// CLAUDE.md calls a bug.
//
// So the model itself is the test. Building it twice -- here and again in
// Prepare -- costs one extra pass over the samples once per render, which is
// nothing beside rendering a strip of background for the whole video.
func (ElevationPanel) Accepts(ctx *Context) bool {
	if ctx.Track == nil {
		return false
	}
	if !ctx.Report.Carries(inspect.MetricElevation) || !ctx.Report.Carries(inspect.MetricDistance) {
		return false
	}
	m := fitactivity.BuildElevationModel(ctx.Track, elevationOptions(ctx))
	if m == nil || m.Empty() {
		return false
	}
	// A flat or zero-span profile also declines, and this is new since the
	// fill became the panel's only distance indicator. A flat profile still
	// has a model and would still take the band, but with nothing left to
	// draw except two identical elevation labels it would show distance
	// nowhere -- the unexplained-hole bug reached from a different
	// direction. Declining hands the band to the distance readout instead,
	// which is the honest outcome. Prepare keeps the equivalent `flat` guard
	// as the same species of defensive check its `m == nil || m.Empty()`
	// guard already is, in case a caller places the panel without asking.
	minElev, maxElev := m.Range()
	profileStart, axisEnd := m.StartDistance(), m.TotalDistance()
	if maxElev <= minElev || axisEnd <= profileStart {
		return false
	}
	return true
}

// elevationOptions tunes the smoothing against the device's own ascent and
// descent totals where the file reported them.
//
// Barometric elevation is noisy enough that a raw per-sample sum wildly
// overcounts climbing, and matching a figure the watch already published is
// more trustworthy than any constant chosen here. It is a function rather than
// inline so Accepts and Prepare build the SAME model -- one deciding to place
// the panel on a model the other did not build would be the disagreement this
// change exists to remove.
func elevationOptions(ctx *Context) fitactivity.ElevationOptions {
	var opts fitactivity.ElevationOptions
	if ctx.Track != nil && ctx.Track.HasElevationTotals {
		opts.TargetGain, opts.TargetLoss = ctx.Track.TotalAscent, ctx.Track.TotalDescent
	}
	return opts
}

// profileSampleStep is how many pixels apart the profile is sampled.
//
// Two: finer than one point per pixel column buys nothing a display can show,
// and the profile is a Painter field computed once, so this trades a little
// smoothing for a shorter polyline the static pass strokes once.
const profileSampleStep = 2.0

// elevationFloorHeadroom is how far below the profile's own minimum the
// fill's baseline sits, as a fraction of the elevation range -- never a
// metre constant, which would give a 100 m climb a floor 1% below it and a
// 5 m one a floor 20% below. A judgement call to check at the gate, not a
// derivation, in the same voice as highlightWashStrength and the strip
// weights.
//
// 0.12 was the starting point and did NOT survive the gate: on an activity
// whose barometer settles a little way in, and whose overall minimum is
// therefore close to its opening reading, the low label sits close enough
// to the plot's bottom edge already that a 0.12 headroom left its own ink
// overlapping the new baseline rule -- the rule reading as a strike-through
// rather than as a floor. 0.22 was checked in its place and stopped that:
// the low label's glyphs cleared the baseline rule at every resolution
// tried (1080p, 4K, portrait) -- but "cleared" meant zero rows of daylight
// at 1080p and portrait and one row at 4K, not a margin. That headroom was
// tuned to exactly the low label's OWN half-height at the size it happened
// to be measured at, which is not a property this fraction's own name
// advertises, so the next person to change labelPx (bigger text, a
// different font, a metrics change upstream) reintroduces the strike-through
// this same headroom already exists to prevent, with nothing here to say
// why. 0.25 buys real clearance -- several rows rather than a fraction of
// one, at every resolution the previous value was checked at -- for a
// three-point rise in this SAME fraction, which is cheap for the identical
// reason 0.22 already was: it also thickens the fill at the activity's
// lowest point (the OTHER thing this fraction has to keep legible) rather
// than trading against it, and it compresses the real trace into a
// negligibly smaller share of the plot (80% of it rather than 82%).
// A reader retuning labelPx is still expected to re-check this gap -- this
// comment records the coupling so that check is not rediscovered from a
// strike-through, not so it can be skipped.
const elevationFloorHeadroom = 0.25

// elevationFillAlpha is the opacity of the COVERED distance fill under the
// profile -- Fade(Theme.Foreground, elevationFillAlpha). A judgement call,
// the same species as elevationFloorHeadroom: strong enough to read as a
// filled area rather than a tint, restrained enough that the Dim trace
// stroked over it in Static stays legible through it.
//
// This is NOT also the dropout wash's opacity, and an earlier version of
// this panel sharing the one constant between both was a real bug, not a
// simplification: Theme.Foreground sits far from Theme.Background in every
// shipped theme (that IS what makes it a live reading), so 0.35 of it reads
// clearly. Theme.Absent is deliberately much closer to Background -- it is
// meant to recede, the way chrome does -- so the identical fraction of it
// composites to something this project's own contrast floor (see
// elevationAbsentFillContrastFloor) rejects when a --highlight background=
// gets that close on purpose. See elevationAbsentFillAlpha.
const elevationFillAlpha = 0.35

// elevationAbsentFillContrastFloor is the floor the dropout wash's OWN
// composited pixel -- Fade(Theme.Absent, alpha) blended over Theme.Background,
// the colour that actually reaches the eye once alpha is anything but 1 --
// must clear against Theme.Background.
//
// 1.5 is the same number, for the same reason, as cmd/render.go's own
// chromeContrastFloor: neither Dim nor Absent is read like a paragraph, so
// WCAG's 4.5:1 for body text does not apply to either, and a ratio of 1.0 --
// the role has become indistinguishable from the background it sits on -- is
// the one unambiguous failure at this end of the scale. It cannot literally
// BE that constant: cmd depends on internal/panel, not the reverse, so
// cmd/render.go's own const is not importable here. If either number is
// ever retuned, check the other -- the two are answering the identical
// question about the identical role and should not quietly drift apart.
const elevationAbsentFillContrastFloor = 1.5

// elevationAbsentFillContrastMargin pads the search elevationAbsentFillAlpha
// runs above elevationAbsentFillContrastFloor, to survive Fade's own alpha
// quantization to an 8-bit channel.
//
// Solving for the alpha that lands EXACTLY on the floor and then rounding it
// to the nearest 1/255 (as Fade does) can round DOWN: measured directly, the
// light theme's own Absent sits close enough to its own Background that this
// happened -- the exact-floor solve rounded to a composited contrast of
// 1.4999:1, a hair under the very floor this exists to guarantee. 0.02 is
// roughly five times the amount one quantization step moved the ratio near
// that theme's own operating point, checked rather than assumed, and it is
// cheap: the alpha this adds is a fraction of one part in 255.
const elevationAbsentFillContrastMargin = 0.02

// fadeOverBackground returns the RGB a compositor actually paints when col is
// drawn through Fade at the given alpha over bg -- the pixel ContrastRatio
// must be asked about once anything is faded, because ContrastRatio (and the
// WCAG definition it implements) carries no opacity term of its own: passed
// col directly, it silently reports col's OWN contrast, which is a question
// about a pixel nothing on screen ever is once alpha is less than 1. This is
// the identical source-over blend gg's rasterizer performs for a Fade-scaled
// fill over an opaque background.
func fadeOverBackground(col color.Color, alpha float64, bg color.Color) color.NRGBA {
	c := color.NRGBAModel.Convert(col).(color.NRGBA)
	b := color.NRGBAModel.Convert(bg).(color.NRGBA)
	mix := func(cc, bc uint8) uint8 {
		v := alpha*float64(cc) + (1-alpha)*float64(bc)
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(math.Round(v))
	}
	return color.NRGBA{R: mix(c.R, b.R), G: mix(c.G, b.G), B: mix(c.B, b.B), A: 255}
}

// minAlphaForContrast finds the smallest alpha in [0,1] at which fg, drawn
// through Fade over bg, reaches AT LEAST target contrast against bg, by
// bisection: WCAG's luminance formula gamma-corrects each channel and has no
// closed-form inverse in alpha, but fadeOverBackground's own linear blend
// moves the composite monotonically from bg (alpha 0) to fg (alpha 1), so
// the contrast against bg is monotonic in alpha too and bisection is exact
// up to the iteration count. 24 halvings resolve alpha to better than
// 1/2^24 -- far finer than Fade's own 8-bit output, so the search is never
// what limits the answer's precision.
//
// Returns 1 (fully opaque) if fg cannot reach target against bg even at full
// strength. That is an honest answer -- the best this function can do -- and
// whether it is good enough is a fact about the two colours involved, not
// about this function; a theme whose Absent cannot clear the floor even
// undimmed is a theme bug for TestThemesAreLegible-style checks to catch,
// not something to paper over here.
func minAlphaForContrast(fg, bg color.Color, target float64) float64 {
	lo, hi := 0.0, 1.0
	for i := 0; i < 24; i++ {
		mid := (lo + hi) / 2
		if ContrastRatio(fadeOverBackground(fg, mid, bg), bg) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// elevationAbsentFillAlpha derives the dropout wash's own opacity from
// theme, rather than reusing elevationFillAlpha (the covered fill's
// constant) the way an earlier version of this panel did -- see that
// constant's own doc comment for why sharing the two was a bug rather than
// an economy.
//
// It is a DERIVATION, computed from theme.Absent and theme.Background,
// rather than a third field added to Theme alongside Absent itself. That is
// a deliberate choice: what this solves for is not a property of the
// palette on its own the way Foreground or Accent are, it is a property of
// how THIS panel happens to use Theme.Absent -- faded and composited over
// Theme.Background. A hand-picked field on Theme would need its own tuned
// value per theme, shipped or future, and nothing would notice if a theme's
// Absent or Background were ever retuned without also retuning it -- exactly
// the silent-drift failure a derivation exists to close off. Solving it here
// means the contrast floor holds for any theme this panel is ever asked to
// draw with, not only the two it was checked against.
//
// Measured (from the shipped themes' own colours, not from any recording):
// roughly 0.61 for DarkTheme and 0.82 for LightTheme -- both well short of
// 1, and both far above elevationFillAlpha's 0.35, which is the reason
// 0.35 could not simply have been reused (see elevationFillAlpha's own
// comment). The light theme's number sits close to that theme's own
// ceiling: ContrastRatio(Theme.Absent, Theme.Background) undimmed is only
// 1.68:1 for LightTheme, so there is little room above the 1.5 floor to
// spend, and the absent wash there necessarily reads closer to solid than
// the covered fill does. That is the honest cost of the floor, not a defect
// in how it is met.
//
// A further, related cost worth recording rather than discovering later:
// raising the absent wash's opacity to clear ITS OWN floor against
// Background narrows how different it looks from the covered fill, because
// both are now closer to opaque over the same background. For LightTheme
// specifically, the absent-wash-vs-covered-fill contrast drops from ~1.86:1
// at the old shared 0.35 to ~1.46:1 once the absent wash is raised to clear
// its own floor -- still a real, visible difference, and the two remain
// distinguishable by hue as well as by lightness (Absent and Foreground are
// not the same colour in either shipped theme), but it is not the same
// margin the covered fill enjoys, and a future gate should keep checking it
// rather than assuming this trade only ever goes one way.
func elevationAbsentFillAlpha(theme Theme) float64 {
	return minAlphaForContrast(theme.Absent, theme.Background,
		elevationAbsentFillContrastFloor+elevationAbsentFillContrastMargin)
}

// Prepare builds the elevation model and lays out the plot, once.
func (ElevationPanel) Prepare(ctx *Context, box Box) Painter {
	p := &elevationPainter{box: box}

	if ctx.Track == nil {
		return p
	}
	m := fitactivity.BuildElevationModel(ctx.Track, elevationOptions(ctx))
	if m == nil || m.Empty() {
		// Accepts builds the same model and declines on this, so reaching here
		// means a caller placed the panel without asking. Drawing nothing is
		// then the least-wrong option available, but it is not one this panel
		// chooses for itself.
		return p
	}

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	p.labelPx = unit * 0.16

	// profileStart is the model's own StartDistance -- where the recorded
	// trace begins, NOT where the axis begins. See the type doc comment for
	// why the axis itself always starts at zero.
	p.profileStart, p.axisEnd = m.StartDistance(), m.TotalDistance()
	p.minElev, p.maxElev = m.Range()
	// The floor sits BELOW the minimum by a fraction of the range, never at a
	// literal zero. A zero floor would flatten a high-altitude run into the
	// top few percent of the plot, and a coastal activity dips below sea
	// level, part of which would then fall below a zero baseline. See
	// elevationFloorHeadroom.
	p.floorElev = p.minElev - elevationFloorHeadroom*(p.maxElev-p.minElev)
	// The start label names what the axis actually starts at -- zero -- not
	// the model's StartDistance, or it would repeat on this axis the exact
	// disagreement between a label and its axis that this panel's y-axis
	// labels are placed via yForElevation to prevent.
	p.startLabel, p.endLabel = formatDistance(0), formatDistance(p.axisEnd)
	p.lowLabel, p.highLabel = formatElevation(p.minElev), formatElevation(p.maxElev)

	// The left gutter is measured from THESE labels rather than from a
	// worst-case template. Reserving room for "-8888 m" when the profile is
	// labelled "8 m" spends a fifth of a small panel's width on nothing, and
	// this panel is usually the narrowest on the frame.
	gutter := box.W * 0.02
	bottom := p.labelPx * 1.8
	if ctx.Fonts != nil {
		wLow, _, errA := ctx.Fonts.Measure(p.lowLabel, p.labelPx)
		wHigh, _, errB := ctx.Fonts.Measure(p.highLabel, p.labelPx)
		if errA == nil && errB == nil {
			gutter = math.Max(wLow, wHigh) * 1.2
		}
	}

	p.plot = Box{
		X: box.X + gutter,
		Y: box.Y + p.labelPx*0.9,
		W: box.W - gutter - box.W*0.02,
		H: box.H - p.labelPx*0.9 - bottom,
	}
	if p.plot.W <= 0 || p.plot.H <= 0 {
		return p
	}

	if p.axisEnd <= p.profileStart || p.maxElev <= p.minElev {
		// A flat route, or one with no distance span: there is a profile in
		// the data but no graph to draw from it. The labels still tell the
		// truth, so they are drawn and the trace is not.
		p.flat = true
	}

	// The two distance labels sit at opposite ends of the plot and grow toward
	// each other, so each gets slightly under half the width. Without this
	// they overlap on a narrow panel and print as one unreadable smear --
	// which is exactly what "5.0 km" over "5.00 km" looked like before this
	// existed. Fitting is what a rectangular Box costs: the panel must fit
	// what it was given rather than growing to suit itself.
	if ctx.Fonts != nil {
		avail := p.plot.W * 0.46
		if px, err := ctx.Fonts.FitSize(p.startLabel, avail, p.labelPx); err == nil {
			p.distPx = px
		}
		if px, err := ctx.Fonts.FitSize(p.endLabel, avail, p.distPx); err == nil {
			p.distPx = px
		}
	}
	if p.distPx <= 0 {
		p.distPx = p.labelPx
	}

	p.lineW = math.Max(1.5, unit*0.02)
	p.headW = math.Max(1, unit*0.014)
	p.dotR = math.Max(2, unit*0.03)
	p.baselineW = math.Max(1, p.lineW*0.5)

	if !p.flat {
		// The trace itself only covers profileStart..axisEnd -- the axis runs
		// from zero, but there is nothing recorded before profileStart to draw
		// a curve from (see the type doc comment and Dynamic's "pre-data
		// region"). Sampling is sized from the trace's OWN pixel width, not
		// the plot's full width: dividing plot.W by profileSampleStep would
		// stretch the sample spacing across the pre-data gap too and
		// under-sample the part that actually has a curve.
		traceX0 := p.xForDistance(p.profileStart)
		traceW := p.plot.X + p.plot.W - traceX0
		n := int(traceW/profileSampleStep) + 1
		if n < 2 {
			n = 2
		}
		p.xs = make([]float64, 0, n)
		p.ys = make([]float64, 0, n)
		for i := 0; i < n; i++ {
			frac := float64(i) / float64(n-1)
			d := p.profileStart + frac*(p.axisEnd-p.profileStart)
			elev, _, _ := m.AtDistance(d)
			p.xs = append(p.xs, p.xForDistance(d))
			p.ys = append(p.ys, p.yForElevation(elev))
		}
	}
	return p
}

type elevationPainter struct {
	box     Box
	plot    Box
	xs, ys  []float64
	labelPx float64
	distPx  float64
	lineW   float64

	startLabel, endLabel string
	lowLabel, highLabel  string
	headW                float64
	dotR                 float64
	flat                 bool

	// axisEnd is the whole point of this axis's design: every x coordinate
	// this panel draws -- the trace, the pre-data wash, both distance
	// labels, the fill, the playhead dot -- comes from xForDistance, which
	// is the only place axisEnd is read, and which spans 0..axisEnd. See the
	// type doc comment for why zero and not the model's own StartDistance.
	axisEnd float64

	// profileStart is the model's own StartDistance -- where the recorded
	// elevation trace itself begins, which is on the axis but is NOT where
	// the axis begins. It marks the boundary of the "pre-data region"
	// documented on Dynamic: xs[0]/ys[0] sit at xForDistance(profileStart),
	// not at the plot's left edge, whenever profileStart > 0.
	profileStart float64

	minElev, maxElev float64

	// floorElev is the fill's baseline: minElev minus a fraction of the
	// range (elevationFloorHeadroom), NOT a literal zero and NOT a real
	// elevation nobody measured. It carries no label -- add one and it would
	// claim to name a reading, which it never was. yForElevation spans
	// floorElev..maxElev, so this single field moves the profile, the
	// labels, the baseline rule and the playhead together; there is no
	// second y mapping for the fill to disagree with.
	floorElev float64

	// baselineW is the stroke width of the rule drawn at yForElevation(floorElev)
	// in Static. Real ink rather than an implied edge: it gives the fill a
	// defined bottom at frame 0, before any distance has been read, and it
	// is what makes a single exported PNG of this panel legible on its own.
	baselineW float64
}

// xForDistance places a distance on the plot's x axis.
//
// The axis spans 0..axisEnd -- the activity's own start line to its own end,
// NOT ElevationModel.StartDistance()..TotalDistance(). An earlier version of
// this panel used the model's own span, for a reason that was real but
// belonged to a different caller: a HUD built from a CLIP keeps the source
// activity's distance numbering, so a plot dividing by TotalDistance alone
// would squeeze a profile beginning at 10.2 km into the right-hand fifth of
// its box, and spanning StartDistance..TotalDistance fixed that.
//
// fitdash has no clips -- it renders one whole activity per run -- so the
// only way profileStart (== StartDistance()) is ever non-zero here is
// BuildElevationModel's own filter, which keeps only samples carrying both
// distance and elevation: a barometer that takes a few metres to settle
// after the GPS distance stream has already started. That is missing data at
// the front of a whole activity, not the edge of a requested clip, and an
// axis that started there instead of at zero would erase the very stretch
// this panel exists to be honest about, the same way skipping a heart-rate
// dropout instead of showing "--" would.
//
// So the axis starts at the activity's own zero, and the gap between 0 and
// profileStart draws as the pre-data region described on Dynamic, rather
// than being folded out of the axis. What survives from the earlier version
// is the part that mattered: one method, used by the trace, the pre-data
// wash, the labels, the fill and the dot alike, so none of them can be
// scaled against a different origin than the others -- there is only one
// origin on the Painter to scale against, it has simply moved to zero.
func (p *elevationPainter) xForDistance(d float64) float64 {
	if p.axisEnd <= 0 {
		return p.plot.X
	}
	frac := d / p.axisEnd
	return p.plot.X + math.Min(1, math.Max(0, frac))*p.plot.W
}

// yForElevation places an elevation on the plot's y axis, higher ground
// nearer the top.
//
// The axis spans floorElev..maxElev, NOT minElev..maxElev: the floor sits a
// headroom fraction below the real minimum (see floorElev), and every y
// coordinate this panel draws -- the trace, BOTH elevation labels, the
// baseline rule, the fill, the playhead dot -- comes from this one method.
// Placing the low label at the plot's hard-coded bottom edge, as an earlier
// version did, would be correct only while the floor happened to equal the
// minimum, which stops being true the moment a floor exists -- the same
// axis-origin disagreement this panel's own xForDistance was written to
// prevent, reintroduced on the y axis.
func (p *elevationPainter) yForElevation(e float64) float64 {
	span := p.maxElev - p.floorElev
	if span <= 0 {
		return p.plot.Y + p.plot.H/2
	}
	frac := (e - p.floorElev) / span
	return p.plot.Y + p.plot.H - math.Min(1, math.Max(0, frac))*p.plot.H
}

// Static draws the profile, the baseline rule and the axis labels, all
// invariant.
func (p *elevationPainter) Static(c *Canvas) {
	if p.plot.W <= 0 || p.plot.H <= 0 {
		return
	}
	if len(p.xs) >= 2 {
		c.Polyline(p.xs, p.ys, p.lineW, c.Theme.Dim)
	}

	// The baseline: real ink at the fill's bottom, not an implied edge. It
	// gives the fill a defined floor at frame 0, before any distance has
	// been read, and is what makes a single exported PNG of this panel
	// legible on its own. Drawn at yForElevation(floorElev), which today
	// equals plot.Y + plot.H by construction -- but through the method
	// rather than the literal, so it moves with the axis if that ever
	// changes.
	baseline := p.yForElevation(p.floorElev)
	c.Polyline([]float64{p.plot.X, p.plot.X + p.plot.W}, []float64{baseline, baseline}, p.baselineW, c.Theme.Dim)

	// Elevation labels are placed BY yForElevation, at the elevations they
	// name -- exactly as both distance labels below are placed by
	// xForDistance. Once the floor sits below the minimum, the plot's
	// hard-coded bottom edge no longer equals yForElevation(minElev): a
	// label placed at the edge would point at the floor while naming the
	// minimum, the axis-origin disagreement this panel exists to prevent,
	// reintroduced on the y axis. highLabel lands on the same pixel it
	// always has, since yForElevation(maxElev) == plot.Y regardless of the
	// floor; lowLabel is the one that moves.
	_ = c.Text(p.highLabel, p.plot.X-p.labelPx*0.3, p.yForElevation(p.maxElev), 1, 0.5, p.labelPx, c.Theme.Dim)
	_ = c.Text(p.lowLabel, p.plot.X-p.labelPx*0.3, p.yForElevation(p.minElev), 1, 0.5, p.labelPx, c.Theme.Dim)

	// Distance labels are placed BY xForDistance, at the distances they name.
	// That is what ties them to the axis the profile and the playhead use: a
	// label positioned by any other arithmetic could disagree with the trace
	// beneath it, and nothing would report the disagreement.
	labelY := p.plot.Y + p.plot.H + p.labelPx*1.0
	_ = c.Text(p.startLabel, p.xForDistance(0), labelY, 0, 0.5, p.distPx, c.Theme.Dim)
	_ = c.Text(p.endLabel, p.xForDistance(p.axisEnd), labelY, 1, 0.5, p.distPx, c.Theme.Dim)
}

// profileYAt returns the profile's own y at x, and the index of the last
// stored point at or before x -- the ONE lookup this panel uses to answer
// "where is the trace at this x", shared by the dot and the fill's leading
// edge so the two can never place "the current position" at different
// pixels.
//
// y is linearly interpolated between whichever two consecutive stored points
// straddle x, which is exactly the straight segment Static's own Polyline
// strokes between them -- never a fresh query of the elevation model, which
// could disagree with the stroked trace by a pixel and leave a hairline of
// background between the fill and the line.
//
// x is expected to fall within the trace's OWN span, p.xs[0]..plot.X+plot.W
// -- NOT the plot's full width. p.xs[0] is xForDistance(profileStart), which
// sits to the right of the plot's left edge whenever the axis's pre-data
// region (see Dynamic) is non-empty, so mapping against the plot's own X and
// W here would misread every index once that region exists.
//
// Called only once len(p.xs) >= 2 is already known; a single point has
// nothing to interpolate between.
func (p *elevationPainter) profileYAt(x float64) (y float64, i int) {
	width := p.plot.X + p.plot.W - p.xs[0]
	frac := (x - p.xs[0]) / width
	pos := frac * float64(len(p.xs)-1)
	pos = math.Min(float64(len(p.xs)-1), math.Max(0, pos))
	i = int(pos)
	if i >= len(p.xs)-1 {
		i = len(p.xs) - 2
	}
	t := pos - float64(i)
	return p.ys[i] + t*(p.ys[i+1]-p.ys[i]), i
}

// fillTo fills the area under the trace from the trace's own left edge
// (p.xs[0] -- see profileYAt) to x (clamped to p.xs[0]..plot.X+plot.W), in
// col.
//
// The polygon is built ENTIRELY from p.xs/p.ys (the trace's own stored
// points) plus the single interpolated vertex profileYAt gives at x -- never
// a fresh model query, for the reason profileYAt's own comment gives. Its top
// edge is therefore pixel-identical to whatever Static already stroked along
// the same span. It closes back to p.xs[0], not to the plot's own left edge:
// whenever a pre-data region exists (profileStart > 0), those two x's
// differ, and closing at the plot's edge would draw a slanted phantom edge
// from the floor up to the trace's real first point instead of the vertical
// one this shape needs. The pre-data region itself is a separate shape --
// see preDataFillTo -- with no curve of its own to close against.
func (p *elevationPainter) fillTo(c *Canvas, x float64, col color.Color) {
	x = math.Min(p.plot.X+p.plot.W, math.Max(p.xs[0], x))
	y, i := p.profileYAt(x)
	floorY := p.yForElevation(p.floorElev)

	n := i + 1 // p.xs[0:n], p.ys[0:n] are all at or before x.
	poly := make([]float64, 0, n+3)
	polyY := make([]float64, 0, n+3)
	poly = append(poly, p.xs[:n]...)
	polyY = append(polyY, p.ys[:n]...)
	poly = append(poly, x, x, p.xs[0])
	polyY = append(polyY, y, floorY, floorY)

	c.Polygon(poly, polyY, col)
}

// preDataFillTo washes the pre-data region -- the stretch of the axis from
// the plot's left edge (distance zero) up to the trace's own start,
// p.xs[0] -- from the plot's left edge to x (clamped to plot.X..p.xs[0]), as
// a plain full-height rectangle in col.
//
// There is no curve to follow here, and none is invented: BuildElevationModel
// keeps only samples carrying BOTH distance and elevation, so the metres
// before profileStart never had an elevation reading at all, not even a
// noisy one to smooth. fillTo's shape answers "how much of the terrain has
// been covered"; this region has no terrain to report, so its shape answers
// a different, honest question instead -- "how much of this UNRECORDED
// stretch has been covered" -- by filling the full plot height rather than
// tracing a shape this panel has no data for. Its x extent still moves with
// distance, which is what keeps the first stretch of an activity from
// reading as no movement at all.
//
// A no-op when the pre-data region is empty (profileStart == 0, the ordinary
// case): x is then clamped to plot.X and the resulting rectangle has zero
// width.
func (p *elevationPainter) preDataFillTo(c *Canvas, x float64, col color.Color) {
	xEnd := math.Min(p.xs[0], math.Max(p.plot.X, x))
	if xEnd <= p.plot.X {
		return
	}
	top := p.plot.Y
	bottom := p.yForElevation(p.floorElev)
	c.Polygon([]float64{p.plot.X, xEnd, xEnd, p.plot.X}, []float64{top, top, bottom, bottom}, col)
}

// Dynamic draws the distance fill and the current-position dot. The profile,
// the baseline rule and every label are invariant and stay in Static; this is
// per-frame because distance, unlike the profile itself, changes every
// frame.
//
// Distance is cumulative and monotone, so the linear interpolation
// AtWithGap-derived samples already carry between 1 Hz readings is a real
// position estimate rather than a smoothing artefact -- which is why the fill
// advances smoothly rather than in visible 1 Hz steps, and why doing that is
// legitimate. This is the one place in the panel where per-frame
// interpolation is visible on screen.
//
// # The pre-data region
//
// The axis spans 0..axisEnd (see xForDistance), but the recorded trace only
// spans profileStart..axisEnd: BuildElevationModel keeps only samples
// carrying both distance and elevation, and a barometer commonly takes a few
// metres to settle after the GPS distance stream has already started. The
// metres before profileStart are on the axis -- they are real distance the
// activity covered -- but they never had an elevation reading, and this
// project does not invent one to fill the gap with.
//
// The fill is the ONLY distance indicator this panel has, so it must still
// show progress through that stretch: an activity that spent its first 40 m
// settling a barometer must not render as though it had not moved. What it
// cannot do honestly is show a SHAPE there, because there is no terrain data
// to shape. preDataFillTo answers this by washing the pre-data region's own
// covered extent as a plain full-height rectangle in Fade(Theme.Absent,
// elevationAbsentFillAlpha(c.Theme)) -- the same colour role the fill's
// dropout case below already uses for "no data", extended to a stretch that
// is permanently without data rather than only absent for one frame. Its x
// extent still advances with distance, its height says nothing, because
// there is nothing to say. No dot is drawn while the playhead is in this
// region: there is no point on a curve that does not exist yet to put one
// on.
//
// On an ordinary recording this region is a few metres wide against several
// kilometres of axis -- sub-pixel, and this policy will not be visible
// there. It is written for the activity where it will be: one
// whose GPS lock or barometer settles slowly enough that the gap is several
// tens of metres, which must still read as "moving, terrain unknown" and not
// as "stalled" or "broken".
//
// # Absence, the rest of it
//
// Beyond the pre-data region, absence is decided per instant, not once for
// the whole render:
//
//   - HasDistance, at or past profileStart, inside the axis: the ordinary
//     case. The fill runs from the trace's own start to the playhead in
//     Fade(Foreground, elevationFillAlpha), and the Accent dot sits on the
//     trace at the same x -- a different job from the fill (WHICH elevation,
//     not how far), and the project's established mark for "current
//     position", matching the route panel's own dot.
//   - HasDistance, past the recorded end: the fill clamps to full and no dot
//     is drawn -- the existing refusal to assert a position outside the axis,
//     unchanged from before the fill existed. (There is no longer a
//     symmetric "before the start" case at this axis's own zero: distance is
//     cumulative and non-negative, so nothing renders before it, and the
//     stretch that used to need this guard, before profileStart, is now the
//     pre-data region above.)
//   - !HasDistance (a dropout mid-activity, or a zero Sample inside a gap):
//     the fill washes the WHOLE axis -- the pre-data region via
//     preDataFillTo AND the recorded trace via fillTo -- in
//     Fade(Theme.Absent, elevationAbsentFillAlpha(c.Theme)). Two more
//     honest-looking answers were considered and both are wrong. Drawing no
//     fill is pixel-identical to a fill of zero, i.e. to "back at the start
//     line" -- the confident lie in pixels this project exists to refuse,
//     and it is also indistinguishable from the panel having crashed.
//     Holding the previous frame's fill is not merely discouraged, it is
//     UNAVAILABLE: Dynamic must not mutate the Painter, so there is no
//     last-frame state here to hold -- the contract forbids the wrong answer
//     before anyone gets to choose it. Washing the axis in Absent says "the
//     extent of your progress is unknown" without inventing an extent, in
//     the same vocabulary the distance readout this fill replaces used for
//     its own "--".
//
// The pre-data region washes in Absent in EVERY case once the playhead has
// passed it, dropout or not -- never Foreground, even though the distance it
// represents is perfectly well known. Foreground would claim a terrain shape
// that was never recorded; Absent is the honest statement regardless of
// whether the CURRENT instant's distance also happens to be known.
//
// # Why the absent wash gets its OWN alpha, not elevationFillAlpha
//
// An earlier version of this panel drew the absent wash at elevationFillAlpha,
// the covered fill's own opacity, reasoning that Theme.Absent and
// Theme.Foreground are distinct colours so fading them by the same fraction
// keeps them distinct too. That reasoning does not survive contact with
// Theme.Absent's own job: the role is DESIGNED to sit close to Background
// (see canvas.go's Theme doc comment), which is exactly what made the shared
// alpha's absent wash measure below this project's own contrast floor
// against Background in BOTH shipped themes -- the identical failure the
// --highlight background= warning in cmd/render.go exists to catch in a
// user's own colour choice, reproduced here in a constant nobody typed.
// elevationAbsentFillAlpha derives a per-theme opacity instead, so the wash
// clears that floor regardless of theme (see its own doc comment for the
// numbers and for the trade-off it costs against the covered fill's own
// distinctness).
//
// What that derivation does NOT do is guarantee the absent wash stays
// distinguishable from the covered fill under EVERY possible background.
// Both fills are composited over whatever Theme.Background is when Dynamic
// runs, and under --highlight-style wash that background can be a user's own
// --highlight background=. A background chosen at or near Theme.Absent pulls
// the absent wash toward invisibility against it regardless of alpha -- Fade
// can only interpolate toward a colour, and a colour that already equals its
// destination has nowhere left to move. cmd/render.go's own background=
// contrast warning is what catches that choice before a render is spent on
// it; this panel relies on that warning rather than re-deriving it, and
// makes no claim of its own that the two fills stay distinct beyond the
// themes this project ships.
//
// The full-height playhead rect this used to draw is gone: the fill's own
// leading edge marks the same x, and keeping both left a Foreground stub
// hanging above the trace through the empty upper region, reading as a
// separate object rather than as the fill's own boundary. If the edge ever
// reads as mushy at the gate, the remedy is to stroke it at headW in Accent
// from the floor to the trace -- NOT to restore the rect.
func (p *elevationPainter) Dynamic(c *Canvas, f Frame) {
	if p.flat || len(p.xs) < 2 {
		return
	}
	// Derived once per call, from this render's own theme -- never cached on
	// the Painter, which Dynamic must not mutate (see the Painter contract),
	// and cheap enough (a bounded bisection over plain arithmetic, not a
	// per-pixel operation) that recomputing it on every frame it is needed
	// costs nothing worth caching against.
	absent := Fade(c.Theme.Absent, elevationAbsentFillAlpha(c.Theme))
	if !f.Sample.HasDistance {
		p.preDataFillTo(c, p.xs[0], absent)
		p.fillTo(c, p.plot.X+p.plot.W, absent)
		return
	}

	d := f.Sample.Distance
	if d < p.profileStart {
		// Known distance, but before the trace's own first recorded point:
		// see "The pre-data region" above. No dot -- there is no curve here
		// to place one on.
		p.preDataFillTo(c, p.xForDistance(d), absent)
		return
	}

	// The playhead is at or past profileStart, so the pre-data region (if
	// any) is now entirely behind it and washes in full, always in Absent --
	// see the doc comment above for why never Foreground here.
	p.preDataFillTo(c, p.xs[0], absent)

	if d > p.axisEnd {
		p.fillTo(c, p.plot.X+p.plot.W, Fade(c.Theme.Foreground, elevationFillAlpha))
		return
	}

	x := p.xForDistance(d)
	p.fillTo(c, x, Fade(c.Theme.Foreground, elevationFillAlpha))

	y, _ := p.profileYAt(x)
	c.Circle(x, y, p.dotR, c.Theme.Accent)
}

// formatElevation renders metres, without decimals: a profile's labels are
// read at a glance, and a tenth of a metre is below the noise of the
// barometric reading behind them.
func formatElevation(m float64) string { return fmt.Sprintf("%.0f m", m) }

// formatDistance renders metres below a kilometre and kilometres above it, so
// a short activity is not labelled "0.4 km" at both ends.
func formatDistance(m float64) string {
	if m < 1000 {
		return fmt.Sprintf("%.0f m", m)
	}
	return fmt.Sprintf("%.1f km", m/1000)
}
