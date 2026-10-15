package plot

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// series is a reading a second from t=0, x the time, absent where ys is NaN.
func series(ys ...float64) []Point {
	ps := make([]Point, len(ys))
	for i, y := range ys {
		ps[i] = Point{X: float64(i), T: float64(i), Y: y, Ok: !math.IsNaN(y)}
	}
	return ps
}

var none = math.NaN()

func lengths(segs [][]Point) []int {
	var n []int
	for _, s := range segs {
		n = append(n, len(s))
	}
	return n
}

// A line breaks where two readings in a row are further apart than the gap
// allowed, whether a sample without the reading or no sample at all lies
// between them -- and a sample without the reading is never drawn as a
// zero.
func TestSegmentsBreakWhereReadingsAreTooFarApart(t *testing.T) {
	ps := series(1, 2, none, 3, 4, 5)
	if got := lengths(Segments(ps, 1500*time.Millisecond)); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Errorf("segments %v, want [2 3]: two seconds between readings is more than the gap allowed", got)
	}
	if got := lengths(Segments(ps, 2*time.Second)); !reflect.DeepEqual(got, []int{5}) {
		t.Errorf("segments %v, want [5]: a missing reading shorter than the gap allowed is no break", got)
	}
	ps[5].T = 60 // a minute after the sample before it
	if got := lengths(Segments(ps, 2*time.Second)); !reflect.DeepEqual(got, []int{4, 1}) {
		t.Errorf("segments %v, want [4 1]: broken at the minute without samples", got)
	}
	for _, seg := range Segments(series(none, 7, none), time.Hour) {
		for _, p := range seg {
			if !p.Ok {
				t.Errorf("a segment holds a point without its reading: %+v", p)
			}
		}
	}
	if segs := Segments(series(none, none), time.Hour); len(segs) != 0 {
		t.Errorf("no readings made %d segments", len(segs))
	}
	if got := lengths(Segments(series(1, 2, 3), 0)); !reflect.DeepEqual(got, []int{1, 1, 1}) {
		t.Errorf("segments %v with no gap allowed, want each reading alone", got)
	}
}

// Smoothing is centred, so a step stays where it was; it leaves a level
// reading level; and it never averages across a break.
func TestSmoothIsCentredAndStaysInItsSegment(t *testing.T) {
	ys := []float64{0, 0, 0, 0, 0, 10, 10, 10, 10, 10}
	sm := Smooth(Segments(series(ys...), time.Second), 2*time.Second)[0]
	if sm[4].Y >= 5 || sm[5].Y <= 5 {
		t.Errorf("the step at 5 s smoothed to %.2f then %.2f; a centred mean keeps it between them", sm[4].Y, sm[5].Y)
	}
	if sm[0].Y != 0 || sm[9].Y != 10 {
		t.Errorf("level readings smoothed to %.2f and %.2f", sm[0].Y, sm[9].Y)
	}
	segs := Smooth(Segments(series(0, 0, 0, none, 100, 100, 100), time.Second), time.Hour)
	if segs[0][2].Y != 0 || segs[1][0].Y != 100 {
		t.Errorf("smoothing reached across the break: %.2f, %.2f", segs[0][2].Y, segs[1][0].Y)
	}
	if got := Smooth(Segments(series(1, 5, 1), time.Second), 0)[0][1].Y; got != 5 {
		t.Errorf("no window smoothed 5 to %.2f", got)
	}
}

// A column holds its readings' mean and extremes; a segment's line runs
// across columns its sparse readings skip; a break between segments is
// a break in the line; a reading outside the span is left out.
func TestBin(t *testing.T) {
	cols := Bin(Segments(series(1, 3, 5, 7), time.Second), 0, 4, 2)
	if c := cols[0]; !c.Ok || c.Mean != 2 || c.Min != 1 || c.Max != 3 || c.Joined {
		t.Errorf("first column %+v, want 1 to 3 about 2, not joined", c)
	}
	if c := cols[1]; !c.Ok || c.Mean != 6 || !c.Joined {
		t.Errorf("second column %+v, want about 6, joined to the first", c)
	}

	sparse := []Point{{X: 0, Y: 1, Ok: true}, {X: 9, T: 9, Y: 2, Ok: true}}
	cols = Bin([][]Point{sparse}, 0, 10, 10)
	if cols[3].Ok || !cols[9].Joined {
		t.Errorf("columns between two readings of one segment: %+v; the last should be joined across them", cols)
	}

	cols = Bin(Segments(series(1, none, 2), time.Second), 0, 3, 3)
	if cols[1].Ok || cols[2].Joined {
		t.Errorf("columns %+v: the missing reading should leave the middle empty and the line broken", cols)
	}

	cols = Bin(Segments(series(1, 2, 3, 4), time.Second), 1, 2, 1)
	if c := cols[0]; c.Mean != 2.5 || c.Min != 2 || c.Max != 3 {
		t.Errorf("column %+v, want only the readings at 1 and 2 s", c)
	}
	if Bin(nil, 0, 1, 0) != nil || Bin(nil, 1, 1, 5) != nil {
		t.Error("no columns or no span made columns")
	}
}

// The range leaves dropouts to zero and short spikes out, so they do not
// flatten the hour between them.
func TestRobustRangeLeavesOutliersOut(t *testing.T) {
	var ys []float64
	for i := range 1000 {
		ys = append(ys, 200+50*math.Sin(float64(i)/20))
	}
	ys = append(ys, 0, 0, 0, 0, 0, 1500, 1500, 1500)
	lo, hi, ok := RobustRange(Segments(series(ys...), time.Second))
	if !ok || lo < 140 || hi > 260 {
		t.Errorf("range %.0f to %.0f, want about 150 to 250, the dropouts and spikes left out", lo, hi)
	}
	if _, _, ok := RobustRange(nil); ok {
		t.Error("a range of no readings")
	}
}

// A y scale ends on ticks round enough to read, holds the readings with
// room, never goes below zero for readings that do not, and opens a reading
// that never changed.
func TestNewScale(t *testing.T) {
	for _, c := range []struct {
		lo, hi   float64
		maxTicks int
	}{{151, 249, 5}, {2, 400, 5}, {0.2, 0.9, 6}, {74, 164, 6}, {-3, 12, 4}, {1200, 1200, 5}, {0, 0, 5}} {
		s := NewScale(c.lo, c.hi, c.maxTicks, Number)
		ticks := s.Ticks()
		if len(ticks) < 2 || len(ticks)-1 > c.maxTicks {
			t.Errorf("%v: %d ticks %v, want 2 to %d", c, len(ticks), ticks, c.maxTicks+1)
			continue
		}
		if ticks[0] != s.Min || ticks[len(ticks)-1] != s.Max {
			t.Errorf("%v: ends %v and %v are not ticks %v", c, s.Min, s.Max, ticks)
		}
		if s.Min >= c.lo && c.lo != 0 || s.Max <= c.hi {
			t.Errorf("%v: scale %v to %v leaves the readings no room", c, s.Min, s.Max)
		}
		if c.lo >= 0 && s.Min < 0 {
			t.Errorf("%v: readings none below zero on a scale from %v", c, s.Min)
		}
		if m := s.Step / math.Pow(10, math.Floor(math.Log10(s.Step))); math.Abs(m-1) > 1e-9 && math.Abs(m-2) > 1e-9 && math.Abs(m-5) > 1e-9 {
			t.Errorf("%v: step %v is not 1, 2 or 5 times a power of ten", c, s.Step)
		}
	}
}

// An x scale spans the activity exactly and ticks inside it, at the round
// times a clock reads.
func TestExactScaleAtClockSteps(t *testing.T) {
	s := ExactScale(0, 77*60, 6, Clock)
	if s.Min != 0 || s.Max != 77*60 {
		t.Errorf("x axis %v to %v, want the activity's 0 to 77 min", s.Min, s.Max)
	}
	var labels []string
	for _, v := range s.Ticks() {
		labels = append(labels, s.Label(v))
	}
	if want := []string{"0:00", "0:15", "0:30", "0:45", "1:00", "1:15"}; !reflect.DeepEqual(labels, want) {
		t.Errorf("ticks %v, want %v", labels, want)
	}
	short := ExactScale(0, 150, 5, Clock)
	if got := short.Label(short.Ticks()[1]); got != "0:30" {
		t.Errorf("under an hour the second tick reads %q, want 0:30 (minutes and seconds)", got)
	}
}

// A number is written with the decimals its step needs and no more.
func TestNumberLabels(t *testing.T) {
	for _, c := range []struct {
		step, v float64
		want    string
	}{{50, 250, "250"}, {0.5, 1.5, "1.5"}, {0.02, 0.3, "0.30"}, {10, math.Copysign(0, -1), "0"}} {
		if got := (Scale{Step: c.step}).Label(c.v); got != c.want {
			t.Errorf("%v at step %v reads %q, want %q", c.v, c.step, got, c.want)
		}
	}
}

// An inverted scale -- a pace's -- puts its smaller numbers at the top, and
// a reading off the scale is held at its edge and said to be.
func TestFrac(t *testing.T) {
	s := Scale{Min: 240, Max: 360, Step: 30}
	if f, _ := s.Frac(300); f != 0.5 {
		t.Errorf("the middle at %v", f)
	}
	s.Inverted = true
	if f, _ := s.Frac(240); f != 1 {
		t.Errorf("the fastest pace at %v, want the top", f)
	}
	if f, clipped := s.Frac(500); f != 0 || !clipped {
		t.Errorf("a pace off the scale at %v (clipped %v), want held at the bottom", f, clipped)
	}
}
