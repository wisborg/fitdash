// Package plot is a chart of one metric of an activity against time or
// distance: the model every way of drawing it shares.
//
// A chart is drawn at very different resolutions -- a PNG some thousand
// pixels wide, a terminal's braille dots two to a character -- and the
// decisions that make it truthful must not differ between them. So they are
// made here, once, and a renderer only turns the result into marks:
//
//   - which readings a line may join (Segments): never across a stretch
//     without readings long enough to show, because a line drawn there
//     claims readings nobody took -- and a missing reading is never a zero;
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
// the readings, broken wherever two in a row are more than maxGap apart in
// time.
//
// A sample without the reading is time without one, no different from time
// without a sample: the watch that dropped its heart rate strap for a
// second and the watch that recorded nothing for a second say the same
// thing, and both are a break only when they last long enough to be one. A
// line straight across ten minutes nobody measured is a confident claim
// about them, which the chart must not make; across one second, at a
// resolution where a second is a fraction of a pixel, a break would claim
// more than the data does, and shred a line with occasional dropouts into
// pieces. So the tolerance is the caller's, and a caller picks it from the
// recording's own rhythm and the chart's resolution. A maxGap of zero or
// less joins only readings recorded at the same instant.
func Segments(points []Point, maxGap time.Duration) [][]Point {
	var out [][]Point
	var run []Point
	gap := maxGap.Seconds()
	for _, p := range points {
		if !p.Ok {
			continue
		}
		if len(run) > 0 && p.T-run[len(run)-1].T > max(gap, 0) {
			out = append(out, run)
			run = nil
		}
		run = append(run, p)
	}
	if len(run) > 0 {
		out = append(out, run)
	}
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
