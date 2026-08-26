package panel

import (
	"fmt"

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

	// carries overrides the default coverage test for panels whose "does this
	// activity have it" question the report cannot answer. See Power.
	carries func(*Context) bool

	// bind, when set, rebuilds the value accessor from the render context.
	// Power needs it: which sensor to read is a render-wide choice, and a
	// closure fixed at construction could not know it.
	bind func(*Context, Readout) Readout
}

// HeartRate reads the standard FIT heart rate field.
func HeartRate() Readout {
	return Readout{
		name: "heart-rate", label: "HEART RATE", unit: "bpm", metric: inspect.MetricHeartRate,
		value: func(s fitactivity.Sample) (float64, bool) {
			return float64(s.HeartRate), s.HasHeartRate
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

// Cadence reads the standard FIT cadence field.
func Cadence() Readout {
	return Readout{
		name: "cadence", label: "CADENCE", unit: "rpm", metric: inspect.MetricCadence,
		value: func(s fitactivity.Sample) (float64, bool) {
			return float64(s.Cadence), s.HasCadence
		},
		format: func(v float64) string { return fmt.Sprintf("%.0f", v) },
	}
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
		if px, err := ctx.Fonts.FitSize("888", box.W*0.6, p.valuePx); err == nil {
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
	_ = c.Text(ReadoutPlaceholder, p.centerX, p.valueY, 0.5, 0.5, p.valuePx, c.Theme.Absent)
}

// ReadoutPlaceholder is what a reading shows when the sensor had nothing at
// this instant. Drawn in Theme.Absent so it cannot be mistaken for a value,
// and never as "0", which is a reading this program refuses to invent.
const ReadoutPlaceholder = "--"
