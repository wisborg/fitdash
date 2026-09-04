package panel

import (
	"fmt"
	"image/color"
	"math"
	"time"

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
//
// # The name rows
//
// A configured --highlight or --label is drawn on this axis as a mark on
// the baseline (see buildMarks and elevMark/elevTick) -- both resolved by
// DISTANCE, never time, for the reason the mapping's own cost is documented
// at buildMarks and TimeToDistance. Naming which mark is active needs
// somewhere to print that does not collide with the trace itself, so
// Prepare reserves up to two rows at the TOP of this panel's own box,
// above the plot: the label name on top, the highlight name beneath it,
// plot below -- the same top-to-bottom order MarkerPanel's own ribbon
// reads in, so a viewer moving between a strip render and a profile render
// sees the same stack. The reservation is CONDITIONAL on what
// ctx.Highlights/ctx.Labels actually configure: zero rows, and no cost to
// the plot's own height, when neither is; only the row that is actually
// used when just one is. Each row's own name draws in its OWN area
// (drawActiveMarks), never a row shared between the two -- a label passing
// over an active highlight must not make the highlight's name vanish and
// reappear, which is exactly the alternative MarkerPanel's own Dynamic
// already rejected for the identical reason, on the identical two names.
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
// So the model itself is the test -- ctx.Elevation, built once by whoever
// constructed ctx (see Context.Elevation's own doc comment), never rebuilt
// here. An earlier version of this method called BuildElevationModel itself,
// which meant Accepts, Prepare and internal/render's profileTakesTheBand each
// built an independent copy of the same model from the same track -- three
// builds of something a target-tuned tuning makes genuinely expensive (up to
// forty full Gaussian smoothing passes), and three places the same track
// could in principle disagree about the same model if the tuning were ever
// threaded through differently.
//
// The actual test lives in elevationIsPlottable, shared with every other
// panel built on the same model -- see that function's own doc comment for
// why a shared function, not a shared comment saying "keep this in sync",
// is what keeps them from disagreeing.
func (ElevationPanel) Accepts(ctx *Context) bool { return elevationIsPlottable(ctx) }

// elevationIsPlottable is the ONE accept test for every panel that reads
// ctx.Elevation -- ElevationPanel here, ClimbPanel in climb.go, and the
// gradient panel to follow -- extracted so all three ask the identical
// question rather than each carrying its own copy that could quietly stop
// agreeing. A model that ElevationPanel declines to plot but ClimbPanel
// happily draws gain and loss bars from is exactly the two-panels-disagree
// failure the panel contract's Accepts/Prepare split exists to prevent at a
// different seam: one panel would be drawing on data the other has already
// judged not worth showing, with nothing on screen explaining why the
// profile is missing while the bars beside it are not.
//
// Requires BOTH elevation and distance, on the same samples -- distance is
// not incidental, it is every one of these panels' x axis (see
// ElevationPanel's own doc comment for why asking the coverage report about
// each metric independently is not enough) -- and a model with a real
// elevation range and a real distance span. A flat or zero-span profile
// still has a model (m != nil, not Empty) and would still take a panel's
// box, but with nothing to show a range or a distance progressing against:
// declining hands that box to whatever the layout falls through to instead,
// which is the honest outcome for every panel built on this model, not only
// the profile.
func elevationIsPlottable(ctx *Context) bool {
	if ctx.Track == nil {
		return false
	}
	if !ctx.Report.Carries(inspect.MetricElevation) || !ctx.Report.Carries(inspect.MetricDistance) {
		return false
	}
	m := ctx.Elevation
	if m == nil || m.Empty() {
		return false
	}
	minElev, maxElev := m.Range()
	profileStart, axisEnd := m.StartDistance(), m.TotalDistance()
	if maxElev <= minElev || axisEnd <= profileStart {
		return false
	}
	return true
}

// DefaultElevationTuning is the smoothing this panel tuned against, absent
// any user-typed override: the device's own ascent and descent totals where
// the file reported them, or the library's own default sigma otherwise.
//
// Barometric elevation is noisy enough that a raw per-sample sum wildly
// overcounts climbing, and matching a figure the watch already published is
// more trustworthy than any constant chosen here. Exported because
// cmd/render.go's four-level --elevation-smoothing / --elevation-gain /
// --elevation-loss precedence bottoms out at exactly this derivation on its
// own third and fourth levels (see cmd's resolveElevationTuning), and this
// project's own test fixtures build the identical Context.Elevation a real
// render would by calling this rather than re-deriving the same rule a
// second time.
func DefaultElevationTuning(track *fitactivity.Track) fitactivity.ElevationOptions {
	var opts fitactivity.ElevationOptions
	if track != nil && track.HasElevationTotals {
		opts.TargetGain, opts.TargetLoss = track.TotalAscent, track.TotalDescent
	}
	return opts
}

// BuildElevation is the single spelling for constructing a render's
// elevation model: cmd/render.go calls it once, into Context.Elevation, and
// every consumer -- ElevationPanel today, the climb and gradient panels this
// field exists to let land -- reads that field rather than calling
// fitactivity.BuildElevationModel a second time on the same track and risking
// a second answer.
//
// A nil track (Context.Track never set) returns nil rather than reaching
// fitactivity.BuildElevationModel, which ranges over track.Samples and has no
// nil guard of its own -- the same case ElevationPanel.Accepts already
// refuses before this function existed, moved here so every caller gets the
// same refusal rather than each panel re-deriving it.
func BuildElevation(track *fitactivity.Track, tuning fitactivity.ElevationOptions) *fitactivity.ElevationModel {
	if track == nil {
		return nil
	}
	return fitactivity.BuildElevationModel(track, tuning)
}

// gradeWindowMeters is the +/- distance a grade reading is averaged over --
// fitactivity.ElevationModel.GradeAtDistance reads d-window..d+window -- taken
// verbatim from videofx's own gradeWindowMeters rather than derived fresh
// here. The defence is agreement with the sibling, not a fresh derivation:
// both programs read the same files through the same library, and reporting
// different grades for the same instant of the same activity is worse than
// either number being individually optimal on its own -- the same argument
// this project's own --power-source already makes about shared vocabulary,
// extended to a constant.
//
// Most of a grade reading's own averaging is already done by the model's
// Gaussian smoothing, tuned to the file's totals (see DefaultElevationTuning
// and cmd's resolveElevationTuning) -- widening this window from 10 m to 120 m barely
// moves the reading on a tuned file. So this is a SECOND-STAGE filter
// choosing the run of ground a grade is measured over, not what rescues the
// reading from a noisy barometer; that job is already done upstream.
const gradeWindowMeters = 30.0

// gradeWindowFor is gradeWindowMeters, widened when this render's own
// compression makes one frame's worth of ground exceed it -- smoothSample's
// own argument (internal/render/smooth.go), applied to distance rather than
// to a gauge reading: a grade measured over less ground than a single frame
// covers is an arbitrary pick from a span the render presents as one instant,
// not an average of it.
//
// A DISTANCE window, deliberately, never a time one. Grade is a property of
// terrain: a time window shrinks the road length considered exactly where a
// runner is slowest -- the steep climb, where the reading matters most --
// while smearing hundreds of metres of a fast descent into one figure. That
// systematically under-reports climbs and over-smooths descents, in opposite
// directions on the same activity.
//
// strideMetres is the ground one frame advances at this render's COARSEST
// segment -- maxSpeedup is Timeline.MaxSpeedup(), not the base rate, the same
// choice bindDistancePrecision (readout.go) already makes and for the
// identical reason: a highlight slowed toward real time must never widen a
// window sized for the rest of the render. Only half of that stride is
// spent, because GradeAtDistance already reads +/-window -- window is the
// RADIUS of the span queried, strideMetres its DIAMETER.
//
// All four inputs are render-wide and available once Prepare runs, which is
// the precedent bindDistancePrecision already sets for resolving a per-render
// quantity there rather than per frame.
//
// Consequence for the animation, worth stating where a reviewer will look for
// the easing this design deliberately omits: at running pace a 30 fps frame
// advances a small fraction of the window's own local variation, so the
// reading this window produces is already smooth from one frame to the next
// -- there is nothing left to ease toward.
func gradeWindowFor(totalDistance, activitySeconds, maxSpeedup, fps float64) float64 {
	if activitySeconds <= 0 || fps <= 0 {
		return gradeWindowMeters
	}
	strideMetres := totalDistance * maxSpeedup / (fps * activitySeconds)
	if half := strideMetres / 2; half > gradeWindowMeters {
		return half
	}
	return gradeWindowMeters
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
// The headroom has to clear the low label's own glyphs against the
// baseline rule below it, or the rule reads as a strike-through rather
// than a floor -- and that clearance depends on labelPx, not on this
// fraction's own name, so a future change to labelPx (bigger text, a
// different font, a metrics change upstream) has to re-check this gap
// rather than assume it still holds. 0.25 is the value that clears the low
// label with real margin -- not merely daylight -- at 1080p, 4K and
// portrait, while still giving the real trace the bulk of the plot's own
// height.
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

// TimeToDistance resolves offset -- an activity-time offset from start,
// exactly what Highlight.From/To and Label.At are measured in -- to a
// distance via track's own gap-aware sample lookup. Exported because it has
// two callers that must never disagree: this panel's own buildMarks, which
// feeds the result into xForDistance to place a mark, and
// cmd/render.go's summary, which only needs ok to report a mark it could not
// place. A local copy of the same lookup in cmd used to exist for that
// second caller; it is gone; both now call this one function.
//
// ok is false, and the caller must not draw a mark at the returned distance
// (which is meaningless when ok is false), in two cases that this function
// deliberately does NOT distinguish between: AtWithGap itself refusing --
// offset falls outside the track's own coverage, or deeper into a real
// recording gap than fitactivity.DefaultMaxGap from either side -- and
// AtWithGap succeeding but returning a sample with HasDistance false, a GPS
// dropout landing exactly on offset without the surrounding gap itself
// being wide enough to trip the first case. Both are the identical fact
// from a caller's point of view: there is no distance to place a mark at,
// and this function's whole job is to make that case distinguishable from
// an actual distance rather than silently returning zero, which a caller
// could mistake for "the start line".
//
// This is deliberately a THIN wrapper, not a second interpolation on top of
// AtWithGap's own. Refusing to draw a straight line across a gap wider than
// DefaultMaxGap is exactly what AtWithGap is for (see its own doc comment
// in fitactivity); resolving the gap differently here -- say, by walking
// Track.Samples for the nearest distance on either side -- would be a
// second, local copy of the same judgement call, on a narrower and less
// reviewed slice of the problem. If a real recording's marks need rescuing
// from a dropout at their own boundary often enough to matter -- widening
// to the nearest known distance INSIDE the span rather than refusing the
// whole mark -- that needs a "first sample carrying distance in [a,b]"
// lookup, which is a fitactivity accessor to add on its own branch and
// verify against every consumer, not a local walk of Track.Samples here.
func TimeToDistance(track *fitactivity.Track, start time.Time, offset time.Duration) (float64, bool) {
	if track == nil {
		return 0, false
	}
	s, ok := track.AtWithGap(start.Add(offset), fitactivity.DefaultMaxGap)
	if !ok || !s.HasDistance {
		return 0, false
	}
	return s.Distance, true
}

// Prepare builds the elevation model and lays out the plot, once.
func (ElevationPanel) Prepare(ctx *Context, box Box) Painter {
	p := &elevationPainter{box: box}

	if ctx.Track == nil {
		return p
	}
	m := ctx.Elevation
	if m == nil || m.Empty() {
		// Accepts checks the same field and declines on this, so reaching
		// here means a caller placed the panel without asking, or without
		// having populated ctx.Elevation at all. Drawing nothing is then the
		// least-wrong option available, but it is not one this panel chooses
		// for itself.
		return p
	}

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	// The axis chrome -- both elevation labels and (via distPx below) both
	// distance labels -- is sized from ctx.BasePx(), the layout's OWN base
	// text size for this frame, not from unit (this panel's own box). See
	// elevAxisLabelFraction's own doc comment for why: unit is exactly the
	// quantity the band's weight in layouts.go controls, and chrome sized
	// from it inherits every future re-weighting of that band as a change
	// in how loud the axis reads, which is not a property axis labels have
	// any business tracking.
	p.labelPx = ctx.BasePx() * elevAxisLabelFraction

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

	// The name rows -- see the type doc comment's "the name rows" section
	// and marks.go's fitLongestName -- are reserved above the plot,
	// CONDITIONAL on what ctx.Highlights/ctx.Labels actually configure: zero
	// rows, and topReserved stays zero, when neither is configured, which is
	// what keeps a render configuring neither pixel-identical to the box
	// this panel resolved to before this feature existed (the frozen
	// pixel-identity test's whole claim -- see elevation_test.go). One row
	// when only one of the two is; two, stacked, when both are. Ordering is
	// label above highlight, matching MarkerPanel's own top-to-bottom read
	// (label above the ribbon, highlight below it), so a viewer moving
	// between a strip render and a profile render reads the same stack.
	//
	// Sized from unit like every other measure here, at the row's own
	// NOMINAL font size -- before fitLongestName (called from buildMarks,
	// once the plot's width is known) ever narrows it for a long name. That
	// order matters: fitLongestName only ever shrinks a size to fit an
	// available width, never grows one, so reserving against the nominal
	// size is already the largest the row will ever need and does not
	// itself depend on which names are configured -- only on whether a row
	// is needed at all.
	var labelRowH, highlightRowH float64
	if len(ctx.Labels) > 0 {
		p.labelNamePx = unit * elevLabelNamePx
		labelRowH = p.labelNamePx * elevNameRowPadding
	}
	if len(ctx.Highlights) > 0 {
		p.namePx = unit * elevNamePx
		highlightRowH = p.namePx * elevNameRowPadding
	}
	rowsTop := box.Y
	if labelRowH > 0 {
		p.labelNameY = rowsTop + labelRowH/2
		rowsTop += labelRowH
	}
	if highlightRowH > 0 {
		p.nameY = rowsTop + highlightRowH/2
		rowsTop += highlightRowH
	}
	topReserved := rowsTop - box.Y

	p.plot = Box{
		X: box.X + gutter,
		Y: box.Y + p.labelPx*0.9 + topReserved,
		W: box.W - gutter - box.W*0.02,
		H: box.H - p.labelPx*0.9 - topReserved - bottom,
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

	// The mark strip -- every configured highlight's block, every
	// configured label's tick -- lies ON the baseline rule, entirely inside
	// the plot. See buildMarks and "the marks" in the type doc comment for
	// why DISTANCE, not time, is the only defensible axis for it once it is
	// drawn inside this panel's own box.
	p.markH = unit * elevMarkFraction
	p.tickW = math.Max(1, unit*0.012)
	p.tickExtend = unit * elevTickFraction
	p.buildMarks(ctx)

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

// elevAxisLabelFraction is the axis chrome's own text size -- both
// elevation labels (highLabel/lowLabel) and, via distPx's own fit-to-width
// starting point, both distance labels -- as a fraction of ctx.BasePx(),
// the layout's base text size for THIS FRAME. Not a fraction of unit (this
// panel's own box height), which is what every other measure on this
// Painter is sized off, and that is the point of the departure, not an
// oversight.
//
// Sizing axis chrome from unit couples it to this panel's own band weight
// in layouts.go: re-weighting the band for an unrelated reason -- to make
// room for the mark name rows, say -- scales the axis labels right along
// with it, for a reason that has nothing to do with legibility, and can
// just as easily invert the hierarchy between chrome and the mark names it
// sits beside (elevNamePx/elevLabelNamePx below) as leave it alone. BasePx
// is fixed for the whole render -- derived from the OUTPUT FRAME's own
// dimensions and the layout's FontScale (see Context.BasePx) -- so it
// cannot change because this panel's own band gained or lost weight. The
// panel contract's own "how does it scale" question names exactly these
// two sources -- frame dimensions or the layout's font scale -- and unit is
// the frame dimension already spent turning INTO this panel's own box;
// BasePx is the other one, upstream of that division.
//
// 0.45 is legible chrome, clearly recessive beside a mark's name, checked
// at 1080p, 4K and in the portrait tree: because BasePx depends only on
// the frame's own smaller dimension, portrait and landscape land the axis
// labels at the identical size for a given output quality, rather than
// following whatever height this panel's own band happens to resolve to
// in each tree.
//
// The trade this does not pretend to dodge: unit-derived measures shrink
// automatically if this panel is ever given a much shorter band, and this
// one no longer does. A band cut short enough that the axis chrome no
// longer fits would need a hand retune, or the labels overflow it --
// nothing here clamps against unit to catch that case. Every layout this
// panel is placed in today gives it a band several times BasePx tall, so
// that is a deliberate omission rather than a gap nobody noticed; a future
// layout wanting a much shorter band is itself a layout-selection question
// the panel contract says to raise, not something to silently guard
// against with a second, competing constant.
const elevAxisLabelFraction = 0.45

// elevMarkFraction is the mark strip's own thickness, as a fraction of unit
// -- the same min(box.W, box.H) every other measure on this Painter is
// sized off. It sits on the baseline rule and stays inside the plot, so it
// never competes with the bottom label row's own reserved height (see
// "bottom" above) the way a strip along the panel's own edge would have.
const elevMarkFraction = 0.05

// elevTickFraction is how far a label's tick rises above the baseline into
// the plot -- deliberately taller than elevMarkFraction so a tick reads as
// rising OUT of the mark strip rather than as another block of the same
// height sitting beside it.
const elevTickFraction = 0.14

// elevNamePx and elevLabelNamePx are the highlight and label name rows' own
// nominal text sizes, reserved above the plot -- see Prepare's own
// "the name rows" comment. Both scale off unit like every other measure on
// this Painter, and both sit smaller than MarkerPanel's own namePx/
// labelNamePx (0.22/0.16): those fight a ribbon and a playhead for the same
// fixed vertical inches, so they earn every fraction they claim; these sit
// in rows the PLOT ITSELF pays for by shrinking, so the fraction is a
// judgement call to check at the gate -- legible at 1080p, 4K and portrait
// without spending more of the plot's own height than the name is worth.
//
// These two are not independent of elevAxisLabelFraction, and must be
// retuned together with it rather than in isolation. A mark's name is this
// panel's annotation, not its content the way it is on MarkerPanel's own
// ribbon, so it has to read smaller than the plot itself but larger than
// the axis chrome it sits above -- and the two are fractions of DIFFERENT
// bases (unit here, ctx.BasePx() for the axis chrome), so retuning one
// without checking the other can silently invert that hierarchy. 0.115 and
// 0.10 keep the highlight name clearly larger than the axis labels and the
// label name a step below the highlight's, at every resolution checked,
// while still leaving the real trace the bulk of the plot's own height.
//
// elevLabelNamePx moved from 0.09 to 0.10 when ClimbPanel joined the bottom
// stack (layouts.go): unit is this Painter's own box, and the box the Alt
// band resolves to is a FRACTION of the tree's total weight that shrank
// once climb's row joined it as a sibling with its own weight -- 2/7 of the
// landscape tree's total before, 2/8 after, the identical accepted cost
// every OTHER sibling in that Col pays too. elevAxisLabelFraction's own
// text size does not move with it (it is sized from ctx.BasePx(), the
// frame's own dimensions, precisely so it would not inherit an unrelated
// re-weighting -- see that constant's own doc comment), so the margin
// between the two closed until TestElevationPanel_MarkNameRowsAreLargerThanTheAxisChrome
// caught the label name row actually landing AT the axis chrome's own size
// on a 1080p and a 4K landscape render, inverting the hierarchy this
// constant exists to keep. 0.10 was measured, not merely bumped until the
// test passed: it restores comfortable headroom at every resolution that
// test checks, including the portrait tree, which had never lost its own
// margin (portrait's Alt weight grew rather than shrank in the same change).
const elevNamePx = 0.115
const elevLabelNamePx = 0.10

// elevNameRowPadding multiplies a name row's own font size into the row's
// reserved height, leaving ascender/descender clearance around the glyph.
// A flat multiple, not the per-shape glyph-extent derivation
// highlight_panel.go's Prepare works through for its own rows: that
// derivation earns its keep fighting a ribbon and a playhead for shared
// space, and a name row here has nothing else in it to fight -- the row is
// wholly the name's own to spend.
const elevNameRowPadding = 1.4

// elevMark is one highlight's span, resolved to a pair of x coordinates on
// THIS panel's own distance axis by buildMarks -- xForDistance(d0) and
// xForDistance(d1), already widened to the axis's own floor and separated
// from its neighbours by the identical geometry (marks.go) MarkerPanel's
// own blocks use on the time axis.
//
// ok is false when either endpoint's distance could not be resolved (see
// TimeToDistance) -- D.1's policy: a mark needs BOTH endpoints, and one
// known, one not is still unplaceable. There is no placeholder for "a mark
// with no position on this axis", the same as there is none on the route
// panel's map (see routeMark), so Static and Dynamic both skip a mark whose
// ok is false; reporting it in words is cmd's job, not this Painter's.
type elevMark struct {
	ok     bool
	x0, x1 float64
}

// elevTick is one label's instant, resolved to a single x the same way.
type elevTick struct {
	ok bool
	x  float64
}

// buildMarks resolves every configured highlight to a block and every
// configured label to a tick on this panel's distance axis, entirely from
// ctx.Highlights, ctx.Labels, ctx.Track and ctx.Timeline.Start() -- all
// fixed for the whole render -- so nothing here reads a Frame and every
// mark's position is invariant across it. The tempting wrong version reads
// f.Sample.Distance in Dynamic instead; that recomputes a lookup Prepare can
// already do once, and it is also simply wrong once a distance dropout is in
// play -- a mark is a property of the highlight's own span (an offset the
// user typed), not of what the CURRENT frame's sensor happened to record
// (see Dynamic's own comment on why marks still draw over the absent wash).
//
// Skipped entirely when neither Highlights nor Labels is configured, so
// ctx.Timeline -- a zero Timeline in a caller that had no reason to set one
// before this change, such as most of this file's own existing tests -- is
// never touched by a render with no marks configured at all.
func (p *elevationPainter) buildMarks(ctx *Context) {
	if len(ctx.Highlights) == 0 && len(ctx.Labels) == 0 {
		return
	}
	start := ctx.Timeline.Start()

	p.marks = make([]elevMark, len(ctx.Highlights))
	p.names = make([]string, len(ctx.Highlights))
	p.anchorX = make([]float64, len(ctx.Highlights))
	minMarkW := math.Max(2, p.markH*minBlockFraction)
	boxes := make([]Box, 0, len(ctx.Highlights))
	order := make([]int, 0, len(ctx.Highlights))
	for i, h := range ctx.Highlights {
		p.names[i] = h.Name
		d0, ok0 := TimeToDistance(ctx.Track, start, h.From)
		d1, ok1 := TimeToDistance(ctx.Track, start, h.To)
		if !ok0 || !ok1 {
			// D.1: unplaceable. p.marks[i] stays its zero value (ok:
			// false); Static and Dynamic both skip it -- including its
			// name, which has no anchor to draw at any more than the mark
			// itself has an x to draw at.
			continue
		}
		x0, x1 := widenToMinimum(p.xForDistance(d0), p.xForDistance(d1), minMarkW)
		boxes = append(boxes, Box{X: x0, W: x1 - x0})
		order = append(order, i)
	}
	// Distance is monotone non-decreasing in time (see the type doc
	// comment's "the marks" section), so highlights already sorted by From
	// (resolveHighlights' own contract) produce boxes already sorted
	// ascending by X -- separateSpans' own precondition for treating this
	// as a single left-to-right sweep, exactly as MarkerPanel's blocks are
	// on the time axis.
	separateSpans(boxes, p.markH*blockGapFraction)

	// The name row's own text size is fit against the LONGEST configured
	// highlight name, once -- never against whichever one is active, which
	// is the jitter fitLongestName (marks.go) exists to prevent, the
	// identical rule MarkerPanel's own namePx follows. Sized against
	// p.plot.W, the width the name row actually has, not against box.W --
	// the plot is already inset from the panel's own box by the y-axis
	// label gutter, and fitting against the wider box would let a long name
	// print into that gutter.
	p.namePx = fitLongestName(ctx.Fonts, p.names, p.namePx, p.plot.W*0.92)
	for j, b := range boxes {
		i := order[j]
		// The name sits centred over its own (already separated) block,
		// clamped to the plot's own x extent -- the same clampAnchor rule
		// (marks.go) MarkerPanel's own anchorX uses, so a highlight near
		// either edge of the plot cannot print its name half off the
		// panel.
		anchor := b.X + b.W/2
		if ctx.Fonts != nil && p.names[i] != "" {
			if w, _, err := ctx.Fonts.Measure(p.names[i], p.namePx); err == nil {
				anchor = clampAnchor(anchor, w/2, p.plot.X, p.plot.X+p.plot.W, p.plot.X+p.plot.W/2)
			}
		}
		p.marks[i] = elevMark{ok: true, x0: b.X, x1: b.X + b.W}
		p.anchorX[i] = anchor
	}

	// Labels have no span to widen -- an instant, not a range -- so each
	// starts as a bare x, widened to a hairline box only for THIS
	// separation pass and then collapsed back to its own centre. Two
	// labels landing on (or nudged onto) the same distance, e.g. either
	// side of a pause, is D.4's case: now that a name draws in the label
	// row, two coincident ticks would print one name directly over the
	// other rather than merely a doubled line, so they are nudged apart by
	// the identical separation sweep the highlight blocks above already
	// use, bounded to 2*tickW away from each tick's own true position. The
	// bound is what keeps the nudge honest -- it moves a tick by less than
	// this axis's own pixel quantization can even express as a different
	// distance, never far enough to claim the two labels fell at genuinely
	// different points on the course. A pair still touching after the
	// bound is left as close as the bound allows, recorded into
	// overlappingLabels below rather than silently accepted: reporting it
	// in words is cmd's job (see OverlappingLabels and TimeToDistance's own
	// D.1 policy above), not this Painter's, but cmd can only report what
	// this Painter hands back -- the geometry that decides "still touching"
	// exists only here, sized from this render's own box.
	p.ticks = make([]elevTick, len(ctx.Labels))
	p.labelNames = make([]string, len(ctx.Labels))
	p.labelAnchorX = make([]float64, len(ctx.Labels))
	tickBoxes := make([]Box, 0, len(ctx.Labels))
	tickOrder := make([]int, 0, len(ctx.Labels))
	tickOrig := make([]float64, 0, len(ctx.Labels))
	for i, l := range ctx.Labels {
		p.labelNames[i] = l.Name
		d, ok := TimeToDistance(ctx.Track, start, l.At)
		if !ok {
			// D.1, restated for a tick: unplaceable, and p.ticks[i] stays
			// its zero value (ok: false); Static and Dynamic both skip it
			// and its name.
			continue
		}
		x := p.xForDistance(d)
		tickBoxes = append(tickBoxes, Box{X: x - p.tickW/2, W: p.tickW})
		tickOrder = append(tickOrder, i)
		tickOrig = append(tickOrig, x)
	}
	separateSpans(tickBoxes, p.tickW)

	// Same template rule as the highlight row, applied to the label row:
	// sized once against the longest configured label name, never against
	// whichever one is active.
	p.labelNamePx = fitLongestName(ctx.Fonts, p.labelNames, p.labelNamePx, p.plot.W*0.92)

	// prevX/havePrev track the immediately preceding tick's own FINAL
	// (clamped) x, in the same ascending-by-time order tickBoxes was built
	// in (see the comment above on why that is also ascending by x). D.4's
	// "still touching" is a fact about NEIGHBOURS -- the separation sweep
	// only ever pulls adjacent pairs apart, so only an adjacent pair can
	// still be under gap p.tickW apart once it has run. p.tickW is the same
	// gap separateSpans was asked to enforce a few lines above; anything
	// short of it once the bound has clamped is exactly the shortfall the
	// bound exists to leave honest rather than paper over (see D.4 above).
	var prevX float64
	var prevI int
	havePrev := false
	for j, b := range tickBoxes {
		i := tickOrder[j]
		x := b.X + b.W/2
		if lo, hi := tickOrig[j]-2*p.tickW, tickOrig[j]+2*p.tickW; x < lo {
			x = lo
		} else if x > hi {
			x = hi
		}
		p.ticks[i] = elevTick{ok: true, x: x}

		if havePrev && x-prevX < p.tickW {
			p.overlappingLabels = appendOverlapping(p.overlappingLabels, prevI)
			p.overlappingLabels = appendOverlapping(p.overlappingLabels, i)
		}
		prevX, prevI, havePrev = x, i, true

		// Anchored over the label's OWN (nudged) tick, exactly the way a
		// highlight's name is anchored over its own block above -- not
		// centred in the row, which is invisible with one label and gives
		// several no visible relationship to any tick at all.
		anchor := x
		if ctx.Fonts != nil && p.labelNames[i] != "" {
			if w, _, err := ctx.Fonts.Measure(p.labelNames[i], p.labelNamePx); err == nil {
				anchor = clampAnchor(anchor, w/2, p.plot.X, p.plot.X+p.plot.W, p.plot.X+p.plot.W/2)
			}
		}
		p.labelAnchorX[i] = anchor
	}
}

// appendOverlapping appends i to overlapping, skipping it when it is already
// the LAST element -- the only duplicate D.4's neighbour-pair scan in
// buildMarks can produce: a label in the middle of three that all collide is
// visited once as the later half of one pair and once as the earlier half
// of the next, and the two calls are always consecutive, so comparing only
// against the last element is enough to keep the result free of repeats
// without a set.
func appendOverlapping(overlapping []int, i int) []int {
	if n := len(overlapping); n > 0 && overlapping[n-1] == i {
		return overlapping
	}
	return append(overlapping, i)
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

	// markH, tickW, tickExtend size the mark strip: markH is the thickness
	// of a highlight's block, resting on the baseline; tickExtend is how
	// far a label's tick rises above the baseline into the plot; tickW is
	// the tick's own stroke width. See elevMarkFraction/elevTickFraction.
	markH, tickW, tickExtend float64

	// marks is parallel to Context.Highlights and indexed by Frame.Interval
	// -- the same convention MarkerPanel's own blocks/anchorX and
	// RoutePanel's own marks use, so a lookup here can never disagree with
	// which highlight the render loop says is active.
	marks []elevMark

	// ticks is parallel to Context.Labels and indexed by Frame.Label, for
	// the identical reason.
	ticks []elevTick

	// namePx, nameY size and place the highlight name row; labelNamePx,
	// labelNameY the label name row beneath it -- both reserved above the
	// plot in Prepare, and both zero (rows never occupied) when the
	// corresponding slice (ctx.Highlights/ctx.Labels) was empty. See
	// Prepare's own "the name rows" comment for why the reservation is
	// conditional and why label sits above highlight.
	namePx, nameY           float64
	labelNamePx, labelNameY float64

	// names and anchorX are parallel to Context.Highlights and indexed by
	// Frame.Interval, exactly the convention marks already uses -- names[i]
	// is empty precisely when the highlight itself was configured with no
	// name, mirroring MarkerPanel's own p.names.
	names   []string
	anchorX []float64

	// labelNames and labelAnchorX are parallel to Context.Labels and
	// indexed by Frame.Label, for the identical reason.
	labelNames   []string
	labelAnchorX []float64

	// overlappingLabels holds, ascending, the index into Context.Labels of
	// every label whose final tick still sits within p.tickW of a neighbour
	// once buildMarks' bounded nudge (D.4) has run -- the case the 2*tickW
	// bound exists to leave honestly unresolved rather than paper over. See
	// OverlappingLabels and buildMarks' own D.4 comment.
	overlappingLabels []int
}

// OverlappingLabels implements LabelOverlapReporter: it reports which
// configured labels, by index into Context.Labels, could not be pulled
// tickW apart on this axis even after buildMarks' bounded nudge. cmd asks
// this rather than re-deriving tick positions itself, the identical reason
// TimeToDistance is exported rather than copied -- the geometry that would
// answer "are these two still touching" lives only on this Painter, sized
// from THIS render's own box, so a second computation of it in cmd could
// disagree with what actually got drawn.
func (p *elevationPainter) OverlappingLabels() []int { return p.overlappingLabels }

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

	// Every highlight's block, at rest, and every label's tick -- drawn HERE,
	// under where the Dynamic fill will later pass over them, so the fill
	// TINTS a rest-state block rather than hiding it: the fill is a
	// translucent wash (elevationFillAlpha), not an opaque stroke the way
	// RoutePanel's covered prefix is, so what Static already drew keeps
	// showing through it. Only the ACTIVE block is re-drawn, brightened, in
	// Dynamic (see drawActiveMarks) -- every other configured highlight's
	// block stays exactly what it drew here for the whole render. Ticks
	// have no "active" state of their own to brighten -- the mark itself
	// never changes once a label is on screen -- so they draw here once,
	// at full strength, and are never touched again; only the NAME above
	// them, in the reserved label name row, fades in and out per frame
	// (see drawActiveMarks).
	baselineTop := baseline - p.markH
	for _, m := range p.marks {
		if !m.ok {
			continue
		}
		c.Rect(Box{X: m.x0, Y: baselineTop, W: m.x1 - m.x0, H: p.markH}, Fade(c.Theme.Highlight, highlightRestAlpha))
	}
	for _, tk := range p.ticks {
		if !tk.ok {
			continue
		}
		c.Rect(Box{X: tk.x - p.tickW/2, Y: baseline - p.tickExtend, W: p.tickW, H: p.tickExtend}, c.Theme.Foreground)
	}
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
//
// # Draw order: fill (or absent wash), then marks and their names, then dot
//
// Mirroring RoutePanel's own Dynamic, and for the identical reason: the fill
// drawn just below is a translucent wash over the WHOLE plot up to the
// playhead, so anything drawn only in Static under it would be tinted by
// it, not obscured (see Static's own comment on the rest-state blocks it
// draws for exactly that effect) -- but the ACTIVE mark needs to read
// clearly above that tint regardless of which branch below drew the fill,
// so drawActiveMarks is called at the end of every branch, never only the
// ordinary one. It runs unconditionally on f.Sample.HasDistance: a
// highlight's mark, and a label's, are properties of the render's TIMELINE
// (an offset the user typed), not of what a sensor recorded at the current
// instant -- MarkerPanel's own doc comment makes the identical argument --
// so both must keep drawing, brightened by weight, straight over the
// absent wash on a distance dropout, and so must their names in the
// reserved rows above the plot: those rows sit outside the plot's own
// area, untouched by any fill or wash, so there is nothing for the name to
// disagree with regardless of which branch below ran. Only the dot is
// gated on a known, in-range distance: unlike a mark, a dot with no
// current position would be inventing one.
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
		p.drawActiveMarks(c, f)
		return
	}

	d := f.Sample.Distance
	if d < p.profileStart {
		// Known distance, but before the trace's own first recorded point:
		// see "The pre-data region" above. No dot -- there is no curve here
		// to place one on.
		p.preDataFillTo(c, p.xForDistance(d), absent)
		p.drawActiveMarks(c, f)
		return
	}

	// The playhead is at or past profileStart, so the pre-data region (if
	// any) is now entirely behind it and washes in full, always in Absent --
	// see the doc comment above for why never Foreground here.
	p.preDataFillTo(c, p.xs[0], absent)

	if d > p.axisEnd {
		p.fillTo(c, p.plot.X+p.plot.W, Fade(c.Theme.Foreground, elevationFillAlpha))
		p.drawActiveMarks(c, f)
		return
	}

	x := p.xForDistance(d)
	p.fillTo(c, x, Fade(c.Theme.Foreground, elevationFillAlpha))
	p.drawActiveMarks(c, f)

	y, _ := p.profileYAt(x)
	c.Circle(x, y, p.dotR, c.Theme.Accent)
}

// drawActiveMarks re-draws the currently active highlight's block, brightened
// by Frame.IntervalWeight through the SAME restAlpha ramp MarkerPanel's own
// blocks and RoutePanel's own route marks use -- one function shared by all
// three, rather than three independent copies of "brighten by weight" that
// could drift out of step (see restAlpha's own doc comment) -- and fades in
// its name in the highlight name row, plus the active label's name in the
// label name row beneath it, each in its OWN reserved row (see Prepare's
// "the name rows" comment) rather than one shared row: a label passing over
// an active highlight must not make the highlight's name vanish and
// reappear, which is exactly the alternative MarkerPanel's own Dynamic
// already rejected for the identical reason, on the identical two names.
//
// Every OTHER configured highlight's block/tick stays exactly what Static
// drew for it; only the active highlight's block and name, and the active
// label's name, need to read clearly here. A configured name that is the
// empty string draws no text, mirroring MarkerPanel's identical choice: the
// block or tick lighting up already says something is active, and empty
// text would print nothing anyway.
//
// A no-op for the highlight side when no highlight is active, or when the
// active one's own mark was never placeable to begin with (m.ok false, see
// buildMarks and D.1) -- there is nothing to brighten or anchor a name to
// in either case. Independently a no-op for the label side under the
// identical unplaceable-tick condition.
func (p *elevationPainter) drawActiveMarks(c *Canvas, f Frame) {
	if f.Interval != NoHighlight && f.Interval >= 0 && f.Interval < len(p.marks) {
		if m := p.marks[f.Interval]; m.ok {
			baseline := p.yForElevation(p.floorElev)
			weight := clampWeight(f.IntervalWeight)
			c.Rect(Box{X: m.x0, Y: baseline - p.markH, W: m.x1 - m.x0, H: p.markH}, Fade(c.Theme.Highlight, restAlpha(weight)))
			if name := p.names[f.Interval]; name != "" {
				_ = c.Text(name, p.anchorX[f.Interval], p.nameY, 0.5, 0.5, p.namePx, Fade(c.Theme.Foreground, weight))
			}
		}
	}

	if f.Label != NoLabel && f.Label >= 0 && f.Label < len(p.ticks) {
		if tk := p.ticks[f.Label]; tk.ok {
			if name := p.labelNames[f.Label]; name != "" {
				weight := clampWeight(f.LabelWeight)
				_ = c.Text(name, p.labelAnchorX[f.Label], p.labelNameY, 0.5, 0.5, p.labelNamePx, Fade(c.Theme.Foreground, weight))
			}
		}
	}
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
