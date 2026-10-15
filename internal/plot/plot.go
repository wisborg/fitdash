// Package plot is a chart of one metric of an activity against time or
// distance: the model every way of drawing it shares.
//
// A chart is drawn at very different resolutions -- a PNG some thousand
// pixels wide, a terminal's braille dots two to a character -- and the
// decisions that make it truthful must not differ between them. So they are
// made here, once, and a renderer only turns the result into marks:
//
//   - which samples a line may join (Segments): never across a sample
//     without the reading, and never across a stretch with no samples at
//     all, because a line drawn there claims readings nobody took;
//   - what each column of the drawing shows (Bin), however many samples
//     fall in it;
//   - the scale (RobustRange, NewScale), set from where most readings lie
//     so that a dropout or a spike does not flatten the rest of the trace;
//   - the ticks a reader measures it by, at round numbers or round times.
package plot

import "time"

// Point is one sample of a series: where it lies on the x axis, when it was
// recorded, and its reading -- or that it has none.
//
// X and T are separate because the axis is not always time. A chart against
// distance still smooths over time and still breaks its line where the
// recording stopped, and neither can be read from distance: standing still
// covers none.
type Point struct {
	// X is the point's place on the x axis, in the axis's own units:
	// seconds for time, the distance unit the chart is drawn in otherwise.
	X float64
	// T is when the sample was recorded, in seconds since the activity's
	// start.
	T float64
	// Y is the reading, meaningful only where Ok.
	Y float64
	// Ok says the sample carries the reading. A sample without it is a
	// break in the line, never a zero.
	Ok bool
}

// Segments splits points, in time order, into the runs a line may join:
// unbroken runs of samples with the reading, ended by a sample without it or
// by more than maxGap of time with no sample at all.
//
// A gap in time is a break as surely as a missing reading. A watch that
// stopped recording for ten minutes has no readings there, and a line drawn
// straight across would be a confident claim about ten minutes nobody
// measured. maxGap of zero or less breaks only at missing readings.
func Segments(points []Point, maxGap time.Duration) [][]Point {
	var out [][]Point
	var run []Point
	end := func() {
		if len(run) > 0 {
			out = append(out, run)
			run = nil
		}
	}
	gap := maxGap.Seconds()
	for _, p := range points {
		if !p.Ok {
			end()
			continue
		}
		if len(run) > 0 && gap > 0 && p.T-run[len(run)-1].T > gap {
			end()
		}
		run = append(run, p)
	}
	end()
	return out
}

// Smooth is each segment with every reading replaced by the mean of the
// readings within half of window either side of it in time, never reaching
// into another segment. A window of zero or less smooths nothing.
//
// It is centred, so a step in the readings stays where it happened rather
// than trailing behind it, and it is by time rather than by sample count, so
// a recording at one sample a second and one at one every few smooth alike.
func Smooth(segments [][]Point, window time.Duration) [][]Point {
	half := window.Seconds() / 2
	if half <= 0 {
		return segments
	}
	out := make([][]Point, len(segments))
	for i, seg := range segments {
		smoothed := make([]Point, len(seg))
		// A window sliding along the segment: lo and hi bound the samples
		// within half either side of the current one, and sum their
		// readings, so the whole segment is one pass.
		lo, hi, sum := 0, 0, 0.0
		for j, p := range seg {
			for hi < len(seg) && seg[hi].T <= p.T+half {
				sum += seg[hi].Y
				hi++
			}
			for seg[lo].T < p.T-half {
				sum -= seg[lo].Y
				lo++
			}
			p.Y = sum / float64(hi-lo)
			smoothed[j] = p
		}
		out[i] = smoothed
	}
	return out
}
