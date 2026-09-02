package render

import (
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
)

var smoothEpoch = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// noisyTrack builds an activity whose gauges jump from second to second.
//
// fittest's generated data varies SMOOTHLY by construction -- it says so -- so
// a smoothing test run against it would measure almost nothing and pass
// whatever smoothing did. Real recordings are not smooth: power from a footpod
// swings by hundreds of watts between strides. This fixture is deliberately
// noisy, and the noise is deterministic so the test is too.
func noisyTrack(n int) *fitactivity.Track {
	samples := make([]fitactivity.Sample, n)
	for i := 0; i < n; i++ {
		f := float64(i)
		// A slow trend with fast noise on top: the trend is what smoothing
		// must preserve, the noise is what it must remove.
		trend := 150 + 20*math.Sin(f/600)
		noise := 25 * math.Sin(f*2.7)
		samples[i] = fitactivity.Sample{
			Time:         smoothEpoch.Add(time.Duration(i) * time.Second),
			HasHeartRate: true, HeartRate: uint8(math.Round(trend + noise)),
			HasPower: true, Power: uint16(math.Round(280 + 120*math.Sin(f*3.1))),
			HasCadence: true, Cadence: uint8(math.Round(85 + 6*math.Sin(f*1.9))),
			HasSpeed: true, Speed: 3 + 0.8*math.Sin(f*2.3),
			HasDistance: true, Distance: f * 3,
			HasGPS: true, Lat: 55 + f*1e-5, Lon: 12 + f*1e-5,
		}
	}
	return &fitactivity.Track{Samples: samples}
}

func smoothContext(t *testing.T, track *fitactivity.Track, speedup float64, smoothing time.Duration) *panel.Context {
	t.Helper()
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivity(timer, 30, speedup)
	if err != nil {
		t.Fatal(err)
	}
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: 320, Height: 180, FontScale: 0.05, Fonts: mustFaces(t),
		Smoothing: panel.Smoothing{Window: smoothing},
	}
}

// meanAbsStep is the average frame-to-frame change in a reading, which is what
// "unreadable flicker" actually measures.
func meanAbsStep(t *testing.T, ctx *panel.Context, read func(fitactivity.Sample) float64) float64 {
	t.Helper()
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	var n int
	prev := read(r.Frame(0).Sample)
	for i := 1; i < r.Frames(); i++ {
		cur := read(r.Frame(i).Sample)
		total += math.Abs(cur - prev)
		prev = cur
		n++
	}
	if n == 0 {
		t.Fatal("no frames")
	}
	return total / float64(n)
}

// TestSmoothing_ReducesFrameToFrameFlicker is the measurement the feature
// exists for.
//
// At 480x a frame advances sixteen seconds, so consecutive frames show samples
// far apart in a noisy signal and the readout is unreadable. The assertion is
// that smoothing cuts the frame-to-frame change substantially -- not that it
// changes some pixels.
func TestSmoothing_ReducesFrameToFrameFlicker(t *testing.T) {
	track := noisyTrack(14400)
	hr := func(s fitactivity.Sample) float64 { return float64(s.HeartRate) }

	off := meanAbsStep(t, smoothContext(t, track, 480, 0), hr)
	auto := smoothContext(t, track, 480, 0)
	autoWindow := auto.Timeline.AutoSmoothingAt(0)
	on := meanAbsStep(t, smoothContext(t, track, 480, autoWindow), hr)

	t.Logf("heart rate changes by %.1f bpm per frame unsmoothed, %.1f smoothed over %v",
		off, on, autoWindow)
	if off < 5 {
		t.Fatalf("the fixture only flickers by %.1f bpm per frame; it cannot show smoothing working", off)
	}
	if on >= off/3 {
		t.Errorf("smoothing cut the flicker from %.1f to %.1f bpm per frame; that is not enough to make it readable", off, on)
	}
}

// TestSmoothing_PreservesTheTrend guards the other side: smoothing that flattened
// everything would pass the flicker test perfectly and destroy the data.
//
// The fixture carries a slow rise and fall under its noise. After smoothing,
// the readout must still span most of that trend -- a hard effort has to show
// as a rise.
func TestSmoothing_PreservesTheTrend(t *testing.T) {
	track := noisyTrack(14400)
	ctx := smoothContext(t, track, 480, 0)
	window := ctx.Timeline.AutoSmoothingAt(0)

	r, err := New(smoothContext(t, track, 480, window), oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := 0; i < r.Frames(); i++ {
		v := float64(r.Frame(i).Sample.HeartRate)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	// The trend alone spans 40 bpm (150 +/- 20). Smoothing must keep most of
	// it; flattening to a single number would be the failure.
	if span := hi - lo; span < 30 {
		t.Errorf("the smoothed readout spans only %.0f bpm; the activity's own trend is 40 and has been flattened away", span)
	}
}

// TestSmoothing_LeavesPositionDistanceAndElevationAlone pins the exclusions,
// each of which is excluded for its own reason.
func TestSmoothing_LeavesPositionDistanceAndElevationAlone(t *testing.T) {
	track := noisyTrack(3600)
	window := 2 * time.Minute
	ctx := smoothContext(t, track, 480, window)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}

	for _, i := range []int{1, r.Frames() / 2, r.Frames() - 2} {
		f := r.Frame(i)
		at := ctx.Timeline.At(i)
		raw, ok := track.AtWithGap(at, maxGap)
		if !ok {
			continue
		}
		// Position: a moving average cuts corners and puts the dot off the
		// route, which is a claim about where somebody was.
		if f.Sample.Lat != raw.Lat || f.Sample.Lon != raw.Lon {
			t.Errorf("frame %d: position was averaged (%v,%v vs %v,%v)",
				i, f.Sample.Lat, f.Sample.Lon, raw.Lat, raw.Lon)
		}
		// Distance accumulates; averaging it buys nothing and lets it go
		// backwards where the window is clipped.
		if f.Sample.Distance != raw.Distance {
			t.Errorf("frame %d: distance was averaged (%v vs %v)", i, f.Sample.Distance, raw.Distance)
		}
		// And the gauges WERE averaged, or this test would pass against
		// smoothing that did nothing at all.
		if f.Sample.HeartRate == raw.HeartRate && f.Sample.Power == raw.Power {
			t.Errorf("frame %d: no gauge was averaged; the exclusions above prove nothing", i)
		}
	}
}

// TestSmoothing_DoesNotFillAGap pins that averaging never invents a reading
// where the activity has none.
//
// An average drawn from either side of a dropout would paper over it with a
// number nobody recorded -- the stale-reading failure wearing arithmetic. Deep
// in a gap the sample stays zero and every panel's placeholder path fires.
func TestSmoothing_DoesNotFillAGap(t *testing.T) {
	// A track with a five-minute hole in the middle.
	var samples []fitactivity.Sample
	for i := 0; i < 1800; i++ {
		if i >= 600 && i < 900 {
			continue
		}
		samples = append(samples, fitactivity.Sample{
			Time:         smoothEpoch.Add(time.Duration(i) * time.Second),
			HasHeartRate: true, HeartRate: uint8(150 + i%20),
		})
	}
	track := &fitactivity.Track{Samples: samples}
	ctx := smoothContext(t, track, 60, 2*time.Minute)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}

	var absent int
	for i := 0; i < r.Frames(); i++ {
		f := r.Frame(i)
		if f.HasSample {
			continue
		}
		absent++
		if f.Sample.HasHeartRate {
			t.Fatalf("frame %d is inside a gap but carries a heart rate; smoothing filled it in", i)
		}
	}
	if absent == 0 {
		t.Fatal("no frame fell in the gap; the fixture cannot exercise this")
	}
}

// TestSmoothSample_AveragesOnlyWhatIsPresent pins the absent-is-not-zero rule
// applied to arithmetic: a missing reading is skipped, never counted as a zero
// that drags the average down.
func TestSmoothSample_AveragesOnlyWhatIsPresent(t *testing.T) {
	// Three samples, two with heart rate 100 and 200, one without.
	track := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: smoothEpoch, HasHeartRate: true, HeartRate: 100},
		{Time: smoothEpoch.Add(time.Second)},
		{Time: smoothEpoch.Add(2 * time.Second), HasHeartRate: true, HeartRate: 200},
	}}
	base := track.Samples[0]
	got := smoothSample(track, smoothEpoch.Add(time.Second), 10*time.Second, base)

	if !got.HasHeartRate {
		t.Fatal("no heart rate resolved from a window containing two")
	}
	// 150, not 100: counting the absent sample as a zero would give 100.
	if got.HeartRate != 150 {
		t.Errorf("averaged heart rate = %d, want 150 -- a missing reading was counted as a zero", got.HeartRate)
	}

	// A window containing no readings at all leaves the field absent rather
	// than inventing one.
	none := &fitactivity.Track{Samples: []fitactivity.Sample{{Time: smoothEpoch}}}
	if got := smoothSample(none, smoothEpoch, time.Minute, fitactivity.Sample{Time: smoothEpoch}); got.HasHeartRate {
		t.Error("a window with no heart rate produced one")
	}
}

// TestSmoothSample_StationaryOpeningYieldsATinyPositiveSpeed pins the
// precondition the pace readout's fix depends on: real smoothing, over a
// real mostly-stationary opening, does not average down to zero. A zero
// would already be caught by the OLD "speed > 0" guard; the bug this test
// backs is a mean that is small and genuinely POSITIVE.
//
// The fixture is nineteen seconds recorded at zero speed -- a runner idling
// at the start of a recording before setting off, an ordinary thing to
// record -- followed by one second at a running pace. smoothSample's own
// rule is "average only what is present" (see its doc comment), and every
// sample here HAS a speed reading, so a twenty-second window spanning all
// twenty samples averages all twenty: (19*0 + 1*3.0) / 20 = 0.15 m/s.
func TestSmoothSample_StationaryOpeningYieldsATinyPositiveSpeed(t *testing.T) {
	var samples []fitactivity.Sample
	for i := 0; i < 19; i++ {
		samples = append(samples, fitactivity.Sample{
			Time:     smoothEpoch.Add(time.Duration(i) * time.Second),
			HasSpeed: true, Speed: 0,
		})
	}
	samples = append(samples, fitactivity.Sample{
		Time:     smoothEpoch.Add(19 * time.Second),
		HasSpeed: true, Speed: 3.0,
	})
	track := &fitactivity.Track{Samples: samples}

	// Centred at 9.5s, half of the 20s window either side spans [-0.5s,
	// 19.5s] -- wide enough to catch all twenty native samples, from the
	// first stationary one to the single moving one at the end.
	at := smoothEpoch.Add(9500 * time.Millisecond)
	got := smoothSample(track, at, 20*time.Second, samples[0])
	if !got.HasSpeed {
		t.Fatal("no speed resolved from a window that contains readings")
	}
	if want := 3.0 / 20; math.Abs(got.Speed-want) > 1e-9 {
		t.Fatalf("smoothed speed = %v, want %v (3.0 m/s over 20 samples, 19 of them zero)", got.Speed, want)
	}
	if got.Speed <= 0 {
		t.Fatal("the fixture averaged to zero; it cannot reproduce a bug that only a genuinely positive tiny mean produces")
	}

	// The pace readout's template, "88:88", has room for at most 99 minutes
	// and 59 seconds per kilometre (see panel.maxPaceSeconds): 1000/5999 m/s.
	// A smoothed speed below that renders a pace the template cannot hold.
	const minRenderablePace = 1000.0 / 5999.0
	if got.Speed >= minRenderablePace {
		t.Fatalf("smoothed speed %v m/s is fast enough for the pace template to hold; want less than %v to reproduce the overflow",
			got.Speed, minRenderablePace)
	}
	t.Logf("nineteen stationary seconds and one at 3.0 m/s smooth to %v m/s, whose naive reciprocal pace is %.0f s/km",
		got.Speed, 1000/got.Speed)
}

// TestSmoothSample_ZeroWindowIsIdentity pins that --smoothing off shows what
// was recorded, byte for byte.
func TestSmoothSample_ZeroWindowIsIdentity(t *testing.T) {
	track := noisyTrack(100)
	base := track.Samples[50]
	got := smoothSample(track, base.Time, 0, base)
	if got.HeartRate != base.HeartRate || got.Power != base.Power || got.Speed != base.Speed {
		t.Error("a zero window changed the reading; --smoothing off must show what was recorded")
	}
}

// TestAutoSmoothing_ScalesWithCompressionAndVanishesAtRealTime pins the rule.
//
// The window is a fixed slice of VIDEO time, so it is the same perceptual
// amount of smoothing whatever the compression -- and at real time it lands
// well under the one-second interval the data is recorded at, so there is
// nothing to average and the readouts show what was recorded.
func TestAutoSmoothing_ScalesWithCompressionAndVanishesAtRealTime(t *testing.T) {
	cases := []struct {
		speedup float64
		want    time.Duration
	}{
		// Below about three times real time the window falls under a second,
		// which spans at most one recorded sample, and auto means off.
		{1, 0},
		{2, 0},
		{3, 0},
		{4, 1200 * time.Millisecond},
		{60, 18 * time.Second},
		{480, 144 * time.Second},
	}
	for _, c := range cases {
		tl, err := panel.NewTimeline(smoothEpoch, time.Hour, 30, c.speedup)
		if err != nil {
			t.Fatal(err)
		}
		if got := tl.AutoSmoothingAt(0); got != c.want {
			t.Errorf("at %vx the auto window is %v, want %v", c.speedup, got, c.want)
		}
	}

	// At real time auto means off, and the reading is exactly what was
	// recorded. This is the property that matters rather than the number
	// above, and it caught a false claim: a sub-second window is NOT a no-op,
	// because Track.Window interpolates its endpoints, so a 300ms window
	// returned three points -- two invented -- and moved a heart rate from 147
	// to 149. See minSmoothingWindow.
	tl, err := panel.NewTimeline(smoothEpoch, time.Hour, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	track := noisyTrack(600)
	base := track.Samples[300]
	if got := smoothSample(track, base.Time, tl.AutoSmoothingAt(0), base); got.HeartRate != base.HeartRate {
		t.Errorf("auto smoothing at real time changed a reading from %d to %d", base.HeartRate, got.HeartRate)
	}
}
