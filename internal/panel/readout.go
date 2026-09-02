package panel

import (
	"fmt"
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
	}
}

// Power reads whichever power sensor Context.PowerSource selects.
//
// A FIT file can carry two: the standard Record.power field, and a developer
// field a footpod such as a Stryd registers under its own name. They are
// different sensors and they disagree -- on the recording this was built
// against, native peaks at 568 W and Stryd at 374 -- so this is a choice about
// which instrument to believe, not about formatting.
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

// Prepare resolves sizes and positions once. See ElapsedPanel.Prepare for why
// this cannot wait until drawing.
func (r Readout) Prepare(ctx *Context, box Box) Painter {
	if r.bind != nil {
		r = r.bind(ctx, r)
	}
	p := &readoutPainter{Readout: r, box: box}

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	p.labelPx = unit * 0.14
	p.valuePx = unit * 0.34
	p.unitPx = unit * 0.12

	// The template is the widest a reading gets, not the current one: sizing
	// to the value would resize the text as the number changed.
	if ctx.Fonts != nil {
		if px, err := ctx.Fonts.FitSize(r.valueTemplate(), box.W*0.6, p.valuePx); err == nil {
			p.valuePx = px
		}
		if px, err := ctx.Fonts.FitSize(r.label, box.W*0.9, p.labelPx); err == nil {
			p.labelPx = px
		}
	}

	// The rows are spaced against `unit` and centred in the box, NOT placed at
	// fractions of the box's height. Two renders taught this.
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
	centerY := box.Y + box.H/2
	p.valueY = centerY
	p.labelY = centerY - unit*0.30
	p.unitY = centerY + unit*0.30
	p.centerX = box.X + box.W/2
	return p
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
}

// Static draws the label and the unit, neither of which changes.
func (p *readoutPainter) Static(c *Canvas) {
	_ = c.Text(p.label, p.centerX, p.labelY, 0.5, 0.5, p.labelPx, c.Theme.Dim)
	_ = c.Text(p.unit, p.centerX, p.unitY, 0.5, 0.5, p.unitPx, c.Theme.Dim)
}

// Dynamic draws the reading, or a placeholder where there is none.
//
// The single presence check is correct inside a dropout WITHOUT a second
// condition on f.HasSample, and that is not an accident: the renderer
// guarantees a zero Sample whenever HasSample is false, and every presence
// flag on a zero Sample is false. That guarantee is what lets every panel in
// this project be written this way.
func (p *readoutPainter) Dynamic(c *Canvas, f Frame) {
	if v, ok := p.value(f.Sample); ok {
		_ = c.Text(p.format(v), p.centerX, p.valueY, 0.5, 0.5, p.valuePx, c.Theme.Foreground)
		return
	}
	_ = c.Text(p.placeholder(), p.centerX, p.valueY, 0.5, 0.5, p.valuePx, c.Theme.Absent)
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
func Distance() Readout {
	return Readout{
		name: "distance", label: "DISTANCE", unit: "km", metric: inspect.MetricDistance,
		template: distanceTemplate,
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
