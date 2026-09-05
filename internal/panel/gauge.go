package panel

import (
	"fmt"
	"image/color"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wisborg/fitactivity"
)

// GaugeStyle selects how Readout draws a fluctuating reading -- heart rate,
// pace, power, cadence, the four metrics that go up and down across an
// activity rather than only ever increasing. GaugeStylePlain is today's
// three centred rows (label, value, unit) and nothing else. GaugeStyleTrack
// adds a scale track beneath them: the activity's own robust range, snapped
// outward to round numbers, with both ends labelled, and a marker -- a dot,
// never a fill -- that moves along it to the current reading's position.
// GaugeStyleDial is a second, permanent shape for the identical scale -- a
// semicircular arc and a needle in place of the track and its dot, laid out
// BESIDE the caption/value/unit rather than beneath them (see
// reserveGaugeDial, readout.go, for why beside and not below, and for the
// geometry). Both GaugeStyleTrack and GaugeStyleDial are kept: this project
// does not prefer one shape over the other, and a user picks between them
// with --gauge-style.
//
// It is a MARKER, not a bar, on purpose: a visual gate rejected an earlier
// version that filled the track from the floor to the value, the way
// ClimbPanel's own cumulative bars do, for two reasons that both trace back
// to "fill" meaning "quantity". Pace sweeps this track on SPEED while its
// printed label reads PACE (see paceGaugeScale below), so a growing fill
// contradicted the number beside it -- a slower runner (smaller pace figure
// wanted, larger speed) filled MORE of the track, at an ordinary effort
// looking like the weakest reading on the dashboard. And a fill that starts
// at the floor is, at a low reading, only a few pixels different from the
// below-floor overflow chevron that lives at the identical left end (see
// gaugeCap) -- the two were confusable exactly where confusing them matters
// most. A position on a labelled axis makes neither claim: it says "the
// reading is here", not "the reading is this much of this track", so the
// pace inversion stops being a misreading and the chevron collision stops
// being possible (see drawGauge's own doc comment for how "no marker at
// all" replaced "a fill that happens to be short").
//
// The labels are the price of a derived scale, not decoration -- a scale
// nobody can read is the dishonesty a fixed-amplification gauge (see
// GradientPanel's own doc comment on why ITS scale is a constant rather than
// derived) exists to avoid, and drawing the endpoints is what earns the
// derived scale here.
type GaugeStyle int

const (
	// GaugeStylePlain is the zero value, so a Context nobody set GaugeStyle
	// on renders exactly as it always has -- every existing test and caller
	// is unaffected.
	GaugeStylePlain GaugeStyle = iota

	// GaugeStyleTrack draws the scale track described on GaugeStyle's own
	// doc comment, for every Readout that carries a scale rule (see
	// Readout.scale) and can resolve a usable range for this activity.
	GaugeStyleTrack

	// GaugeStyleDial is the speedometer-style alternative to
	// GaugeStyleTrack, kept as a permanent second option rather than a
	// throwaway comparison: an earlier version of this constant was built
	// only to put the shape in front of the user for a side-by-side look,
	// and that framing no longer applies now that both shapes are shipped.
	//
	// It spans the IDENTICAL gaugeScale a Readout's own scale rule resolves
	// for GaugeStyleTrack -- same smoothed-series range, same snapping, same
	// per-metric rules (robustGaugeScale, paceGaugeScale) -- so the two styles are a
	// like-for-like comparison of SHAPE alone, never of two different
	// ranges. A semicircular arc replaces the horizontal axis, and a needle
	// from the arc's centre to the current value's angle replaces the
	// moving dot; the two endpoint labels sit at the arc's own ends exactly
	// as they sit at the track's. Absence and off-scale keep GaugeStyleTrack's
	// policies verbatim: an absent reading draws no needle AND washes the
	// arc (see readoutPainter.Dynamic), and an off-scale reading stops at
	// the arc's own end and reuses gaugeCap's chevron there -- the arc's two
	// ends sit at the pivot's own height, exactly like the track's own left
	// and right edges, so there is no dial-specific overflow shape to
	// invent.
	GaugeStyleDial
)

// GaugeStyleNamePlain and GaugeStyleNameTrack name GaugeStylePlain and
// GaugeStyleTrack for the --gauge-style flag, in the exact spelling
// SelectGaugeStyle accepts. Exported, the same way LayoutAuto and
// BottomBandProfile/BottomBandDistance are, so the flag's own default value
// and SelectGaugeStyle's accepted set are read off ONE pair of strings rather
// than the CLI restating them as literals a rename here could silently stop
// matching.
const (
	GaugeStyleNamePlain = "plain"
	GaugeStyleNameTrack = "track"

	// GaugeStyleNameDial names GaugeStyleDial -- see that type's doc
	// comment. Behind the same flag, and the same SelectGaugeStyle
	// refusal-of-typos discipline, as the other two values: a third
	// permanent choice, not a hidden or experimental one.
	GaugeStyleNameDial = "dial"
)

// gaugeStyleNames pairs each --gauge-style value with its GaugeStyle, in the
// order the flag's help text and SelectGaugeStyle's own error both list them.
var gaugeStyleNames = []struct {
	name  string
	style GaugeStyle
}{
	{GaugeStyleNamePlain, GaugeStylePlain},
	{GaugeStyleNameTrack, GaugeStyleTrack},
	{GaugeStyleNameDial, GaugeStyleDial},
}

// SelectGaugeStyle returns the named GaugeStyle.
//
// An unknown name is refused rather than falling back to GaugeStylePlain --
// the same discipline SelectTheme and SelectLayout apply to their own flags:
// a typo should not silently render the plain style nobody asked to keep, on
// a render that takes long enough that finding out at the end is a real cost.
func SelectGaugeStyle(name string) (GaugeStyle, error) {
	for _, g := range gaugeStyleNames {
		if g.name == name {
			return g.style, nil
		}
	}
	names := make([]string, len(gaugeStyleNames))
	for i, g := range gaugeStyleNames {
		names[i] = g.name
	}
	return GaugeStylePlain, fmt.Errorf("panel: unknown gauge style %q; use %s", name, strings.Join(names, " or "))
}

// GaugeStyleName is SelectGaugeStyle's own inverse: it names style in the
// exact spelling SelectGaugeStyle accepts.
//
// The render summary (writeGaugeSummary, cmd/render.go) uses this to report
// which style actually drew, rather than a second switch statement in cmd
// that could name a style differently from how SelectGaugeStyle itself
// spells it. An unrecognised style (unreachable in practice -- GaugeStyle
// has exactly the three values gaugeStyleNames lists) names as
// GaugeStyleNamePlain rather than panicking or returning an empty string, on
// the same "least-wrong option" reasoning ClimbPanel's own defensive Prepare
// guard states explicitly (climb.go).
func GaugeStyleName(style GaugeStyle) string {
	for _, g := range gaugeStyleNames {
		if g.style == style {
			return g.name
		}
	}
	return GaugeStyleNamePlain
}

// gaugeScale is a resolved axis range, in the SAME space the readout's own
// bound value() and format() work in -- speed (m/s) for pace, never pace
// itself, because Pace's own format already takes a speed and renders it as
// pace text (FormatPace). Keeping the scale in that space is what lets
// Dynamic, the endpoint labels and the out-of-range check share one fraction
// and one format call across every metric, with no metric-specific branch
// anywhere outside this file.
//
// Computed exactly ONCE, in Prepare, through the readout's own bound value
// accessor -- never through ctx.Report (see Readout.scale's own doc comment
// for the three concrete places the two spaces diverge) -- and never
// re-derived or widened afterward. Dynamic reads p.scale; it must never
// build a new one. The tempting variant -- "the scale is the maximum seen so
// far, so the bar always uses its full range" -- is this project's
// axis-origin bug rebuilt: the endpoint labels rasterized once in Static
// would be right for frame 0 and wrong for every frame after it, both
// layers would draw, they would disagree, and nothing would error.
type gaugeScale struct {
	floor, ceiling float64
}

// fraction maps v into [0,1] of the scale, NOT clamped -- callers (drawGauge,
// below) decide whether an out-of-range v is drawn clipped.
func (s gaugeScale) fraction(v float64) float64 {
	return (v - s.floor) / (s.ceiling - s.floor)
}

// gaugeSeries is one readout's own gauge-MARKER series: value(), extra-
// smoothed over a window far wider than render.smoothSample already applies
// to the printed number (see gaugeMarkerWindow), evaluated once per NATIVE
// track sample rather than once per rendered frame. See buildGaugeSeries
// for how it is built and gaugeSeries.At for how a per-frame query
// (Dynamic, keyed by f.At -- see readoutPainter.series) is answered from
// it.
//
// # Why the marker gets its OWN series, more smoothed than the number
//
// The user complaint this exists to fix was that the track/dial marker
// "maxes out" -- sits pinned at one end of its own axis more than a
// fluctuating reading should. Read literally, "span the raw min/max" fixes
// the symptom by construction (the marker can never exceed an axis built
// from its own extremes) but reintroduces exactly the failure the
// predecessor of this design (quantile clipping, gaugeQuantileLow/High) was
// built to avoid: a single power spike sits at roughly double the sustained
// reading, and a single stopped sample (a red light, a knot retied) puts a
// raw cadence floor at zero -- either one dominates an axis built from the
// literal extremes, and the marker spends the rest of the activity crushed
// into a sliver near the OTHER end, further from the middle than before.
//
// The fix is not a different clip, it is a different SERIES. Extra-smooth
// the reading the marker itself is driven by (gaugeMarkerWindow, far wider
// than the number's own --smoothing), and THEN take that series' own
// literal range -- no clipping needed, because the smoothing already did a
// clip's job honestly: a single spike or a brief stop is averaged into its
// surroundings before the range is ever measured, rather than trimmed away
// after the fact by a percentile that has no idea whether a given reading
// was noise or the fastest kilometre of the run. The marker then touches
// each end of ITS OWN axis at most once across the whole render and can
// never max out, while the axis itself stays honestly tied to what the
// activity actually did.
//
// # What the number keeps, and what it gives up
//
// The printed number is untouched: still driven by f.Sample, subject only
// to whatever --smoothing already resolves, never the extra pass this type
// applies. That is deliberate -- see change 4's own framing in the commit
// this type landed in -- and the consequence is that the number can now
// legitimately read a value the marker's own axis does not contain, or a
// position visibly different from the marker's. That divergence is not a
// bug: it is "the raw reading went past what the smoothed marker shows" (a
// real event -- see drawGauge's chevron branch) or "the smoothed trend
// lags the instant" (a real relationship between a mean and its own most
// recent input), and it is why the marker and the number are drawn in
// different colours -- see gaugeRampColor -- as much as different
// positions: nothing here claims the two are the same reading.
type gaugeSeries struct {
	times  []time.Time
	values []float64

	// ok[i] is whether values[i] is a genuine average -- at least one
	// present reading fell inside the window centred on times[i] -- rather
	// than the zero value standing in for one. A track with no samples at
	// all, or a value accessor that finds nothing present anywhere, is the
	// zero gaugeSeries: every slice nil, Range and At both correctly answer
	// "nothing here" without a separate empty-series branch.
	ok []bool
}

// buildGaugeSeries extra-smooths value across track's own samples,
// evaluated once per NATIVE sample time -- never once per rendered frame,
// and never through Context.Timeline -- centred windows of the given
// width, exactly the shape render.smoothSample already applies to the
// printed number (see that function's own doc comment), independently
// resolved here and ordinarily much wider (gaugeMarkerWindow).
//
// A window that catches no PRESENT reading around a given sample leaves
// that point's own ok false, mirroring smoothSample's identical policy:
// never invent a reading standing in for absence. window <= 0, a nil
// track, or an empty one all return the zero gaugeSeries.
func buildGaugeSeries(track *fitactivity.Track, window time.Duration, value func(fitactivity.Sample) (float64, bool)) gaugeSeries {
	if track == nil || len(track.Samples) == 0 || window <= 0 {
		return gaugeSeries{}
	}
	half := window / 2
	s := gaugeSeries{
		times:  make([]time.Time, len(track.Samples)),
		values: make([]float64, len(track.Samples)),
		ok:     make([]bool, len(track.Samples)),
	}
	for i, sample := range track.Samples {
		t := sample.Time
		s.times[i] = t
		var sum float64
		var n int
		for _, x := range track.Window(t.Add(-half), t.Add(half)) {
			if v, ok := value(x); ok {
				sum += v
				n++
			}
		}
		if n > 0 {
			s.values[i] = sum / float64(n)
			s.ok[i] = true
		}
	}
	return s
}

// gaugeSeriesMinPresent is the fewest ok points buildGaugeSeries must
// produce before Range answers at all -- this type's own analogue of
// inspect.minQuantileSamples, which used to gate robustGaugeScale and
// paceGaugeScale directly, back when they asked inspect.Quantiles for a
// range rather than a gaugeSeries for one. Kept at the identical figure and
// for the identical reasoning: below it there is no trend to speak of, and
// a "range" computed from a handful of points would look exactly as
// confident as one backed by real coverage, but fitdash targets short
// (30-60s) renders of activities that can themselves be brief, so the
// floor stays deliberately small rather than refusing a gauge to nearly
// every short activity.
const gaugeSeriesMinPresent = 10

// Range reports the literal minimum and maximum of every ok point in s --
// no quantile trimming, unlike the range this type's own predecessor took
// from inspect.Quantiles. See gaugeSeries' own doc comment for why a
// LITERAL range is safe to take now: buildGaugeSeries's own smoothing is
// what keeps a single spike or stop from dominating an end of it, which is
// the job quantile clipping used to do less honestly. ok is false below
// gaugeSeriesMinPresent genuine points.
func (s gaugeSeries) Range() (lo, hi float64, ok bool) {
	n := 0
	for i, v := range s.values {
		if !s.ok[i] {
			continue
		}
		if n == 0 {
			lo, hi = v, v
		} else {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		n++
	}
	return lo, hi, n >= gaugeSeriesMinPresent
}

// At reports s's own value at instant t, for a Dynamic that is called with
// an arbitrary f.At rather than one of s's own point times.
//
// It interpolates between the two bracketing points the same way
// fitactivity.Track.At interpolates between two bracketing SAMPLES --
// deliberately, since s's own points sit at exactly the track's native
// sample times (buildGaugeSeries), so the identical gap policy applies: t
// before the first point or after the last -- false, no extrapolation; t
// exactly at a point -- that point's own value and ok, verbatim; t between
// two points -- linear interpolation, but ONLY when BOTH bracketing points
// are themselves ok and no more than fitactivity.DefaultMaxGap apart.
// Otherwise this is a real recording gap (or a stretch where neither
// bracketing point had a reading to average, which is the identical fact
// one step removed) and At refuses rather than draw a straight line across
// it -- an absent instant must still draw the absent state, never a marker
// interpolated across the hole.
//
// Unlike Track.AtWithGap, this does not snap to the nearer neighbour when t
// falls inside a gap wider than DefaultMaxGap. Refusing outright is always
// the honest answer for a marker -- a snap-to-nearest here would draw a
// marker whose position quietly stopped moving partway through the gap,
// which looks exactly like a live reading holding steady.
func (s gaugeSeries) At(t time.Time) (float64, bool) {
	n := len(s.times)
	if n == 0 {
		return 0, false
	}
	i := sort.Search(n, func(i int) bool { return !s.times[i].Before(t) })
	if i < n && s.times[i].Equal(t) {
		return s.values[i], s.ok[i]
	}
	if i == 0 || i == n {
		return 0, false
	}
	lo, hi := i-1, i
	if !s.ok[lo] || !s.ok[hi] {
		return 0, false
	}
	gap := s.times[hi].Sub(s.times[lo])
	if gap > fitactivity.DefaultMaxGap {
		return 0, false
	}
	frac := float64(t.Sub(s.times[lo])) / float64(gap)
	return s.values[lo] + (s.values[hi]-s.values[lo])*frac, true
}

// gaugeMarkerSmoothingMultiplier is how much MORE the gauge marker's own
// series (gaugeSeries) is smoothed than the printed number already is by
// render.smoothSample -- see gaugeMarkerWindow. A judgement call, in the
// same voice the quantile pair this design replaced was: large enough that
// a single spike or a brief stop sits well inside the averaging window
// rather than near its edge, small enough that the marker still visibly
// tracks the SAME trend the number does rather than lagging it into
// looking unrelated. Four times render/smooth.go's own resolved window,
// checked against a real activity for "calms the marker without the two
// visibly parting ways" -- the figures behind that check are deliberately
// not repeated here, see CLAUDE.md.
const gaugeMarkerSmoothingMultiplier = 4

// gaugeMarkerWindow resolves the marker's own extra-smoothing window for
// this render: gaugeMarkerSmoothingMultiplier times the render-wide BASE
// smoothing window render.smoothSample already applies to f.Sample (see
// Context.Smoothing) -- the render-wide base, not the per-frame WindowAt a
// highlight's own rate can vary, because this series is built ONCE for the
// whole render rather than re-resolved per frame (see
// Timeline.AutoSmoothingBase's own doc comment for why a render-wide
// figure is the right one for exactly this kind of once-per-render use).
//
// The base-window arithmetic itself -- resolve, floor, multiply -- lives in
// smoothingSeriesWindow, shared with BalancePanel's own series (balance.go),
// which needs the IDENTICAL base but a multiplier of 1 rather than
// gaugeMarkerSmoothingMultiplier: the gauge marker's axis is DERIVED from
// its own series and must not max out, which is what earns it a wider
// multiple, while a balance bar's axis is a fixed constant that cannot be
// stretched by smoothing at all -- see BalancePanel's own doc comment for
// why widening its window further would buy nothing.
func gaugeMarkerWindow(ctx *Context) time.Duration {
	return smoothingSeriesWindow(ctx, gaugeMarkerSmoothingMultiplier)
}

// smoothingSeriesWindow resolves this render's own base smoothing window --
// the render-wide base render.smoothSample applies to f.Sample, floored at
// minSmoothingWindow -- times multiplier, for a buildGaugeSeries caller that
// wants some multiple of it. Shared by gaugeMarkerWindow (multiplier
// gaugeMarkerSmoothingMultiplier) and BalancePanel's own series window
// (multiplier 1), so the two can never disagree about what "the render's own
// base window" means before they diverge on how much wider than it to go.
//
// base is floored at minSmoothingWindow -- the same floor
// Timeline.autoSmoothingFor already treats as "the shortest window worth
// applying" -- BEFORE the multiplier, even when --smoothing is explicitly
// off (Context.Smoothing.Window == 0). A user who disables the printed
// number's own smoothing has asked for the raw figure; they have not asked
// a series built from it to stop being smoothed at all. Without this floor,
// a literal zero would carry through the multiplier unchanged, and
// buildGaugeSeries's own "smoothed" series would be the untouched raw
// series -- for the gauge marker, exactly the literal raw range its own doc
// comment explains was tried and rejected; for a balance bar, the zeros
// buildGaugeSeries is relied on to average away would instead land in the
// series one at a time, each a spurious below-floor chevron.
func smoothingSeriesWindow(ctx *Context, multiplier float64) time.Duration {
	base := ctx.Smoothing.Window
	if ctx.Smoothing.Auto {
		base = ctx.Timeline.AutoSmoothingBase()
	}
	if base < minSmoothingWindow {
		base = minSmoothingWindow
	}
	return time.Duration(float64(base) * multiplier)
}

// robustGaugeScale derives a gaugeScale from ctx.Track through value --
// which MUST be the readout's own bound accessor, see Readout.scale's doc
// comment for why it can never be ctx.Report -- by extra-smoothing value
// into a gaugeSeries (buildGaugeSeries, over gaugeMarkerWindow) and
// snapping that series' own literal Range outward to the nearest multiple
// of step. Snapping outward (floor down, ceiling up) makes the endpoint a
// statement about the axis ("this track reads 100-190 bpm") rather than a
// claim about the activity ("you hit 189"), and buys headroom so an
// out-of-range reading is rarer than the series' own range implies. See
// gaugeSeries' own doc comment for why the range is the series' literal
// extremes now, with no quantile trimming on top -- the extra smoothing
// already does that job.
//
// Power and cadence use this identical rule, with no floor forced to 0. An
// earlier version took a hardZeroFloor bool and overrode the snapped low
// end with a literal 0 for those two, on the reasoning that "0 W" and "0
// rpm" are real, meaningful readings -- coasting, not pedalling -- that
// deserved to sit on the axis rather than be treated as an extreme to be
// snapped away from. That reasoning was correct about the READING and
// wrong about the CONSEQUENCE: forcing the floor to a value most of the
// activity never approaches does not put the zero on the axis so much as it
// crushes the axis's own ordinary range into a sliver at the far end -- a
// render gate found cadence's whole range pressed against the top of a
// hard-zero-floored track, its marker travelling a handful of pixels across
// a track hundreds of pixels wide for the middle half of the render: an
// instrument that admitted every possible reading and communicated none of
// them. (The measurement was taken against a private recording, so the
// figures behind it are deliberately not repeated here -- see CLAUDE.md;
// the shape of the result is the part that generalises anyway, and it
// follows from where a cadence sits relative to zero rather than from
// whose cadence it was.) A reading of 0 does not need to sit ON the scale
// to be reported honestly; it needs to be reported at all, which the
// below-floor overflow chevron plus the true, unclipped printed number
// (drawGauge, below) already does for any reading under the floor, zero
// included. So there is no hard-floor parameter any more, and there should
// not be one again: a future "0 W is real, force the floor" is the
// identical mistake, now measured.
//
// ok is false when the series itself refuses (fewer than
// gaugeSeriesMinPresent genuine points) or when the snapped range is
// degenerate (ceiling <= floor, which a flat or near-flat series can
// produce). Both are the fourth drawing state, "no usable range" -- the
// caller falls back to the plain readout, it does not decline the panel.
func robustGaugeScale(ctx *Context, value func(fitactivity.Sample) (float64, bool), step float64) (gaugeScale, bool) {
	if ctx == nil || ctx.Track == nil || step <= 0 {
		return gaugeScale{}, false
	}
	series := buildGaugeSeries(ctx.Track, gaugeMarkerWindow(ctx), value)
	lo, hi, ok := series.Range()
	if !ok {
		return gaugeScale{}, false
	}
	floor := math.Floor(lo/step) * step
	ceiling := math.Ceil(hi/step) * step
	if ceiling <= floor {
		return gaugeScale{}, false
	}
	return gaugeScale{floor: floor, ceiling: ceiling}, true
}

// paceGaugeStep is the snap step pace's own endpoint labels round outward
// to, in seconds per kilometre -- 30, a judgement call: coarse enough that
// neighbouring paces do not look like meaningfully different bounds, fine
// enough that a short activity's own range does not snap away to nothing.
const paceGaugeStep = 30.0

// paceGaugeScale is pace's own gaugeScale constructor, and the one place
// "sweep on speed, label in pace" (docs/architecture.md) actually happens.
//
// The gaugeSeries built here is asked about SPEED -- value is Pace's own
// bound accessor, which reports speed and refuses exactly the speeds Pace
// itself refuses to print a pace for (see Pace's minPaceSpeed) -- so the
// track and the printed number can never disagree about which instants
// count.
//
// The snap happens in PACE space and is only THEN converted back to speed,
// rather than snapping the series' own speed range directly: a viewer
// reads the printed endpoint as a pace ("4:30"), so the round number the
// snap produces has to be round in the unit that actually appears on
// screen. The slow end (the series' own LOW speed, the LARGER pace figure)
// rounds UP to the next 30-second multiple; the fast end (the series' own
// HIGH speed, the SMALLER pace figure) rounds DOWN -- both away from the
// middle of the range, the same "outward" rule robustGaugeScale applies in
// value space, applied here in pace space before the reciprocal converts
// it back.
//
// The returned gaugeScale is still in SPEED: Dynamic's fraction arithmetic
// is shared with every other gauge, and "more speed moves the marker further
// along the track" is what keeps "further along the track = more of the good
// thing" true here too, even though the marker therefore moves toward the
// ceiling end as the printed pace FALLS. See the type doc comment on
// GaugeStyle for why that no longer needs defending the way it did when this
// track drew a fill: a growing FILL invoked quantity, and a quantity that
// grows as its own printed number shrinks read as a contradiction; a marker
// at a POSITION makes no quantity claim, so the same sweep-on-speed,
// label-in-pace arithmetic stopped being a misreading once the fill became a
// dot. floor is the SLOWER of the two speeds and ceiling the FASTER: floor <
// ceiling, the same invariant every other gaugeScale carries, even though
// the pace numbers they came from run the other way.
func paceGaugeScale(ctx *Context, value func(fitactivity.Sample) (float64, bool)) (gaugeScale, bool) {
	if ctx == nil || ctx.Track == nil {
		return gaugeScale{}, false
	}
	series := buildGaugeSeries(ctx.Track, gaugeMarkerWindow(ctx), value)
	loSpeed, hiSpeed, ok := series.Range()
	if !ok {
		return gaugeScale{}, false
	}
	if loSpeed <= 0 || hiSpeed <= 0 {
		// A non-positive speed has no pace at all (see Pace's own doc
		// comment) -- there is no honest reciprocal to snap.
		return gaugeScale{}, false
	}
	slowSec := math.Ceil((1000/loSpeed)/paceGaugeStep) * paceGaugeStep
	fastSec := math.Floor((1000/hiSpeed)/paceGaugeStep) * paceGaugeStep
	if fastSec < paceGaugeStep {
		fastSec = paceGaugeStep
	}
	if fastSec >= slowSec {
		return gaugeScale{}, false
	}
	return gaugeScale{floor: 1000 / slowSec, ceiling: 1000 / fastSec}, true
}

// Per-metric snap steps, in whatever unit the readout's own bound value()
// reports -- see each Readout constructor's scale field (readout.go).
// Cadence's is the same 10 whether the sport doubled it to spm or left it in
// rpm, because Cadence's own bind has already resolved that before
// buildGaugeSeries ever sees a sample.
const (
	heartRateGaugeStep = 10.0
	powerGaugeStep     = 50.0
	cadenceGaugeStep   = 10.0
)

// drawTrack draws the reserved strip's track rectangle -- the full-length
// axis, at p.spanX, width p.spanW, thickness p.trackH -- in col. Shared by
// the dim ghost (Static) and the absent wash (Dynamic) so the two are
// pixel-identical rectangles differing only in colour, never independently
// drawn shapes that could disagree about the strip's own geometry --
// ClimbPanel's drawBar (climb.go) is the precedent this mirrors.
//
// It is never called with a value-derived width any more: the axis is
// chrome, drawn at its own full length regardless of the reading, and the
// reading is drawNotch's job. An earlier version took a width argument and
// this function WAS the live fill; see GaugeStyle's own doc comment for why
// that fill was replaced with a marker.
func (p *readoutPainter) drawTrack(c *Canvas, col color.Color) {
	if p.spanW <= 0 {
		return
	}
	c.Rect(Box{X: p.spanX, Y: p.trackY - p.trackH/2, W: p.spanW, H: p.trackH}, col)
}

// drawNotch draws the marker itself -- a filled dot, not a bar and not a
// triangle -- at the track position frac (0 = floor, 1 = ceiling) implies.
// The shape is deliberately a third one: gaugeCap already owns "triangle",
// for the two ends' overflow marks, and ClimbPanel's own bars already own
// "horizontal rectangle that only grows" one row down in the portrait
// layout (see GaugeStyle's doc comment for why colour was rejected as the
// distinguishing device). A round dot cannot be mistaken for either at any
// position on the track, including frac 0 or 1 themselves, which a short
// fill anchored at the left end could be mistaken for the below-floor
// chevron living in that identical spot.
//
// notchR is sized in Prepare (reserveGaugeStrip) against the box the
// gauge was actually placed in, never a pixel constant -- see that
// function's own doc comment for the figures checked at 1080p.
func (p *readoutPainter) drawNotch(c *Canvas, frac float64, col color.Color) {
	if p.spanW <= 0 || p.notchR <= 0 {
		return
	}
	c.Circle(p.spanX+p.spanW*frac, p.trackY, p.notchR, col)
}

// gaugeCap draws the overflow mark -- a small triangle pointing off-scale --
// at the track's own right end (right true, an above-ceiling reading) or
// left end (a below-floor reading). The same symmetric code marks both ends
// rather than only the ceiling: for pace especially, a below-floor reading
// (a speed slower than the track's own slow-end floor) is a real, ordinary
// event -- easing off after the fast stretch the ceiling end was scaled
// against -- not the rarer case a reader might assume "out of range" always
// means. It is drawn in Theme.Foreground, never a new Theme role: Theme has
// no "out of range" colour (see canvas.go's own Theme doc comment on why a
// panel never invents one), and Foreground already means "a live reading",
// which an out-of-range instant still is -- the one instant this panel is
// most certain about.
//
// The body is drawGaugeCap, a free function taking the geometry rather than
// a *readoutPainter, so a Painter that is not a readoutPainter -- BalancePanel
// (balance.go), which draws the identical off-scale chevron at a fixed
// scale's own two ends -- can call the exact same shape rather than a second
// copy of this polygon that could drift from it. This method is now a thin
// adapter kept so every EXISTING call site (drawGauge, drawDialGauge, both in
// this file) is untouched.
func (p *readoutPainter) gaugeCap(c *Canvas, right bool, col color.Color) {
	drawGaugeCap(c, p.spanX, p.spanW, p.trackY, p.capW, p.capHalfH, right, col)
}

// drawGaugeCap is gaugeCap's own body, lifted out to a free function so a
// non-readoutPainter caller can draw the identical chevron -- see gaugeCap's
// own doc comment. spanX, spanW, trackY, capW and capHalfH are the same five
// fields gaugeCap already reads off p; a caller with no readoutPainter of its
// own (BalancePanel) resolves the identical five during its own Prepare and
// passes them straight through.
func drawGaugeCap(c *Canvas, spanX, spanW, trackY, capW, capHalfH float64, right bool, col color.Color) {
	if capW <= 0 {
		return
	}
	half := capHalfH
	if right {
		x0 := spanX + spanW
		c.Polygon([]float64{x0, x0, x0 + capW}, []float64{trackY - half, trackY + half, trackY}, col)
		return
	}
	x0 := spanX
	c.Polygon([]float64{x0, x0, x0 - capW}, []float64{trackY - half, trackY + half, trackY}, col)
}

// drawGauge is Dynamic's own gauge-specific drawing for a PRESENT reading v
// (the caller already confirmed p.value(f.Sample) returned ok), called only
// when p.hasTrack -- Prepare has already resolved p.scale and decided there
// is a usable range, and this function must never re-derive or widen it (see
// gaugeScale's own doc comment).
//
// mv is the SAME reading, positioned instead: the marker's own extra-
// smoothed value (p.series.At(f.At), with Dynamic's own fallback to v where
// the series has nothing -- see readoutPainter.Dynamic), which is what
// actually places the notch. v alone decides which of the three states
// below applies -- the cap is a statement about the RAW reading crossing
// the smoothed axis, not about where the smoothed trend itself sits, which
// is guaranteed inside [floor, ceiling] by construction (gaugeSeries.Range
// feeds the very same snap p.scale was built from). See gaugeSeries' own
// doc comment for why the two can legitimately differ at all.
//
// Three of this panel's four drawing states live here:
//
//   - below floor: no marker at all -- there is no honest POSITION for it,
//     the way there was no honest FILL width for it before -- plus the
//     left-hand overflow cap. This is not the same as the absent wash's own
//     "nothing drawn": the cap and the unclipped printed number both say
//     "known, and off this end", where the absent wash's silence says "not
//     known at all". A genuine 0 W or 0 rpm (see robustGaugeScale's own doc
//     comment, now that neither gauge forces its floor to 0) lands here.
//     This is now ALSO where a raw spike the marker's own series smoothed
//     away lands, once it slips isolated enough from the smoothed axis --
//     "the raw reading went past what the smoothed marker shows", a real
//     event the chevron is kept to mark.
//   - above ceiling: the mirror case, the right-hand overflow cap and no
//     marker, for the identical reason -- there is no point past the
//     ceiling for a dot to honestly sit at, and parking it AT the ceiling
//     would be indistinguishable from an in-range reading that happens to
//     equal it.
//   - in range: the marker sits at the exact fraction mv implies, coloured
//     by that SAME fraction (gaugeRampColor) -- see that function's own doc
//     comment for why position and colour are computed from one shared
//     frac rather than two independent quantities that could disagree. No
//     cap.
//
// The fourth state, "no usable range", never reaches this function at all:
// Prepare leaves p.hasTrack false and readoutPainter.Dynamic skips straight
// to the plain reading.
func (p *readoutPainter) drawGauge(c *Canvas, v, mv float64) {
	switch {
	case v < p.scale.floor:
		p.gaugeCap(c, false, c.Theme.Foreground)
	case v > p.scale.ceiling:
		p.gaugeCap(c, true, c.Theme.Foreground)
	default:
		frac := p.scale.fraction(mv)
		p.drawNotch(c, frac, gaugeRampColor(frac))
	}
}

// dialAngle maps frac in [0,1] (0 = floor, 1 = ceiling) to the needle's own
// angle in radians, in the standard x = r*cos(theta), y = r*sin(theta)
// convention against the canvas's own y-DOWN axes. theta=pi is due LEFT of
// the pivot (frac=0, the same end the linear track's own left edge is), and
// sweeping theta upward from pi through 3*pi/2 (due UP, frac=0.5) to 2*pi
// (due RIGHT, frac=1) is what makes "up" on screen actually be up, rather
// than the "down" a naive theta=pi/2 midpoint would draw with y increasing
// downward. This is what keeps the dial's own left-to-right sense of "higher
// reading, further right" identical to the track's fraction -- the two
// shapes must agree on that or switching --gauge-style between them would
// change more than the shape.
func dialAngle(frac float64) float64 {
	return math.Pi * (1 + frac)
}

// drawDialArc draws the chrome semicircle itself -- the SAME half-circle
// regardless of frame, in col -- shared by the dim ghost (Static) and the
// absent wash (Dynamic), the dial's own analogue of drawTrack, above: one
// shape drawn twice in two colours, never two independently coded arcs that
// could silently disagree about the sweep.
func (p *readoutPainter) drawDialArc(c *Canvas, col color.Color) {
	if p.dialR <= 0 {
		return
	}
	c.Arc(p.dialCX, p.dialCY, p.dialR, dialAngle(0), dialAngle(1), p.dialLineW, col)
}

// drawNeedle draws the live needle, from the pivot to the point at fraction
// frac along the arc (dialAngle), plus a small pivot dot -- the dial's own
// analogue of drawNotch. This is the marker: it claims "the reading is
// here", so drawDialGauge (below) calls it only for a value this instant
// actually has, exactly as drawNotch is only ever reached for an in-range
// PRESENT reading.
func (p *readoutPainter) drawNeedle(c *Canvas, frac float64, col color.Color) {
	if p.dialR <= 0 {
		return
	}
	a := dialAngle(frac)
	tipX := p.dialCX + p.dialR*math.Cos(a)
	tipY := p.dialCY + p.dialR*math.Sin(a)
	c.Polyline([]float64{p.dialCX, tipX}, []float64{p.dialCY, tipY}, p.needleW, col)
	c.Circle(p.dialCX, p.dialCY, p.dialPivotR, col)
}

// drawDialGauge is Dynamic's own dial-specific drawing for a PRESENT reading
// v, mirroring drawGauge's three states exactly -- see that function's own
// doc comment for why each is drawn the way it is. Only the in-range state
// differs (a needle rather than a notch); below-floor and above-ceiling
// reuse gaugeCap completely unchanged, because reserveGaugeDial sets
// spanX/spanW/trackY to the arc's own two ends and the pivot's own height --
// the identical horizontal extent and height the linear track's overflow
// chevron already draws against -- so there is no dial-specific overflow
// shape to invent.
// mv is drawGauge's own second argument, restated here: the needle's
// position comes from the marker's own extra-smoothed value, never from v
// directly -- see drawGauge's doc comment for the full reasoning, which
// applies verbatim to a needle in place of a notch.
func (p *readoutPainter) drawDialGauge(c *Canvas, v, mv float64) {
	switch {
	case v < p.scale.floor:
		p.gaugeCap(c, false, c.Theme.Foreground)
	case v > p.scale.ceiling:
		p.gaugeCap(c, true, c.Theme.Foreground)
	default:
		frac := p.scale.fraction(mv)
		p.drawNeedle(c, frac, gaugeRampColor(frac))
	}
}

// gaugeRampLow, gaugeRampMid and gaugeRampHigh are the three fixed stops
// gaugeRampColor interpolates between -- green, amber, red, low value to
// high. See gaugeRampColor's own doc comment for why they live here rather
// than on Theme.
var (
	gaugeRampLow  = color.NRGBA{R: 0x2E, G: 0xA6, B: 0x4D, A: 0xFF}
	gaugeRampMid  = color.NRGBA{R: 0xC8, G: 0x8A, B: 0x00, A: 0xFF}
	gaugeRampHigh = color.NRGBA{R: 0xD1, G: 0x35, B: 0x2E, A: 0xFF}
)

// gaugeRampColor is the gauge marker's own colour at fraction frac (0 = the
// scale's floor, 1 = its ceiling, clamped) along a fixed green -> amber ->
// red ramp.
//
// Theme deliberately had no role for this before now, and this is not an
// oversight closed by adding one. Every existing Theme role names a FIXED
// meaning that holds regardless of magnitude -- Foreground is "a live
// reading", Absent is "no reading", Accent is "the current position" -- and
// a theme can restate any of those in its own palette without touching
// what they MEAN. This ramp's entire point is the opposite: its colour IS
// the magnitude, which is not a fact about a theme, it is a fact about
// where THIS reading sits on THIS gauge's own scale. Folding it into Theme
// would force every theme, shipped or future, to invent stops it has no
// other use for, or would quietly overload an existing role with a second
// meaning depending on which panel happened to be reading it. It lives
// here instead, once, as a gauge-specific palette, deliberately unexported
// since nothing outside this file draws a gauge.
//
// It is checked against both shipped themes rather than assumed to work:
// against DarkTheme's and LightTheme's own Background, Dim, Absent,
// Foreground and Accent, every stop stays comfortably distinguishable by
// hue even where its WCAG contrast ratio against one of them is low (a low
// number there means similar LUMINANCE, not an identical colour -- Theme's
// own Accent, an orange-red, and this ramp's own high-end red read as
// visibly different hues at a similar brightness in both themes).
//
// Green-to-red is the pairing red-green colour-vision deficiency -- the
// most common form -- confuses most easily. It is used anyway, on the
// user's own suggestion and because it is this project's effort-convention
// default (green/amber/red, low to high), and it is safe only because the
// design's answer is REDUNDANT encoding, not because the colours were
// chosen to be distinguishable to every viewer: the marker's own POSITION
// on the axis (drawNotch/drawNeedle, called from drawGauge/drawDialGauge
// with the SAME frac this function is) already carries the identical
// information a viewer with ordinary colour vision reads from the ramp.
// Colour therefore only adds EMPHASIS for a viewer who can use it -- nothing
// in this panel is, or may become, distinguishable BY COLOUR ALONE, and
// computing frac exactly once per marker and passing it to both the
// position call and this function (drawGauge, drawDialGauge) is the whole
// mechanism that keeps that true.
func gaugeRampColor(frac float64) color.Color {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	if frac <= 0.5 {
		return blendColor(gaugeRampLow, gaugeRampMid, frac/0.5)
	}
	return blendColor(gaugeRampMid, gaugeRampHigh, (frac-0.5)/0.5)
}

// blendColor linearly interpolates each of a and b's own RGB channels at t
// (0 -> a, 1 -> b), always fully opaque -- an ordinary colour LERP, not an
// alpha composite (compare Fade, which fades ONE colour's own alpha down
// rather than mixing two together). Used both by gaugeRampColor's own
// three-stop ramp and by drawTrackGradient/drawDialArcGradient's tint of
// the axis chrome toward it.
func blendColor(a, b color.Color, t float64) color.Color {
	na := color.NRGBAModel.Convert(a).(color.NRGBA)
	nb := color.NRGBAModel.Convert(b).(color.NRGBA)
	lerp := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return color.NRGBA{R: lerp(na.R, nb.R), G: lerp(na.G, nb.G), B: lerp(na.B, nb.B), A: 0xFF}
}

// gaugeAxisRampMix is how much gaugeRampColor tints the axis chrome, blended
// (blendColor, an opaque RGB mix -- never Fade's alpha) into Theme.Dim --
// see drawTrackGradient/drawDialArcGradient. Small on purpose: the axis is
// chrome (gaugeAxisLineFraction's own "chrome, not a quantity" reasoning),
// so the gradient must read as a colour SHIFT of the dim ghost the marker
// sits on, not as saturated paint competing with it.
const gaugeAxisRampMix = 0.28

// gaugeAxisRampSegments is how many discrete strips (drawTrackGradient) or
// arc segments (drawDialArcGradient) approximate the ramp along the axis's
// own length or sweep. Static draws these ONCE per render, never once per
// frame, so the count is chosen for a visually smooth gradient rather than
// for draw-call economy.
const gaugeAxisRampSegments = 48

// drawTrackGradient is drawTrack's own Static-only sibling: the identical
// rectangle drawTrack fills flat, drawn instead as gaugeAxisRampSegments
// adjacent strips tinted along gaugeRampColor (gaugeAxisRampMix) rather
// than one flat Theme.Dim fill.
//
// Deliberately Static-only: the absent wash (Dynamic) keeps calling the
// ORIGINAL drawTrack, in Fade(Theme.Absent, gaugeAbsentWashAlpha(theme)),
// unchanged -- a dropout must still read as a single flat colour meaning
// "no reading", never as a gradient that could be mistaken for a live
// reading's own track. See gaugeAbsentWashAlpha's own doc comment for how
// its contrast search accounts for compositing over this tinted ghost
// rather than flat Theme.Dim.
//
// Segments overlap by a pixel (segW+1, not segW) so a rounding error in
// gg's own sub-pixel rectangle fill cannot leave a hairline seam of raw
// background between two strips -- the same reasoning ClimbPanel's own bar
// geometry states for its own adjacent fills.
func (p *readoutPainter) drawTrackGradient(c *Canvas, theme Theme) {
	if p.spanW <= 0 {
		return
	}
	n := gaugeAxisRampSegments
	segW := p.spanW / float64(n)
	for i := 0; i < n; i++ {
		frac := (float64(i) + 0.5) / float64(n)
		col := blendColor(theme.Dim, gaugeRampColor(frac), gaugeAxisRampMix)
		c.Rect(Box{X: p.spanX + float64(i)*segW, Y: p.trackY - p.trackH/2, W: segW + 1, H: p.trackH}, col)
	}
}

// gaugeAxisRampArcOverlapFraction pads each interior arc segment's own
// angular endpoints by this fraction of one segment's width, so consecutive
// strokes overlap slightly rather than sharing an exact seam. Unlike
// drawTrackGradient's rectangles, which sit edge to edge and share a flat
// SIDE, gg's own DrawArc caps each stroke's own two ends -- two arcs that
// meet at an exact shared angle can still leave a hairline gap at the
// join, and one such join lands EXACTLY at the arc's own topmost pixel
// whenever gaugeAxisRampSegments is even (frac=0.5 is then a segment
// boundary): a real gate found that gap rendering as raw Theme.Background
// showing through, rather than either segment's own tinted colour, which
// broke the absent wash's own contrast guarantee at exactly that pixel
// (TestReadoutGaugeDial_AbsentWashClearsContrastFloorAgainstTheArcsOwnGhost).
// The two outermost endpoints (frac 0 and 1, the arc's own true ends) are
// NOT padded, so the arc's overall sweep still matches drawDialArc's
// unchanged.
const gaugeAxisRampArcOverlapFraction = 0.6

// drawDialArcGradient is drawDialArc's own Static-only sibling, mirroring
// drawTrackGradient's own reasoning for the dial's semicircle: the
// identical sweep drawDialArc strokes in one flat colour, drawn instead as
// gaugeAxisRampSegments adjacent arc segments tinted along gaugeRampColor.
// The absent wash (Dynamic) keeps calling the ORIGINAL drawDialArc,
// unchanged, for the same reason drawTrackGradient's own doc comment gives.
func (p *readoutPainter) drawDialArcGradient(c *Canvas, theme Theme) {
	if p.dialR <= 0 {
		return
	}
	n := gaugeAxisRampSegments
	pad := gaugeAxisRampArcOverlapFraction / float64(n)
	for i := 0; i < n; i++ {
		f0 := float64(i) / float64(n)
		f1 := float64(i+1) / float64(n)
		col := blendColor(theme.Dim, gaugeRampColor((f0+f1)/2), gaugeAxisRampMix)
		lo, hi := f0, f1
		if i > 0 {
			lo -= pad
		}
		if i < n-1 {
			hi += pad
		}
		c.Arc(p.dialCX, p.dialCY, p.dialR, dialAngle(lo), dialAngle(hi), p.dialLineW, col)
	}
}

// gaugeAbsentWashContrastFloor is the same "has the role become
// indistinguishable from what it sits on" threshold elevationAbsentFillContrastFloor
// (elevation.go) applies, restated as this panel's own constant rather than
// a shared one -- see gaugeAbsentWashAlpha's own doc comment for why the two
// derivations must stay separate even though the THRESHOLD they both check
// happens to be the identical number.
const gaugeAbsentWashContrastFloor = 1.5

// gaugeAbsentWashContrastMargin is elevationAbsentFillContrastMargin's own
// reasoning (elevation.go) restated for this panel's own search: padding
// above gaugeAbsentWashContrastFloor to survive Fade's rounding to an 8-bit
// alpha channel, which can otherwise land a hair under the floor it was
// solved for.
const gaugeAbsentWashContrastMargin = 0.02

// gaugeAbsentWashAlpha derives the dropout wash's own opacity from theme,
// against theme.Dim -- NOT theme.Background, which is elevationAbsentFillAlpha's
// own derivation and is the wrong one here.
//
// A gate measured why: this panel's dim ghost (drawTrack, Static) already
// fills the exact rectangle the absent wash is drawn into, so the wash
// composites ON TOP OF Theme.Dim, never directly onto Theme.Background the
// way ElevationPanel's and ClimbPanel's own dropout washes do. Deriving the
// alpha against Background anyway -- reusing elevationAbsentFillAlpha, as an
// earlier version of this panel did -- solves the wrong equation: measured
// directly, that alpha composites to a contrast of only 1.37:1 against the
// ghost it actually sits on in the dark theme (2.04:1 in light), both well
// under the 1.5 floor either derivation is trying to guarantee. The root
// cause is not a tuning slip -- Theme.Absent and Theme.Dim are inherently
// close in luminance in both shipped themes (undimmed ContrastRatio(Absent,
// Dim) is only about 1.74:1 in DarkTheme), so even alpha 1.0 barely clears
// what elevationAbsentFillAlpha's own search was aiming for against a
// different background entirely.
//
// This is why the two derivations must NOT be unified into one later: they
// are the identical piece of arithmetic (minAlphaForContrast) applied to two
// different backgrounds, and a panel's wash must always be solved against
// the colour it is ACTUALLY drawn over, not against Theme.Background as a
// default that happens to be right for the panels that invented it.
//
// Measured (from the shipped themes' own colours, not from any recording):
// roughly 0.76 for DarkTheme and 0.46 for LightTheme against flat Theme.Dim,
// both comfortably under 1, composited to a contrast against Theme.Dim of
// about 1.53:1 in both -- just clearing the floor plus its margin, which is
// the honest cost of Absent and Dim sitting as close together as they do.
//
// # Solved again against the tinted ghost, not only flat Dim
//
// drawTrackGradient/drawDialArcGradient (see their own doc comments) mean
// the dim ghost this wash composites over is no longer flat Theme.Dim
// everywhere along the axis -- it is Theme.Dim tinted toward gaugeRampColor
// by gaugeAxisRampMix, and that tint varies continuously from the green end
// to the red one. Solving only against flat Dim, as this function did
// before the ramp existed, would leave the contrast floor unchecked for
// every pixel the wash actually sits on now. gaugeAbsentWashSampleFracs
// samples five points across the ramp's own domain (both ends, the
// midpoint, and one quarter in from each end -- the ramp is piecewise
// linear between only three stops, so its contrast against a fixed colour
// cannot have an interior extremum this coarse a sample would miss) and
// this returns the LARGEST alpha any of them, or flat Dim itself, needs --
// the worst case, so the floor holds wherever along the gradient a dropout
// happens to land, not only at the one point (pure Dim) the ghost's own
// geometry no longer is everywhere.
//
// Measured worst case (again from the shipped themes' own colours, not
// from any recording): roughly 0.88 for DarkTheme (at the red end, where
// the tint pulls Dim's luminance furthest from Absent's) and roughly 0.52
// for LightTheme (at the midpoint amber tint) -- both still comfortably
// under 1, so no tint this small ever makes the floor unreachable.
func gaugeAbsentWashAlpha(theme Theme) float64 {
	target := gaugeAbsentWashContrastFloor + gaugeAbsentWashContrastMargin
	alpha := minAlphaForContrast(theme.Absent, theme.Dim, target)
	for _, f := range gaugeAbsentWashSampleFracs {
		tinted := blendColor(theme.Dim, gaugeRampColor(f), gaugeAxisRampMix)
		if a := minAlphaForContrast(theme.Absent, tinted, target); a > alpha {
			alpha = a
		}
	}
	return alpha
}

// gaugeAbsentWashSampleFracs are the points along gaugeRampColor's own
// domain gaugeAbsentWashAlpha checks the wash's contrast against -- see
// that function's own doc comment for why these five suffice for a ramp
// that is only piecewise linear between three stops.
var gaugeAbsentWashSampleFracs = []float64{0, 0.25, 0.5, 0.75, 1}
