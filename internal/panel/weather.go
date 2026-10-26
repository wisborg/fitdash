package panel

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/weather"
)

// Weather is the weather the activity was done in, as --weather fetched it,
// with the direction the athlete was moving at each sample, for the
// weather panel to turn the wind to.
//
// Built once, by NewWeather, and only read after: the headings are worked
// out over the whole track, which no frame could afford.
type Weather struct {
	Report *weather.Report

	times      []time.Time
	heading    []float64
	hasHeading []bool
}

// NewWeather is the weather of r for track t.
func NewWeather(t *fitactivity.Track, r *weather.Report) *Weather {
	w := &Weather{Report: r}
	w.heading, w.hasHeading = weather.Headings(t)
	w.times = make([]time.Time, len(t.Samples))
	for i, s := range t.Samples {
		w.times[i] = s.Time
	}
	return w
}

// At is the weather where and when s was.
func (w *Weather) At(s fitactivity.Sample) (weather.Conditions, bool) {
	return w.Report.At(s.Lat, s.Lon, s.HasGPS, s.Time)
}

// HeadingAt is the direction of travel at t: the nearest sample's that has
// one, so a stop at a crossing keeps the heading the athlete stopped with.
func (w *Weather) HeadingAt(t time.Time) (float64, bool) {
	n := len(w.times)
	i := sort.Search(n, func(i int) bool { return !w.times[i].Before(t) })
	for d := 0; d < n; d++ {
		for _, j := range []int{i - d, i + d} {
			if j >= 0 && j < n && w.hasHeading[j] {
				return w.heading[j], true
			}
		}
		if i-d < 0 && i+d >= n {
			break
		}
	}
	return 0, false
}

// weatherPanelName is the weather panel's Name, which internal/render's
// keep filter compares against.
const weatherPanelName = "weather"

// IsWeatherPanel reports whether p is the weather panel, for the render's
// keep filter to leave it out of a render not asked for --weather: it is
// not missing from the activity, it was not asked for.
func IsWeatherPanel(p Panel) bool {
	_, ok := p.(WeatherPanel)
	return ok
}

// WeatherPanel shows the weather at the playhead: the air's temperature,
// the wind's speed, and an arrow for the wind turned to the athlete's own
// direction of travel -- up the dial is straight ahead, so a headwind's
// arrow points down at them and a tailwind's up and away. Which it mostly
// is, head, tail or cross, is said under the speed.
//
// The data are Open-Meteo's, and their licence asks for a credit beside
// them, which the panel draws in its corner.
type WeatherPanel struct{}

func (WeatherPanel) Name() string { return weatherPanelName }

// Accepts is whether there is weather to show: --weather was given and an
// answer came back for an activity with positions.
func (WeatherPanel) Accepts(ctx *Context) bool { return ctx.Weather != nil }

// Proportions of the panel, as fractions of its smaller side or its width.
const (
	weatherDialFraction    = 0.40 // the dial's radius, of the smaller side
	weatherTextWidth       = 0.55 // the readings' column, of the width
	weatherValueFraction   = 0.34 // the temperature's size, of the smaller side
	weatherWindFraction    = 0.22 // the wind's size
	weatherKindFraction    = 0.15 // "headwind"'s size
	weatherCreditFraction  = 0.09 // the credit's size
	weatherCalm            = 0.3  // m/s; less is calm, with no arrow
	weatherTemperatureText = "-88°C"
	weatherWindText        = "188 km/h"
	weatherKindText        = "crosswind"
)

func (WeatherPanel) Prepare(ctx *Context, box Box) Painter {
	p := &weatherPainter{box: box, w: ctx.Weather, units: ctx.units()}
	unit := math.Min(box.W, box.H)
	textW := box.W * weatherTextWidth
	dialW := box.W - textW
	// The ring's tick stands out above it, and the readings need air
	// beside it: a tenth of the dial's share is left on every side.
	p.radius = math.Min(math.Min(dialW, box.H)*0.40, unit*weatherDialFraction)
	p.cx, p.cy = box.X+dialW/2, box.Y+box.H/2
	p.textX = box.X + dialW + box.W*0.02
	p.valuePx, p.windPx, p.kindPx = unit*weatherValueFraction, unit*weatherWindFraction, unit*weatherKindFraction
	p.creditPx = maxf(9, unit*weatherCreditFraction)
	if ctx.Fonts != nil {
		fit := func(template string, px float64) float64 {
			if f, err := ctx.Fonts.FitSize(template, textW*0.92, px); err == nil {
				return f
			}
			return px
		}
		p.valuePx, p.windPx, p.kindPx = fit(weatherTemperatureText, p.valuePx), fit(weatherWindText, p.windPx), fit(weatherKindText, p.kindPx)
		p.creditPx = func() float64 {
			if f, err := ctx.Fonts.FitSize(weather.Credit, box.W*0.96, p.creditPx); err == nil {
				return f
			}
			return p.creditPx
		}()
	}
	return p
}

type weatherPainter struct {
	box   Box
	w     *Weather
	units units.Set

	cx, cy, radius          float64
	textX                   float64
	valuePx, windPx, kindPx float64
	creditPx                float64
}

// Static draws the dial's ring, with a mark at the top for straight ahead,
// and the credit.
func (p *weatherPainter) Static(c *Canvas) {
	if p.w == nil {
		return
	}
	ring := math.Max(1, p.radius*0.06)
	c.Arc(p.cx, p.cy, p.radius, 0, 2*math.Pi, ring, Fade(c.Theme.Dim, 0.6))
	tip := p.radius * 0.18
	// Pointing up and out of the ring: the way the athlete is going.
	c.Polygon(
		[]float64{p.cx - tip*0.7, p.cx + tip*0.7, p.cx},
		[]float64{p.cy - p.radius + tip*0.2, p.cy - p.radius + tip*0.2, p.cy - p.radius - tip},
		c.Theme.Dim)
	_ = c.Text(weather.Credit, p.box.X+p.box.W, p.box.Y+p.box.H, 1, 1, p.creditPx, c.Theme.Dim)
}

// Dynamic draws the wind's arrow and the readings at the playhead.
func (p *weatherPainter) Dynamic(c *Canvas, f Frame) {
	if p.w == nil {
		return
	}
	s := f.Sample
	s.Time = f.At
	cond, ok := p.w.At(s)
	if !ok || !f.HasSample {
		_ = c.Text(ReadoutPlaceholder, p.textX, p.cy, 0, 0.5, p.valuePx, c.Theme.Absent)
		return
	}
	heading, hasHeading := p.w.HeadingAt(f.At)
	if !hasHeading {
		heading = 0
	}
	kind := "calm"
	if cond.WindSpeed >= weatherCalm {
		head, cross := weather.Relative(cond, heading)
		switch {
		case !hasHeading:
			kind = "from " + weather.Compass(cond.WindFrom)
		case math.Abs(cross) > math.Abs(head):
			kind = "crosswind"
		case head > 0:
			kind = "headwind"
		default:
			kind = "tailwind"
		}
		p.arrow(c, cond.WindFrom-heading)
	}
	temp := fmt.Sprintf("%.0f%s", unsignedZero(p.units.Temperature.FromSI(cond.Temperature), 0), p.units.Temperature.Name)
	wind := fmt.Sprintf("%.0f %s", p.units.Speed.FromSI(cond.WindSpeed), p.units.Speed.Name)
	gap := p.windPx * 0.35
	top := p.cy - (p.valuePx+gap+p.windPx+gap+p.kindPx)/2
	_ = c.Text(temp, p.textX, top+p.valuePx, 0, 0, p.valuePx, c.Theme.Foreground)
	_ = c.Text(wind, p.textX, top+p.valuePx+gap+p.windPx, 0, 0, p.windPx, c.Theme.Foreground)
	_ = c.Text(kind, p.textX, top+p.valuePx+gap+p.windPx+gap+p.kindPx, 0, 0, p.kindPx, c.Theme.Dim)
}

// arrow draws the wind blowing from rel degrees off straight ahead: from
// that side of the dial to the other, its head where the wind goes.
func (p *weatherPainter) arrow(c *Canvas, rel float64) {
	a := (rel + 180) * math.Pi / 180 // where it blows to
	dx, dy := math.Sin(a), -math.Cos(a)
	l := p.radius * 0.72
	x0, y0 := p.cx-dx*l, p.cy-dy*l
	x1, y1 := p.cx+dx*l, p.cy+dy*l
	width := math.Max(2, p.radius*0.10)
	head := p.radius * 0.32
	bx, by := x1-dx*head, y1-dy*head
	c.Polyline([]float64{x0, bx}, []float64{y0, by}, width, c.Theme.Foreground)
	nx, ny := -dy, dx
	c.Polygon(
		[]float64{x1, bx + nx*head*0.55, bx - nx*head*0.55},
		[]float64{y1, by + ny*head*0.55, by - ny*head*0.55},
		c.Theme.Foreground)
}
