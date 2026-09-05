package panel

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
)

// --- scale arithmetic --------------------------------------------------

// gaugeQuantileEpsilon tolerates ordinary float64 rounding in the
// hand-derived expectations below.
const gaugeQuantileEpsilon = 1e-9

func gaugeAlmostEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= gaugeQuantileEpsilon
}

// gaugeEpoch is an arbitrary, synthetic reference instant -- not a real
// activity's start time (see CLAUDE.md: no real dates in a committed
// fixture) -- gaugeSampleTime offsets from it so every fixture track below
// carries real, DISTINCT, evenly spaced Times. buildGaugeSeries windows on
// Time via fitactivity.Track.Window, and that function DEDUPLICATES
// samples sharing an identical Time, keeping only the first (see its own
// doc comment) -- so a track whose samples all share one Time (the zero
// value, which every fixture in this file used before gaugeSeries existed)
// does not average them at all: every window collapses to that single
// surviving sample, and the whole series becomes one constant point stuck
// at whichever value that first sample happened to carry. See
// TestBuildGaugeSeries_ConstantValueOrOneSharedTimeCollapses, which pins
// that as real behaviour rather than leaving it as something a future
// fixture could stumble into by accident.
var gaugeEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// gaugeSampleTime is 1Hz spacing off gaugeEpoch -- the rate the real FIT
// files this project targets record at, and the rate every hand-derived
// expectation in this file assumes.
func gaugeSampleTime(i int) time.Time {
	return gaugeEpoch.Add(time.Duration(i) * time.Second)
}

// gaugeRampEdgeShift is how far buildGaugeSeries' own smoothing pulls a
// LINEAR ramp's two edge points inward from their raw values, in ramp
// units per sample, under the default 4-second marker window
// (gaugeMarkerWindow, for a bare &Context{}: minSmoothingWindow(1s) *
// gaugeMarkerSmoothingMultiplier(4) = 4s, i.e. a 2-second half-window, i.e.
// 2 samples either side at 1Hz) -- the very first point's window is
// truncated to itself plus its own next 2 samples (indices 0,1,2), whose
// average sits 1 sample to the right of index 0, so the smoothed series'
// own minimum is the raw minimum PLUS this shift, and by the mirror
// argument its maximum is the raw maximum MINUS it. See
// TestBuildGaugeSeries_LinearRampEdgesShiftInwardBySymmetricAverage for the
// full derivation, checked against every point of a small series rather
// than only its extremes.
const gaugeRampEdgeShift = 1.0

// rampDistanceTrack returns n samples with present Distance lo, lo+1, ...,
// lo+n-1, one per second from gaugeEpoch -- built on Distance (an ordinary
// presence-gated float field) because robustGaugeScale and paceGaugeScale
// take an arbitrary value closure and neither cares which field or metric
// it is looking at.
func rampDistanceTrack(lo float64, n int) *fitactivity.Track {
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), Distance: lo + float64(i), HasDistance: true}
	}
	return &fitactivity.Track{Samples: samples}
}

func distanceGaugeValue(s fitactivity.Sample) (float64, bool) { return s.Distance, s.HasDistance }

// TestRobustGaugeScale_SnapsOutwardToStep derives the expected floor and
// ceiling from robustGaugeScale's own documented arithmetic: rampDistanceTrack(25,
// 101) carries raw Distance 25..125 one second apart, so buildGaugeSeries'
// own smoothed series has minimum 25+gaugeRampEdgeShift = 26 (at the very
// first sample) and maximum 125-gaugeRampEdgeShift = 124 (at the very
// last) -- see gaugeRampEdgeShift's own doc comment. With step=10, floor =
// floor(26/10)*10 = 20 and ceiling = ceil(124/10)*10 = 130.
func TestRobustGaugeScale_SnapsOutwardToStep(t *testing.T) {
	track := rampDistanceTrack(25, 101)
	ctx := &Context{Track: track}
	got, ok := robustGaugeScale(ctx, distanceGaugeValue, 10)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !gaugeAlmostEqual(got.floor, 20) || !gaugeAlmostEqual(got.ceiling, 130) {
		t.Errorf("got {floor:%v ceiling:%v}, want {floor:20 ceiling:130}", got.floor, got.ceiling)
	}
}

// Power and Cadence used to take a hardZeroFloor bool here that forced the
// snapped low end to a literal 0, on the theory that "0 W" and "0 rpm" are
// real readings that deserved to sit on the axis. A gate measured the cost
// -- cadence's own ordinary range crushed into the top few percent of a
// hard-zero-floored track -- and robustGaugeScale no longer takes that
// parameter at all; see its own doc comment for the correction, and
// TestReadoutGauge_GenuineZeroCadenceDrawsLowChevronAndUnclippedZero, below,
// for how a genuine zero is reported instead.

// TestRobustGaugeScale_DegenerateRangeRefuses checks the fourth drawing
// state's own trigger at the scale layer: every present sample reading the
// SAME value (50) gives a smoothed series that is ALSO constant at 50 (an
// average of nothing but 50s is 50, whatever the window truncates to at
// the edges), which step=10 snaps to floor=ceiling=50 -- a scale with no
// span at all, refused rather than handed to a caller that would divide by
// zero.
func TestRobustGaugeScale_DegenerateRangeRefuses(t *testing.T) {
	samples := make([]fitactivity.Sample, 20)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), Distance: 50, HasDistance: true}
	}
	track := &fitactivity.Track{Samples: samples}
	ctx := &Context{Track: track}

	if got, ok := robustGaugeScale(ctx, distanceGaugeValue, 10); ok {
		t.Errorf("ok = true for a flat track, want false (got %+v)", got)
	}
}

// TestRobustGaugeScale_RequiresPositiveStep guards the one input this
// function refuses outright rather than letting propagate into a
// divide-by-zero snap.
func TestRobustGaugeScale_RequiresPositiveStep(t *testing.T) {
	track := rampDistanceTrack(0, 101)
	ctx := &Context{Track: track}
	if _, ok := robustGaugeScale(ctx, distanceGaugeValue, 0); ok {
		t.Error("ok = true with step = 0, want false")
	}
	if _, ok := robustGaugeScale(ctx, distanceGaugeValue, -5); ok {
		t.Error("ok = true with a negative step, want false")
	}
}

// TestRobustGaugeScale_TooFewPresentSamplesRefuses is the gauge layer's own
// check that gaugeSeriesMinPresent actually propagates as "no usable
// range" here, not merely inside gaugeSeries' own test suite.
func TestRobustGaugeScale_TooFewPresentSamplesRefuses(t *testing.T) {
	track := rampDistanceTrack(0, 5) // well under the floor
	ctx := &Context{Track: track}
	if _, ok := robustGaugeScale(ctx, distanceGaugeValue, 10); ok {
		t.Error("ok = true with too few present samples, want false")
	}
}

// TestRobustGaugeScale_NilContextOrTrackRefuses guards the two inputs
// robustGaugeScale must refuse outright rather than let propagate into
// buildGaugeSeries.
func TestRobustGaugeScale_NilContextOrTrackRefuses(t *testing.T) {
	if _, ok := robustGaugeScale(nil, distanceGaugeValue, 10); ok {
		t.Error("ok = true with a nil Context, want false")
	}
	if _, ok := robustGaugeScale(&Context{}, distanceGaugeValue, 10); ok {
		t.Error("ok = true with a nil Track, want false")
	}
}

// paceRampTrack returns n samples with present Speed lo, lo+step, ...,
// lo+(n-1)*step, HasSpeed true throughout, one second apart from gaugeEpoch
// -- the pace analogue of rampDistanceTrack, above.
func paceRampTrack(lo, step float64, n int) *fitactivity.Track {
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), Speed: lo + float64(i)*step, HasSpeed: true}
	}
	return &fitactivity.Track{Samples: samples}
}

// TestPaceGaugeScale_SweepsOnSpeedSnapsInPaceSpace derives its expected
// floor and ceiling by hand, in the same units paceGaugeScale's own doc
// comment describes:
//
// paceRampTrack(2.0, 0.02, 101) carries raw Speed 2.0, 2.02, ..., 4.0, one
// second apart. By the identical edge-truncation arithmetic
// gaugeRampEdgeShift documents (here in units of 0.02 m/s per sample,
// since this ramp's own step is 0.02, not 1), the smoothed series' own
// minimum is 2.0 + gaugeRampEdgeShift*0.02 = 2.02 m/s (at the very first
// sample) and its maximum is 4.0 - gaugeRampEdgeShift*0.02 = 3.98 m/s (at
// the very last).
//
// The SLOW end (2.02 m/s) has pace 1000/2.02 = 495.0495...s, which
// paceGaugeStep=30 rounds UP (away from the range) to 510s (8:30/km); the
// FAST end (3.98 m/s) has pace 1000/3.98 = 251.2563...s, rounded DOWN to
// 240s (4:00/km). Converting back: floor = 1000/510, ceiling = 1000/240.
//
// Pace().value is used (not a raw closure) deliberately: every speed here
// (2.0-4.0 m/s) is comfortably above minPaceSpeed, so this also demonstrates
// the production accessor -- the one Prepare actually calls -- produces the
// hand-derived numbers, not just some closure shaped like it.
func TestPaceGaugeScale_SweepsOnSpeedSnapsInPaceSpace(t *testing.T) {
	track := paceRampTrack(2.0, 0.02, 101)
	ctx := &Context{Track: track}

	got, ok := paceGaugeScale(ctx, Pace().value)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	wantFloor, wantCeiling := 1000.0/510, 1000.0/240
	if !gaugeAlmostEqual(got.floor, wantFloor) {
		t.Errorf("floor = %v, want %v (1000/510, the snapped slow end)", got.floor, wantFloor)
	}
	if !gaugeAlmostEqual(got.ceiling, wantCeiling) {
		t.Errorf("ceiling = %v, want %v (1000/240, the snapped fast end)", got.ceiling, wantCeiling)
	}
	if got.floor >= got.ceiling {
		t.Errorf("floor (%v) >= ceiling (%v); the scale must run slow-speed-low to fast-speed-high like every other gauge", got.floor, got.ceiling)
	}
}

// TestPaceGaugeScale_NonPositiveSpeedRefuses exercises paceGaugeScale's own
// defensive guard directly, with a raw closure rather than Pace's own bound
// value -- Pace's accessor already refuses a speed this low (see
// minPaceSpeed), so this condition cannot actually be reached through it in
// production. It is tested anyway because paceGaugeScale is a general
// function that must not divide by a non-positive speed no matter what
// closure a future caller hands it.
//
// paceRampTrack(-50, 1, 101) carries raw Speed -50..50; by the same
// edge-truncation arithmetic (step=1 here) the smoothed minimum is
// -50+gaugeRampEdgeShift = -49, still comfortably non-positive.
func TestPaceGaugeScale_NonPositiveSpeedRefuses(t *testing.T) {
	raw := func(s fitactivity.Sample) (float64, bool) { return s.Speed, s.HasSpeed }
	track := paceRampTrack(-50, 1, 101)
	ctx := &Context{Track: track}
	if got, ok := paceGaugeScale(ctx, raw); ok {
		t.Errorf("ok = true for a non-positive low speed, want false (got %+v)", got)
	}
}

// TestGaugeScale_FractionMapsFloorAndCeilingToZeroAndOne pins the pure
// arithmetic drawGauge's "in range" branch depends on.
func TestGaugeScale_FractionMapsFloorAndCeilingToZeroAndOne(t *testing.T) {
	s := gaugeScale{floor: 100, ceiling: 200}
	if got := s.fraction(100); !gaugeAlmostEqual(got, 0) {
		t.Errorf("fraction(floor) = %v, want 0", got)
	}
	if got := s.fraction(200); !gaugeAlmostEqual(got, 1) {
		t.Errorf("fraction(ceiling) = %v, want 1", got)
	}
	if got := s.fraction(150); !gaugeAlmostEqual(got, 0.5) {
		t.Errorf("fraction(midpoint) = %v, want 0.5", got)
	}
}

// --- gaugeSeries -----------------------------------------------------------

// TestBuildGaugeSeries_LinearRampEdgesShiftInwardBySymmetricAverage checks
// EVERY point of a small, fully hand-computable series, not only its
// extremes -- gaugeRampEdgeShift (above) is the two-point summary the
// scale-arithmetic tests rely on, and this is what earns it: seven
// samples, one second apart, Distance 10,11,...,16 (a pure ramp),
// window=4s (half=2s, a 5-point centred average at 1Hz).
//
//	i=0: window catches i=0,1,2  -> avg(10,11,12)      = 11
//	i=1: window catches i=0..3   -> avg(10,11,12,13)    = 11.5
//	i=2: window catches i=0..4   -> avg(10,11,12,13,14) = 12   (= raw)
//	i=3: window catches i=1..5   -> avg(11,12,13,14,15) = 13   (= raw)
//	i=4: window catches i=2..6   -> avg(12,13,14,15,16) = 14   (= raw)
//	i=5: window catches i=3..6   -> avg(13,14,15,16)    = 14.5
//	i=6: window catches i=4..6   -> avg(14,15,16)       = 15
//
// Interior points (i=2,3,4) reproduce the raw value exactly -- a symmetric
// window average of a LINEAR function equals the value at its own centre --
// which is what lets every scale-arithmetic test above trust an interior
// reading without re-deriving this per case.
func TestBuildGaugeSeries_LinearRampEdgesShiftInwardBySymmetricAverage(t *testing.T) {
	const n = 7
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), Distance: 10 + float64(i), HasDistance: true}
	}
	track := &fitactivity.Track{Samples: samples}

	s := buildGaugeSeries(track, 4*time.Second, distanceGaugeValue)
	want := []float64{11, 11.5, 12, 13, 14, 14.5, 15}
	if len(s.values) != n {
		t.Fatalf("got %d points, want %d", len(s.values), n)
	}
	for i, w := range want {
		if !s.ok[i] {
			t.Errorf("point %d: ok = false, want true", i)
			continue
		}
		if !gaugeAlmostEqual(s.values[i], w) {
			t.Errorf("point %d: value = %v, want %v", i, s.values[i], w)
		}
	}
}

// TestBuildGaugeSeries_ConstantValueOrOneSharedTimeCollapses is the
// degenerate case gaugeEpoch's own doc comment warns every OTHER fixture
// in this file away from: samples that all share one Time (as every
// fixture here did before gaugeSeries existed) never get a window wider
// than one sample -- fitactivity.Track.Window deduplicates same-Time
// entries down to the first (see its own doc comment) -- so every point's
// own "average" is really just the FIRST sample's own raw value, unchanged,
// and the whole series is one constant point stuck there, wherever a real
// range was wanted. Checked directly here so the reasoning in gaugeEpoch's
// comment is pinned as behaviour, not merely asserted in prose.
func TestBuildGaugeSeries_ConstantValueOrOneSharedTimeCollapses(t *testing.T) {
	shared := gaugeEpoch
	samples := make([]fitactivity.Sample, 20)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: shared, Distance: float64(i), HasDistance: true}
	}
	track := &fitactivity.Track{Samples: samples}

	s := buildGaugeSeries(track, 4*time.Second, distanceGaugeValue)
	lo, hi, ok := s.Range()
	if !ok {
		t.Fatal("ok = false, want true (20 points, all sharing one Time, is still >= gaugeSeriesMinPresent)")
	}
	if !gaugeAlmostEqual(lo, hi) {
		t.Errorf("Range() = (%v, %v), want lo == hi -- every window collapses to the same single surviving sample when all Times are equal", lo, hi)
	}
	if want := 0.0; !gaugeAlmostEqual(lo, want) { // the FIRST sample's own raw value, samples[0].Distance == 0
		t.Errorf("Range() lo/hi = %v, want %v -- the first sample's own value, not an average of every present one", lo, want)
	}
}

// TestBuildGaugeSeries_ZeroWindowOrEmptyTrackIsTheZeroSeries pins the three
// inputs buildGaugeSeries refuses outright, all producing the zero
// gaugeSeries rather than a panic.
func TestBuildGaugeSeries_ZeroWindowOrEmptyTrackIsTheZeroSeries(t *testing.T) {
	track := rampDistanceTrack(0, 20)
	if s := buildGaugeSeries(track, 0, distanceGaugeValue); s.values != nil {
		t.Error("window = 0: want the zero gaugeSeries")
	}
	if s := buildGaugeSeries(nil, 4*time.Second, distanceGaugeValue); s.values != nil {
		t.Error("nil track: want the zero gaugeSeries")
	}
	if s := buildGaugeSeries(&fitactivity.Track{}, 4*time.Second, distanceGaugeValue); s.values != nil {
		t.Error("empty track: want the zero gaugeSeries")
	}
}

// TestGaugeSeries_RangeContainsEveryOkValue is the property change 1/4's
// own fix depends on, checked directly rather than only through the scale
// it feeds: for ANY series, Range's own [lo, hi] contains every ok point's
// value -- which is what guarantees a marker positioned from THIS series
// (gaugeScale.fraction of a value drawn from it) can never fall outside
// [0, 1] once floor/ceiling are snapped outward from lo/hi, and so can
// never "max out" the way a raw, unsmoothed reading could.
func TestGaugeSeries_RangeContainsEveryOkValue(t *testing.T) {
	track := rampDistanceTrack(0, 40)
	s := buildGaugeSeries(track, 6*time.Second, distanceGaugeValue)
	lo, hi, ok := s.Range()
	if !ok {
		t.Fatal("ok = false, want true")
	}
	for i, v := range s.values {
		if !s.ok[i] {
			continue
		}
		if v < lo || v > hi {
			t.Errorf("point %d (value %v) falls outside Range()'s own [%v, %v]", i, v, lo, hi)
		}
	}
}

// TestGaugeSeries_AtInterpolatesBetweenBracketingPoints checks the ordinary
// case: a query strictly between two ok points returns the linear
// interpolation between them, matching fitactivity.Track.At's own
// convention for two bracketing samples. The two points are 2 seconds
// apart -- within fitactivity.DefaultMaxGap (3s), so this exercises the
// ordinary interpolation branch rather than the wide-gap refusal
// TestGaugeSeries_AtRefusesAcrossAWideRealGap covers separately.
func TestGaugeSeries_AtInterpolatesBetweenBracketingPoints(t *testing.T) {
	s := gaugeSeries{
		times:  []time.Time{gaugeSampleTime(0), gaugeSampleTime(2)},
		values: []float64{100, 200},
		ok:     []bool{true, true},
	}
	got, ok := s.At(gaugeSampleTime(0).Add(800 * time.Millisecond)) // 40% of the way from 0 to 2s
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := 140.0; !gaugeAlmostEqual(got, want) {
		t.Errorf("At() = %v, want %v (40%% of the way from 100 to 200)", got, want)
	}

	// Exactly on a point returns that point's own value, not an
	// interpolation that happens to equal it.
	if got, ok := s.At(gaugeSampleTime(0)); !ok || !gaugeAlmostEqual(got, 100) {
		t.Errorf("At(point 0) = (%v, %v), want (100, true)", got, ok)
	}
}

// TestGaugeSeries_AtRefusesBeforeFirstOrAfterLast mirrors
// fitactivity.Track.At's own "never extrapolate outside coverage" rule.
func TestGaugeSeries_AtRefusesBeforeFirstOrAfterLast(t *testing.T) {
	s := gaugeSeries{
		times:  []time.Time{gaugeSampleTime(0), gaugeSampleTime(10)},
		values: []float64{100, 200},
		ok:     []bool{true, true},
	}
	if _, ok := s.At(gaugeSampleTime(0).Add(-time.Second)); ok {
		t.Error("ok = true for an instant before the first point, want false")
	}
	if _, ok := s.At(gaugeSampleTime(11)); ok {
		t.Error("ok = true for an instant after the last point, want false")
	}
}

// TestGaugeSeries_AtRefusesAcrossADropoutNoInterpolatedMarker is the
// absent-data policy's own load-bearing case: an instant that falls
// between two points where EITHER side has no genuine reading (ok=false --
// the marker's own window found nothing there, a real dropout) must refuse
// rather than draw a straight line between whatever numbers happen to sit
// either side of the hole. This is what keeps a dropout drawing the absent
// state instead of a marker quietly interpolated across it.
func TestGaugeSeries_AtRefusesAcrossADropoutNoInterpolatedMarker(t *testing.T) {
	s := gaugeSeries{
		times:  []time.Time{gaugeSampleTime(0), gaugeSampleTime(5), gaugeSampleTime(10)},
		values: []float64{100, 0, 200}, // the middle point's own value is unused: ok says so.
		ok:     []bool{true, false, true},
	}
	if got, ok := s.At(gaugeSampleTime(2)); ok {
		t.Errorf("At() = (%v, true), want ok=false -- the right-hand bracketing point has no reading", got)
	}
	if got, ok := s.At(gaugeSampleTime(7)); ok {
		t.Errorf("At() = (%v, true), want ok=false -- the left-hand bracketing point has no reading", got)
	}
}

// TestGaugeSeries_AtRefusesAcrossAWideRealGap checks the other half of the
// absent-data policy: even when BOTH bracketing points are individually
// ok, a gap between them wider than fitactivity.DefaultMaxGap is a real
// recording gap, not a stretch to draw a straight line across -- and,
// unlike fitactivity.Track.AtWithGap, this does not snap to the nearer
// neighbour either (see gaugeSeries.At's own doc comment for why refusing
// outright is the honest answer for a marker).
func TestGaugeSeries_AtRefusesAcrossAWideRealGap(t *testing.T) {
	s := gaugeSeries{
		times:  []time.Time{gaugeSampleTime(0), gaugeSampleTime(0).Add(fitactivity.DefaultMaxGap + time.Minute)},
		values: []float64{100, 200},
		ok:     []bool{true, true},
	}
	mid := gaugeSampleTime(0).Add((fitactivity.DefaultMaxGap + time.Minute) / 2)
	if got, ok := s.At(mid); ok {
		t.Errorf("At() = (%v, true), want ok=false -- the two points are further apart than DefaultMaxGap", got)
	}
}

// TestGaugeSeries_AtOnTheEmptySeriesRefuses guards the zero gaugeSeries (no
// track, or a window too small to build one -- buildGaugeSeries' own early
// returns) against an index panic rather than merely against a wrong
// answer.
func TestGaugeSeries_AtOnTheEmptySeriesRefuses(t *testing.T) {
	var s gaugeSeries
	if _, ok := s.At(gaugeSampleTime(0)); ok {
		t.Error("ok = true on the zero gaugeSeries, want false")
	}
}

// --- gaugeMarkerWindow -------------------------------------------------

// TestGaugeMarkerWindow_MultipliesTheResolvedBaseAndFloors checks
// gaugeMarkerWindow's own three cases: an explicit --smoothing window is
// multiplied by gaugeMarkerSmoothingMultiplier unchanged; "auto" reads
// Timeline.AutoSmoothingBase instead; and a window that resolves to less
// than minSmoothingWindow (explicit --smoothing off, Window==0, included)
// is floored to minSmoothingWindow BEFORE the multiplier, never carrying a
// literal zero through it.
func TestGaugeMarkerWindow_MultipliesTheResolvedBaseAndFloors(t *testing.T) {
	explicit := &Context{Smoothing: Smoothing{Window: 10 * time.Second}}
	if got, want := gaugeMarkerWindow(explicit), 10*time.Second*gaugeMarkerSmoothingMultiplier; got != want {
		t.Errorf("explicit 10s window: gaugeMarkerWindow() = %v, want %v", got, want)
	}

	off := &Context{}
	if got, want := gaugeMarkerWindow(off), minSmoothingWindow*gaugeMarkerSmoothingMultiplier; got != want {
		t.Errorf("--smoothing off (zero window): gaugeMarkerWindow() = %v, want the floor %v, not a literal zero carried through the multiplier", got, want)
	}

	belowFloor := &Context{Smoothing: Smoothing{Window: 200 * time.Millisecond}}
	if got, want := gaugeMarkerWindow(belowFloor), minSmoothingWindow*gaugeMarkerSmoothingMultiplier; got != want {
		t.Errorf("below-floor explicit window: gaugeMarkerWindow() = %v, want the floor %v applied before the multiplier", got, want)
	}
}

// --- colour ramp ---------------------------------------------------------

// TestGaugeRampColor_DeterministicSameFractionSameColour guards against a
// future version of this ramp depending on anything but frac -- a render's
// own theme, say, or wall-clock time -- which would make two markers at the
// identical reading draw two different colours depending on when or in
// which theme they happened to be rendered.
func TestGaugeRampColor_DeterministicSameFractionSameColour(t *testing.T) {
	for _, f := range []float64{0, 0.1, 0.37, 0.5, 0.82, 1} {
		a := gaugeRampColor(f)
		b := gaugeRampColor(f)
		if a != b {
			t.Errorf("frac %v: gaugeRampColor() = %v then %v, want the identical colour both times", f, a, b)
		}
	}
}

// TestGaugeRampColor_EndpointsAndMidpointDiffer is the minimum bar for "a
// ramp" rather than a solid colour: the low end, the middle and the high
// end must be three distinguishable colours.
func TestGaugeRampColor_EndpointsAndMidpointDiffer(t *testing.T) {
	lo, mid, hi := gaugeRampColor(0), gaugeRampColor(0.5), gaugeRampColor(1)
	if lo == mid || mid == hi || lo == hi {
		t.Errorf("gaugeRampColor(0)=%v, gaugeRampColor(0.5)=%v, gaugeRampColor(1)=%v -- want three distinct colours", lo, mid, hi)
	}
}

// TestGaugeRampColor_ClampsOutOfRangeFractions guards drawGauge/
// drawDialGauge's own contract: mv is guaranteed inside [floor, ceiling] by
// gaugeSeries.Range's own construction (see that method's doc comment), so
// frac should never legitimately land outside [0, 1] -- but gaugeRampColor
// clamps anyway rather than producing an out-of-gamut colour or a wrapped
// one, the same defensive posture GaugeStyleName's own doc comment states
// for an unreachable case.
func TestGaugeRampColor_ClampsOutOfRangeFractions(t *testing.T) {
	if got, want := gaugeRampColor(-0.5), gaugeRampColor(0); got != want {
		t.Errorf("gaugeRampColor(-0.5) = %v, want gaugeRampColor(0) = %v", got, want)
	}
	if got, want := gaugeRampColor(1.5), gaugeRampColor(1); got != want {
		t.Errorf("gaugeRampColor(1.5) = %v, want gaugeRampColor(1) = %v", got, want)
	}
}

// TestGaugeRampColor_DistinctFromAbsentAndForegroundInBothThemes is the
// collision constraint stated in gaugeRampColor's own doc comment, checked
// against BOTH shipped themes rather than assumed: the ramp must never
// exactly equal Theme.Absent (the dropout wash's own colour) or
// Theme.Foreground (the off-scale chevron's), at any of five points across
// its own domain, in either theme -- an exact collision would make a live,
// in-range marker indistinguishable from either an absent reading or an
// off-scale one.
func TestGaugeRampColor_DistinctFromAbsentAndForegroundInBothThemes(t *testing.T) {
	for _, th := range Themes() {
		for _, f := range []float64{0, 0.25, 0.5, 0.75, 1} {
			got := color.NRGBAModel.Convert(gaugeRampColor(f)).(color.NRGBA)
			if absent := color.NRGBAModel.Convert(th.Absent).(color.NRGBA); got == absent {
				t.Errorf("theme %s, frac %v: gaugeRampColor() equals Theme.Absent (%v) -- a live marker would read as absent", th.Name, f, got)
			}
			if fg := color.NRGBAModel.Convert(th.Foreground).(color.NRGBA); got == fg {
				t.Errorf("theme %s, frac %v: gaugeRampColor() equals Theme.Foreground (%v) -- an in-range marker would read as the off-scale chevron", th.Name, f, got)
			}
		}
	}
}

// TestGaugeRampColor_VisibleAgainstBothThemesOwnBackground checks the
// "works in both themes" requirement as a legibility floor rather than
// leaving it to eyeballing alone: every one of five points across the
// ramp's own domain clears a low contrast floor against Theme.Background in
// BOTH shipped themes. 1.3:1 is deliberately far below WCAG's own 4.5:1
// text floor (ContrastRatio's own doc comment) -- this is a small marker
// dot or needle tip, not a paragraph of text, and the ramp's own redundant
// encoding (gaugeRampColor's doc comment) means position, not colour alone,
// is what a reading MUST be legible by; this floor only catches a colour
// that would be lost entirely, not one that is merely subtle.
func TestGaugeRampColor_VisibleAgainstBothThemesOwnBackground(t *testing.T) {
	const floor = 1.3
	for _, th := range Themes() {
		for _, f := range []float64{0, 0.25, 0.5, 0.75, 1} {
			if ratio := ContrastRatio(gaugeRampColor(f), th.Background); ratio < floor {
				t.Errorf("theme %s, frac %v: contrast against Background = %.3f:1, want >= %v:1", th.Name, f, ratio, floor)
			}
		}
	}
}

// --- drawing -------------------------------------------------------------

// gaugeHRTrack builds n samples, one second apart from gaugeEpoch, with
// present HeartRate ramping from lo, lo+1, ..., lo+n-1, and nothing else --
// enough for HeartRate's own scale rule to resolve a range through
// robustGaugeScale.
func gaugeHRTrack(lo uint8, n int) *fitactivity.Track {
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), HasHeartRate: true, HeartRate: lo + uint8(i)}
	}
	return &fitactivity.Track{Samples: samples}
}

// gaugeFixture builds a Canvas, its backing image and a Context requesting
// GaugeStyleTrack over track -- the gauge analogue of elapsedFixture
// (elapsed_test.go). The image is sized to exactly the box a real gauge
// readout is placed in (roughly 442x123 at 1080p), rather than a square canvas the way most other panels'
// fixtures use, because this panel's own geometry is sensitive to the box
// being wide and short.
func gaugeFixture(t *testing.T, track *fitactivity.Track) (*Canvas, *image.RGBA, *Context, Box) {
	t.Helper()
	const w, h = 442, 123
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, float64(h)*0.05, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Width: w, Height: h, FontScale: 0.05, Fonts: faces, Track: track, GaugeStyle: GaugeStyleTrack}
	return c, img, ctx, Box{X: 0, Y: 0, W: w, H: h}
}

// heartRateGaugePainter prepares HeartRate() over track and returns the
// concrete painter, failing the test if GaugeStyleTrack did not resolve a
// track -- most tests below need one to exist before they can check what it
// draws.
func heartRateGaugePainter(t *testing.T, track *fitactivity.Track) (*Canvas, *image.RGBA, *readoutPainter) {
	t.Helper()
	c, img, ctx, box := gaugeFixture(t, track)
	p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if !p.hasTrack {
		t.Fatal("precondition failed: want a resolved gauge track")
	}
	return c, img, p
}

// TestReadoutGauge_PlainStyleDrawsNoTrack pins the zero-Context contract:
// GaugeStylePlain (the zero value) must resolve hasTrack=false regardless of
// how good the activity's own range is, and the render it produces must be
// BYTE-IDENTICAL to a Context that never mentions gauges at all -- Track
// nil, GaugeStyle unset -- which is what every existing caller and test
// looks like today. This is the pixel-identical check
// docs/architecture.md's own equality test (Static/Dynamic vs Render) uses
// for the identical reason: "hasTrack is false" alone would not catch a
// bug where the plain rows were nonetheless nudged to make room for a strip
// nobody draws into.
func TestReadoutGauge_PlainStyleDrawsNoTrack(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, ctx, box := gaugeFixture(t, track)
	ctx.GaugeStyle = GaugeStylePlain

	p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if p.hasTrack {
		t.Fatal("GaugeStylePlain resolved a track; the zero style must never draw one")
	}
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 150}})
	gauged := make([]byte, len(img.Pix))
	copy(gauged, img.Pix)

	// The reference: a Context that never mentions gauges at all.
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	img2 := image.NewRGBA(image.Rect(0, 0, int(box.W), int(box.H)))
	c2, err := NewCanvas(img2, float64(box.H)*0.05, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	plainCtx := &Context{Width: int(box.W), Height: int(box.H), FontScale: 0.05, Fonts: faces}
	p2 := HeartRate().Prepare(plainCtx, box)
	c2.Fill(c2.Theme.Background)
	p2.Static(c2)
	p2.Dynamic(c2, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 150}})

	if string(gauged) != string(img2.Pix) {
		t.Error("GaugeStylePlain over an activity with a perfectly usable range renders differently from a Context with no gauge fields set at all")
	}
}

// TestReadoutGauge_NoUsableRangeFallsBackToPlain is the fourth drawing
// state's own end-to-end check: GaugeStyleTrack requested, but the activity
// has too few present readings for gaugeSeries to answer at all. The panel
// must still draw the ordinary reading -- it does not decline -- and must
// draw no track.
func TestReadoutGauge_NoUsableRangeFallsBackToPlain(t *testing.T) {
	track := gaugeHRTrack(150, 3) // well under gaugeSeriesMinPresent
	c, img, ctx, box := gaugeFixture(t, track)

	p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if p.hasTrack {
		t.Fatal("resolved a track from an activity with no usable range")
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 150}})
	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("no usable range must still draw the ordinary reading; it drew nothing at all")
	}
}

// gaugeCadenceTrack builds n samples, one second apart from gaugeEpoch,
// with present Cadence ramping from lo, lo+1, ..., lo+n-1, and nothing else
// -- Cadence's own analogue of gaugeHRTrack, above, used for the
// genuine-zero test below where the HARD-FLOOR-FREE scale needs to be
// Cadence's own, not HeartRate's.
func gaugeCadenceTrack(lo uint8, n int) *fitactivity.Track {
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), HasCadence: true, Cadence: lo + uint8(i)}
	}
	return &fitactivity.Track{Samples: samples}
}

// gaugeNotchSearchBox is the rectangle drawNotch's own ink can possibly
// land in, given p's already-resolved geometry -- the track's travel span
// in x, and the marker's own radius either side of trackY in y. Every test
// below that asks "is there a marker anywhere on the track" searches this
// box rather than a hand-picked one, so widening or narrowing notchR can
// never silently make one of these tests stop looking where the marker
// could actually be.
func gaugeNotchSearchBox(p *readoutPainter) Box {
	return Box{X: p.spanX, Y: p.trackY - p.notchR, W: p.spanW, H: p.notchR * 2}
}

// gaugeNotchShoulderBox is the narrower slice of gaugeNotchSearchBox that
// NOTHING but the marker itself (drawNotch) ever paints into: the axis
// line/wash rectangle (drawTrack) only ever reaches trackY +/- trackH/2,
// and notchR is deliberately larger than that (gaugeNotchRadiusFactor's own
// doc comment), so the band strictly above the axis line but still inside
// the marker's own possible reach is untouched by anything except a
// marker, whatever ITS colour is. X is inset by a couple of pixels from
// spanX/spanX+spanW so antialiasing at the exact boundary column the
// overflow chevron sits just outside of cannot bleed a pixel into the
// snapshot either.
//
// Used instead of gaugeNotchSearchBox by every "no marker was drawn"
// before/after snapshot test below: gaugeNotchSearchBox's own full height
// also contains the axis line/wash itself, which legitimately DOES change
// between Static and Dynamic (that is the wash, working as intended) --
// snapshotting the WHOLE box there would flag the wash's own expected
// change as if it were an unwanted marker.
func gaugeNotchShoulderBox(p *readoutPainter) Box {
	const xInset = 2.0
	return Box{X: p.spanX + xInset, Y: p.trackY - p.notchR, W: p.spanW - 2*xInset, H: p.notchR - p.trackH/2}
}

// snapshotBox copies every pixel inside b, in scan order, R G B A per
// pixel -- a region "before" and "after" comparison (bytes.Equal on two
// snapshots) is a strictly stronger check than searching for one
// hand-picked colour, and is what the off-scale tests below use to confirm
// "no marker ink was added here at all", now that a marker's own colour
// (gaugeRampColor) is no longer the single fixed Theme.Foreground a colour
// search could look for.
func snapshotBox(img *image.RGBA, b Box) []byte {
	x0, y0 := int(b.X), int(b.Y)
	x1, y1 := int(b.X+b.W), int(b.Y+b.H)
	out := make([]byte, 0, (x1-x0)*(y1-y0)*4)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p := img.RGBAAt(x, y)
			out = append(out, p.R, p.G, p.B, p.A)
		}
	}
	return out
}

// TestReadoutGauge_DropoutDrawsNoMarkerAndWashesTheWholeTrack is the
// absence-in-a-gauge check, covering both halves of the policy CLAUDE.md
// asks every panel to state: an instant with no reading draws NO marker
// anywhere on the track -- a marker is a real, drawable value, and drawing
// one anywhere (including parked at the floor) would be exactly the
// confident lie this project forbids, spread across a shape instead of a
// number -- while the track itself still washes its own FULL length, so a
// "no reading this instant" gauge stays visually distinct from a gauge that
// simply failed to draw.
func TestReadoutGauge_DropoutDrawsNoMarkerAndWashesTheWholeTrack(t *testing.T) {
	track := gaugeHRTrack(100, 101) // floor=100, ceiling=200 (see TestRobustGaugeScale_SnapsOutwardToStep's sibling arithmetic)
	c, img, p := heartRateGaugePainter(t, track)

	notchBox := gaugeNotchShoulderBox(p)

	c.Fill(c.Theme.Background)
	p.Static(c)
	before := snapshotBox(img, notchBox)
	p.Dynamic(c, Frame{}) // the zero Frame: HasSample false.
	after := snapshotBox(img, notchBox)

	if !bytes.Equal(before, after) {
		t.Error("the notch search box changed on a dropout instant -- absence must draw no marker at all, not a marker parked anywhere")
	}

	// The wash sits on top of Static's own dim ghost, which is now a
	// gradient (drawTrackGradient) rather than flat Theme.Dim -- so "just
	// inside the track's own far (right) edge" must be checked against the
	// ACTUAL rendered ghost pixel, read back before Dynamic draws the wash
	// over it, not against a hand-blended flat-Dim colour that is no longer
	// what is actually there. See gaugeAbsentWashAlpha's own doc comment
	// for why the composite is solved against Theme.Dim (and the tinted
	// colours drawTrackGradient can produce), not Theme.Background.
	x := int(p.spanX+p.spanW) - 2
	y := int(p.trackY)
	c.Fill(c.Theme.Background)
	p.Static(c)
	ghost := img.RGBAAt(x, y)
	p.Dynamic(c, Frame{})
	wash := img.RGBAAt(x, y)
	if ratio := ContrastRatio(wash, ghost); ratio < gaugeAbsentWashContrastFloor {
		t.Errorf("pixel near the track's far edge: wash %v over ghost %v measured %.3f:1, below the %v:1 floor -- "+
			"a dropout must wash the WHOLE track, not stop partway through it, or fail to visibly change it at all",
			wash, ghost, ratio, gaugeAbsentWashContrastFloor)
	}
}

// gaugeInteriorReading is a (time, HeartRate) pair drawn from an INTERIOR
// sample of gaugeHRTrack(100, 101) -- at least 2 samples from either end,
// so buildGaugeSeries' own smoothing reproduces the raw value EXACTLY (see
// TestBuildGaugeSeries_LinearRampEdgesShiftInwardBySymmetricAverage). Every
// test in this file that wants the marker's own smoothed position to equal
// a specific, hand-picked reading uses one of these rather than an
// arbitrary index, so v (the injected Sample) and mv (the series lookup at
// the matching At) agree by construction -- which is what a test checking
// POSITION, rather than the number/marker divergence itself, needs.
func gaugeInteriorReading(idx int, hr uint8) (at time.Time, sample fitactivity.Sample) {
	return gaugeSampleTime(idx), fitactivity.Sample{HasHeartRate: true, HeartRate: hr}
}

// TestReadoutGauge_NotchPositionIsProportionalToValue checks the ordinary
// case: a reading strictly inside [floor, ceiling] places the marker at the
// exact fraction the scale implies, and a higher reading places it further
// to the right, never at the same x as a lower one.
//
// gaugeHRTrack(100, 101) resolves floor=100, ceiling=200 (see
// TestRobustGaugeScale_SnapsOutwardToStep's sibling arithmetic, reused here
// on HeartRate's own bound accessor). Readings are drawn from
// gaugeInteriorReading at indices 10, 50 and 90 -- comfortably interior, so
// the marker's own smoothed position equals the injected reading exactly --
// giving HeartRate 110, 150, 190 and fractions 0.10, 0.50, 0.90.
func TestReadoutGauge_NotchPositionIsProportionalToValue(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, p := heartRateGaugePainter(t, track)
	if !gaugeAlmostEqual(p.scale.floor, 100) || !gaugeAlmostEqual(p.scale.ceiling, 200) {
		t.Fatalf("precondition failed: want scale {100 200}, got %+v", p.scale)
	}

	for _, tc := range []struct {
		idx  int
		hr   uint8
		frac float64
	}{
		{10, 110, 0.10},
		{50, 150, 0.50},
		{90, 190, 0.90},
	} {
		at, sample := gaugeInteriorReading(tc.idx, tc.hr)
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{At: at, HasSample: true, Sample: sample})

		want := gaugeRampColor(tc.frac)
		wantRGBA := color.RGBAModel.Convert(want).(color.RGBA)
		wantX := int(p.spanX + p.spanW*tc.frac)
		if got := img.RGBAAt(wantX, int(p.trackY)); !closeRGBA(got, wantRGBA, 4) {
			t.Errorf("reading %d (fraction %v): pixel at the expected marker centre x=%d is %v, want the ramp's own colour at that fraction %v",
				tc.hr, tc.frac, wantX, got, wantRGBA)
		}
	}

	// The marker must actually MOVE, not merely exist somewhere on the
	// track at every reading: the leftmost marker ink for a low reading
	// must sit to the left of the leftmost marker ink for a high one.
	// Searched with a colour tolerant of EITHER end of the ramp (a wide
	// window in the R channel alone, since low is green and high is red --
	// see gaugeRampLow/gaugeRampHigh) rather than one fixed colour, since
	// the marker's own colour now depends on its position.
	notchInk := func(frac float64) (int, bool) {
		want := color.RGBAModel.Convert(gaugeRampColor(frac)).(color.RGBA)
		x, _, ok := findRGBA(img, gaugeNotchSearchBox(p), want, 6)
		return x, ok
	}

	atLow, sampleLow := gaugeInteriorReading(10, 110)
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: atLow, HasSample: true, Sample: sampleLow})
	xLow, okLow := notchInk(0.10)

	atHigh, sampleHigh := gaugeInteriorReading(90, 190)
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: atHigh, HasSample: true, Sample: sampleHigh})
	xHigh, okHigh := notchInk(0.90)

	if !okLow || !okHigh {
		t.Fatal("could not find marker ink for a low and a high in-range reading")
	}
	if xHigh <= xLow {
		t.Errorf("the marker for the higher reading (leftmost ink at x=%d) is not to the right of the lower reading's (x=%d)", xHigh, xLow)
	}
}

// TestReadoutGauge_MarkerPositionedFromSmoothedSeriesNotRawValue is change
// 1/4's own headline behaviour, checked directly: the marker's own
// position comes from the SMOOTHED series at f.At (gaugeSeries.At), not
// from the raw/injected f.Sample value Dynamic also uses for the printed
// text -- the two are allowed to legitimately differ, and this constructs
// an instant where they do.
//
// gaugeHRTrack(100, 101)'s very first sample (index 0, f.At =
// gaugeSampleTime(0)) has a smoothed value of 101, not the raw 100 (see
// gaugeRampEdgeShift). The injected Sample carries HeartRate 130 instead --
// an arbitrary, different, still in-range reading. The marker must sit at
// fraction (101-100)/(200-100) = 0.01, NOT at (130-100)/100 = 0.30, which
// is where a marker driven by the raw/printed value would have gone.
func TestReadoutGauge_MarkerPositionedFromSmoothedSeriesNotRawValue(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, p := heartRateGaugePainter(t, track)

	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: gaugeSampleTime(0), HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 130}})

	const smoothedFrac = 0.01 // (101-100)/100
	const rawFrac = 0.30      // (130-100)/100, what a raw-driven marker would have used

	wantCol := color.RGBAModel.Convert(gaugeRampColor(smoothedFrac)).(color.RGBA)
	wantX := int(p.spanX + p.spanW*smoothedFrac)
	if got := img.RGBAAt(wantX, int(p.trackY)); !closeRGBA(got, wantCol, 4) {
		t.Errorf("pixel at the SMOOTHED fraction's own x=%d is %v, want the ramp colour there %v -- "+
			"the marker must be positioned from the smoothed series, not the raw injected value", wantX, got, wantCol)
	}

	rawX := int(p.spanX + p.spanW*rawFrac)
	if math.Abs(float64(rawX-wantX)) < float64(p.notchR) {
		t.Fatalf("test precondition failed: the raw-value x=%d is too close to the smoothed x=%d to distinguish (notchR=%v)", rawX, wantX, p.notchR)
	}
	notchCol := color.RGBAModel.Convert(gaugeRampColor(rawFrac)).(color.RGBA)
	if got := img.RGBAAt(rawX, int(p.trackY)); closeRGBA(got, notchCol, 4) {
		t.Errorf("pixel at the RAW value's own fraction x=%d is %v, matching what a raw-driven marker would have drawn -- "+
			"the marker must not be positioned from the raw/printed value", rawX, got)
	}
}

// TestReadoutGauge_MarkerColourMatchesItsOwnFractionExactly ties the
// marker's colour to the SAME frac that positions it -- the mechanism
// gaugeRampColor's own doc comment says is what keeps nothing in this panel
// distinguishable by colour alone: if the two were computed from two
// independent quantities, they could drift apart, and colour would start
// carrying information position did not already carry.
func TestReadoutGauge_MarkerColourMatchesItsOwnFractionExactly(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, p := heartRateGaugePainter(t, track)

	at, sample := gaugeInteriorReading(50, 150) // interior: smoothed == raw == 150, fraction 0.5
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: at, HasSample: true, Sample: sample})

	want := color.RGBAModel.Convert(gaugeRampColor(0.5)).(color.RGBA)
	x := int(p.spanX + p.spanW*0.5)
	if got := img.RGBAAt(x, int(p.trackY)); !closeRGBA(got, want, 4) {
		t.Errorf("marker pixel at the midpoint is %v, want gaugeRampColor(0.5) = %v", got, want)
	}
}

// TestReadoutGauge_OutOfRangeAboveCeilingDrawsCapNoMarkerPrintsTrueNumber is
// the headline off-scale case: a reading above the scale's ceiling draws
// (1) no marker at all -- there is no honest position past the ceiling for
// a dot to sit at, and parking it AT the ceiling would be indistinguishable
// from an in-range reading that happens to equal it -- (2) the overflow cap
// past the track's own right edge, and (3) the TRUE, unclipped value, never
// the ceiling it would have been clamped to.
func TestReadoutGauge_OutOfRangeAboveCeilingDrawsCapNoMarkerPrintsTrueNumber(t *testing.T) {
	track := gaugeHRTrack(100, 101) // ceiling = 200
	c, img, p := heartRateGaugePainter(t, track)

	notchBox := gaugeNotchShoulderBox(p)
	c.Fill(c.Theme.Background)
	p.Static(c)
	before := snapshotBox(img, notchBox)

	const trueValue = 230 // above the 200 ceiling
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: trueValue}})

	// (1) No marker ink anywhere along the track's own span.
	after := snapshotBox(img, notchBox)
	if !bytes.Equal(before, after) {
		t.Error("the notch search box changed for an above-ceiling reading, want no marker ink at all")
	}

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)

	// (2) The cap draws past the track's own edge, inside the box.
	capBox := Box{X: p.spanX + p.spanW, Y: p.trackY - p.capHalfH, W: p.capW, H: p.capHalfH * 2}
	if _, _, ok := findRGBA(img, capBox, fg, 4); !ok {
		t.Error("no overflow cap ink found past the track's right edge for an above-ceiling reading")
	}
	if capBox.X+capBox.W > p.box.W {
		t.Errorf("the overflow cap (right edge at %v) is not reserved within the box (width %v)", capBox.X+capBox.W, p.box.W)
	}

	// (3) The printed number is the true reading, drawn in Foreground like
	// any other live reading -- this project has no pixel-level way to read
	// back the digits themselves, but Theme.Foreground (rather than
	// Theme.Absent, or nothing) is what distinguishes "the true value was
	// printed" from a placeholder or a value silently clamped to the
	// ceiling and reformatted.
	valueBox := Box{X: p.centerX - p.box.W*0.3, Y: p.valueY - p.valuePx, W: p.box.W * 0.6, H: p.valuePx * 2}
	if _, _, ok := findRGBA(img, valueBox, fg, 4); !ok {
		t.Error("no live-coloured reading found where the value is drawn")
	}
}

// TestReadoutGauge_OutOfRangeBelowFloorDrawsMirrorCapNoMarker is the mirror
// case, load-bearing for pace in particular: a reading below the scale's
// floor draws no marker at all -- never one parked at the floor's own
// position, which is exactly what the fill-based version of this panel drew
// and exactly what a visual gate found indistinguishable from the below-
// floor chevron living in that same spot -- plus the LEFT overflow cap.
func TestReadoutGauge_OutOfRangeBelowFloorDrawsMirrorCapNoMarker(t *testing.T) {
	track := gaugeHRTrack(100, 101) // floor = 100
	c, img, p := heartRateGaugePainter(t, track)

	notchBox := gaugeNotchShoulderBox(p)
	c.Fill(c.Theme.Background)
	p.Static(c)
	before := snapshotBox(img, notchBox)

	const trueValue = 40 // below the 100 floor
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: trueValue}})

	after := snapshotBox(img, notchBox)
	if !bytes.Equal(before, after) {
		t.Error("the notch search box changed for a below-floor reading, want no marker ink at all")
	}

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)

	// The mirror cap draws past the track's own LEFT edge.
	capBox := Box{X: p.spanX - p.capW, Y: p.trackY - p.capHalfH, W: p.capW, H: p.capHalfH * 2}
	if _, _, ok := findRGBA(img, capBox, fg, 4); !ok {
		t.Error("no overflow cap ink found past the track's left edge for a below-floor reading")
	}
	if capBox.X < p.box.X {
		t.Errorf("the mirror overflow cap (left edge at %v) is not reserved within the box (left edge %v)", capBox.X, p.box.X)
	}
}

// TestReadoutGauge_GenuineZeroCadenceDrawsLowChevronAndUnclippedZero pins
// the headline behaviour change this rework makes: with robustGaugeScale no
// longer forcing a floor to 0 (see its own doc comment), a genuine 0 rpm --
// not pedalling, or not striding between steps -- falls BELOW the derived
// floor exactly like any other below-floor reading, drawing the left
// overflow chevron, no marker, and the TRUE printed number ("0"), rather
// than a floor bent down to meet it and a marker sitting confidently at the
// far left end of the track.
//
// gaugeCadenceTrack(50, 101) resolves floor=50, ceiling=150 by the identical
// arithmetic TestRobustGaugeScale_SnapsOutwardToStep already derives by hand
// for a ramp of this shape (50+gaugeRampEdgeShift=51 -> floor 50,
// 150-gaugeRampEdgeShift-1=149 -> ceiling 150 at step=10).
func TestReadoutGauge_GenuineZeroCadenceDrawsLowChevronAndUnclippedZero(t *testing.T) {
	track := gaugeCadenceTrack(50, 101)
	c, img, ctx, box := gaugeFixture(t, track)
	p, ok := Cadence().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if !p.hasTrack {
		t.Fatal("precondition failed: want a resolved gauge track")
	}
	if !gaugeAlmostEqual(p.scale.floor, 50) || !gaugeAlmostEqual(p.scale.ceiling, 150) {
		t.Fatalf("precondition failed: want scale {50 150}, got %+v", p.scale)
	}

	notchBox := gaugeNotchShoulderBox(p)
	c.Fill(c.Theme.Background)
	p.Static(c)
	before := snapshotBox(img, notchBox)

	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasCadence: true, Cadence: 0}})

	after := snapshotBox(img, notchBox)
	if !bytes.Equal(before, after) {
		t.Error("the notch search box changed for a genuine zero reading, want no marker ink at all")
	}

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	capBox := Box{X: p.spanX - p.capW, Y: p.trackY - p.capHalfH, W: p.capW, H: p.capHalfH * 2}
	if _, _, ok := findRGBA(img, capBox, fg, 4); !ok {
		t.Error("no overflow cap ink found past the track's left edge for a genuine zero reading")
	}

	// The printed number is drawn in Foreground -- distinguishing "a true
	// zero was printed" from the Absent-coloured placeholder a dropout
	// would draw instead. See the identical note on the above-ceiling test
	// for why colour, not the digits themselves, is the check available
	// here.
	valueBox := Box{X: p.centerX - p.box.W*0.3, Y: p.valueY - p.valuePx, W: p.box.W * 0.6, H: p.valuePx * 2}
	if _, _, ok := findRGBA(img, valueBox, fg, 4); !ok {
		t.Error("no live-coloured reading found where the value is drawn -- a genuine zero must print, not fall back to a placeholder")
	}
}

// TestReadoutGauge_ScaleIsAFunctionOfContextAlone guards the constraint
// gaugeScale's own doc comment states outright: the scale is resolved once, in Prepare, from
// Context, and Dynamic must never re-derive or widen it. Two independent
// Prepare calls over the identical Context and box must agree exactly, and
// running Dynamic over a wide spread of readings between them must not have
// changed what a THIRD Prepare call resolves.
func TestReadoutGauge_ScaleIsAFunctionOfContextAlone(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, ctx, box := gaugeFixture(t, track)

	first, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok || !first.hasTrack {
		t.Fatal("precondition failed: want a resolved track")
	}
	second, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if first.scale != second.scale {
		t.Fatalf("two Prepare calls over the identical Context disagree: %+v vs %+v", first.scale, second.scale)
	}

	c.Fill(c.Theme.Background)
	first.Static(c)
	for _, v := range []uint8{60, 100, 150, 200, 250} {
		first.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: v}})
	}
	_ = img

	third, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if third.scale != first.scale {
		t.Errorf("the scale changed after Dynamic ran several frames: %+v vs %+v -- Dynamic must never widen or re-derive it", third.scale, first.scale)
	}
}

// TestReadoutGauge_ScalesWithFrameDimensionsNotAPixelConstant is the
// contract's own "how does it scale" question: the same track, prepared
// against boxes shaped like 1080p, 4K and the portrait tree, must produce a
// track width and thickness that scale with the box, never a size unrelated
// to it -- ClimbPanel's identical test (climb_test.go) is the precedent.
func TestReadoutGauge_ScalesWithFrameDimensionsNotAPixelConstant(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	sizes := []struct {
		name string
		w, h float64
	}{
		{"1080p-gauge-shaped", 442, 123},
		{"4K-gauge-shaped", 884, 246},
		{"portrait-gauge-shaped", 300, 160},
	}
	var prevW, prevH, prevR float64
	for i, s := range sizes {
		box := Box{X: 0, Y: 0, W: s.w, H: s.h}
		ctx := &Context{Width: int(s.w), Height: int(s.h), FontScale: 0.05, Fonts: faces, Track: track, GaugeStyle: GaugeStyleTrack}
		p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
		if !ok || !p.hasTrack {
			t.Fatalf("%s: expected a resolved track", s.name)
		}
		if p.spanW <= 0 || p.trackH <= 0 || p.notchR <= 0 {
			t.Fatalf("%s: spanW=%v trackH=%v notchR=%v, want all three positive", s.name, p.spanW, p.trackH, p.notchR)
		}
		if i > 0 && p.spanW == prevW {
			t.Errorf("%s: spanW is identical to the previous box's (%v) despite a different box width", s.name, prevW)
		}
		if i > 0 && p.trackH == prevH {
			t.Errorf("%s: trackH is identical to the previous box's (%v) despite a different box height", s.name, prevH)
		}
		if i > 0 && p.notchR == prevR {
			t.Errorf("%s: notchR is identical to the previous box's (%v) despite a different box height", s.name, prevR)
		}
		prevW, prevH, prevR = p.spanW, p.trackH, p.notchR
	}
}

// --- --gauge-style ---------------------------------------------------------

// TestSelectGaugeStyle_RefusesAnUnknownName is --gauge-style's own version of
// TestSelectTheme_RefusesAnUnknownName: a typo must be refused rather than
// silently rendering the plain style nobody asked to keep.
func TestSelectGaugeStyle_RefusesAnUnknownName(t *testing.T) {
	got, err := SelectGaugeStyle(GaugeStyleNamePlain)
	if err != nil || got != GaugeStylePlain {
		t.Errorf("SelectGaugeStyle(%q) = %v, %v; want GaugeStylePlain, nil", GaugeStyleNamePlain, got, err)
	}
	got, err = SelectGaugeStyle(GaugeStyleNameTrack)
	if err != nil || got != GaugeStyleTrack {
		t.Errorf("SelectGaugeStyle(%q) = %v, %v; want GaugeStyleTrack, nil", GaugeStyleNameTrack, got, err)
	}
	for _, bad := range []string{"", "Plain", "bars", "scale"} {
		if _, err := SelectGaugeStyle(bad); err == nil {
			t.Errorf("SelectGaugeStyle(%q) was accepted", bad)
		}
	}
}

// --- Readout.HasGaugeRule / GaugeRangeText ----------------------------------

// TestReadout_HasGaugeRuleNamesExactlyTheFourFluctuatingMetrics pins which
// readouts are gauge candidates at all, without any Context: HeartRate,
// Pace, Power and Cadence each set a scale rule in their own constructor;
// Distance -- the one readout that only ever increases across an activity,
// never fluctuates -- sets none.
func TestReadout_HasGaugeRuleNamesExactlyTheFourFluctuatingMetrics(t *testing.T) {
	for _, r := range []Readout{HeartRate(), Pace(), Power(), Cadence()} {
		if !r.HasGaugeRule() {
			t.Errorf("%s.HasGaugeRule() = false, want true", r.Name())
		}
	}
	if Distance().HasGaugeRule() {
		t.Error("Distance().HasGaugeRule() = true, want false -- it never fluctuates and has no gauge variant")
	}
}

// TestReadout_GaugeRangeTextMatchesTheResolvedScale derives its expected text
// from the identical arithmetic TestRobustGaugeScale_SnapsOutwardToStep
// already pins: gaugeHRTrack(100, 101) resolves floor=100, ceiling=200 (see
// that test's own comment), so HeartRate's own format ("%.0f") and unit
// ("bpm") must produce "100-200 bpm" -- the exact text this render summary
// prints, and the exact numbers Static draws as the track's own endpoint
// labels, since both read the same format function and the same scale.
func TestReadout_GaugeRangeTextMatchesTheResolvedScale(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	ctx := &Context{Track: track}
	got, ok := HeartRate().GaugeRangeText(ctx)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := "100-200 bpm"; got != want {
		t.Errorf("GaugeRangeText() = %q, want %q", got, want)
	}
}

// TestReadout_GaugeRangeTextReportsNoUsableRange is the fourth drawing
// state's own summary-side check: an activity with too few present readings
// for the smoothed series to answer at all must report ok=false here too,
// matching Prepare's own silent fallback to the plain readout -- the render
// summary's whole reason for calling this method is to say so instead of
// leaving it silent.
func TestReadout_GaugeRangeTextReportsNoUsableRange(t *testing.T) {
	track := gaugeHRTrack(150, 3) // well under gaugeSeriesMinPresent
	ctx := &Context{Track: track}
	if _, ok := HeartRate().GaugeRangeText(ctx); ok {
		t.Error("ok = true for an activity with no usable range, want false")
	}
}

// TestReadout_GaugeRangeTextHasNoGaugeRuleReturnsFalse pins the defensive
// branch: a readout with no scale rule at all (Distance) must report
// ok=false from GaugeRangeText too, not merely from HasGaugeRule -- the two
// methods must agree, since writeGaugeSummary (cmd/render.go) trusts
// HasGaugeRule alone to decide which readouts to ask, but nothing else
// should be able to misuse GaugeRangeText into claiming a range for a
// readout that was never a gauge candidate.
func TestReadout_GaugeRangeTextHasNoGaugeRuleReturnsFalse(t *testing.T) {
	if _, ok := Distance().GaugeRangeText(&Context{}); ok {
		t.Error("ok = true for a readout with no gauge rule at all, want false")
	}
}

// TestReadout_GaugeRangeTextResolvesThroughBind pins that GaugeRangeText
// performs the SAME two steps Prepare does, in order -- bind, then scale
// (see both fields' own doc comments) -- so a readout whose value accessor
// depends on render-wide state reports the range for whichever choice was
// actually made, not whatever the unbound constructor would have used. A
// running sport doubles cadence from rpm to spm (see perLegCadence); the
// printed range must be in the doubled unit, matching what Dynamic prints
// beside it for the same activity.
func TestReadout_GaugeRangeTextResolvesThroughBind(t *testing.T) {
	samples := make([]fitactivity.Sample, 101)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: gaugeSampleTime(i), HasCadence: true, Cadence: uint8(50 + i%50)}
	}
	track := &fitactivity.Track{Sport: "running", Samples: samples}
	ctx := &Context{Track: track}

	got, ok := Cadence().GaugeRangeText(ctx)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !strings.HasSuffix(got, "spm") {
		t.Errorf("GaugeRangeText() = %q, want the doubled running unit spm, not the unbound rpm", got)
	}
}

// --- GaugeStyleDial ------------------------------------------------------
//
// GaugeStyleDial is a second, permanent gauge shape kept alongside
// GaugeStyleTrack (see that type's own doc comment, gauge.go) -- this
// section brings it up to the track's own standard above: the scale
// arithmetic is shared and already covered by the "scale arithmetic"
// section, so this repeats only what differs by SHAPE -- the needle's own
// angle arithmetic, the absent and off-scale drawing policies restated for
// an arc and a needle instead of a track and a dot, the beside-not-below
// layout's own geometry (scale-with-frame-dimensions, no overlap with the
// text column it sits beside), and the wash's own contrast measured against
// the arc's actual rendered pixels rather than assumed from the track's.

// heartRateDialPainter is heartRateGaugePainter's own analogue for
// GaugeStyleDial, failing the test if no dial resolved.
func heartRateDialPainter(t *testing.T, track *fitactivity.Track) (*Canvas, *image.RGBA, *readoutPainter) {
	t.Helper()
	c, img, ctx, box := gaugeFixture(t, track)
	ctx.GaugeStyle = GaugeStyleDial
	p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if !p.hasDial {
		t.Fatal("precondition failed: want a resolved gauge dial")
	}
	return c, img, p
}

// TestDialAngle_SweepsLeftToRightThroughTheTop pins dialAngle's own
// arithmetic against the convention its doc comment states: frac 0 (the
// floor) must point due LEFT of the pivot, frac 1 (the ceiling) due RIGHT,
// and frac 0.5 due UP -- a smaller y, since the canvas is y-DOWN -- never
// due DOWN, which the naive "theta = frac*pi" a reader might reach for
// instead would draw. Getting this wrong would draw a needle that sweeps
// through the FLOOR of the box rather than its dome, which no pixel-level
// test below would catch without also duplicating this arithmetic by hand.
func TestDialAngle_SweepsLeftToRightThroughTheTop(t *testing.T) {
	for _, tc := range []struct{ frac, wantCos, wantSin float64 }{
		{0, -1, 0},
		{0.5, 0, -1},
		{1, 1, 0},
	} {
		a := dialAngle(tc.frac)
		gotCos, gotSin := math.Cos(a), math.Sin(a)
		if !gaugeAlmostEqual(gotCos, tc.wantCos) || !gaugeAlmostEqual(gotSin, tc.wantSin) {
			t.Errorf("dialAngle(%v): cos=%.6f sin=%.6f, want cos=%v sin=%v", tc.frac, gotCos, gotSin, tc.wantCos, tc.wantSin)
		}
	}
}

// TestReadoutGaugeDial_ScalesWithFrameDimensionsNotAPixelConstant is
// TestReadoutGauge_ScalesWithFrameDimensionsNotAPixelConstant's own analogue
// for the dial: the same track, prepared against boxes shaped like 1080p,
// 4K and the portrait tree, must produce a radius and stroke widths that
// scale with the box, never a size unrelated to it.
func TestReadoutGaugeDial_ScalesWithFrameDimensionsNotAPixelConstant(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	sizes := []struct {
		name string
		w, h float64
	}{
		{"1080p-gauge-shaped", 442, 123},
		{"4K-gauge-shaped", 884, 246},
		{"portrait-gauge-shaped", 300, 160},
	}
	var prevR, prevLineW, prevNeedleW float64
	for i, s := range sizes {
		box := Box{X: 0, Y: 0, W: s.w, H: s.h}
		ctx := &Context{Width: int(s.w), Height: int(s.h), FontScale: 0.05, Fonts: faces, Track: track, GaugeStyle: GaugeStyleDial}
		p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
		if !ok || !p.hasDial {
			t.Fatalf("%s: expected a resolved dial", s.name)
		}
		if p.dialR <= 0 || p.dialLineW <= 0 || p.needleW <= 0 {
			t.Fatalf("%s: dialR=%v dialLineW=%v needleW=%v, want all three positive", s.name, p.dialR, p.dialLineW, p.needleW)
		}
		if i > 0 && p.dialR == prevR {
			t.Errorf("%s: dialR is identical to the previous box's (%v) despite a different box height", s.name, prevR)
		}
		if i > 0 && p.dialLineW == prevLineW {
			t.Errorf("%s: dialLineW is identical to the previous box's (%v) despite a different box height", s.name, prevLineW)
		}
		if i > 0 && p.needleW == prevNeedleW {
			t.Errorf("%s: needleW is identical to the previous box's (%v) despite a different box height", s.name, prevNeedleW)
		}
		prevR, prevLineW, prevNeedleW = p.dialR, p.dialLineW, p.needleW
	}
}

// dialSweepBox is the rectangle any IN-RANGE needle drawn by this painter
// can land in: gaugeNotchSearchBox's own analogue for the dial, bounded in x
// by p.spanX/p.spanW -- the identical inset the track's own marker search
// uses -- rather than the arc's full width, so this cannot mistake the
// overflow cap living just past those same bounds (reserveGaugeDial reserves
// exactly capW there for it, see that function's own doc comment) for needle
// ink.
func dialSweepBox(p *readoutPainter) Box {
	return Box{X: p.spanX, Y: p.dialCY - p.dialR, W: p.spanW, H: p.dialR + p.dialPivotR}
}

// dialPivotBox is the small square around the pivot that ONLY a needle
// (drawNeedle) ever paints into: the arc itself (drawDialArc/
// drawDialArcGradient, and the absent wash that reuses it) strokes only
// near radius dialR, nowhere close to the centre, so any change inside this
// box can only be drawNeedle's own line-from-pivot-to-arc plus its pivot
// dot -- unlike dialSweepBox, which also contains the arc's own stroke and
// therefore DOES legitimately change when the wash redraws it, this box is
// the one region a before/after snapshot can use to detect "was a needle
// drawn ANYWHERE" without being confused by the arc's own expected
// redraw.
func dialPivotBox(p *readoutPainter) Box {
	r := p.dialPivotR*2 + p.needleW*2
	return Box{X: p.dialCX - r, Y: p.dialCY - r, W: r * 2, H: r * 2}
}

// TestReadoutGaugeDial_DropoutDrawsNoNeedleAndWashesTheArc is the dial's own
// version of TestReadoutGauge_DropoutDrawsNoMarkerAndWashesTheWholeTrack --
// the first half of the absence policy CLAUDE.md and the task both ask
// every gauge shape to state: an instant with no reading draws no needle
// anywhere, while the arc itself still washes its own full sweep, so a
// dial reporting "no reading this instant" cannot be mistaken for one that
// simply failed to draw.
func TestReadoutGaugeDial_DropoutDrawsNoNeedleAndWashesTheArc(t *testing.T) {
	track := gaugeHRTrack(100, 101) // floor=100, ceiling=200
	c, img, p := heartRateDialPainter(t, track)

	pivotBox := dialPivotBox(p)
	c.Fill(c.Theme.Background)
	p.Static(c)
	before := snapshotBox(img, pivotBox)
	p.Dynamic(c, Frame{}) // the zero Frame: HasSample false.
	after := snapshotBox(img, pivotBox)
	if !bytes.Equal(before, after) {
		t.Error("the pivot box changed on a dropout instant, want no needle ink at all")
	}

	// The wash sits on top of the dim ghost Static already painted along
	// the identical arc -- now a gradient (drawDialArcGradient), not flat
	// Theme.Dim, so this reads the ACTUAL rendered ghost pixel back rather
	// than assuming what colour it is.
	x, y := int(p.dialCX), int(p.dialCY-p.dialR) // the arc's own topmost point.
	c.Fill(c.Theme.Background)
	p.Static(c)
	ghost := img.RGBAAt(x, y)
	p.Dynamic(c, Frame{})
	wash := img.RGBAAt(x, y)
	if ratio := ContrastRatio(wash, ghost); ratio < gaugeAbsentWashContrastFloor {
		t.Errorf("pixel at the arc's own top (%d,%d): wash %v over ghost %v measured %.3f:1, below the %v:1 floor",
			x, y, wash, ghost, ratio, gaugeAbsentWashContrastFloor)
	}
}

// TestReadoutGaugeDial_NeedlePositionMovesWithValue checks the ordinary
// case: a higher in-range reading places the needle's tip further along the
// arc TOWARD the right end than a lower one, mirroring
// TestReadoutGauge_NotchPositionIsProportionalToValue for the track's own
// dot. gaugeHRTrack(100, 101) resolves floor=100, ceiling=200, and readings
// are drawn from gaugeInteriorReading (see that function's own doc
// comment) so the needle's own smoothed position equals the injected
// reading exactly.
func TestReadoutGaugeDial_NeedlePositionMovesWithValue(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, p := heartRateDialPainter(t, track)
	if !gaugeAlmostEqual(p.scale.floor, 100) || !gaugeAlmostEqual(p.scale.ceiling, 200) {
		t.Fatalf("precondition failed: want scale {100 200}, got %+v", p.scale)
	}

	tipSearchBox := func(frac float64) (Box, color.RGBA) {
		a := dialAngle(frac)
		tipX := p.dialCX + p.dialR*math.Cos(a)
		tipY := p.dialCY + p.dialR*math.Sin(a)
		pad := p.needleW*2 + 2
		return Box{X: tipX - pad, Y: tipY - pad, W: pad * 2, H: pad * 2}, color.RGBAModel.Convert(gaugeRampColor(frac)).(color.RGBA)
	}

	atLow, sampleLow := gaugeInteriorReading(10, 110) // fraction 0.10
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: atLow, HasSample: true, Sample: sampleLow})
	lowBox, lowCol := tipSearchBox(0.10)
	xLow, _, okLow := findRGBA(img, lowBox, lowCol, 6)

	atHigh, sampleHigh := gaugeInteriorReading(90, 190) // fraction 0.90
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: atHigh, HasSample: true, Sample: sampleHigh})
	highBox, highCol := tipSearchBox(0.90)
	xHigh, _, okHigh := findRGBA(img, highBox, highCol, 6)

	if !okLow || !okHigh {
		t.Fatal("could not find needle ink near the expected tip for a low and a high in-range reading")
	}
	if xHigh <= xLow {
		t.Errorf("the higher reading's needle tip (x=%d) is not to the right of the lower reading's (x=%d)", xHigh, xLow)
	}
}

// TestReadoutGaugeDial_OutOfRangeDrawsCapNoNeedlePrintsTrueNumber is the
// off-scale policy the task asks every shape to keep: an above-ceiling
// reading draws no needle, the SAME overflow chevron gaugeCap already draws
// for the track (reserveGaugeDial sets spanX/spanW/trackY to the arc's own
// ends and pivot height, see that function's own doc comment), and the true,
// unclipped number.
func TestReadoutGaugeDial_OutOfRangeDrawsCapNoNeedlePrintsTrueNumber(t *testing.T) {
	track := gaugeHRTrack(100, 101) // ceiling = 200
	c, img, p := heartRateDialPainter(t, track)

	sweepBox := dialSweepBox(p)
	c.Fill(c.Theme.Background)
	p.Static(c)
	before := snapshotBox(img, sweepBox)

	const trueValue = 230 // above the 200 ceiling
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: trueValue}})

	after := snapshotBox(img, sweepBox)
	if !bytes.Equal(before, after) {
		t.Error("the needle sweep box changed for an above-ceiling reading, want no needle ink at all")
	}

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	capBox := Box{X: p.spanX + p.spanW, Y: p.trackY - p.capHalfH, W: p.capW, H: p.capHalfH * 2}
	if _, _, ok := findRGBA(img, capBox, fg, 4); !ok {
		t.Error("no overflow cap ink found past the arc's right end for an above-ceiling reading")
	}
	if capBox.X+capBox.W > p.box.W {
		t.Errorf("the overflow cap (right edge at %v) is not reserved within the box (width %v)", capBox.X+capBox.W, p.box.W)
	}

	valueBox := Box{X: p.centerX - p.box.W*0.3, Y: p.valueY - p.valuePx, W: p.box.W * 0.6, H: p.valuePx * 2}
	if _, _, ok := findRGBA(img, valueBox, fg, 4); !ok {
		t.Error("no live-coloured reading found where the value is drawn")
	}
}

// TestReadoutGaugeDial_NoUsableRangeFallsBackToPlain is
// TestReadoutGauge_NoUsableRangeFallsBackToPlain's own analogue for the
// dial: the fourth drawing state, "no usable range", must still draw the
// ordinary reading -- the panel does not decline -- and must draw no arc at
// all, for the identical reason reserveGaugeStrip's own doc comment gives:
// a box or a range too narrow to draw an instrument in is "nothing honest
// to show", not "nothing at all to show".
func TestReadoutGaugeDial_NoUsableRangeFallsBackToPlain(t *testing.T) {
	track := gaugeHRTrack(150, 3) // well under gaugeSeriesMinPresent
	c, img, ctx, box := gaugeFixture(t, track)
	ctx.GaugeStyle = GaugeStyleDial

	p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if p.hasDial {
		t.Fatal("resolved a dial from an activity with no usable range")
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 150}})
	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("no usable range must still draw the ordinary reading; it drew nothing at all")
	}
}

// TestReadoutGaugeDial_ScaleIsAFunctionOfContextAlone is
// TestReadoutGauge_ScaleIsAFunctionOfContextAlone's own analogue for the
// dial: the scale AND the dial's own geometry are resolved once, in
// Prepare, from Context and box alone. Two independent Prepare calls over
// the identical Context and box must agree exactly on both, and running
// Dynamic over a wide spread of readings between them must not have changed
// what a THIRD Prepare call resolves -- Dynamic reads p.scale and p.dialR
// etc, it must never write them.
func TestReadoutGaugeDial_ScaleIsAFunctionOfContextAlone(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	c, img, ctx, box := gaugeFixture(t, track)
	ctx.GaugeStyle = GaugeStyleDial

	first, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok || !first.hasDial {
		t.Fatal("precondition failed: want a resolved dial")
	}
	second, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if first.scale != second.scale {
		t.Fatalf("two Prepare calls over the identical Context disagree on scale: %+v vs %+v", first.scale, second.scale)
	}
	if first.dialCX != second.dialCX || first.dialCY != second.dialCY || first.dialR != second.dialR {
		t.Fatalf("two Prepare calls over the identical Context disagree on the dial's own geometry: "+
			"(%v,%v,r=%v) vs (%v,%v,r=%v)", first.dialCX, first.dialCY, first.dialR, second.dialCX, second.dialCY, second.dialR)
	}

	c.Fill(c.Theme.Background)
	first.Static(c)
	for _, v := range []uint8{60, 100, 150, 200, 250} {
		first.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: v}})
	}
	_ = img

	third, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
	if !ok {
		t.Fatal("Prepare did not return a *readoutPainter")
	}
	if third.scale != first.scale {
		t.Errorf("the scale changed after Dynamic ran several frames: %+v vs %+v -- Dynamic must never widen or re-derive it", third.scale, first.scale)
	}
	if third.dialCX != first.dialCX || third.dialCY != first.dialCY || third.dialR != first.dialR {
		t.Errorf("the dial's own geometry changed after Dynamic ran several frames -- Dynamic must never mutate the painter")
	}
}

// dialArcRightEdge is the arc's own rightmost reach, including the overflow
// chevron -- the identical extent capBox's own right edge already checks in
// TestReadoutGaugeDial_OutOfRangeDrawsCapNoNeedlePrintsTrueNumber, restated
// as a function so the overlap test below can compare it against where the
// text column actually starts drawing.
func dialArcRightEdge(p *readoutPainter) float64 {
	return p.spanX + p.spanW + p.capW
}

// TestReadoutGaugeDial_ArcAndTextDoNotOverlap is the contract's own "which
// part is invariant" question turned into a placement check: the arc's own
// reserved column and the caption/value/unit column beside it must never
// draw into the SAME pixels, at every box shape the two layout trees
// actually place a gauge readout in (see gaugeFixture's own doc comment for
// where the 1080p figure comes from; 4K and the portrait tree's own
// narrower box are ClimbPanel's and the track's identical three-resolution
// discipline, restated here for the dial).
//
// It checks this on RENDERED ink, not only on the resolved fields, because
// a geometry field can be right while a font that overflowed its FitSize
// budget still draws past it -- the two are pinned together by asserting
// the reserved GAP between the two columns (reserveGaugeDial's own dialSide
// plus dialGapFraction) stays free of ink from either side.
func TestReadoutGaugeDial_ArcAndTextDoNotOverlap(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	sizes := []struct {
		name string
		w, h float64
	}{
		{"1080p-gauge-shaped", 442, 123},
		{"4K-gauge-shaped", 884, 246},
		{"portrait-gauge-shaped", 300, 160},
	}
	for _, s := range sizes {
		t.Run(s.name, func(t *testing.T) {
			box := Box{X: 0, Y: 0, W: s.w, H: s.h}
			img := image.NewRGBA(image.Rect(0, 0, int(s.w), int(s.h)))
			c, err := NewCanvas(img, s.h*0.05, DefaultTheme(), faces)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &Context{Width: int(s.w), Height: int(s.h), FontScale: 0.05, Fonts: faces, Track: track, GaugeStyle: GaugeStyleDial}
			p, ok := HeartRate().Prepare(ctx, box).(*readoutPainter)
			if !ok || !p.hasDial {
				t.Fatalf("%s: expected a resolved dial", s.name)
			}

			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 150}})

			// Recomputed by the SAME arithmetic reserveGaugeDial itself uses
			// (see that function's own doc comment) rather than read back off
			// p, so this test cannot pass merely because the implementation's
			// own fields agree with themselves.
			dialSide := math.Min(box.H, box.W*dialMaxWidthFraction)
			gap := box.W * dialGapFraction
			if right := dialArcRightEdge(p); right > box.X+dialSide {
				t.Fatalf("%s: the arc's own rightmost reach (%v) exceeds its reserved column (right edge %v)", s.name, right, box.X+dialSide)
			}

			gapBox := Box{X: box.X + dialSide, Y: box.Y, W: gap, H: box.H}
			if n := inkCount(img, gapBox, c.Theme); n != 0 {
				t.Errorf("%s: %d ink pixels found in the reserved gap between the arc and the text column -- they overlap", s.name, n)
			}

			arcBox := Box{X: box.X, Y: box.Y, W: dialSide, H: box.H}
			if inkCount(img, arcBox, c.Theme) == 0 {
				t.Errorf("%s: no ink at all in the arc's own reserved column", s.name)
			}
			textBox := Box{X: box.X + dialSide + gap, Y: box.Y, W: box.W - dialSide - gap, H: box.H}
			if inkCount(img, textBox, c.Theme) == 0 {
				t.Errorf("%s: no ink at all in the text column beside it", s.name)
			}
		})
	}
}

// --- absent wash contrast ----------------------------------------------

// TestGaugeAbsentWashAlpha_ClearsContrastFloorAgainstDim is this panel's own
// version of TestElevationAbsentFillAlpha_ClearsTheContrastFloor
// (elevation_test.go): the composited pixel a viewer sees -- Theme.Absent,
// faded by gaugeAbsentWashAlpha, blended over flat Theme.Dim -- must clear
// gaugeAbsentWashContrastFloor against Theme.Dim, for every shipped theme.
// gaugeAbsentWashAlpha now also checks against the TINTED colours
// drawTrackGradient/drawDialArcGradient can produce (see that function's
// own doc comment) and returns the largest alpha any of them needs, so its
// result composited over flat Dim alone -- the weakest of the backgrounds
// it was solved against -- clears the floor with room to spare.
func TestGaugeAbsentWashAlpha_ClearsContrastFloorAgainstDim(t *testing.T) {
	for _, th := range Themes() {
		t.Run(th.Name, func(t *testing.T) {
			alpha := gaugeAbsentWashAlpha(th)
			if alpha <= 0 || alpha > 1 {
				t.Fatalf("gaugeAbsentWashAlpha(%s) = %v, want a value in (0, 1]", th.Name, alpha)
			}
			composited := blendOver(th.Dim, th.Absent, alpha)
			if ratio := ContrastRatio(composited, th.Dim); ratio < gaugeAbsentWashContrastFloor {
				t.Errorf("theme %s: absent wash at alpha=%.4f composites to a contrast of %.3f:1 against "+
					"the Dim ghost it is actually drawn over, below gaugeAbsentWashContrastFloor (%v:1)",
					th.Name, alpha, ratio, gaugeAbsentWashContrastFloor)
			}
		})
	}
}

// TestGaugeAbsentWashAlpha_ClearsContrastFloorAgainstEveryTintedSample is
// the new half of the search gaugeAbsentWashAlpha performs now that the
// ghost it composites over is a gradient, not flat Dim: the SAME
// composited-alpha result must ALSO clear the floor against every one of
// gaugeAbsentWashSampleFracs' own tinted backgrounds, not only against flat
// Dim (the test above).
func TestGaugeAbsentWashAlpha_ClearsContrastFloorAgainstEveryTintedSample(t *testing.T) {
	for _, th := range Themes() {
		t.Run(th.Name, func(t *testing.T) {
			alpha := gaugeAbsentWashAlpha(th)
			for _, f := range gaugeAbsentWashSampleFracs {
				tinted := blendColor(th.Dim, gaugeRampColor(f), gaugeAxisRampMix)
				composited := blendOver(tinted, th.Absent, alpha)
				if ratio := ContrastRatio(composited, tinted); ratio < gaugeAbsentWashContrastFloor {
					t.Errorf("theme %s, frac %v: absent wash at alpha=%.4f composites to %.3f:1 against "+
						"the tinted ghost at that point on the ramp, below the %v:1 floor",
						th.Name, f, alpha, ratio, gaugeAbsentWashContrastFloor)
				}
			}
		})
	}
}

// TestGaugeAbsentWashAlpha_RegressionElevationAlphaFailsAgainstTheGhost pins
// the exact bug a visual gate found and measured, so it cannot come back
// silently if a future change "simplifies" this panel back to reusing
// elevationAbsentFillAlpha: that derivation is correct for a wash drawn
// directly over Theme.Background (ElevationPanel, ClimbPanel), and wrong
// here, where the wash is drawn over the dim ghost this panel's OWN Static
// pass already painted. In DarkTheme specifically, elevationAbsentFillAlpha
// composites to roughly 1.37:1 against Dim -- comfortably under the 1.5
// floor either derivation is trying to guarantee -- while
// gaugeAbsentWashAlpha, solved against the correct background(s), clears it.
//
// If the "precondition failed" branch ever fires, elevationAbsentFillAlpha's
// own numbers have changed enough that this test can no longer demonstrate
// the bug it exists to guard against, and it needs new numbers, not
// deletion.
func TestGaugeAbsentWashAlpha_RegressionElevationAlphaFailsAgainstTheGhost(t *testing.T) {
	th := DarkTheme()

	wrongAlpha := elevationAbsentFillAlpha(th)
	wrongRatio := ContrastRatio(blendOver(th.Dim, th.Absent, wrongAlpha), th.Dim)
	if wrongRatio >= gaugeAbsentWashContrastFloor {
		t.Fatalf("precondition failed: elevationAbsentFillAlpha composited to %.3f:1 against Dim, want it to fail the %v:1 floor "+
			"(otherwise this test cannot demonstrate the regression it exists to catch)", wrongRatio, gaugeAbsentWashContrastFloor)
	}

	rightAlpha := gaugeAbsentWashAlpha(th)
	rightRatio := ContrastRatio(blendOver(th.Dim, th.Absent, rightAlpha), th.Dim)
	if rightRatio < gaugeAbsentWashContrastFloor {
		t.Errorf("gaugeAbsentWashAlpha itself composited to %.3f:1 against Dim, below the %v:1 floor it exists to clear", rightRatio, gaugeAbsentWashContrastFloor)
	}
	if rightAlpha == wrongAlpha {
		t.Error("gaugeAbsentWashAlpha equals elevationAbsentFillAlpha for DarkTheme; the two derivations solve against different backgrounds and should not coincide")
	}
}

// TestReadoutGaugeDial_AbsentWashClearsContrastFloorAgainstTheArcsOwnGhost is
// the dial's own version of TestGaugeAbsentWashAlpha_ClearsContrastFloorAgainstDim,
// and it is NOT the same measurement wearing a different name: that test is
// pure colour arithmetic (blendOver(Dim, Absent, alpha) against Dim), true
// of any shape that composites the wash over a Theme.Dim-derived ghost by
// the same formula. This one renders the dial for real and reads the
// pixels back, because Canvas.Arc strokes a thin, anti-aliased line where
// Canvas.Rect (the track's own drawTrack) fills a solid interior -- a
// stroke that thin can leave a rendered pixel that is not cleanly the
// expected colour even where the arc's own ghost is the only thing drawn
// there, and the track's own measurement says nothing about whether THAT
// pixel still clears the floor once composited. See gaugeAbsentWashAlpha's
// own doc comment for why a wash must always be solved -- and, here,
// checked -- against what a panel actually composites over, never assumed
// from a different panel's own backdrop.
//
// The sample point is the arc's own topmost pixel (dialCX, dialCY-dialR),
// the identical point TestReadoutGaugeDial_DropoutDrawsNoNeedleAndWashesTheArc
// already reads -- the tangent there runs horizontal, which is where a thin
// stroke's own antialiasing has the most room to soften the pixel away from
// a clean fill, so it is the least forgiving point on the arc to measure,
// not an arbitrary one.
func TestReadoutGaugeDial_AbsentWashClearsContrastFloorAgainstTheArcsOwnGhost(t *testing.T) {
	track := gaugeHRTrack(100, 101)
	for _, th := range Themes() {
		t.Run(th.Name, func(t *testing.T) {
			const w, h = 442, 123
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewCanvas(img, float64(h)*0.05, th, faces)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &Context{Width: w, Height: h, FontScale: 0.05, Fonts: faces, Track: track, GaugeStyle: GaugeStyleDial}
			p, ok := HeartRate().Prepare(ctx, Box{X: 0, Y: 0, W: w, H: h}).(*readoutPainter)
			if !ok || !p.hasDial {
				t.Fatal("precondition failed: want a resolved dial")
			}
			x, y := int(p.dialCX), int(p.dialCY-p.dialR)

			// The ghost alone: Static only, nothing drawn over it yet.
			c.Fill(c.Theme.Background)
			p.Static(c)
			ghost := img.RGBAAt(x, y)

			// The ghost plus the dropout wash Dynamic composites on top of it.
			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{})
			wash := img.RGBAAt(x, y)

			ratio := ContrastRatio(wash, ghost)
			if ratio < gaugeAbsentWashContrastFloor {
				t.Errorf("theme %s: the dial's own rendered wash pixel measured %.3f:1 against its own rendered ghost pixel, "+
					"below the %v:1 floor -- rendered pixels, not the track's colour arithmetic, are what a viewer actually sees",
					th.Name, ratio, gaugeAbsentWashContrastFloor)
			}
		})
	}
}
