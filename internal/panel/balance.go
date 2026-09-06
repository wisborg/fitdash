package panel

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/wisborg/fitactivity"
)

// BalancePanel draws one foot's share of some quantity -- ground contact
// time, impact loading rate, leg spring stiffness or vertical oscillation --
// as a centre-anchored bar against a FIXED symmetric scale: 50 (even) in the
// middle, +-balanceHalfRange either side. One responsibility, the same
// discipline Readout states for itself: this panel draws a bar and a
// magnitude, nothing else, so it can be placed independently of its three
// siblings and of everything else on the frame.
//
// One type, four constructors (ContactBalance, ImpactBalance,
// StiffnessBalance, OscillationBalance below), not a fourth GaugeStyle and
// not four separate types. GaugeStyle was rejected because the shape this
// panel draws -- a fill anchored at a FIXED centre -- is the exact one
// GaugeStyle's own doc comment (gauge.go) spends thirty lines explaining why
// it removed from the gauge track, and "--gauge-style dial" would then
// apply to a balance bar and mean nothing there. Four types were rejected
// because they would duplicate the placeholder branch, the off-scale
// chevron and the fixed-scale arithmetic four ways -- the very duplication
// this project is least willing to accept (see Readout's own doc comment
// for the identical argument, one metric family over).
//
// # Why a FILL is right here, even though the gauges rejected one
//
// GaugeStyle's own doc comment records three reasons a fill was wrong for
// heart rate/pace/power/cadence: it invoked quantity where pace's own
// number ran backwards against it, it collided with ClimbPanel's bars using
// the identical grammar for a different kind of value, and its absent wash
// was indistinguishable from a genuine low reading. None of the three
// applies here. There is no inverted metric: magnitude only ever grows
// away from the centre as it grows away from 50, in lockstep with the
// number beside it, so "fills more, reads bigger" is never a lie. It does
// not collide with ClimbPanel's own bars: those fill from the LEFT edge and
// only ever grow across the whole render, where this bar fills from a
// CENTRE tick in either direction and can shrink back toward it the next
// frame -- two visibly different behaviours, not one grammar reused for two
// meanings. And the confusability the gauges' fill risked at a low reading
// (a short fill anchored at the floor looking like the below-floor
// chevron, which lives at that identical spot) does not exist here either:
// this bar's chevron lives at the SPAN'S OWN ENDS, not at its centre, so a
// short fill near the centre and an off-scale chevron at an end are never
// close enough to confuse. The quantity a fill draws -- "how much
// asymmetry" -- is real and genuinely starts at zero at the centre tick,
// which is exactly the shape a fill is honest about.
//
// # The scale is a constant, and deliberately inverts the gauge ruling
//
// docs/architecture.md's gauge chapter derives HeartRate/Pace/Power/
// Cadence's own range from the activity, and labels both ends, because
// those four metrics have no natural anchor or width of their own -- a
// derived, LABELLED range is what makes that honest.
//
// Balance has both a natural anchor and a natural width: 50 is exact
// symmetry, a real zero for "how much asymmetry", and comparability is the
// entire point of drawing this at all -- across the three bars on screen at
// once, so three equal-length bars mean three equal asymmetries, and across
// every render this program ever produces, because balance is something a
// person tracks against their OWN past renders over months. A derived range
// would make the identical bar length mean a different real asymmetry in a
// different video, with nothing on screen saying so -- the exact dishonesty
// GradientPanel's own fixed amplification factor (docs/architecture.md) was
// chosen to avoid, one metric family over. So the scale here is
// balanceHalfRange, a literal constant, never derived from ctx.Track or
// from ctx.Report -- see that constant's own doc comment for what changes
// the day anyone makes it otherwise.
//
// Because the scale is a constant, it needs no endpoint labels, for the
// identical reason GaugeStyleDial's chrome is not what makes it honest and
// GradientPanel draws no dial at all: a number that never moves needs no
// caption repeating it every render. What keeps a constant scale honest
// instead is the UNEXAGGERATED number printed beside the bar -- see
// balanceReadingText -- which is why that number is never dropped even
// when the bar itself is pinned at an end.
//
// # Which side the bar names
//
// FIT's own stance_time_balance is conventionally read as the left foot's
// share, and the two Stryd fields were originally left unlabelled because
// this package had no documented convention it could confirm for them. That
// convention is now confirmed for all four: checked against a user's own
// activity summary (its own official left/right split for each of the four
// quantities, never reproduced here -- see CLAUDE.md), every one of
// ContactBalance, ImpactBalance, StiffnessBalance and OscillationBalance
// tracked that summary's LEFT-side figure, not the right's. So all four
// carry the LEFT foot's share, uniformly, and a reading above 50 means the
// LEFT side dominates -- balanceFillFraction is the one place that fact
// becomes a drawn direction (fill left, not right, for a positive
// deviation), and balanceReadingText is the one place it becomes a printed
// letter ("L" or "R") beside the magnitude.
//
// The letter is drawn in the VALUE row (Dynamic, per frame), never the unit
// row (Static, once): the side genuinely flips mid-activity -- whichever
// foot leads at one instant can trail at the next -- where the unit row is
// rasterized once at the very start of the render and never touched again.
// See Static's own doc comment for why, which restates Distance's identical
// trap with metres and kilometres.
//
// # Zero is not a balance
//
// All four source fields bottom out at a raw 0, and a 0% reading would
// mean the entire load sits on one foot -- physiologically implausible and,
// in every case checked, a device convention for "not computed yet"
// wearing the field's ordinary shape, exactly the judgement HeartRate's own
// doc comment (readout.go) makes about a strap that has not found skin
// contact yet. Refused in this panel's own value accessors below, the same
// place HeartRate refuses its own zero -- never upstream in fitactivity,
// whose presence flags exist for FIT's own SENTINEL values and treat a
// recorded 0 as a legal reading, and never by this panel changing what
// `fitdash inspect` reports: the field is present and its recorded minimum
// genuinely is 0, which is the honest answer to "what did the file
// record". This panel answers a different question -- "does that recording
// mean anything as a balance" -- which only a balance panel needs to ask.
//
// # Do not read f.Sample for the value
//
// internal/render/smooth.go (smoothSample) averages a developer field
// across its own smoothing window keyed purely by presence -- "the key
// exists in DevFields" -- with no notion that a recorded 0 for one of
// these four fields is not a real reading. On a wide gauge scale that is a
// latent nuisance; on this panel's narrow +-5 scale it is fatal, because a
// footpod's own first several seconds of "not computed yet" zeros form one
// contiguous run at the very start of the recording, and averaging even a
// few of them into an early smoothing window drags the reading far
// outside the scale for the whole of that window -- pinning the bar at one
// end through the opening of the video with nothing on screen explaining
// why.
//
// So this panel never reads f.Sample.DevFields or f.Sample.StanceTimeBalance
// at all. Its own series (balancePainter.series, a gaugeSeries -- see that
// type's own doc comment, gauge.go) is built once in Prepare, from
// ctx.Track's raw samples, through this panel's OWN zero-refusing value
// accessor -- so a refused zero is excluded from the average before it is
// ever taken, not averaged in and then argued with. Both the bar and the
// printed number are read back from that ONE series (gaugeSeries.At), never
// from two different sources that could disagree about which instants
// counted -- see Dynamic's own doc comment.
//
// # Static versus dynamic
//
// Static draws the caption, the full-length dim ghost track, the centre
// tick and the unit row ("%") -- every one of them fixed for the whole
// render, the ghost and tick because the scale itself never changes, the
// caption and unit because they name that scale rather than any one
// reading. Dynamic draws the live fill, the off-scale chevron and the
// magnitude, all per frame. No colour ramp: unlike the gauges' green-
// through-red track, this panel's ghost is flat Theme.Dim -- a colour ramp
// encodes MAGNITUDE along a track whose own two ends are a derived
// activity range (docs/architecture.md's gauge chapter); this track's two
// ends are a fixed +-5, so a ramp along it would tint the SAME percentage
// point a different colour in every render, exactly the comparability this
// panel exists to guarantee undone by its own chrome.
//
// # Scaling
//
// Every measurement here is a fraction of this panel's own box or of unit
// (min(box.W, box.H)), or of ctx.Fonts' own FitSize against that box --
// never a pixel constant. It has to read at 1080p, at 4K and in the
// portrait tree's own narrower box without a layout-specific branch in this
// file, the same contract every other panel in this package keeps.
//
// # Registration
//
// A Placement in whichever Layout wants one, same as every other panel --
// this file and its own tests are the whole of what adding it costs.
type BalancePanel struct {
	name  string
	label string
	value func(fitactivity.Sample) (float64, bool)
}

// newBalancePanel builds a BalancePanel from its own name, caption and
// zero-refusing value accessor. The one constructor body every exported
// constructor below shares, so the placeholder policy, the fixed scale and
// the drawing states are written once rather than four times.
func newBalancePanel(name, label string, value func(fitactivity.Sample) (float64, bool)) BalancePanel {
	return BalancePanel{name: name, label: label, value: value}
}

// ContactBalance reads Sample.StanceTimeBalance -- one foot's share of total
// ground contact time, the one native FIT field of the four (see
// fitactivity.Sample.StanceTimeBalance's own doc comment; the other three
// are Stryd developer fields, below).
//
// A recorded 0 is refused here, on top of fitactivity's own presence flag --
// see BalancePanel's own doc comment, "Zero is not a balance", for why that
// judgement belongs to this panel and not to fitactivity.
func ContactBalance() BalancePanel {
	return newBalancePanel("contact-balance", "GROUND CONTACT BALANCE", contactBalanceValue)
}

func contactBalanceValue(s fitactivity.Sample) (float64, bool) {
	return s.StanceTimeBalance, s.HasStanceTimeBalance && s.StanceTimeBalance != 0
}

// ImpactBalance reads the Stryd developer field
// fitactivity.StrydImpactLoadingRateBalanceField -- one foot's share of
// impact loading rate, a different quantity from ContactBalance's ground
// contact time (see that constant's own doc comment in fitactivity).
func ImpactBalance() BalancePanel {
	return newBalancePanel("impact-balance", "IMPACT LOADING RATE (ILR) BALANCE", devBalanceValue(fitactivity.StrydImpactLoadingRateBalanceField))
}

// StiffnessBalance reads the Stryd developer field
// fitactivity.StrydLegSpringStiffnessBalanceField -- one foot's share of leg
// spring stiffness.
func StiffnessBalance() BalancePanel {
	return newBalancePanel("stiffness-balance", "LEG SPRING STIFFNESS (LSS) BALANCE", devBalanceValue(fitactivity.StrydLegSpringStiffnessBalanceField))
}

// OscillationBalance reads the Stryd developer field
// fitactivity.StrydVerticalOscillationBalanceField -- one foot's share of
// vertical oscillation.
func OscillationBalance() BalancePanel {
	return newBalancePanel("oscillation-balance", "VERTICAL OSCILLATION BALANCE", devBalanceValue(fitactivity.StrydVerticalOscillationBalanceField))
}

// devBalanceValue builds a zero-refusing value accessor for a Stryd balance
// developer field named key. Presence is "the key exists in DevFields AND
// the recorded value is not 0" -- see BalancePanel's own doc comment, "Zero
// is not a balance", for why the second half of that conjunction belongs
// here rather than being left to fitactivity's own DevFields presence rule
// ("the key exists"), which has no notion of value at all.
func devBalanceValue(key string) func(fitactivity.Sample) (float64, bool) {
	return func(s fitactivity.Sample) (float64, bool) {
		v, ok := s.DevFields[key]
		return v, ok && v != 0
	}
}

// balanceHalfRange is the fixed half-width of every balance bar's own scale,
// in percentage points either side of the exact 50 midpoint -- +-5,
// identical for every one of the four bars and for every render fitdash
// ever produces. See BalancePanel's own doc comment, "The scale is a
// constant", for why it is not derived per activity the way the gauges'
// own range is.
//
// If this ever becomes derived instead of a literal constant, endpoint
// labels become MANDATORY that same day -- a derived, unlabelled scale is
// the exact dishonesty this panel exists to avoid, the day it stops being a
// constant it needs the gauges' own labelled-endpoint discipline
// (docs/architecture.md) to stay honest. And if 5 ever turns out too
// narrow for some activity's real range, the remedy is to widen THIS
// CONSTANT once, for every render fitdash will ever produce -- never to
// derive a per-activity width. A per-activity width is the "0 W is real,
// force the floor" mistake reached from the opposite direction: there it
// was a floor bent down to chase one reading, here it would be a scale
// stretched to chase one activity, and either way the same bar length
// would stop meaning the same thing twice in a row.
const balanceHalfRange = 5.0

// balanceSeriesMultiplier is BalancePanel's own multiplier for
// smoothingSeriesWindow (gauge.go) -- 1, the render's own base smoothing
// window UNCHANGED, the identical window the printed numbers elsewhere in
// this render already use. This is deliberately NOT
// gaugeMarkerSmoothingMultiplier: that multiplier exists because a gauge's
// axis is DERIVED from its own series and must not max out, so widening the
// window buys headroom the axis needs. A balance bar's axis
// (balanceHalfRange) is a fixed constant that no amount of smoothing could
// stretch or shrink -- widening the window here would only lag the reading
// behind the activity for no benefit the axis could ever use.
const balanceSeriesMultiplier = 1

// balanceSeriesWindow resolves this panel's own series window through the
// shared smoothingSeriesWindow helper (gauge.go) -- the render's own base
// window, floored, at balanceSeriesMultiplier.
func balanceSeriesWindow(ctx *Context) time.Duration {
	return smoothingSeriesWindow(ctx, balanceSeriesMultiplier)
}

// balanceDeviation is v - 50, signed: positive means the reading sits ABOVE
// the exact midpoint, negative BELOW it -- deliberately left in this raw,
// "above or below 50" shape rather than pre-mapped to a side, so the
// numeric result stays stable regardless of which physical foot either
// direction turns out to name. See BalancePanel's own doc comment, "Which
// side the bar names", for the two functions that DO turn this sign into a
// side -- balanceFillFraction for the drawn direction, balanceReadingText
// for the printed letter -- and for why both read this same raw sign rather
// than each encoding the mapping separately.
func balanceDeviation(v float64) float64 {
	return v - 50
}

// balanceMagnitude is the unsigned size of the deviation -- the number this
// panel prints (balanceReadingText) and, separately, clamped into
// balanceFraction, the distance the bar fills.
func balanceMagnitude(v float64) float64 {
	return math.Abs(balanceDeviation(v))
}

// balanceFraction maps a deviation to a fraction of balanceHalfRange, in
// [-1, 1] when the reading is on-scale and outside it when not -- NOT
// clamped here, deliberately, the same contract gaugeScale.fraction keeps:
// the caller (Dynamic) decides whether an out-of-range deviation is drawn
// clipped at the span's own end, and drawFill does the clamping only at the
// point it actually draws.
func balanceFraction(deviation float64) float64 {
	return deviation / balanceHalfRange
}

// balanceFillFraction turns balanceFraction's raw, "above or below 50"
// fraction into the fraction drawFill actually fills toward: NEGATED,
// because all four source fields carry the LEFT foot's share (see
// BalancePanel's own doc comment, "Which side the bar names") and drawFill's
// own convention is negative-fills-left. So a POSITIVE raw fraction --
// reading above 50, left dominant -- must fill LEFT, which is
// balanceFillFraction's negative output; a NEGATIVE raw fraction -- right
// dominant -- fills RIGHT.
//
// This is the one place that confirmed convention becomes a drawn pixel.
// Everywhere else in this file (balanceDeviation, balanceMagnitude,
// balanceFraction) stays in the raw, unmapped sign on purpose, so a future
// correction to the convention -- if one is ever needed -- touches this one
// function and balanceReadingText's own side letter, never the arithmetic
// both of them read from.
func balanceFillFraction(frac float64) float64 {
	return -frac
}

// balanceReadingText formats a signed deviation as its magnitude, to one
// decimal, plus a side letter -- "L" when the deviation is positive (reading
// above 50, left dominant; see BalancePanel's own doc comment, "Which side
// the bar names"), "R" when negative -- or "EVEN", with no letter at all,
// when the magnitude rounds to 0.0: a deviation too small to mean anything
// at one decimal's own precision reads as exact symmetry, not as a tiny
// signed number nobody could act on, and "EVEN" cannot sensibly favour
// either side.
func balanceReadingText(deviation float64) string {
	magnitude := math.Abs(deviation)
	if math.Round(magnitude*10) == 0 {
		return "EVEN"
	}
	side := "R"
	if deviation > 0 {
		side = "L"
	}
	return fmt.Sprintf("%.1f %s", magnitude, side)
}

// balanceValueTemplate is the widest string balanceReadingText ever prints:
// two integer digits, one decimal and a side letter ("50.0 L", the ceiling a
// magnitude can reach -- a deviation of 50 from a raw reading of 100, the
// largest percentage a refused-zero-excluded reading can be -- plus the
// space and letter every non-EVEN reading now carries). "EVEN" is checked
// alongside it in Prepare (below) since a font's own letterforms need not
// be exactly as wide as its digits at the same nominal size.
const balanceValueTemplate = "50.0 L"

// Name identifies the panel -- the same string a future --panels flag would
// select it by (see docs/architecture.md).
func (p BalancePanel) Name() string { return p.name }

// Accepts declines when the activity carries no usable reading of this
// balance at all -- walking ctx.Track through p.value itself, NEVER through
// ctx.Report.
//
// This needs its own carries rule for HeartRate's reason, not Power's:
// Power needs one because the coverage report's single "Power" row cannot
// say which of two SENSORS a render selected. HeartRate has the OTHER
// reason -- a report counts a metric present by NAME, regardless of
// whether every recorded value is one this panel's own accessor refuses --
// but HeartRate does not act on it today, so a device that never once
// found skin contact still shows a heart-rate panel that draws nothing but
// placeholders for the whole render. Repeating that gap here would be
// worse, not merely identical: an activity whose every Stryd balance
// reading is a refused "not computed yet" 0 reads as carried by the report
// (the DevFields key exists), and this panel would be PLACED, taking a box
// away from a sibling that has something to draw in it, for a render that
// never shows a single reading. Walking p.value directly is what makes the
// rule that PLACES this panel agree, by construction, with the rule that
// DRAWS a number in it -- the same guarantee Power's own carries gives,
// reached here for the reading-refusal reason rather than the
// which-sensor one.
func (p BalancePanel) Accepts(ctx *Context) bool {
	if ctx == nil || ctx.Track == nil {
		return false
	}
	for _, s := range ctx.Track.Samples {
		if _, ok := p.value(s); ok {
			return true
		}
	}
	return false
}

// balanceCaptionFraction is the caption's own nominal font size, as a
// fraction of unit (min(box.W, box.H)) -- the starting point FitSize then
// only ever shrinks from, the same fraction Readout's own label uses
// (readout.go's Prepare) so a balance panel's caption reads at a comparable
// size to a Readout's beside it.
const balanceCaptionFraction = 0.14

// balanceCaptionRowFraction is how much of the box's own height the caption
// reserves off the top, before the bar row gets the rest -- a judgement
// call, checked at 1080p, 4K and in the portrait tree: tall enough that the
// caption never crowds the bar beneath it, restrained enough that the bar
// row -- this panel's whole reason for existing -- keeps most of the box.
const balanceCaptionRowFraction = 0.30

// balanceReadingWidthFraction is the reading column's own reserved width,
// as a fraction of the bar row's own width -- ClimbPanel's
// climbReadingWidthFraction (climb.go) restated for this panel's own single
// column. It sizes the column; it does not place it at the row's far edge --
// see Prepare for why readingX is anchored one balanceGapFraction past
// where the track's OWN span ends, the identical "the reading is the remainder-
// taker's own neighbour, not the box's far edge" lesson climb.go's own
// Prepare records.
const balanceReadingWidthFraction = 0.22

// balanceGapFraction is the daylight between the bar's own reserved column
// and the reading column, as a fraction of the bar row's own width.
const balanceGapFraction = 0.03

// balanceBarHeightFraction is the bar's own thickness, as a fraction of the
// bar row's own height -- ClimbPanel's climbBarHeightFraction restated for
// this panel's single row: thick enough to read as a fill, restrained
// enough to leave the centre tick room to visibly poke past it.
const balanceBarHeightFraction = 0.38

// balanceTickOverhangFraction is how far the centre tick pokes past the
// bar's own top and bottom edges, as a fraction of the bar row's own
// height -- what makes the tick read as a distinct landmark rather than a
// slightly brighter patch of the bar it sits on.
const balanceTickOverhangFraction = 0.14

// balanceCapWidthFraction and balanceCapHalfHeightFraction size the reused
// off-scale chevron (drawGaugeCap, gauge.go) off the bar's OWN thickness --
// the one dimension guaranteed to exist wherever this panel is placed,
// mirroring gaugeCapWidthFraction's identical reasoning (readout.go) for
// why the gauges size their own chevron off their instrument band rather
// than off the box.
const (
	balanceCapWidthFraction      = 0.9
	balanceCapHalfHeightFraction = 0.85
)

// Prepare resolves the series and every geometry field once, from ctx and
// box alone -- Dynamic reads them back and never recomputes or widens
// anything, the same discipline gaugeScale's own doc comment (gauge.go)
// states for the gauges' derived range.
func (p BalancePanel) Prepare(ctx *Context, box Box) Painter {
	bp := &balancePainter{BalancePanel: p, box: box}
	if ctx == nil || ctx.Track == nil {
		// Accepts should have prevented this -- drawing nothing is the
		// least-wrong option left, mirroring ClimbPanel's own identical
		// defensive guard (climb.go).
		return bp
	}
	bp.series = buildGaugeSeries(ctx.Track, balanceSeriesWindow(ctx), p.value)

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	// bp.captionX defaults to the whole box's own centre -- the box-wide
	// fallback used only if the bar row below turns out too degenerate to
	// resolve a bar column at all (barBox or barColW <= 0, just below).
	// Once a bar column DOES resolve, this is overwritten with that column's
	// own centre, which is what task 4 (centre the caption over the bar, not
	// the panel) actually asks for -- see the overwrite below.
	bp.captionX = box.X + box.W/2

	captionRowH := box.H * balanceCaptionRowFraction
	bp.captionY = box.Y + captionRowH/2
	bp.captionPx = unit * balanceCaptionFraction

	barBox := Box{X: box.X, Y: box.Y + captionRowH, W: box.W, H: box.H - captionRowH}
	if barBox.H <= 0 || barBox.W <= 0 {
		if ctx.Fonts != nil {
			if px, err := ctx.Fonts.FitSize(p.label, box.W*0.92, bp.captionPx); err == nil {
				bp.captionPx = px
			}
		}
		return bp
	}

	readingW := barBox.W * balanceReadingWidthFraction
	gap := barBox.W * balanceGapFraction
	barColW := barBox.W - readingW - gap
	if barColW <= 0 {
		if ctx.Fonts != nil {
			if px, err := ctx.Fonts.FitSize(p.label, box.W*0.92, bp.captionPx); err == nil {
				bp.captionPx = px
			}
		}
		return bp
	}

	// The caption centres on the BAR COLUMN's own centre, not the box's --
	// task 4's fix for a caption that used to sit visibly right of its own
	// bar, pulled there by the reading column at the box's own right edge.
	// barColW's own two ends are symmetric about this point (capW is added
	// and then subtracted twice below, spanX := barBox.X+capW, spanW :=
	// barColW-2*capW), so this is the identical point the drawn SPAN itself
	// centres on -- centring on the column and centring on the span are the
	// same x, computed here before capW exists yet rather than duplicated
	// after it does.
	bp.captionX = barBox.X + barColW/2
	// Fitted against the BAR COLUMN's own width, not the whole box's: sizing
	// against box.W (as the caption used to, when it was centred on the box)
	// would let a caption this wide overhang past the box's own left edge
	// now that its anchor has moved left, off-centre from the box, to sit
	// over the narrower bar column instead.
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize(p.label, barColW*0.92, bp.captionPx); err == nil {
			bp.captionPx = px
		}
	}

	bp.trackY = barBox.Y + barBox.H/2
	bp.trackH = math.Max(2, barBox.H*balanceBarHeightFraction)
	bp.tickHalfH = bp.trackH/2 + barBox.H*balanceTickOverhangFraction

	bp.capW = bp.trackH * balanceCapWidthFraction
	bp.capHalfH = bp.trackH * balanceCapHalfHeightFraction

	spanX := barBox.X + bp.capW
	spanW := barColW - 2*bp.capW
	if spanW <= 0 {
		return bp
	}
	bp.spanX = spanX
	bp.spanW = spanW
	bp.hasBar = true

	bp.readingX = barBox.X + barColW + gap

	centerY := barBox.Y + barBox.H/2
	bp.readingValueY = centerY - unit*0.14
	bp.readingUnitY = centerY + unit*0.22
	bp.readingValuePx = unit * 0.30
	// The unit row is sized at a LARGER fraction of the box than a Readout's
	// is, and the difference is not an inconsistency to be tidied away. A
	// Readout owns a box roughly a quarter of the gauge column's height; a
	// balance bar is one horizontal element and its box is a fraction of
	// that, so the same fraction that reads comfortably there resolved to
	// single-digit pixels here and the "%" was decoration nobody could read.
	// A unit nobody can read is a hole: the reading is a percentage-point
	// deviation, and stripped of its unit "3.8" does not say what it is.
	bp.readingUnitPx = unit * 0.20
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize(balanceValueTemplate, readingW*0.92, bp.readingValuePx); err == nil {
			bp.readingValuePx = px
		}
		if px, err := ctx.Fonts.FitSize(ReadoutPlaceholder, readingW*0.92, bp.readingValuePx); err == nil {
			bp.readingValuePx = px
		}
		if px, err := ctx.Fonts.FitSize("EVEN", readingW*0.92, bp.readingValuePx); err == nil {
			bp.readingValuePx = px
		}
		if px, err := ctx.Fonts.FitSize("%", readingW*0.92, bp.readingUnitPx); err == nil {
			bp.readingUnitPx = px
		}
	}

	return bp
}

type balancePainter struct {
	BalancePanel
	box Box

	// series is built once in Prepare, through p.value -- see BalancePanel's
	// own doc comment, "Do not read f.Sample for the value", for why both
	// the bar and the printed number are read back from THIS, never from
	// f.Sample directly.
	series gaugeSeries

	captionY, captionPx float64

	// captionX is the caption's own anchor -- the bar COLUMN's centre once
	// one resolves, the whole box's centre as a fallback otherwise. See
	// Prepare's own comment on this field for why it is not simply the box's
	// centre the way an earlier version of this panel drew it.
	captionX float64

	// hasBar is false when the box (or its bar row) was too degenerate to
	// draw a track in at all -- the identical "a box too narrow to draw in
	// is the same nothing-honest-to-show as a range too narrow to compute"
	// case reserveGaugeStrip's own doc comment (readout.go) names. Static
	// and Dynamic both check it before touching any of the geometry below,
	// which is otherwise left at its zero value.
	hasBar bool

	// spanX, spanW, trackY, trackH, capW, capHalfH are named identically to
	// readoutPainter's own gauge fields (readout.go) and mean the same
	// things, because drawGaugeCap (gauge.go) is shared verbatim between the
	// two: spanX/spanW bound the bar's own travel span (where fraction -1
	// sits at spanX and +1 at spanX+spanW, the centre tick at the
	// midpoint), trackY/trackH the bar's own vertical centre and thickness,
	// capW/capHalfH the reused chevron's size.
	spanX, spanW, trackY, trackH float64
	capW, capHalfH               float64

	// tickHalfH is the centre tick's own half-height, taller than trackH/2
	// so it visibly pokes past the bar (balanceTickOverhangFraction).
	tickHalfH float64

	readingX                      float64
	readingValueY, readingUnitY   float64
	readingValuePx, readingUnitPx float64
}

// drawTrack draws the bar's own full-length rectangle -- the travel span,
// spanX to spanX+spanW -- in col. Shared by the dim ghost (Static) and the
// absent wash (Dynamic) so the two are pixel-identical rectangles differing
// only in colour, the identical "one shape, two colours" discipline
// ClimbPanel's own drawBar and the gauges' own drawTrack (gauge.go) both
// keep for the same reason: two independently drawn rectangles could
// silently disagree about the row's own geometry, and one shared shape
// cannot.
func (p *balancePainter) drawTrack(c *Canvas, col color.Color) {
	if !p.hasBar {
		return
	}
	c.Rect(Box{X: p.spanX, Y: p.trackY - p.trackH/2, W: p.spanW, H: p.trackH}, col)
}

// drawFill fills from the centre tick outward toward frac's own sign,
// clamped to [-1, 1] here -- this is the ONE place an off-scale frac is
// actually clipped to the span's own end; balanceFraction itself returns
// the unclamped value so Dynamic can still tell an off-scale reading apart
// from an on-scale one before this clips it for drawing.
func (p *balancePainter) drawFill(c *Canvas, frac float64, col color.Color) {
	if !p.hasBar {
		return
	}
	if frac > 1 {
		frac = 1
	} else if frac < -1 {
		frac = -1
	}
	half := p.spanW / 2
	center := p.spanX + half
	w := math.Abs(frac) * half
	if w <= 0 {
		return
	}
	x := center
	if frac < 0 {
		x = center - w
	}
	c.Rect(Box{X: x, Y: p.trackY - p.trackH/2, W: w, H: p.trackH}, col)
}

// Static draws the caption, the dim ghost track, the centre tick and the
// unit row -- every one fixed for the whole render because the scale
// itself (balanceHalfRange) never changes and the caption/unit name that
// scale rather than any one reading.
//
// The unit row ("%") is drawn HERE, once, deliberately -- and this is the
// one thing about this panel a future change must not get backwards. The
// reading's own side letter (see BalancePanel's own doc comment, "Which
// side the bar names") is drawn in the VALUE row (Dynamic, below), fresh
// every frame, and never folded into this unit row: the side genuinely
// flips mid-activity (whichever foot leads at one instant can trail at the
// next), where this row is rasterized once at the very start of the render
// and never touched again. Distance's own doc comment (readout.go) records
// the identical trap with metres and kilometres -- a unit drawn once that
// later stops matching the value drawn every frame over it, both layers
// correct when they were each drawn, disagreeing by the time a viewer sees
// them together.
func (p *balancePainter) Static(c *Canvas) {
	_ = c.Text(p.label, p.captionX, p.captionY, 0.5, 0.5, p.captionPx, c.Theme.Dim)
	if !p.hasBar {
		return
	}
	p.drawTrack(c, c.Theme.Dim)

	tickW := math.Max(1, p.trackH*0.12)
	cx := p.spanX + p.spanW/2
	c.Rect(Box{X: cx - tickW/2, Y: p.trackY - p.tickHalfH, W: tickW, H: p.tickHalfH * 2}, c.Theme.Dim)

	_ = c.Text("%", p.readingX, p.readingUnitY, 0, 0.5, p.readingUnitPx, c.Theme.Dim)
}

// Dynamic draws the live fill, the off-scale chevron and the magnitude --
// every one read from p.series, NEVER from f.Sample -- exactly once per
// frame, over Static's own chrome.
//
// Four states this panel's own doc comment enumerates; the fourth
// ("activity carries none at all") never reaches here at all, since Accepts
// declined the whole panel before any box existed:
//
//   - absent this instant (p.series.At refuses -- before the series' own
//     first point, after its last, inside a genuine recording gap wider
//     than fitactivity.DefaultMaxGap, or a stretch where buildGaugeSeries'
//     own window caught nothing this panel's accessor would count present):
//     NO fill at all -- a zero-length fill sits exactly on the centre tick
//     and reads as perfectly even, which is the confident lie in
//     geometric form -- plus the track washed in Fade(Theme.Absent,
//     gaugeAbsentWashAlpha(theme)), the identical derivation the gauges use
//     for the identical reason: this wash composites onto the bar's own
//     flat Theme.Dim ghost (drawTrack, Static), never directly onto
//     Theme.Background, so it must be solved against what it is actually
//     drawn over (see gaugeAbsentWashAlpha's own doc comment, gauge.go).
//   - present, past +-balanceHalfRange: the fill clips at the span's own
//     end (drawFill's own clamp) plus the reused off-scale chevron
//     (drawGaugeCap) at that end, and the printed magnitude and side are the
//     TRUE, unclipped deviation's -- never the clamped fraction the bar
//     itself stops at.
//   - present, on scale: the fill reaches exactly the fraction the
//     deviation implies, no chevron, and the printed reading is that same
//     on-scale deviation's magnitude and side -- or "EVEN", with no side at
//     all, where the magnitude rounds to 0.0 (balanceReadingText).
//
// fillFrac -- balanceFillFraction(frac), NOT frac itself -- is what actually
// decides which end the chevron draws at and which way drawFill fills: see
// balanceFillFraction's own doc comment for why the raw, "above or below 50"
// frac has to be negated before it drives a drawn direction. The printed
// text reads deviation directly, never fillFrac or frac: balanceReadingText
// derives its own side from deviation's sign using the identical convention,
// so the fill's direction and the letter beside the reading can never
// disagree about which side a positive deviation names.
func (p *balancePainter) Dynamic(c *Canvas, f Frame) {
	if !p.hasBar {
		return
	}
	v, ok := p.series.At(f.At)
	if !ok {
		p.drawTrack(c, Fade(c.Theme.Absent, gaugeAbsentWashAlpha(c.Theme)))
		_ = c.Text(ReadoutPlaceholder, p.readingX, p.readingValueY, 0, 0.5, p.readingValuePx, c.Theme.Absent)
		return
	}

	deviation := balanceDeviation(v)
	frac := balanceFraction(deviation)
	fillFrac := balanceFillFraction(frac)

	switch {
	case fillFrac < -1:
		drawGaugeCap(c, p.spanX, p.spanW, p.trackY, p.capW, p.capHalfH, false, c.Theme.Foreground)
	case fillFrac > 1:
		drawGaugeCap(c, p.spanX, p.spanW, p.trackY, p.capW, p.capHalfH, true, c.Theme.Foreground)
	}
	p.drawFill(c, fillFrac, c.Theme.Foreground)

	_ = c.Text(balanceReadingText(deviation), p.readingX, p.readingValueY, 0, 0.5, p.readingValuePx, c.Theme.Foreground)
}
