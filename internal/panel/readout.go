package panel

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// Readout is a labelled single-value panel: a metric's current reading, its
// unit, and a caption.
//
// One type serves every scalar metric because the panels differ only in which
// field they read and what they call it. A separate HeartRatePanel and
// PowerPanel with the same body would be two places to fix the same
// placeholder bug.
type Readout struct {
	name   string
	label  string
	unit   string
	metric string
	value  func(fitactivity.Sample) (float64, bool)
	format func(float64) string

	// template is the widest string the reading can render as, which the
	// panel sizes its text against. Empty means "888", which suits a
	// three-digit integer and nothing else -- a pace of "88:88" or a distance
	// of "88.88" needs more room, and sizing those against "888" prints them
	// wider than their box.
	template string

	// absent overrides the generic placeholder. Empty means ReadoutPlaceholder.
	absent string

	// carries overrides the default coverage test for panels whose "does this
	// activity have it" question the report cannot answer. See Power.
	carries func(*Context) bool

	// bind, when set, rebuilds the value accessor from the render context.
	// Power needs it: which sensor to read is a render-wide choice, and a
	// closure fixed at construction could not know it.
	bind func(*Context, Readout) Readout

	// scale, when set, is this readout's own GaugeStyleTrack range rule --
	// see GaugeStyle's own doc comment (gauge.go) for what the track is and
	// gaugeScale's for the space it is expressed in. nil means this readout
	// has no gauge variant at all; Prepare treats that identically to the
	// scale rule returning ok=false (no usable range), which is why no
	// Readout constructor needs its own separate "does not support gauges"
	// flag.
	//
	// It is called AFTER bind, on the already-rebound Readout -- Prepare
	// passes itself, post-bind, as the argument -- and it MUST derive the
	// range through r.value, never through ctx.Report. Report is built once,
	// in the RECORDED space, before any panel's own render-time choices are
	// known, and three of these four readouts draw in a DIFFERENT space:
	// HeartRate refuses a recorded zero that Report still counts as present
	// (a zero would mean the person is dead); Power may resolve a Stryd
	// developer field the report's own "Power" row never saw, depending on
	// Context.PowerSource; and Cadence doubles rpm to spm for a running
	// sport, where Report's own range stays in rpm. A scale built from
	// Report would put a floor at a value the printed number beside it can
	// never show, or scale cadence's track to half the number printed on it.
	// Deriving through r.value instead means the track and the number can
	// never disagree about which instants count -- which for pace matters
	// twice over, because Pace's own value refuses speeds below
	// minPaceSpeed, so a track built through it cannot place a marker at an
	// instant where the number reads PacePlaceholder.
	scale func(*Context, Readout) (gaugeScale, bool)

	// rule draws a rule beneath the caption, matching ElapsedPanel's own --
	// see Distance's doc comment for why it is the one readout that sets
	// this. Every other Readout leaves it false and keeps its plainer,
	// unruled chrome.
	rule bool
}

// HeartRate reads the standard FIT heart rate field.
//
// A recorded zero is treated as no reading, on top of the field's own
// presence flag -- HasHeartRate is true while a strap has not yet found skin
// contact, and some devices write 0 for that state rather than leaving the
// sample absent. A heart rate of zero would mean the person is dead, which is
// never the honest reading of "not connected yet"; it is a device convention
// for absence wearing the field's ordinary shape, and refusing it is the
// same judgement Pace makes about a speed of zero, just answered here instead
// of upstream.
//
// This is deliberately NOT fitactivity's call: that library's presence flags
// exist to turn the FIT format's SENTINEL values into an explicit "absent",
// and zero is not one of those sentinels -- it is a legal recorded heart
// rate value as far as the format is concerned. Deciding that a strap's zero
// does not count as a measurement is a domain judgement about what a human
// heart rate can be, and that judgement belongs to the panel that draws it,
// not to the shared decoder.
//
// This does NOT change what `fitdash inspect` reports for the same file: the
// field is present (HasHeartRate is true) and its recorded minimum genuinely
// is zero, so the report is right to say so. The two are not in
// disagreement -- inspect answers "what did the file record", and this
// answers "does that recording mean anything as a heart rate", which is a
// question only a heart rate panel needs to ask.
func HeartRate() Readout {
	return Readout{
		name: "heart-rate", label: "HEART RATE", unit: "bpm", metric: inspect.MetricHeartRate,
		value: func(s fitactivity.Sample) (float64, bool) {
			return float64(s.HeartRate), s.HasHeartRate && s.HeartRate != 0
		},
		format: func(v float64) string { return fmt.Sprintf("%.0f", v) },
		scale: func(ctx *Context, r Readout) (gaugeScale, bool) {
			// No forced floor: a genuine zero is already refused by value
			// above, so there is nothing this scale needs to force -- the
			// snapped low end is itself never a device's "not
			// connected yet" zero. Power and Cadence use this identical
			// rule (see robustGaugeScale's own doc comment for why they no
			// longer force a floor either).
			return robustGaugeScale(ctx, r.value, heartRateGaugeStep)
		},
	}
}

// Power reads whichever power sensor Context.PowerSource selects.
//
// A FIT file can carry two: the standard Record.power field, and a developer
// field a footpod such as a Stryd registers under its own name. They are
// different sensors and they disagree, by enough at the same instant to change
// what the gauge says -- so this is a choice about which instrument to believe,
// not about formatting.
//
// The resolution rule is fitactivity's ResolvedPower, shared with videofx
// rather than reimplemented, including its strictness: asking for one source
// specifically and not getting it yields a PLACEHOLDER, never the other
// sensor's number quietly substituted.
func Power() Readout {
	return Readout{
		name: "power", label: "POWER", unit: "W", metric: inspect.MetricPower,
		format: func(v float64) string { return fmt.Sprintf("%.0f", v) },

		// Whether this activity has power AT ALL cannot be answered from the
		// coverage report, which is why this panel is the one exception to
		// panels asking the report. The report counts metrics by NAME, and
		// both sources are called "Power" -- so a lookup finds the native row
		// and reports on a sensor the user may not have selected. HasPower
		// applies ResolvedPower to every sample, which is the same rule the
		// readout itself uses, so the decision to place the panel and the
		// decision to draw a number cannot disagree.
		carries: func(ctx *Context) bool {
			return ctx.Track != nil && ctx.Track.HasPower(ctx.PowerSource)
		},
		bind: func(ctx *Context, r Readout) Readout {
			src := ctx.PowerSource
			r.value = func(s fitactivity.Sample) (float64, bool) { return s.ResolvedPower(src) }
			return r
		},
		scale: func(ctx *Context, r Readout) (gaugeScale, bool) {
			// No forced floor: 0 W (coasting) is a real, meaningful reading,
			// but it is reported by the below-floor overflow chevron plus
			// the true, unclipped printed number, not by bending the axis
			// down to meet it -- see robustGaugeScale's own doc comment for
			// the measurement that overturned the earlier "force floor to
			// 0" rule. r.value here is the ALREADY-BOUND accessor bind
			// installed above -- Prepare calls scale after bind, so this
			// reads whichever sensor ctx.PowerSource selected, never the
			// other one Report's single "Power" row would answer for.
			return robustGaugeScale(ctx, r.value, powerGaugeStep)
		},
	}
}

// Cadence reads the standard FIT cadence field, in the unit its sport is
// counted in.
//
// FIT stores cadence as revolutions per minute, which for a bike is crank
// revolutions and is the number a cyclist wants. For a runner it is
// revolutions PER LEG, so the figure a runner recognises -- steps per minute --
// is twice it. A run showing "87 rpm" where the watch said 174 spm is not
// wrong so much as unrecognisable; videofx applies the same doubling, as do
// Garmin and Telemetry Overlay.
//
// Which one applies depends on the sport, so it cannot be decided when the
// panel is constructed. Unknown sports are left in rpm: reporting the recorded
// number under its recorded unit is the answer that cannot be wrong, where
// guessing at a doubling would silently halve or double somebody's cadence.
func Cadence() Readout {
	return Readout{
		name: "cadence", label: "CADENCE", unit: "rpm", metric: inspect.MetricCadence,
		value: func(s fitactivity.Sample) (float64, bool) {
			return float64(s.Cadence), s.HasCadence
		},
		format: func(v float64) string { return fmt.Sprintf("%.0f", v) },
		bind: func(ctx *Context, r Readout) Readout {
			if ctx.Track == nil || !perLegCadence(ctx.Track.Sport) {
				return r
			}
			r.unit = "spm"
			r.value = func(s fitactivity.Sample) (float64, bool) {
				return float64(s.Cadence) * 2, s.HasCadence
			}
			return r
		},
		scale: func(ctx *Context, r Readout) (gaugeScale, bool) {
			// No forced floor: not pedalling (or not striding, between
			// steps) is a real, meaningful 0, reported the same way Power's
			// own 0 W is -- the below-floor chevron plus the true, unclipped
			// number, not a floor bent down to meet it (see
			// robustGaugeScale's own doc comment). r.value is the
			// ALREADY-BOUND accessor bind installed above, so this
			// smooths and scales whichever unit -- rpm or the doubled spm --
			// this activity's sport actually resolved to, in cadenceGaugeStep's
			// own units, agreeing with what the number beside it prints.
			return robustGaugeScale(ctx, r.value, cadenceGaugeStep)
		},
	}
}

// perLegCadence reports whether a sport's FIT cadence counts one leg, so the
// figure the athlete recognises is twice it.
//
// The sport strings come from the FIT profile via fitactivity.Track.Sport, and
// are matched case-insensitively because they are a device's vocabulary rather
// than this program's.
func perLegCadence(sport string) bool {
	switch strings.ToLower(sport) {
	case "running", "walking", "hiking":
		return true
	}
	return false
}

// placeholder is what this readout shows with no reading behind it.
//
// A readout may name its own -- pace uses "--:--" so the panel keeps its shape
// when a runner stops, which for pace is a routine event rather than a sensor
// failure. It is an explicit field rather than something inferred from the
// template, because two readouts sharing a template shape do not thereby share
// a placeholder, and inferring it would hand one of them the other's.
func (r Readout) placeholder() string {
	if r.absent == "" {
		return ReadoutPlaceholder
	}
	return r.absent
}

// valueTemplate is the widest string this readout can show.
func (r Readout) valueTemplate() string {
	if r.template == "" {
		return "888"
	}
	return r.template
}

// Name identifies the panel.
//
// It is NOT the metric name. The two are different things that were briefly
// the same string: a panel's identity, which appears in the render summary
// telling a user which panels drew and which declined, and the name of the
// datum it reads, which is what the inspect report calls a column. Conflating
// them made the summary read "elapsed, Heart rate, Power" -- three panels
// named in two different styles because two of them were answering a different
// question.
func (r Readout) Name() string { return r.name }

// Accepts declines when the activity carries no reading of this metric at all.
//
// One rule, applied uniformly to every readout: an indoor ride declines its
// GPS panels, a walk declines power, a pool swim declines most of them, and in
// each case the layout closes up around the gap rather than showing a box that
// will never fill.
//
// It reads the report rather than the samples, so this answer and the one
// `fitdash inspect` prints for the same file cannot differ.
//
// Note the asymmetry with what Dynamic does. Absent for the WHOLE activity is
// answered here, by declining. Absent at one INSTANT -- a strap that dropped
// out for ninety seconds -- cannot be answered here at all, because the box is
// already assigned by then; that is a placeholder. Which of the two a panel
// gets is decided by when the absence is knowable, not by preference.
func (r Readout) Accepts(ctx *Context) bool {
	if r.carries != nil {
		return r.carries(ctx)
	}
	return ctx.Report.Carries(r.metric)
}

// HasGaugeRule reports whether this readout defines a GaugeStyleTrack range
// rule at all -- see the scale field's own doc comment. It needs no Context:
// scale is set, or left nil, once in the readout's own constructor, never
// inside bind, so which readouts are gauge candidates at all is a fact about
// the readout's own definition, not about any one activity or render.
//
// The render summary (writeGaugeSummary, cmd/render.go) calls this FIRST, to
// decide which placed readouts to report on -- HeartRate, Pace, Power and
// Cadence today. A readout with no gauge variant at all (Distance, which is
// never one of the four fluctuating metrics GaugeStyle exists for) is never
// reported as having "fallen back" to plain, which would misname a fact
// about its own definition as a fact about this activity's range.
func (r Readout) HasGaugeRule() bool {
	return r.scale != nil
}

// GaugeRangeText resolves this readout's own GaugeStyleTrack range against
// ctx, through the SAME two steps Prepare performs -- bind, then scale (see
// both fields' own doc comments) -- and formats it with this readout's own
// format function and unit, e.g. "100-190 bpm" or "4:00-7:00 min/km".
// Reusing r.format and r.unit, rather than a second formatting rule in cmd,
// is what lets the render summary report exactly the text the track's own
// endpoint labels draw (Static's p.format(p.scale.floor) / .ceiling, above)
// -- the two can never disagree about what a gauge's endpoints say.
//
// The range itself is the extra-smoothed MARKER series' own literal extent
// (robustGaugeScale/paceGaugeScale, via gaugeSeries -- see gaugeSeries' own
// doc comment, gauge.go), not the raw activity's: this is what makes the
// printed summary describe the same axis the marker actually travels,
// including that a single spike or a brief stop no longer stretches it.
//
// ok is false when the range rule finds no usable range for this activity --
// Prepare's own silent fallback to the plain readout, named here rather than
// left for a viewer to notice only as a track missing from one gauge and not
// its neighbours. It is also false for a readout with no gauge rule at all
// (HasGaugeRule false), but the render summary never reaches this method
// that way: it checks HasGaugeRule first, so that branch only guards a
// caller that skips the check.
func (r Readout) GaugeRangeText(ctx *Context) (string, bool) {
	if r.bind != nil {
		r = r.bind(ctx, r)
	}
	if r.scale == nil {
		return "", false
	}
	s, ok := r.scale(ctx, r)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s-%s %s", r.format(s.floor), r.format(s.ceiling), r.unit), true
}

// Prepare resolves sizes and positions once. See ElapsedPanel.Prepare for why
// this cannot wait until drawing.
func (r Readout) Prepare(ctx *Context, box Box) Painter {
	if r.bind != nil {
		r = r.bind(ctx, r)
	}
	p := &readoutPainter{Readout: r, box: box}

	// rowBox is what the three ordinary rows (label, value, unit) are laid
	// out against below -- box itself, unless GaugeStyleTrack resolves a
	// usable scale, in which case it is box with a strip reserved off its
	// bottom for the track and its two endpoint labels (see
	// reserveGaugeStrip). Every reference to the box from here on reads
	// rowBox, not box, so a gauge-styled readout's three rows centre in
	// whatever height the strip left them rather than the box's own full
	// height, and one code path serves both styles:
	// an ungauged readout (ctx.GaugeStyle == GaugeStylePlain, or this
	// readout's own scale is nil, or the activity has no usable range)
	// takes rowBox == box and this function is byte-for-byte what it always
	// was.
	rowBox := box
	if r.scale != nil {
		switch ctx.GaugeStyle {
		case GaugeStyleTrack:
			if s, ok := r.scale(ctx, r); ok {
				rowBox = p.reserveGaugeStrip(box, s, ctx.Fonts)
				if p.hasTrack {
					// Built once, here, through the SAME r.value r.scale
					// itself just used to derive s -- see gaugeSeries' own
					// doc comment (gauge.go) for why the marker needs its
					// own series at all, and readoutPainter.Dynamic for how
					// a per-frame query (keyed by f.At, not by frame index --
					// Dynamic receives an arbitrary instant, not necessarily
					// one this render's own Timeline ever produces, e.g. in
					// a test) reads it back.
					p.series = buildGaugeSeries(ctx.Track, gaugeMarkerWindow(ctx), r.value)
				}
			}
		case GaugeStyleDial:
			// Sits beside GaugeStyleTrack in this exact switch on purpose:
			// both branches call r.scale, the SAME rule (see GaugeStyle's
			// own doc comment, gauge.go), so the two styles can never
			// resolve two different ranges for what a viewer can switch
			// between and expect to read as the identical scale in a
			// different shape.
			if s, ok := r.scale(ctx, r); ok {
				rowBox = p.reserveGaugeDial(box, s, ctx.Fonts)
				if p.hasDial {
					p.series = buildGaugeSeries(ctx.Track, gaugeMarkerWindow(ctx), r.value)
				}
			}
		}
	}

	unit := rowBox.H
	if rowBox.W < unit {
		unit = rowBox.W
	}
	p.labelPx = unit * 0.14
	p.valuePx = unit * 0.34
	p.unitPx = unit * 0.12

	// The template is the widest a reading gets, not the current one: sizing
	// to the value would resize the text as the number changed.
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize(r.valueTemplate(), rowBox.W*0.6, p.valuePx); err == nil {
			p.valuePx = px
		}
		if px, err := ctx.Fonts.FitSize(r.label, rowBox.W*0.9, p.labelPx); err == nil {
			p.labelPx = px
		}
	}

	// The rows are spaced against `unit` and centred in rowBox, NOT placed at
	// fractions of its height. Two renders taught this.
	//
	// The rows must CLEAR one another: text is drawn centred on its y, so each
	// occupies roughly its own size either side of that point, and a value
	// sized at 0.42 of the box centred at 0.55 of its height reaches past a
	// unit label at 0.82. That is how "bpm" came to be printed across the
	// bottom of "160".
	//
	// And they must stay TOGETHER. Fractions of the box's height spread the
	// group as the box grows, so when the power panel declined and heart rate
	// inherited a column twice as tall, its label floated to the top and its
	// unit sank to the bottom with the reading marooned between them. Spacing
	// against the box's smaller dimension keeps the group the same shape
	// whatever rectangle the layout hands over -- which is the whole bargain
	// of a Box being a rectangle a panel must fit.
	centerY := rowBox.Y + rowBox.H/2
	p.valueY = centerY
	p.unitY = centerY + unit*0.30
	p.centerX = rowBox.X + rowBox.W/2

	// The CAPTION alone departs from `unit`: for every OTHER Readout it stays
	// centerY - unit*captionOffset, unchanged, because HeartRate/Pace/Power/
	// Cadence are not placed beside anything they need to agree with, and
	// TestReadout_RowsClearOneAnother is what keeps their three rows a group
	// as their own box grows. r.rule is true only for Distance, the one
	// readout layouts.go seats beside ElapsedPanel in a Row, and Row
	// siblings are guaranteed the same box HEIGHT -- never the same width,
	// which is what `unit` collapses to the moment this readout's own box is
	// narrower than it is tall. See captionOffset's own doc comment for the
	// mismatch that using `unit` here produced. rowBox.H >= unit always, so
	// this can only push the caption further from the value than `unit`
	// would have, never closer, and equals `unit` outright in the ordinary
	// case this readout is placed in, where its box is comfortably wider
	// than it is tall.
	capUnit := unit
	if p.rule {
		capUnit = rowBox.H
	}
	p.labelY = centerY - capUnit*captionOffset

	if p.rule {
		// Shares its geometry with ElapsedPanel's own rule via
		// captionRuleGeometry, so that when this readout sits beside the
		// clock (layouts.go), the two rules read as a matched pair rather
		// than one panel having a rule and its neighbour having none, and
		// retuning the gap or the thickness cannot happen in only one of
		// the two files. Each rule is still confined to its own box; see
		// Distance's doc comment for why that is the chosen answer rather
		// than one rule spanning both. Distance has no scale rule, so
		// rowBox is always box here in practice -- but this reads rowBox
		// regardless, on the same principle as the rest of Prepare.
		p.ruleY, p.ruleW, p.ruleH = captionRuleGeometry(capUnit, rowBox, p.labelY)
	}
	return p
}

// gaugeTrackStripFraction is how much of the readout's OWN box height the
// whole gauge strip -- the track plus its two endpoint labels -- reserves
// off the bottom, leaving the rest to the three ordinary rows exactly as an
// ungauged Readout draws them. A judgement call, checked at 1080p, 4K and in
// the portrait tree's narrower gauge box (roughly 442x123 at 1080p): tall
// enough that the axis, its marker and its labels are legible, restrained
// enough that the caption, value and unit above it still have room to
// breathe.
//
// Smaller than the fill-bar version of this panel used (0.34): a visual gate
// found the value text paying a real cost -- about a 25% size cut at 1080p --
// for that strip's own height, and a MARKER does not need the strip a FILLED
// BAR did. A bar had to be visibly thick to read as "filled" at all; a dot
// reads as a marker at any size against a track that can be a thin line
// (see gaugeAxisLineFraction), so the same legible axis, marker and endpoint
// labels fit in less height, and the reclaimed fraction goes back to the
// rows that shrank to make room for it.
const gaugeTrackStripFraction = 0.32

// gaugeTrackBandShare is the "instrument band" -- the axis line, the marker
// and the overflow chevrons together -- share of the strip's height
// (gaugeTrackStripFraction, above); the remainder goes to the endpoint
// labels drawn beneath it. Named "band" rather than the earlier "bar
// share" because nothing drawn in it is a bar any more: gaugeAxisLineFraction
// and gaugeNotchRadiusFactor both scale off THIS band, not off the box or
// the strip directly, so retuning how tall the band is moves the axis, the
// marker and the chevrons together rather than one at a time.
const gaugeTrackBandShare = 0.42

// gaugeAxisLineFraction is the visible axis line's own thickness, as a
// fraction of the band (gaugeTrackBandShare, above). Thin on purpose: the
// line is chrome -- the scale itself, drawn once in Static and washed absent
// in Dynamic (drawTrack, gauge.go) -- never a quantity, so it no longer
// needs the thickness a filled bar once did to read as "full" or "half
// full". The marker (gaugeNotchRadiusFactor) is what carries the reading now,
// sized independently and larger than this line on purpose.
const gaugeAxisLineFraction = 0.30

// gaugeNotchRadiusFactor sizes the marker's own radius as a fraction of the
// band (gaugeTrackBandShare, above) -- deliberately larger than half the
// band, so the dot's diameter exceeds the band's own height and it reads
// unmistakably as a marker sitting ON the axis rather than a thick point
// somewhere inside it. Checked at 1080p, where a gauge box is roughly
// 442x123: the band comes out around seventeen pixels tall, so a factor of
// 0.7 draws a marker about twenty-three pixels across, next to a sixteen-
// pixel-tall, fifteen-pixel-wide overflow chevron (gaugeCapWidthFraction)
// and a five-pixel-thick axis line -- three legibly different sizes as well
// as three different shapes, at the one resolution this project checks
// pixel budgets against by hand.
const gaugeNotchRadiusFactor = 0.7

// gaugeTrackInsetFraction insets the track from the box's own left and right
// edges, as a fraction of box.W, so an out-of-range overflow cap
// (gaugeCap, gauge.go) has somewhere to draw without leaving the box, and so
// the two endpoint labels -- anchored under each end -- do not run past it
// either.
const gaugeTrackInsetFraction = 0.05

// gaugeCapWidthFraction is the overflow cap's own width, as a fraction of
// the instrument band's own height (gaugeTrackBandShare, above) -- a small
// triangle rather than a wide arrow, sized off the one dimension it is
// guaranteed to fit against regardless of how wide the box is, and off the
// band rather than the (now much thinner) axis line so the chevron keeps
// its own legible size independent of how thin the line was drawn.
const gaugeCapWidthFraction = 0.9

// reserveGaugeStrip resolves the track's geometry from box and the already-
// derived scale s, sets it on p, and returns the SHRUNKEN box the three
// ordinary rows should centre in instead of box itself.
//
// It is the one place a degenerate BOX (not scale -- Prepare's own caller
// already refused a degenerate or unavailable RANGE before this is reached)
// is caught: if insetting for the overflow caps leaves no positive width for
// the track's own travel span, p.hasTrack is left false and box comes back
// unchanged, which is the identical "no usable range" fallback taken for a
// range the marker series itself refused (gaugeSeries.Range, gauge.go) -- a
// box too narrow to draw a track in is the same "nothing honest to show" as
// a range too narrow to compute one from, just discovered one step later.
func (p *readoutPainter) reserveGaugeStrip(box Box, s gaugeScale, fonts *FaceCache) Box {
	stripH := box.H * gaugeTrackStripFraction
	bandH := stripH * gaugeTrackBandShare
	labelH := stripH - bandH

	trackX := box.X + box.W*gaugeTrackInsetFraction
	trackW := box.W * (1 - 2*gaugeTrackInsetFraction)
	capW := bandH * gaugeCapWidthFraction
	spanX := trackX + capW
	spanW := trackW - 2*capW
	if spanW <= 0 {
		return box
	}

	p.scale = s
	p.hasTrack = true
	p.trackY = box.Y + box.H - stripH + bandH/2
	p.trackH = math.Max(1, bandH*gaugeAxisLineFraction)
	p.capHalfH = bandH / 2
	p.notchR = bandH * gaugeNotchRadiusFactor
	p.spanX = spanX
	p.spanW = spanW
	p.capW = capW
	p.endpointY = box.Y + box.H - labelH/2
	p.endpointPx = math.Max(1, labelH*0.7)

	if fonts != nil {
		maxW := spanW * 0.45
		lo, hi := p.format(s.floor), p.format(s.ceiling)
		if px, err := fonts.FitSize(lo, maxW, p.endpointPx); err == nil {
			p.endpointPx = px
		}
		if px, err := fonts.FitSize(hi, maxW, p.endpointPx); err == nil {
			p.endpointPx = px
		}
	}

	return Box{X: box.X, Y: box.Y, W: box.W, H: box.H - stripH}
}

// --- GaugeStyleDial ------------------------------------------------------
//
// See GaugeStyle's own doc comment (gauge.go) for what this style is. The
// arc is laid out BESIDE the caption/value/unit, not above or below them --
// see reserveGaugeDial's own doc comment for why, and for the geometry.

// dialMaxWidthFraction bounds how much of the box's own WIDTH the dial's own
// near-square sub-box may claim, as a fraction of box.W. The sub-box's side
// is ordinarily box.H (see reserveGaugeDial) -- a semicircle needs to be
// roughly as tall as it is wide to read as a dial at all, and box.H is the
// dimension that determines that -- but box.H alone would let a box nearer
// square (the portrait tree's own gauge boxes run closer to 2:1 than the
// landscape tree's roughly 3.6:1) hand the dial as much as half the box and
// leave the caption/value/unit starved of width. 0.55 is a judgement call,
// checked at 1080p, 4K and in the portrait tree: loose enough that it almost
// never binds against an ordinary wide, short gauge box (where box.H is
// already comfortably under it), tight enough that it does when the box
// stops being wide and short.
//
// An earlier version of this Painter reserved a fixed FRACTION OF THE BOX
// HEIGHT for the whole dial (a strip taken off the bottom, the arc centred
// in the box's full width above empty margins either side of it) rather
// than fitting the arc into a column beside the text -- see
// reserveGaugeDial's own doc comment for why that shape is wrong for a box
// this wide and short, the identical critique climb.go's own Prepare
// levels at an earlier version of ITS OWN geometry.
const dialMaxWidthFraction = 0.55

// dialGapFraction is the daylight between the dial's own reserved column and
// the text column beside it, as a fraction of box.W -- ClimbPanel's
// climbGapFraction (climb.go) restated for this panel's own two columns, so
// the arc and the caption/value/unit group never touch even in the
// narrowest box either tree places this panel in.
//
// Narrowed from an earlier 0.02 after a user, looking at the shipped dial,
// found the daylight this leaves excessive -- "a lot of space between the
// dial and the numeric values". 0.006 is still real, positive daylight
// (never zero: this is a fraction of box.W, not a pixel constant, so it
// scales with the frame the same way the value it is narrowing from did),
// checked at 1080p, 4K and in the portrait tree that it still reads as a
// gap rather than a collision -- the arc and the text column remain two
// visibly separate things, just closer together than before. This is the
// ONLY number this change touches on the dial: the beside arrangement
// itself, dialMaxWidthFraction, and every text size stay exactly as they
// were.
const dialGapFraction = 0.006

// dialLabelShare is the dial's own reserved column's endpoint-label share,
// as a fraction of the column's side (dialSide, reserveGaugeDial) -- the
// dial's analogue of gaugeTrackBandShare, smaller than the track's 0.42
// because a dial's two labels sit at one fixed height (the pivot's) rather
// than spanning the strip themselves, so they need less of it.
const dialLabelShare = 0.16

// dialArcLineFraction is the arc's own stroke width, as a fraction of the
// band height (bandH, below) -- thin, the same "chrome, not a quantity"
// reasoning gaugeAxisLineFraction documents for the track's own axis line.
const dialArcLineFraction = 0.05

// dialNeedleLineFraction sizes the needle's own stroke width, as a fraction
// of the resolved radius -- thin, on the same "one clean needle, no taper"
// instruction that ruled out a tapered or gradient-filled needle here.
const dialNeedleLineFraction = 0.045

// dialPivotRadiusFraction sizes the needle's pivot dot, as a fraction of the
// resolved radius.
const dialPivotRadiusFraction = 0.09

// dialCapWidthFraction and dialCapHalfHeightFraction size the reused
// gaugeCap overflow chevron off the resolved radius, rather than off a band
// height the way the track's own gaugeCapWidthFraction is -- the dial has no
// separate "instrument band" the way the track's thin axis does, so the
// radius itself is the one dimension both fractions have to scale against.
const (
	dialCapWidthFraction      = 0.12
	dialCapHalfHeightFraction = 0.14
)

// reserveGaugeDial is reserveGaugeStrip's own analogue for GaugeStyleDial: it
// resolves the arc's geometry from box and the already-derived scale s, sets
// it on p, and returns the shrunken box the three ordinary rows centre in.
//
// # Beside, not above
//
// An earlier version of this function took a horizontal STRIP off the
// bottom of box -- the full width, height shrunk -- and centred a circle in
// it, leaving the box's left and right margins empty: a semicircle is
// roughly as wide as it is tall, so a box that is wide and short (roughly
// 3.6:1 at 1080p) left most of its own width unused, and the text rows
// above the strip inherited whatever sliver of height survived, well under
// half of what GaugeStyleTrack leaves its own three rows for the identical
// box. This version instead reserves a near-square COLUMN off one side of
// box for the arc and its endpoint labels, and returns the rest of box --
// full height, narrower width -- for the caption/value/unit group beside
// it, the arrangement ClimbPanel's own Static/Dynamic already uses for the
// identical reason (see climb.go's type doc comment): a wide, short box
// suits "instrument here, reading there" laid out left-to-right far better
// than a vertical stack of the two.
//
// # Which side is sized first
//
// ClimbPanel's own Prepare records the lesson its geometry learned the hard
// way: reserving a FIXED FRACTION of the box for the instrument was wrong,
// because neither a bigger nor a smaller box has any correct fixed share --
// the fix there was to size the text columns (which have a real, bounded
// content -- "GAIN", "LOSS", a handful of digits) and let the track absorb
// whatever width is left, since a linear track has no "correct" size of its
// own to defend.
//
// The dial's own instrument is different: unlike a track, a semicircular
// arc DOES have a correct size to defend -- it must read as roughly square
// (dialMaxWidthFraction's own doc comment) or it stops looking like a dial
// at all. So here the two elements swap roles from ClimbPanel's own: the
// arc's side, dialSide, is resolved FIRST, off box.H (the one dimension a
// "roughly square" arc is naturally sized against), clamped by
// dialMaxWidthFraction so a box nearer square cannot let it claim more than
// roughly half of box.W -- and the caption/value/unit group is what absorbs
// whatever width is left, exactly the way it already shrinks to fit
// rowBox.W through FitSize in the plain (ungauged) case Prepare falls back
// to. Sizing the arc first and the text second, rather than the other way
// around, is the one choice this function makes that ClimbPanel's own
// Prepare does not: there the remainder-taker (the track) had no size of
// its own to protect, so the fixed-size elements went first; here it does,
// so it goes first instead.
func (p *readoutPainter) reserveGaugeDial(box Box, s gaugeScale, fonts *FaceCache) Box {
	dialSide := box.H
	if max := box.W * dialMaxWidthFraction; dialSide > max {
		dialSide = max
	}
	if dialSide <= 0 {
		return box
	}

	labelH := dialSide * dialLabelShare
	bandH := dialSide - labelH
	if bandH <= 0 {
		return box
	}

	// The stroke bulges outward by half its own width, so the nominal
	// radius has to shrink by that much to keep the arc's outer edge --
	// not just its centreline -- inside its own column, the same "draw
	// inside the box" contract every other panel keeps.
	//
	// r is bound by BOTH the column's height (bandH, above the label band)
	// and its width (the diameter 2r must fit inside dialSide, the column's
	// own side) -- min() picks whichever is tighter. In practice this is
	// always the width: bandH works out to roughly 0.84*dialSide (given
	// dialLabelShare=0.16), comfortably more than dialSide/2, so the column
	// is never taller than it needs to be for the radius the width already
	// allows. The min() is kept anyway as the honest statement of both
	// constraints, not a tuning that happens to favour one of them.
	strokeW := bandH * dialArcLineFraction
	r := math.Min(bandH, dialSide/2) - strokeW/2
	if r <= 0 {
		return box
	}

	capW := r * dialCapWidthFraction
	spanW := 2*r - 2*capW
	if spanW <= 0 {
		return box
	}

	gap := box.W * dialGapFraction
	rowBox := Box{X: box.X + dialSide + gap, Y: box.Y, W: box.W - dialSide - gap, H: box.H}
	if rowBox.W <= 0 {
		return box
	}

	p.scale = s
	p.hasDial = true
	// dialSide can be smaller than box.H when dialMaxWidthFraction clamps
	// it (a box nearer square than the ordinary wide, short gauge box) --
	// dialTop centres the whole arc-plus-labels group vertically in box.H
	// rather than pinning it to box's own top edge, so the group sits in
	// the middle of whatever height it was not given a full claim on. When
	// unclamped (dialSide == box.H, the ordinary case at every resolution
	// this project checks), dialTop == box.Y and this is exactly the old
	// bottom-pinned placement.
	dialTop := box.Y + (box.H-dialSide)/2
	p.dialCX = box.X + dialSide/2
	p.dialCY = dialTop + dialSide - labelH - strokeW/2
	p.dialR = r
	p.dialLineW = strokeW
	p.needleW = math.Max(1, r*dialNeedleLineFraction)
	p.dialPivotR = r * dialPivotRadiusFraction
	p.spanX = p.dialCX - r + capW
	p.spanW = spanW
	p.trackY = p.dialCY
	p.capW = capW
	p.capHalfH = r * dialCapHalfHeightFraction
	p.endpointY = p.dialCY + labelH/2 + strokeW/2
	p.endpointPx = math.Max(1, labelH*0.6)

	if fonts != nil {
		maxW := spanW * 0.45
		lo, hi := p.format(s.floor), p.format(s.ceiling)
		if px, err := fonts.FitSize(lo, maxW, p.endpointPx); err == nil {
			p.endpointPx = px
		}
		if px, err := fonts.FitSize(hi, maxW, p.endpointPx); err == nil {
			p.endpointPx = px
		}
	}

	return rowBox
}

type readoutPainter struct {
	Readout
	box     Box
	labelPx float64
	valuePx float64
	unitPx  float64
	labelY  float64
	valueY  float64
	unitY   float64
	centerX float64
	ruleY   float64
	ruleW   float64
	ruleH   float64

	// The gauge track's own geometry, set only when hasTrack -- see
	// reserveGaugeStrip. Static and Dynamic (gauge.go) read these and MUST
	// NOT write them; box, above, is untouched by any of this and still
	// names the FULL rectangle Prepare was handed.
	//
	// spanX, spanW bound the travel span the marker moves within and the
	// axis line/wash rectangle is drawn across -- named for what they ARE
	// now (a span the marker travels, not a bar that fills) rather than
	// carried over as "fillX/fillW" from the version of this panel that
	// drew a fill; see GaugeStyle's own doc comment for why the fill is
	// gone. trackH is the thin axis LINE's own thickness; capHalfH and
	// capW size the overflow chevrons (gaugeCap); notchR sizes the moving
	// marker (drawNotch). All four of trackH, capHalfH, capW and notchR are
	// scaled off the same "instrument band" height in reserveGaugeStrip,
	// never off each other, so retuning one cannot silently resize another.
	hasTrack       bool
	scale          gaugeScale
	trackY, trackH float64
	spanX, spanW   float64
	capW, capHalfH float64
	notchR         float64
	endpointY      float64
	endpointPx     float64

	// The dial's own geometry -- GaugeStyleDial (see its own doc comment,
	// gauge.go) -- set only when hasDial, by reserveGaugeDial. capW,
	// capHalfH, spanX, spanW and trackY, above, are
	// SHARED with the track: reserveGaugeDial resolves them to the arc's own
	// two ends and the pivot's own height, which is what lets
	// drawDialGauge's off-scale branches reuse gaugeCap completely
	// unchanged rather than inventing a second overflow shape.
	hasDial                        bool
	dialCX, dialCY, dialR          float64
	dialLineW, needleW, dialPivotR float64

	// series is the marker's own extra-smoothed reading (gaugeSeries,
	// gauge.go), built once in Prepare -- alongside p.scale, through the
	// SAME r.value -- when hasTrack or hasDial, never in Dynamic. See
	// gaugeSeries' own doc comment for why the marker is driven by this
	// rather than by p.value(f.Sample) directly, and readoutPainter.Dynamic
	// for how a per-frame query reads it back.
	series gaugeSeries
}

// Static draws the label and the unit, neither of which changes, and --
// for the readouts that opt in -- the rule beneath the caption, and -- for a
// readout drawing GaugeStyleTrack with a usable range -- the track's dim
// ghost and its two endpoint labels. All are fixed for the whole render:
// the ghost is the scale itself, not a reading, and the reading is what
// Dynamic draws over it every frame.
func (p *readoutPainter) Static(c *Canvas) {
	_ = c.Text(p.label, p.centerX, p.labelY, 0.5, 0.5, p.labelPx, c.Theme.Dim)
	_ = c.Text(p.unit, p.centerX, p.unitY, 0.5, 0.5, p.unitPx, c.Theme.Dim)
	if p.rule {
		c.Rect(Box{X: p.centerX - p.ruleW/2, Y: p.ruleY, W: p.ruleW, H: p.ruleH}, c.Theme.Dim)
	}
	if p.hasTrack {
		p.drawTrackGradient(c, c.Theme)
		p.drawGaugeEndpoints(c, c.Theme.Dim)
	}
	if p.hasDial {
		p.drawDialArcGradient(c, c.Theme)
		p.drawGaugeEndpoints(c, c.Theme.Dim)
	}
}

// drawGaugeEndpoints draws the two endpoint labels GaugeStyleTrack and
// GaugeStyleDial share verbatim -- both anchor them against the identical
// p.spanX/p.spanW/p.endpointY/p.endpointPx fields, which reserveGaugeStrip
// and reserveGaugeDial each resolve to their own shape's own two ends, so
// this is the ONE place either style's endpoint text is drawn rather than
// two copies that could drift apart. Always Dim: a label describes the
// SCALE, not a reading, so it does not go absent with the sample the way the
// track's axis and the dial's arc are washed in Dynamic, below.
func (p *readoutPainter) drawGaugeEndpoints(c *Canvas, col color.Color) {
	_ = c.Text(p.format(p.scale.floor), p.spanX, p.endpointY, 0, 0.5, p.endpointPx, col)
	_ = c.Text(p.format(p.scale.ceiling), p.spanX+p.spanW, p.endpointY, 1, 0.5, p.endpointPx, col)
}

// Dynamic draws the reading, or a placeholder where there is none, and --
// when hasTrack -- the track's own live state alongside it. The number
// itself is drawn from p.value(f.Sample) UNCHANGED in every case, including
// an out-of-range gauge reading: the printed figure is never clipped, only
// the marker's own POSITION can be out of reach (see drawGauge, gauge.go),
// which is precisely the division of labour GradientPanel already uses
// between its tilted line and the unexaggerated percentage beside it.
//
// The single presence check is correct inside a dropout WITHOUT a second
// condition on f.HasSample, and that is not an accident: the renderer
// guarantees a zero Sample whenever HasSample is false, and every presence
// flag on a zero Sample is false. That guarantee is what lets every panel in
// this project be written this way.
//
// # mv: the marker's own position, separate from v
//
// v still decides presence and the printed text, unchanged. But when a
// track or dial is drawn, the MARKER'S position comes from mv --
// p.series.At(f.At), the extra-smoothed reading gaugeSeries' own doc
// comment (gauge.go) explains -- not from v directly. mv falls back to v
// itself when the series has nothing at f.At (before the series' own first
// point, after its last, or inside a gap wider than
// fitactivity.DefaultMaxGap -- see gaugeSeries.At): v is already known
// present here, so a fraction computed from v instead of an unavailable
// smoothed value is the honest degrade, not a fabricated one, and it keeps
// a marker drawing at every instant the number does, exactly as before this
// series existed. It never happens for a real render's own frame times
// once the render is under way for more than gaugeMarkerWindow -- it is a
// genuine possibility only at a track's own extreme edges and in tests that
// construct a Frame directly, so it is not itself something a render gate
// need go looking for.
func (p *readoutPainter) Dynamic(c *Canvas, f Frame) {
	v, ok := p.value(f.Sample)
	if !ok {
		if p.hasTrack {
			// The wash, and no marker at all -- not a marker parked at the
			// floor, which would be exactly the confident lie CLAUDE.md
			// forbids, spread across a shape instead of a number: a marker
			// is a real, drawable value, and drawing one anywhere claims
			// "the reading is here", which this instant does not know.
			// "No marker anywhere" cannot be confused with "the reading is
			// at the low end", which is what made the earlier fill-based
			// version of this branch collide with the below-floor overflow
			// chevron (see GaugeStyle's own doc comment). The wash itself
			// still covers the track's own full length, exactly as
			// ClimbPanel's dropout wash covers its own bars (climb.go), so
			// a viewer can tell "gauge with no reading this instant" apart
			// from "gauge that failed to draw" -- but it is derived against
			// gaugeAbsentWashAlpha, not elevationAbsentFillAlpha: see that
			// function's own doc comment for why the two must not be
			// unified, this panel's wash sits on top of the dim ghost
			// (Theme.Dim), never directly on Theme.Background.
			p.drawTrack(c, Fade(c.Theme.Absent, gaugeAbsentWashAlpha(c.Theme)))
		}
		if p.hasDial {
			// The identical policy, drawn as an arc instead of a rectangle:
			// no needle at all -- a needle parked at the floor would be a
			// real, drawable value, so parking it there is the confident
			// lie CLAUDE.md forbids, drawn in geometry instead of digits --
			// plus the arc washed its own full sweep, so a "no reading this
			// instant" dial stays visually distinct from one that failed to
			// draw at all -- this is the identical policy's own load-bearing
			// check for the dial shape, not decoration.
			p.drawDialArc(c, Fade(c.Theme.Absent, gaugeAbsentWashAlpha(c.Theme)))
		}
		_ = c.Text(p.placeholder(), p.centerX, p.valueY, 0.5, 0.5, p.valuePx, c.Theme.Absent)
		return
	}
	if p.hasTrack || p.hasDial {
		mv := v
		if smoothed, ok := p.series.At(f.At); ok {
			mv = smoothed
		}
		if p.hasTrack {
			p.drawGauge(c, v, mv)
		}
		if p.hasDial {
			p.drawDialGauge(c, v, mv)
		}
	}
	_ = c.Text(p.format(v), p.centerX, p.valueY, 0.5, 0.5, p.valuePx, c.Theme.Foreground)
}

// ReadoutPlaceholder is what a reading shows when the sensor had nothing at
// this instant. Drawn in Theme.Absent so it cannot be mistaken for a value,
// and never as "0", which is a reading this program refuses to invent.
const ReadoutPlaceholder = "--"

// PacePlaceholder is the pace readout's own placeholder, shaped like the
// reading it stands in for so the panel does not jump when a runner stops.
const PacePlaceholder = "--:--"

// Distance reads cumulative distance, in kilometres.
//
// Always kilometres, never switching to metres below one. The unit is drawn in
// the STATIC layer -- it does not change across a render -- so a readout that
// began in metres and crossed into kilometres would leave "m" rasterized under
// a figure that had become kilometres, and nothing would report it. "0.34 km"
// is a slightly odd way to start a run and an honest one.
//
// This is the one Readout that sets rule: true. Elapsed time and distance are
// the two metrics that only ever increase across an activity -- everything
// else on the dashboard fluctuates -- which is why layouts.go seats this
// readout beside ElapsedPanel rather than in the ordinary gauge column, and a
// panel cannot reach into a sibling's box to draw a rule spanning both (Box's
// own contract: a panel draws inside its box and never outside it, and
// Prepare never sees a sibling to begin with). Giving this readout the same
// rule ElapsedPanel already draws, at the same offset beneath the caption, is
// what makes the two read as a matched pair through repetition of the same
// chrome rather than through one continuous stroke neither panel could draw
// alone. It costs nothing where Distance is placed alone (the bottom band's
// fallback slot): a lone rule under a lone caption looks exactly like
// ElapsedPanel's own, not out of place.
func Distance() Readout {
	return Readout{
		name: "distance", label: "DISTANCE", unit: "km", metric: inspect.MetricDistance,
		template: distanceTemplate, rule: true,
		value: func(s fitactivity.Sample) (float64, bool) {
			return s.Distance / 1000, s.HasDistance
		},
		format: func(v float64) string { return fmt.Sprintf("%.2f", v) },
		bind:   bindDistancePrecision,
	}
}

// distanceTemplate is the widest string a SHORT activity's distance draws:
// two integer digits and two decimals, four digit places in all.
// bindDistancePrecision trades one of those decimals for an extra integer
// digit as a reading (or a render's own churn) demands more room, which is
// what lets a hundred-kilometre ride grow into a box no wider than this one.
const distanceTemplate = "88.88"

// distanceDigitCount counts the digit characters in a template, ignoring the
// point -- so distanceLayout reads distanceTemplate's own shape (its total
// digit budget, and how many of those digits sit before the point) rather
// than restating either as a bare number that could drift from it. Compare
// maxPaceSeconds, which does the same for the pace template.
func distanceDigitCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// distanceTemplateIntDigits is how many integer-part digits template itself
// reserves, read off the template rather than assumed.
func distanceTemplateIntDigits(template string) int {
	i := strings.IndexByte(template, '.')
	if i < 0 {
		i = len(template)
	}
	return distanceDigitCount(template[:i])
}

// coarseDistanceStep is how much activity a frame must cover before distance
// drops to one decimal.
//
// Distance is not smoothed -- averaging an accumulator buys nothing -- but it
// still churns when the activity is compressed. At 480x a frame advances
// sixteen seconds, which is something like eighty metres, so a readout in
// hundredths of a kilometre changes both decimals every single frame and is
// unreadable for a different reason than a gauge is. One decimal changes far
// less often and loses precision nobody could read at that speed anyway.
//
// Five seconds is the threshold because below it a frame covers roughly twenty
// metres, which the second decimal can still track without flickering.
const coarseDistanceStep = 5 * time.Second

// distanceDecimalsFor is the one place that turns an integer-digit count
// into a decimal count: whatever the budget has left once intDigits places
// are spent on the integer part, and never fewer than zero. distanceLayout
// calls this every time it needs decimals rather than restating "budget minus
// intDigits, floored at zero" itself, which is what keeps its two call sites
// below -- the ordinary case and the coarse override -- from being two
// hand-written rules that could quietly drift apart.
func distanceDecimalsFor(intDigits int) int {
	d := distanceDigitCount(distanceTemplate) - intDigits
	if d < 0 {
		d = 0
	}
	return d
}

// distanceLayout picks how many integer digits and how many decimals a
// distance readout shows, from two pressures resolved by ONE rule rather
// than two independent ones that could disagree:
//
//   - maxKm, the widest reading the ACTIVITY will actually reach, known from
//     the report before the render starts. Past 99.99 the default template
//     ("88.88") has no room for a third integer digit, and printing one
//     anyway ("100.00") draws six characters into a box sized for five --
//     the same class of overflow the pace fix closed, but for distance the
//     honest fix is to trade a decimal for the extra digit, not to hide a
//     hundred-kilometre ride behind a placeholder the way a stopped runner
//     hides behind PacePlaceholder. A hundred-kilometre activity is real
//     data, not a runner standing still.
//   - coarse, whether the render is compressed past coarseDistanceStep, where
//     even a SHORT activity's second decimal changes every frame and is
//     unreadable. This is not about the activity's length at all, so it sets
//     its own floor of one extra integer digit -- matching what this readout
//     always reserved under compression before magnitude was considered --
//     and lets the magnitude pressure above win when the activity genuinely
//     needs more.
//
// Past 1000 km the decimal goes entirely rather than the reserved width
// growing. At that scale a tenth of a kilometre is noise, and dropping it
// keeps four integer digits inside the SAME four-digit budget the template
// always had, so the box never has to reserve more room than it started
// with. The ladder is therefore two decimals below 100 km, one below 1000,
// and none at or above it, which holds the width until 99999 km -- past any
// activity anyone records.
//
// Stated as a rule rather than three thresholds, because the thresholds are
// consequences and the rule is the thing that must stay true: the decimals
// are whatever the digits the activity actually reaches leave room for
// inside the budget (distanceDecimalsFor, above). Rewriting this as three
// hand-written cases would let them drift out of step with the budget the
// way a derivation cannot.
//
// The loop re-derives decimals from intDigits on every pass and checks the
// ROUNDED reading against the new threshold, not the raw one: a reading like
// 999.96 rounds to "1000.0" at one decimal, which needs a fourth integer
// digit that the raw value's own three-digit magnitude would not have asked
// for. Checking the rounded figure is what keeps the boundary itself safe.
func distanceLayout(maxKm float64, coarse bool) (intDigits, decimals int) {
	baseInt := distanceTemplateIntDigits(distanceTemplate)

	intDigits = baseInt
	for {
		decimals = distanceDecimalsFor(intDigits)
		scale := math.Pow10(decimals)
		rounded := math.Round(maxKm*scale) / scale
		if rounded < math.Pow10(intDigits) {
			break
		}
		intDigits++
	}

	if coarse && intDigits < baseInt+1 {
		intDigits = baseInt + 1
		decimals = distanceDecimalsFor(intDigits)
	}
	return intDigits, decimals
}

// bindDistancePrecision resolves distanceLayout against this render's
// timeline (for coarse) and this activity's own recorded maximum (for
// magnitude), then builds the template and format that layout implies.
//
// It changes the FORMAT and not the value, so the number is still the
// distance at this instant -- shown to the precision the render and the
// activity's own scale can support, rather than a precision that either
// flickers or overflows.
func bindDistancePrecision(ctx *Context, r Readout) Readout {
	coarse := false
	if ctx.Timeline.FPS() > 0 {
		step := time.Duration(ctx.Timeline.MaxSpeedup() / ctx.Timeline.FPS() * float64(time.Second))
		coarse = step >= coarseDistanceStep
	}

	maxKm := 0.0
	if m, ok := ctx.Report.Metric(inspect.MetricDistance); ok && m.Present > 0 {
		maxKm = m.Max / 1000
	}

	intDigits, decimals := distanceLayout(maxKm, coarse)
	r.template = distanceTemplateFor(intDigits, decimals)
	r.format = func(v float64) string { return fmt.Sprintf("%.*f", decimals, v) }
	return r
}

// distanceTemplateFor builds the widest string a distance with this many
// integer digits and decimals can print. No decimal point when there are no
// decimals: "%.0f" prints none, and a template carrying one would reserve a
// column the value never fills.
func distanceTemplateFor(intDigits, decimals int) string {
	t := strings.Repeat("8", intDigits)
	if decimals > 0 {
		t += "." + strings.Repeat("8", decimals)
	}
	return t
}

// StepLength reads Sample.StepLength -- the standard FIT Running Dynamics
// field, native, in millimetres (Sample.StepLength's own doc comment,
// fitactivity) -- and presents it in centimetres, at the user's own request:
// a raw millimetre figure ("1187") does not read as a natural quantity on a
// dashboard, and this project follows the same "convert at the presentation
// layer, leave the recorded field alone" discipline Cadence (rpm doubled to
// spm for a running sport) and Pace (speed inverted to time-per-kilometre)
// already keep -- fitactivity's own field stays in the unit FIT recorded it
// in, and this readout's value/format pair is where the conversion happens,
// once, for the number this panel actually prints.
//
// This is context for reading the balance bars alongside pace, not a
// balance itself -- see gaugeBalanceColumn's own doc comment (gauges.go) for
// where it is seated and why it is an ordinary magnitude readout rather than
// a fifth bar: a step length has no natural "even" midpoint the way a
// left/right split does, so BalancePanel's whole fixed-centre-scale
// machinery does not apply to it.
//
// videofx carries this same field only in its fitdump debug dump, labelled
// "step length (mm)" -- a raw diagnostic dump, not a HUD panel a viewer
// would read, so there is no established on-screen vocabulary to defer to
// beyond the field's own plain-English name, which this readout's caption
// matches.
//
// No zero-refusal: unlike the four balance panels' own "zero is not a
// balance" rule, a step length of zero is a real, meaningful reading -- not
// striding at this instant -- the same judgement Cadence and Power already
// make about their own zero (see Cadence's own doc comment).
func StepLength() Readout {
	return Readout{
		name: "step-length", label: "STEP LENGTH", unit: "cm", metric: inspect.MetricStepLength,
		template: stepLengthTemplate,
		value: func(s fitactivity.Sample) (float64, bool) {
			return s.StepLength / 10, s.HasStepLength
		},
		format: func(v float64) string { return fmt.Sprintf("%.0f", v) },
	}
}

// stepLengthTemplate is the widest string this readout ever prints in
// centimetres: three digits, wide enough for an elite sprinter's stride
// (a step length in the low 200s of centimetres is a real, recorded value,
// not an outlier this panel needs to special-case) with headroom to spare,
// while still shrinking cleanly for a child's or a walker's much shorter
// step. Whole centimetres only -- no decimal -- which is the readability the
// user's own choice of unit was for in the first place; a tenth of a
// centimetre is not a distinction a viewer could act on.
const stepLengthTemplate = "888"

// paceTemplate is the widest string the pace readout ever draws: two digits
// of minutes and two of seconds. It is the single place that shape lives --
// the box sizing in Prepare and the speed floor below (maxPaceSeconds,
// minPaceSpeed) both derive from this one string, so the two cannot drift
// into disagreeing about what the panel is actually capable of printing.
const paceTemplate = "88:88"

// maxPaceSeconds is the greatest seconds-per-kilometre paceTemplate has room
// for, read off the template's own shape rather than restated as a number of
// its own: however many digit places sit before the colon (two, for
// "88:88") bound the minutes at all-nines, and the seconds column -- always
// a mod-60 remainder, so always two digits and under sixty -- adds 59. For
// "88:88" that is 99 minutes and 59 seconds, 99*60+59 = 5999.
func maxPaceSeconds(template string) int {
	i := strings.IndexByte(template, ':')
	if i <= 0 {
		// No minutes column to read a width from -- nothing to bound, so
		// let every positive speed through rather than refuse all of them.
		return math.MaxInt32
	}
	maxMinutes := 1
	for range template[:i] {
		maxMinutes *= 10
	}
	maxMinutes--
	return maxMinutes*60 + 59
}

// minPaceSpeed is the slowest speed, in m/s, whose pace still fits
// paceTemplate: 1000 metres divided by the template's own ceiling. Below it
// the reciprocal needs more minutes than the template reserves digits for.
func minPaceSpeed(template string) float64 {
	return 1000 / float64(maxPaceSeconds(template))
}

// Pace reads speed and shows it as time per kilometre.
//
// Pace is the number runners actually think in, and it is the reciprocal of
// the recorded quantity, which is where the care goes.
//
// A speed of zero is a real reading and has NO pace: standing still is not
// infinitely slow, it is undefined. Dividing anyway yields either a division
// by zero or, for a speed a hair above it, a nonsensical multi-hour figure
// presented with the same confidence as a real one. So a stopped runner gets
// the placeholder -- which is the absent-data rule reached from an unusual
// direction, since here the reading is present and it is the DERIVED value
// that does not exist.
//
// A bare "speed > 0" is not enough to catch every such case, though: the
// render loop smooths speed over a window (see render.smoothSample), and a
// window that is mostly a stopped runner with a handful of moving samples
// averages to a speed that is genuinely positive -- never zero -- while
// still being far too small for a real pace. minPaceSpeed is the floor below
// which that reciprocal no longer fits paceTemplate, and it is applied here,
// in the presence decision, rather than left as a formatting special case:
// see the value closure below.
//
// The format matches videofx's, M:SS per kilometre, since both programs read
// the same activities.
func Pace() Readout {
	minSpeed := minPaceSpeed(paceTemplate)
	return Readout{
		name: "pace", label: "PACE", unit: "min/km", metric: inspect.MetricSpeed,
		template: paceTemplate, absent: PacePlaceholder,
		value: func(s fitactivity.Sample) (float64, bool) {
			// Speed itself is carried through; the reciprocal happens in
			// format. The presence decision belongs here, which is why the
			// zero-speed case -- and now the case of a speed too small for
			// its pace to fit the template -- is answered as "no reading"
			// rather than as a formatting special case downstream.
			return s.Speed, s.HasSpeed && s.Speed >= minSpeed
		},
		format: FormatPace,
		scale: func(ctx *Context, r Readout) (gaugeScale, bool) {
			// r.value here reports SPEED (see the value closure above), and
			// paceGaugeScale sweeps on that speed while snapping and
			// labelling in pace -- see its own doc comment (gauge.go) for
			// "sweep on speed, label in pace" in full. FormatPace (this
			// readout's own format) already turns a speed back into pace
			// text, so the endpoint labels Static draws need no
			// metric-specific handling either.
			return paceGaugeScale(ctx, r.value)
		},
	}
}

// FormatPace renders a speed in m/s as M:SS per kilometre.
//
// A non-positive speed, or one slow enough that its pace would not fit
// paceTemplate, returns the placeholder rather than printing a reciprocal
// the template has no digits for. See Pace: standing still has no pace, and
// neither -- for the panel's own purposes -- does a speed so close to it
// that the figure would need six characters where "88:88" reserves five.
// This mirrors the guard in Pace's value closure rather than replacing it:
// value decides presence (and therefore colour), this is the belt-and-braces
// half that keeps FormatPace itself safe for any caller.
func FormatPace(speedMS float64) string {
	if speedMS <= 0 {
		return PacePlaceholder
	}
	totalSec := int(math.Round(1000 / speedMS))
	if totalSec > maxPaceSeconds(paceTemplate) {
		return PacePlaceholder
	}
	return fmt.Sprintf("%d:%02d", totalSec/60, totalSec%60)
}
