package render

import (
	"math"
	"time"

	"github.com/wisborg/fitactivity"
)

// smoothSample replaces base's gauge readings with averages over window,
// centred on at.
//
// # Why average at all
//
// Compressing four hours into thirty seconds advances the activity sixteen
// seconds per frame. Every frame therefore SKIPS most of what it stands for,
// and showing one sample out of those sixteen seconds is not a faithful
// reading -- it is an arbitrary pick from a span, presented as an instant, and
// it makes power flick between 200 and 500 from one frame to the next until
// nothing can be read at all.
//
// Averaging over the span the frame represents is the more honest answer, not
// the less: the number then describes the stretch of activity the frame is
// showing, which is what a viewer takes it to mean anyway.
//
// # What is NOT averaged, and why each one matters
//
// Position is left alone. A moving average of latitude and longitude cuts
// corners: the dot leaves the route and drifts across the inside of every bend,
// which is a claim about where somebody was.
//
// Distance is left alone because it accumulates. Averaging a monotonic series
// over a centred window returns very nearly the centre value anyway, so it
// would buy nothing and would let the figure go backwards at the ends where
// the window is clipped.
//
// Elevation is left alone because the profile panel gets its own smoothing
// from the elevation model, tuned against the device's ascent totals. Smoothing
// it twice by different rules is how two views of one number come to disagree.
//
// # Absence
//
// A field is present in the result if ANY sample in the window carried it, and
// the average is over those samples alone -- never over zeros standing in for
// missing readings, which is the whole absent-is-not-zero rule applied to
// arithmetic instead of to pixels.
//
// A window that catches no samples at all leaves base untouched. So does a
// window of zero, which is what --smoothing off resolves to.
func smoothSample(track *fitactivity.Track, at time.Time, window time.Duration, base fitactivity.Sample) fitactivity.Sample {
	if window <= 0 || track == nil {
		return base
	}
	half := window / 2
	in := track.Window(at.Add(-half), at.Add(half))
	if len(in) == 0 {
		return base
	}

	out := base
	avg := func(get func(fitactivity.Sample) (float64, bool)) (float64, bool) {
		var sum float64
		var n int
		for _, s := range in {
			if v, ok := get(s); ok {
				sum += v
				n++
			}
		}
		if n == 0 {
			return 0, false
		}
		return sum / float64(n), true
	}

	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) { return s.Speed, s.HasSpeed }); ok {
		out.Speed, out.HasSpeed = v, true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return float64(s.HeartRate), s.HasHeartRate
	}); ok {
		out.HeartRate, out.HasHeartRate = uint8(math.Round(v)), true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return float64(s.Cadence), s.HasCadence
	}); ok {
		out.Cadence, out.HasCadence = uint8(math.Round(v)), true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return float64(s.Power), s.HasPower
	}); ok {
		out.Power, out.HasPower = uint16(math.Round(v)), true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return float64(s.Temperature), s.HasTemperature
	}); ok {
		out.Temperature, out.HasTemperature = int8(math.Round(v)), true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return s.VerticalOscillation, s.HasVerticalOscillation
	}); ok {
		out.VerticalOscillation, out.HasVerticalOscillation = v, true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return s.StanceTime, s.HasStanceTime
	}); ok {
		out.StanceTime, out.HasStanceTime = v, true
	}
	if v, ok := avg(func(s fitactivity.Sample) (float64, bool) {
		return s.StepLength, s.HasStepLength
	}); ok {
		out.StepLength, out.HasStepLength = v, true
	}

	// Developer fields are averaged per key, and the key set is the union of
	// what the window holds -- a footpod that connected part way through the
	// window contributes its readings rather than being dropped for having
	// missed the frame's own instant.
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, s := range in {
		for k, v := range s.DevFields {
			sums[k] += v
			counts[k]++
		}
	}
	if len(sums) > 0 {
		out.DevFields = make(map[string]float64, len(sums))
		for k, sum := range sums {
			out.DevFields[k] = sum / float64(counts[k])
		}
	}
	return out
}
