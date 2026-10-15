package plot

import (
	"fmt"
	"math"
	"sort"
)

// RobustQuantile is how much of the readings at either end RobustRange
// leaves outside the range: a hundredth at the bottom and a hundredth at
// the top.
//
// A power meter reads zero for every second the rider coasts and spikes to
// three times the ride's average on a sprint, and a scale stretched to
// include both draws the hour between them as a flat band a few pixels
// tall. Which is exactly what the first prototype of this chart did. A
// hundredth is enough to leave dropouts and spikes out while keeping
// anything held for longer than a minute of an hour-and-a-half's readings.
const RobustQuantile = 0.01

// RobustRange is the span of segments' readings with RobustQuantile of them
// left out at either end, and whether there are any readings at all.
func RobustRange(segments [][]Point) (lo, hi float64, ok bool) {
	var ys []float64
	for _, seg := range segments {
		for _, p := range seg {
			ys = append(ys, p.Y)
		}
	}
	if len(ys) == 0 {
		return 0, 0, false
	}
	sort.Float64s(ys)
	at := func(q float64) float64 { return ys[int(math.Round(q*float64(len(ys)-1)))] }
	return at(RobustQuantile), at(1 - RobustQuantile), true
}

// Kind is what a scale's numbers are, which decides the steps it ticks at
// and how it writes them.
type Kind int

const (
	// Number is a quantity, ticked at 1, 2 or 5 times a power of ten.
	Number Kind = iota
	// Clock is a duration in seconds, ticked at the steps a clock reads in
	// -- 15 s, 5 min, 1 h -- and written as one: "1:05", "1:30:00". A pace
	// is one too: seconds a kilometre.
	Clock
)

// Scale is one axis of a chart: the span it shows, the step between its
// ticks, and whether it runs backwards.
type Scale struct {
	// Min and Max are the axis's ends: each on a tick for a scale from
	// NewScale, wherever the readings end for one from ExactScale.
	Min, Max float64
	// Step is the distance between ticks.
	Step float64
	Kind Kind
	// Inverted puts Min at the top or the right: a pace's axis, on which
	// a smaller number is faster and should read as higher.
	Inverted bool
}

// NewScale is a scale for readings from lo to hi -- a chart's y axis --
// with at most maxTicks steps between its ends, widened at either end to
// the next tick.
//
// The ends are rounded out to ticks because an axis that ends between them
// leaves its reader estimating the last stretch, and the readings are first
// given a fiftieth of their span of room either side, so the line does not
// run along the frame at its highest and lowest. That room never takes a
// scale of readings that are none of them negative below zero: a heart
// rate's axis marked -20 is a claim about something that cannot be. A span
// of nothing -- a reading that never changed -- is opened to a twentieth
// of the reading either side, or one, so the line is drawn level in the
// middle rather than on a scale of zero height.
func NewScale(lo, hi float64, maxTicks int, kind Kind) Scale {
	if hi < lo {
		lo, hi = hi, lo
	}
	nonNegative := lo >= 0
	if hi-lo <= 0 {
		open := max(math.Abs(lo)*0.05, 1)
		lo, hi = lo-open, hi+open
	}
	pad := (hi - lo) / 50
	lo, hi = lo-pad, hi+pad
	if nonNegative {
		lo = max(lo, 0)
	}
	step := niceStep((hi-lo)/float64(max(maxTicks, 1)), kind)
	return Scale{
		Min:  math.Floor(lo/step) * step,
		Max:  math.Ceil(hi/step) * step,
		Step: step,
		Kind: kind,
	}
}

// ExactScale is a scale from exactly lo to hi -- a chart's x axis -- ticked
// at round steps within it, at most maxTicks of them.
//
// The x axis is not rounded out as the y axis is: it spans the activity,
// and an hour and seventeen minutes drawn on an axis to an hour and a half
// is a chart with an eighth of it empty for no reason. Its ends need no
// tick, since a reader knows where the activity began and the last tick is
// near enough its end.
func ExactScale(lo, hi float64, maxTicks int, kind Kind) Scale {
	if hi < lo {
		lo, hi = hi, lo
	}
	if hi <= lo {
		hi = lo + 1
	}
	return Scale{Min: lo, Max: hi, Step: niceStep((hi-lo)/float64(max(maxTicks, 1)), kind), Kind: kind}
}

// clockSteps are the steps a clock's axis ticks at, in seconds, up to a day.
var clockSteps = []float64{1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

// niceStep is the smallest round step of kind at least raw.
func niceStep(raw float64, kind Kind) float64 {
	if kind == Clock {
		for _, s := range clockSteps {
			if s >= raw {
				return s
			}
		}
		return niceStep(raw/86400, Number) * 86400
	}
	if raw <= 0 {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*exp >= raw*(1-1e-9) {
			return m * exp
		}
	}
	return 10 * exp
}

// Ticks are the values the scale marks: every multiple of Step from Min to
// Max.
func (s Scale) Ticks() []float64 {
	if s.Step <= 0 || s.Max < s.Min {
		return nil
	}
	first := math.Ceil(s.Min/s.Step - 1e-9)
	last := math.Floor(s.Max/s.Step + 1e-9)
	var ticks []float64
	for i := first; i <= last; i++ {
		// Each a multiple of Step rather than the last plus Step, so a
		// step of 0.1 does not drift into 0.30000000000000004 by the third.
		ticks = append(ticks, i*s.Step)
	}
	return ticks
}

// Frac is where v lies along the scale, from 0 at the bottom or the left to
// 1 at the top or the right, and whether it lay outside the scale and was
// held at its edge.
func (s Scale) Frac(v float64) (f float64, clipped bool) {
	f = (v - s.Min) / (s.Max - s.Min)
	if s.Inverted {
		f = 1 - f
	}
	if f < 0 || f > 1 {
		return min(max(f, 0), 1), true
	}
	return f, false
}

// Label is v as the scale writes it: a number with as many decimals as its
// step needs, or a clock -- minutes and seconds, or hours and minutes on an
// axis that reaches an hour, with seconds where the step is under a minute.
func (s Scale) Label(v float64) string {
	if s.Kind == Clock {
		return clockLabel(v, s.Max >= 3600, s.Step < 60)
	}
	decimals := max(0, int(-math.Floor(math.Log10(s.Step)+1e-9)))
	if v == 0 {
		v = 0 // no "-0"
	}
	return fmt.Sprintf("%.*f", decimals, v)
}

func clockLabel(v float64, hours, seconds bool) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	total := int(math.Round(v))
	h, m, sec := total/3600, total%3600/60, total%60
	switch {
	case hours && seconds:
		return fmt.Sprintf("%s%d:%02d:%02d", sign, h, m, sec)
	case hours:
		return fmt.Sprintf("%s%d:%02d", sign, h, m)
	}
	return fmt.Sprintf("%s%d:%02d", sign, total/60, sec)
}
