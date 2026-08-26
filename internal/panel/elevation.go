package panel

import (
	"fmt"
	"math"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// ElevationPanel draws the activity's elevation profile against distance, with
// a playhead marking where on it the activity currently is.
//
// This is the panel that would have walked into the axis-origin trap. The
// profile's x axis runs from the model's StartDistance to its TotalDistance,
// not from zero, and the difference is invisible for a whole activity recorded
// from its own start line -- which is why the sibling HUD project got away with
// dividing by the total alone until a clip-scoped profile squeezed itself into
// the right-hand fifth of its box.
//
// Here the axis is two fields on the Painter, and the SAME method places the
// profile, the labels and the playhead. They cannot be scaled against
// different origins because there is only one origin to scale against.
type ElevationPanel struct{}

// Name identifies the panel.
func (ElevationPanel) Name() string { return "elevation" }

// Accepts requires BOTH elevation and distance.
//
// Distance is not incidental: it is the profile's x axis. An activity with
// barometric elevation but no distance -- a treadmill session, a rowing
// machine -- has readings with nothing to plot them against, and a profile
// drawn against sample index would be a different graph wearing this one's
// labels.
func (ElevationPanel) Accepts(ctx *Context) bool {
	return ctx.Report.Carries(inspect.MetricElevation) && ctx.Report.Carries(inspect.MetricDistance)
}

// profileSampleStep is how many pixels apart the profile is sampled.
//
// Two: finer than one point per pixel column buys nothing a display can show,
// and the profile is a Painter field computed once, so this trades a little
// smoothing for a shorter polyline the static pass strokes once.
const profileSampleStep = 2.0

// Prepare builds the elevation model and lays out the plot, once.
func (ElevationPanel) Prepare(ctx *Context, box Box) Painter {
	p := &elevationPainter{box: box}

	// Tune the smoothing against the device's own ascent and descent totals
	// where the file reported them. Barometric elevation is noisy enough that
	// a raw per-sample sum wildly overcounts climbing, and matching a figure
	// the watch already published is more trustworthy than any constant
	// chosen here.
	var opts fitactivity.ElevationOptions
	if ctx.Track != nil && ctx.Track.HasElevationTotals {
		opts.TargetGain, opts.TargetLoss = ctx.Track.TotalAscent, ctx.Track.TotalDescent
	}
	m := fitactivity.BuildElevationModel(ctx.Track, opts)
	if m == nil || m.Empty() {
		return p
	}

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	p.labelPx = unit * 0.16

	p.axisStart, p.axisEnd = m.StartDistance(), m.TotalDistance()
	p.minElev, p.maxElev = m.Range()
	p.startLabel, p.endLabel = formatDistance(p.axisStart), formatDistance(p.axisEnd)
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

	if p.axisEnd <= p.axisStart || p.maxElev <= p.minElev {
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

	if !p.flat {
		n := int(p.plot.W/profileSampleStep) + 1
		p.xs = make([]float64, 0, n)
		p.ys = make([]float64, 0, n)
		for i := 0; i < n; i++ {
			frac := float64(i) / float64(n-1)
			d := p.axisStart + frac*(p.axisEnd-p.axisStart)
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

	// The axis. These two are the whole point of this panel's design: every
	// x coordinate it draws -- the profile, the end labels, the playhead --
	// comes from xForDistance, which is the only place they are read.
	axisStart, axisEnd float64
	minElev, maxElev   float64
}

// xForDistance places a distance on the plot's x axis.
//
// The axis spans axisStart..axisEnd, NOT 0..axisEnd. Dividing by the total
// alone is correct only while the profile happens to begin at zero, and is the
// specific mistake this panel is shaped to make impossible: one method, used
// by the profile, the labels and the playhead alike.
func (p *elevationPainter) xForDistance(d float64) float64 {
	span := p.axisEnd - p.axisStart
	if span <= 0 {
		return p.plot.X
	}
	frac := (d - p.axisStart) / span
	return p.plot.X + math.Min(1, math.Max(0, frac))*p.plot.W
}

// yForElevation places an elevation on the plot's y axis, higher ground
// nearer the top.
func (p *elevationPainter) yForElevation(e float64) float64 {
	span := p.maxElev - p.minElev
	if span <= 0 {
		return p.plot.Y + p.plot.H/2
	}
	frac := (e - p.minElev) / span
	return p.plot.Y + p.plot.H - math.Min(1, math.Max(0, frac))*p.plot.H
}

// Static draws the profile and the axis labels, all invariant.
func (p *elevationPainter) Static(c *Canvas) {
	if p.plot.W <= 0 || p.plot.H <= 0 {
		return
	}
	if len(p.xs) >= 2 {
		c.Polyline(p.xs, p.ys, p.lineW, c.Theme.Dim)
	}

	// Elevation labels sit against the plot's left edge, at the extremes they
	// name -- so a reader can see which number belongs to which end of the
	// trace without a legend.
	_ = c.Text(p.highLabel, p.plot.X-p.labelPx*0.3, p.plot.Y, 1, 0.5, p.labelPx, c.Theme.Dim)
	_ = c.Text(p.lowLabel, p.plot.X-p.labelPx*0.3, p.plot.Y+p.plot.H, 1, 0.5, p.labelPx, c.Theme.Dim)

	// Distance labels are placed BY xForDistance, at the distances they name.
	// That is what ties them to the axis the profile and the playhead use: a
	// label positioned by any other arithmetic could disagree with the trace
	// beneath it, and nothing would report the disagreement.
	labelY := p.plot.Y + p.plot.H + p.labelPx*1.0
	_ = c.Text(p.startLabel, p.xForDistance(p.axisStart), labelY, 0, 0.5, p.distPx, c.Theme.Dim)
	_ = c.Text(p.endLabel, p.xForDistance(p.axisEnd), labelY, 1, 0.5, p.distPx, c.Theme.Dim)
}

// Dynamic draws the playhead: where on the profile the activity now is.
//
// When this instant has no distance reading, no playhead is drawn -- and the
// profile and its labels remain, because they are still true. This is not the
// silent-nothing the panel contract forbids: the panel HAS drawn, and what is
// missing is a position on it, which there is honestly no way to mark. Drawing
// a playhead at the last known distance would assert a position nobody
// measured.
func (p *elevationPainter) Dynamic(c *Canvas, f Frame) {
	if p.flat || len(p.xs) < 2 || !f.Sample.HasDistance {
		return
	}
	d := f.Sample.Distance
	if d < p.axisStart || d > p.axisEnd {
		return
	}
	x := p.xForDistance(d)
	c.Rect(Box{X: x - p.headW/2, Y: p.plot.Y, W: p.headW, H: p.plot.H}, c.Theme.Foreground)

	// And a dot ON the trace, which is what makes the playhead read as a
	// position on the profile rather than a line across a picture.
	frac := (x - p.plot.X) / p.plot.W
	i := int(math.Round(frac * float64(len(p.ys)-1)))
	if i >= 0 && i < len(p.ys) {
		c.Circle(x, p.ys[i], p.dotR, c.Theme.Accent)
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
