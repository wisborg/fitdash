package panel

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
)

var epoch = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// TestNewTimeline_FrameCountIsDurationTimesRate derives every expectation from
// the inputs rather than pinning what the code returns.
func TestNewTimeline_FrameCountIsDurationTimesRate(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		fps  float64
		want int
	}{
		{"ten seconds at 30", 10 * time.Second, 30, 300},
		{"ten seconds at 60", 10 * time.Second, 60, 600},
		{"one minute at 30", time.Minute, 30, 1800},
		// 29.97 is the reason FPS is a float64. 600s * 29.97 = 17982.
		{"ten minutes at 29.97", 10 * time.Minute, 29.97, 17982},
		// A 25-minute activity at 30 fps: the render-cost figure the
		// architecture is designed around.
		{"twenty-five minutes at 30", 25 * time.Minute, 30, 45000},
		// Not a whole number of frames: 10.02s * 30 = 300.6, nearest is 301.
		// Rounding rather than truncating means a duration lands on the
		// closest frame instead of always losing the remainder.
		{"a duration that is not a whole number of frames", 10020 * time.Millisecond, 30, 301},
		// Rounds to 0.3 frames; a positive duration must still produce a
		// video, and a zero-frame file is one no player will open.
		{"shorter than a single frame", 10 * time.Millisecond, 30, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl, err := NewTimeline(epoch, c.d, c.fps, 1)
			if err != nil {
				t.Fatalf("NewTimeline: %v", err)
			}
			if got := tl.Frames(); got != c.want {
				t.Errorf("Frames() = %d, want %d", got, c.want)
			}
		})
	}
}

// TestTimeline_AtIsAffineAndHalfOpen pins the two properties the whole design
// leans on: At is Start + i/FPS with no special cases, and the last frame sits
// one step SHORT of the end.
//
// The half-open interval is what makes the encoded video's duration equal the
// activity's. A frame occupies the time until the next one, so a final frame
// landing exactly on the end instant would make the video one frame longer
// than the activity it shows -- an error too small to notice and too systematic
// to be anything but a bug.
func TestTimeline_AtIsAffineAndHalfOpen(t *testing.T) {
	const (
		d   = 10 * time.Second
		fps = 30.0
	)
	tl, err := NewTimeline(epoch, d, fps, 1)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}

	if got := tl.At(0); !got.Equal(epoch) {
		t.Errorf("At(0) = %v, want the start %v", got, epoch)
	}

	end := epoch.Add(d)
	last := tl.At(tl.Frames() - 1)
	if !last.Before(end) {
		t.Errorf("At(last) = %v, which is not before the end %v; frames must cover [start, end)", last, end)
	}
	if want := end.Add(-time.Duration(math.Round(float64(time.Second) / fps))); !last.Equal(want) {
		t.Errorf("At(last) = %v, want one frame short of the end, %v", last, want)
	}

	// Affine: every step is the same size to within the nanosecond the
	// instants are rounded to.
	//
	// One nanosecond of slack, not zero. 1/30 s is 33333333.33 ns, so
	// consecutive rounded instants necessarily alternate between 33333333 and
	// 33333334 ns. That alternation IS the correct behaviour -- it is what
	// keeps the error against the exact instant bounded rather than letting it
	// accumulate -- and an earlier version of this test demanded exact
	// uniformity, which is stricter than right and failed against working
	// code. The property that actually matters is the absence of cumulative
	// drift, and TestTimeline_AtDoesNotDriftAcrossALongRender pins that.
	step := time.Duration(math.Round(float64(time.Second) / fps))
	for _, i := range []int{1, 2, 149, 150, 298, 299} {
		got := tl.At(i).Sub(tl.At(i - 1))
		if d := got - step; d < -time.Nanosecond || d > time.Nanosecond {
			t.Errorf("At(%d)-At(%d) = %v, want %v within a nanosecond", i, i-1, got, step)
		}
	}

	// Defined outside the render too, deliberately: a caller computing a
	// lookahead instant should get arithmetic, not a special case.
	if got, want := tl.At(-1), epoch.Add(-step); !got.Equal(want) {
		t.Errorf("At(-1) = %v, want %v", got, want)
	}
}

// TestTimeline_AtDoesNotDriftAcrossALongRender guards the rounding.
//
// Accumulating a per-frame offset, or truncating instead of rounding, leaves
// an error that grows with the frame index -- invisible at frame 10 and worth
// most of a second by frame 45,000, where it shows up as a clock that has
// fallen behind the video it is burned into.
func TestTimeline_AtDoesNotDriftAcrossALongRender(t *testing.T) {
	const fps = 30.0
	tl, err := NewTimeline(epoch, 25*time.Minute, fps, 1)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	if tl.Frames() != 45000 {
		t.Fatalf("precondition: expected 45000 frames, got %d", tl.Frames())
	}
	for _, i := range []int{0, 1, 1000, 22500, 44999} {
		want := epoch.Add(time.Duration(math.Round(float64(i) / fps * float64(time.Second))))
		got := tl.At(i)
		if diff := got.Sub(want); diff < -time.Nanosecond || diff > time.Nanosecond {
			t.Errorf("At(%d) = %v, want %v (off by %v)", i, got, want, diff)
		}
	}
}

// TestTimeline_IndexAtRoundTripsWithAt pins the inverse. Having it is most of
// the reason At is affine -- it is what makes "the frame at 12m30s" a division
// rather than a search through a pause list.
func TestTimeline_IndexAtRoundTripsWithAt(t *testing.T) {
	tl, err := NewTimeline(epoch, 25*time.Minute, 30, 1)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	for _, i := range []int{0, 1, 37, 1000, 22500, 44999} {
		offset := tl.At(i).Sub(tl.Start())
		if got := tl.IndexAt(offset); got != i {
			t.Errorf("IndexAt(At(%d)) = %d, want %d", i, got, i)
		}
	}
	// A named offset, computed by hand: 12m30s at 30 fps is frame 22500.
	if got, want := tl.IndexAt(12*time.Minute+30*time.Second), 22500; got != want {
		t.Errorf("IndexAt(12m30s) = %d, want %d", got, want)
	}
}

// TestTimeline_IndexAtClampsRatherThanErroring documents a deliberate
// asymmetry with the PNG sink, which treats an unreachable frame index as an
// error.
//
// An OFFSET past the end of the activity has an obvious intended meaning --
// the last frame -- whereas a raw frame INDEX past the end of the render is a
// number the caller must have computed wrongly. Different questions, different
// answers.
func TestTimeline_IndexAtClampsRatherThanErroring(t *testing.T) {
	tl, err := NewTimeline(epoch, 10*time.Second, 30, 1)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	if got, want := tl.IndexAt(time.Hour), tl.Frames()-1; got != want {
		t.Errorf("IndexAt(past the end) = %d, want the last frame %d", got, want)
	}
	if got := tl.IndexAt(-time.Hour); got != 0 {
		t.Errorf("IndexAt(before the start) = %d, want 0", got)
	}
}

// TestNewTimeline_RejectsUnusableInputs keeps the constructor from producing a
// Timeline whose arithmetic is meaningless.
func TestNewTimeline_RejectsUnusableInputs(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		d     time.Duration
		fps   float64
	}{
		{"no start instant", time.Time{}, time.Minute, 30},
		{"zero duration", epoch, 0, 30},
		{"negative duration", epoch, -time.Minute, 30},
		{"zero fps", epoch, time.Minute, 0},
		{"negative fps", epoch, time.Minute, -30},
		{"infinite fps", epoch, time.Minute, math.Inf(1)},
		{"NaN fps", epoch, time.Minute, math.NaN()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTimeline(c.start, c.d, c.fps, 1); err == nil {
				t.Error("NewTimeline accepted it")
			}
		})
	}
}

// TestNewTimelineForActivity_SpansTheModelsWindowNotTheSamples is the test
// that justifies taking a TimerModel rather than a Track.
//
// The fixture's session declares an elapsed total, and its records end where
// they end; those need not coincide. A timeline built from the samples would
// span a different interval than Frame.Elapsed is measured against, so the
// final frame's clock would read something other than the activity's total --
// silently, and by a second or so.
func TestNewTimelineForActivity_SpansTheModelsWindowNotTheSamples(t *testing.T) {
	opts := fittest.DefaultOptions()
	opts.Count = 600
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	winStart, winEnd := timer.Window()

	const fps = 30.0
	tl, err := NewTimelineForActivity(timer, fps, 1)
	if err != nil {
		t.Fatalf("NewTimelineForActivity: %v", err)
	}

	if !tl.Start().Equal(winStart) {
		t.Errorf("Start() = %v, want the model's window start %v", tl.Start(), winStart)
	}
	want := int(math.Round(winEnd.Sub(winStart).Seconds() * fps))
	if got := tl.Frames(); got != want {
		t.Errorf("Frames() = %d, want %d (the model's window at %v fps)", got, want, fps)
	}

	// The property the whole thing is for: the last frame's elapsed time is
	// within one frame of the activity's own total, as Elapsed reports it.
	total := timer.Elapsed(winEnd)
	lastElapsed := timer.Elapsed(tl.At(tl.Frames() - 1))
	oneFrame := time.Duration(math.Round(float64(time.Second) / fps))
	if gap := total - lastElapsed; gap < 0 || gap > oneFrame+time.Millisecond {
		t.Errorf("the last frame reads %v against the activity's total %v; the gap %v should be under one frame",
			lastElapsed, total, gap)
	}
}

// TestNewTimelineForActivity_RejectsAnActivityWithNoExtent covers the file
// that carried neither session timing nor samples. Producing a zero-frame
// timeline instead would push the failure into the encoder, where it surfaces
// as a broken video rather than as a message about the input.
func TestNewTimelineForActivity_RejectsAnActivityWithNoExtent(t *testing.T) {
	if _, err := NewTimelineForActivity(nil, 30, 1); err == nil {
		t.Error("accepted a nil timer model")
	}
	empty := fitactivity.BuildTimerModel(&fitactivity.Track{})
	if _, err := NewTimelineForActivity(empty, 30, 1); err == nil {
		t.Error("accepted an activity with no resolved window")
	}
}

// TestTimeline_SpeedupCompressesTheVideoNotTheActivity pins what a speedup
// does and, as importantly, what it does not.
//
// The frame count falls, so the video is shorter. The instants the frames show
// still march through the whole activity, so the dashboard's clock still reads
// activity time -- the video is compressed, the data is not relabelled.
func TestTimeline_SpeedupCompressesTheVideoNotTheActivity(t *testing.T) {
	const (
		activity = 60 * time.Minute
		fps      = 30.0
	)
	cases := []struct {
		name       string
		speedup    float64
		wantFrames int
	}{
		// 3600 s at 30 fps.
		{"real time", 1, 108000},
		{"ten times", 10, 10800},
		{"sixty times: an hour becomes a minute", 60, 1800},
		// Slower than real time is legal and occasionally wanted.
		{"half speed", 0.5, 216000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl, err := NewTimeline(epoch, activity, fps, c.speedup)
			if err != nil {
				t.Fatalf("NewTimeline: %v", err)
			}
			if got := tl.Frames(); got != c.wantFrames {
				t.Errorf("Frames() = %d, want %d", got, c.wantFrames)
			}
			// The video's own length is frames/fps.
			wantVideo := time.Duration(float64(c.wantFrames) / fps * float64(time.Second))
			if got := tl.Duration(); got != wantVideo {
				t.Errorf("Duration() = %v, want %v", got, wantVideo)
			}
			// And the frames still span the activity: the last one lands
			// within a single frame's worth of its end.
			covered := tl.At(tl.Frames() - 1).Sub(tl.Start())
			step := time.Duration(c.speedup / fps * float64(time.Second))
			if gap := activity - covered; gap < 0 || gap > step+time.Millisecond {
				t.Errorf("the frames cover %v of a %v activity, short by %v; that is more than one frame's step of %v",
					covered, activity, gap, step)
			}
		})
	}
}

// TestTimeline_SpeedupLeavesFrameZeroAtTheStart checks the compression does not
// shift the origin: whatever the speedup, the first frame shows the activity's
// beginning.
func TestTimeline_SpeedupLeavesFrameZeroAtTheStart(t *testing.T) {
	for _, speedup := range []float64{0.5, 1, 10, 60, 3600} {
		tl, err := NewTimeline(epoch, time.Hour, 30, speedup)
		if err != nil {
			t.Fatalf("speedup %v: %v", speedup, err)
		}
		if got := tl.At(0); !got.Equal(epoch) {
			t.Errorf("speedup %v: At(0) = %v, want the start %v", speedup, got, epoch)
		}
		// Speedup was renamed to BaseSpeedup (see the Timeline doc comment):
		// with no highlights, the single segment's rate IS the whole-activity
		// rate that was asked for, so the value this pins is unchanged.
		if got := tl.BaseSpeedup(); got != speedup {
			t.Errorf("BaseSpeedup() = %v, want %v", got, speedup)
		}
	}
}

// TestTimeline_IndexAtMeansActivityTimeNotVideoTime pins whose clock an offset
// is on.
//
// A user asking for the frame at 12m30s means twelve and a half minutes into
// their RUN. At a speedup of 10 that is seventy-five seconds into the video,
// and answering with the frame at 12m30s of video would be a different frame
// entirely -- two hours into the activity.
func TestTimeline_IndexAtMeansActivityTimeNotVideoTime(t *testing.T) {
	tl, err := NewTimeline(epoch, 2*time.Hour, 30, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 12m30s of activity at 10x and 30 fps: 750 s / 10 * 30 = 2250.
	i := tl.IndexAt(12*time.Minute + 30*time.Second)
	if want := 2250; i != want {
		t.Errorf("IndexAt(12m30s) = %d, want %d", i, want)
	}
	// And the frame it names shows that instant back.
	if got, want := tl.At(i).Sub(tl.Start()), 12*time.Minute+30*time.Second; got != want {
		t.Errorf("the frame IndexAt named shows %v into the activity, want %v", got, want)
	}
}

// TestTimeline_DurationAndActivityDurationDiffer keeps the two apart. Reporting
// one where the other belongs would put a four-hour figure on a three-minute
// file.
func TestTimeline_DurationAndActivityDurationDiffer(t *testing.T) {
	tl, err := NewTimeline(epoch, 4*time.Hour, 30, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tl.Duration(), 4*time.Minute; got != want {
		t.Errorf("video Duration() = %v, want %v", got, want)
	}
	if got, want := tl.ActivityDuration(), 4*time.Hour; got != want {
		t.Errorf("ActivityDuration() = %v, want %v", got, want)
	}

	// At real time they agree, which is the case that would let a confusion
	// between them go unnoticed.
	real1, err := NewTimeline(epoch, 4*time.Hour, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	if real1.Duration() != real1.ActivityDuration() {
		t.Errorf("at real time the two should agree: %v vs %v", real1.Duration(), real1.ActivityDuration())
	}
}

// TestSpeedupFor_IsTheInverseOfTheCompression checks a target duration and a
// factor are two ways of saying one thing.
func TestSpeedupFor_IsTheInverseOfTheCompression(t *testing.T) {
	cases := []struct {
		activity, target time.Duration
		want             float64
	}{
		{time.Hour, time.Minute, 60},
		{4 * time.Hour, 4 * time.Minute, 60},
		{25 * time.Minute, 25 * time.Minute, 1},
		{10 * time.Minute, 20 * time.Minute, 0.5},
	}
	for _, c := range cases {
		if got := SpeedupFor(c.activity, c.target); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("SpeedupFor(%v, %v) = %v, want %v", c.activity, c.target, got, c.want)
		}
	}

	// Round trip: the factor a target implies produces a video of that length.
	const activity = 4 * time.Hour
	target := 3 * time.Minute
	tl, err := NewTimeline(epoch, activity, 30, SpeedupFor(activity, target))
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.Duration(); math.Abs(float64(got-target)) > float64(time.Second/30) {
		t.Errorf("a timeline built for a %v video came out %v", target, got)
	}

	// Nonsense inputs yield real time rather than an invented compression.
	if got := SpeedupFor(0, time.Minute); got != 1 {
		t.Errorf("SpeedupFor with no activity = %v, want 1", got)
	}
	if got := SpeedupFor(time.Hour, 0); got != 1 {
		t.Errorf("SpeedupFor with no target = %v, want 1", got)
	}
}

// TestNewTimeline_RejectsAnUnusableSpeedup keeps a nonsense factor from
// producing a timeline whose arithmetic is meaningless.
func TestNewTimeline_RejectsAnUnusableSpeedup(t *testing.T) {
	for _, bad := range []float64{0, -1, math.Inf(1), math.NaN()} {
		if _, err := NewTimeline(epoch, time.Hour, 30, bad); err == nil {
			t.Errorf("NewTimeline accepted a speedup of %v", bad)
		}
	}
}

// TestNewSegmentedTimeline_ThirtySecondsPlusNineSecondsOfHighlightsIsThirtyNine
// is the named example from the design: a base rate solved for a 30s video,
// plus one highlight explicitly paced to take ten video seconds where it
// would otherwise have taken about one, comes out to 39 seconds of video.
//
// Derived by hand: a 30-minute (1800s) activity at a base speedup of 60
// renders in 30s (1800/60). Carving a one-minute (60s) highlight out of it
// and pacing that highlight to 10s of video leaves 1740s of NON-highlighted
// activity at the base rate -- round(1740/60*30) = 870 frames -- plus the
// highlight's own round(60/6*30) = 300 frames, where 6 = 60s/10s is the rate
// video=10s implies. 870+300 = 1170 frames at 30fps is exactly 39s: the 30s
// the base alone would have produced, plus the 9s the highlight added beyond
// the ~1s (1740's remainder... see below) it would have taken as part of the
// base.
func TestNewSegmentedTimeline_ThirtySecondsPlusNineSecondsOfHighlightsIsThirtyNine(t *testing.T) {
	const (
		fps  = 30.0
		base = 60.0
	)
	activity := 30 * time.Minute
	highlight := Highlight{From: 10 * time.Minute, To: 11 * time.Minute, Video: 10 * time.Second}

	// The base alone, no highlight: the 30s --video-duration was solved for.
	plain, err := NewTimeline(epoch, activity, fps, base)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	if got, want := plain.Duration(), 30*time.Second; got != want {
		t.Fatalf("precondition: the base alone renders in %v, want %v", got, want)
	}

	tl, err := NewSegmentedTimeline(epoch, activity, fps, base, []Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	if got, want := tl.Frames(), 1170; got != want {
		t.Errorf("Frames() = %d, want %d", got, want)
	}
	if got, want := tl.Duration(), 39*time.Second; got != want {
		t.Errorf("Duration() = %v, want %v (30s base + 9s of highlight)", got, want)
	}
	// The activity itself is still covered end to end; only the video's
	// length changed.
	if got, want := tl.ActivityDuration(), activity; got != want {
		t.Errorf("ActivityDuration() = %v, want %v -- a highlight repaces the video, not the activity it covers", got, want)
	}
}

// TestNewSegmentedTimeline_AnchorsEachSegmentAtItsTrueBoundary pins Trap 1:
// a highlight's segment starts at the true offset from the timeline's own
// start, not wherever the previous segment's rounded frame count happened to
// land. If it were chained instead, rounding error from the segments before
// it would accumulate and this highlight would start measurably away from
// the instant it was asked for.
func TestNewSegmentedTimeline_AnchorsEachSegmentAtItsTrueBoundary(t *testing.T) {
	const fps = 29.97 // an FPS that never divides evenly, to force rounding
	highlight := Highlight{From: 12*time.Minute + 34*time.Second, To: 12*time.Minute + 44*time.Second, RateFactor: 3}

	tl, err := NewSegmentedTimeline(epoch, 25*time.Minute, fps, 17, []Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	i := tl.IndexAt(highlight.From)
	if got, want := tl.At(i), epoch.Add(highlight.From); got != want {
		t.Errorf("the highlight's first frame shows %v, want exactly its own boundary %v (off by %v)",
			got, want, got.Sub(want))
	}
}

// TestTimeline_AtIsStrictlyIncreasingAcrossEverySeam pins the property the
// design's own derivation predicts rather than asserts: giving up a single
// affine rate does not give up monotonicity. Every segment has a positive
// rate and its own true-boundary origin, so the map stays strictly
// increasing across a seam even though the per-frame step there is not
// exactly 1/FPS of activity time -- see NewSegmentedTimeline's doc comment.
func TestTimeline_AtIsStrictlyIncreasingAcrossEverySeam(t *testing.T) {
	highlights := []Highlight{
		{From: 30 * time.Second, To: 40 * time.Second, RateFactor: 20},
		{From: 60 * time.Second, To: 61 * time.Second, RateFactor: 0.5},
	}
	tl, err := NewSegmentedTimeline(epoch, 100*time.Second, 30, 2, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	if tl.Frames() < 100 {
		t.Fatalf("precondition: expected a render of some size, got %d frames", tl.Frames())
	}

	prev := tl.At(0)
	for i := 1; i < tl.Frames(); i++ {
		cur := tl.At(i)
		if !cur.After(prev) {
			t.Fatalf("At(%d) = %v is not strictly after At(%d) = %v; the map must never go flat or backward",
				i, cur, i-1, prev)
		}
		prev = cur
	}
}

// TestTimeline_IndexAtRoundTripsInsideEachSegment extends
// TestTimeline_IndexAtRoundTripsWithAt to a piecewise timeline: At and
// IndexAt must still invert each other within every segment, not only the
// render's single one.
func TestTimeline_IndexAtRoundTripsInsideEachSegment(t *testing.T) {
	highlights := []Highlight{
		{From: 30 * time.Second, To: 40 * time.Second, RateFactor: 20},
		{From: 60 * time.Second, To: 61 * time.Second, RateFactor: 0.5},
	}
	tl, err := NewSegmentedTimeline(epoch, 100*time.Second, 30, 2, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	// The first, middle and last frame of every segment, located via IndexAt
	// on each highlight's own boundary plus the frames on either side of it.
	probes := []int{0, 1, tl.Frames() / 2, tl.Frames() - 1}
	for _, h := range highlights {
		probes = append(probes, tl.IndexAt(h.From), tl.IndexAt(h.From)+1, tl.IndexAt(h.To)-1, tl.IndexAt(h.To))
	}
	for _, i := range probes {
		if i < 0 || i >= tl.Frames() {
			continue
		}
		offset := tl.At(i).Sub(tl.Start())
		if got := tl.IndexAt(offset); got != i {
			t.Errorf("IndexAt(At(%d)) = %d, want %d", i, got, i)
		}
	}
}

// TestNewSegmentedTimeline_ZeroFrameHighlightIsForcedToOne pins Trap 2: a
// highlight so short, or paced so fast, that length/rate*fps rounds to zero
// frames still occupies exactly one rather than silently vanishing from the
// render. A named highlight occupying no video is the same class of failure
// as a silently-missing panel.
func TestNewSegmentedTimeline_ZeroFrameHighlightIsForcedToOne(t *testing.T) {
	// Ten milliseconds at 30fps and the base rate (1x, unset video/speedup --
	// "mark it, do not re-pace it") is 0.3 of a frame, which rounds to zero.
	highlight := Highlight{From: 500 * time.Second, To: 500*time.Second + 10*time.Millisecond}

	tl, err := NewSegmentedTimeline(epoch, 1000*time.Second, 30, 1, []Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	i := tl.IndexAt(highlight.From)
	index, weight := tl.IntervalAt(i, 0)
	if index != 0 {
		t.Fatalf("the highlight's own first frame does not report belonging to it: IntervalAt = %d, want 0", index)
	}
	if weight != 1 {
		t.Errorf("IntervalAt weight = %v, want 1 with a hard cut (transition=0)", weight)
	}
	// And it really is exactly one frame: the very next frame belongs to
	// nothing.
	if index, _ := tl.IntervalAt(i+1, 0); index != NoHighlight {
		t.Errorf("the highlight spans more than the one frame it was forced to: frame %d also reports index %d", i+1, index)
	}
}

// TestTimeline_MaxSpeedupIsTheCoarsestSegment pins MaxSpeedup against
// BaseSpeedup: with no highlights they agree, and a highlight running FASTER
// than the base -- the case bindDistancePrecision cares about -- moves
// MaxSpeedup but never BaseSpeedup.
func TestTimeline_MaxSpeedupIsTheCoarsestSegment(t *testing.T) {
	plain, err := NewTimeline(epoch, time.Hour, 30, 60)
	if err != nil {
		t.Fatal(err)
	}
	if plain.BaseSpeedup() != 60 || plain.MaxSpeedup() != 60 {
		t.Errorf("with no highlights BaseSpeedup=%v MaxSpeedup=%v, want both 60", plain.BaseSpeedup(), plain.MaxSpeedup())
	}

	fast := Highlight{From: 10 * time.Minute, To: 11 * time.Minute, RateFactor: 480}
	tl, err := NewSegmentedTimeline(epoch, time.Hour, 30, 60, []Highlight{fast})
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.BaseSpeedup(); got != 60 {
		t.Errorf("BaseSpeedup() = %v, want 60 -- a highlight's own rate must not change it", got)
	}
	if got := tl.MaxSpeedup(); got != 480 {
		t.Errorf("MaxSpeedup() = %v, want the highlight's own coarser rate, 480", got)
	}
}

// TestTimeline_AutoSmoothingAtScalesPerSegment pins that a slowed highlight
// gets its OWN auto-smoothing window rather than one derived from the
// render-wide base rate, which is the whole reason AutoSmoothing was
// retired in favour of a per-frame AutoSmoothingAt.
func TestTimeline_AutoSmoothingAtScalesPerSegment(t *testing.T) {
	slow := Highlight{From: 10 * time.Minute, To: 11 * time.Minute, RateFactor: 2}
	tl, err := NewSegmentedTimeline(epoch, time.Hour, 30, 480, []Highlight{slow})
	if err != nil {
		t.Fatal(err)
	}

	outside := tl.AutoSmoothingAt(0)
	if want := time.Duration(float64(autoSmoothingVideoWindow) * 480); outside != want {
		t.Errorf("outside the highlight, AutoSmoothingAt = %v, want the base-rate window %v", outside, want)
	}

	inside := tl.IndexAt(slow.From) + 1
	got := tl.AutoSmoothingAt(inside)
	// At 2x the video-time window scales to 600ms, under minSmoothingWindow,
	// so auto means off inside the highlight -- exactly the point: a rep
	// slowed down to show its own detail must not still be averaged over a
	// window sized for 480x.
	if got != 0 {
		t.Errorf("inside the slowed highlight, AutoSmoothingAt = %v, want 0 (below minSmoothingWindow at 2x)", got)
	}
}

// TestTimeline_AutoSmoothingBaseMatchesTheBaseRateEverywhereOutsideAHighlight
// pins the method the summary reads (cmd/render.go's smoothingNote, "auto
// (Ns base)"): it must report the window the RENDER-WIDE base rate implies,
// not a highlight's own rate, and it must agree with what AutoSmoothingAt
// itself returns for a frame that actually runs at that base rate -- the
// property that makes "auto (Ns base)" plus the per-highlight table in the
// summary add up to the whole picture rather than two numbers that quietly
// disagree.
func TestTimeline_AutoSmoothingBaseMatchesTheBaseRateEverywhereOutsideAHighlight(t *testing.T) {
	slow := Highlight{From: 10 * time.Minute, To: 11 * time.Minute, RateFactor: 2}
	tl, err := NewSegmentedTimeline(epoch, time.Hour, 30, 480, []Highlight{slow})
	if err != nil {
		t.Fatal(err)
	}

	want := time.Duration(float64(autoSmoothingVideoWindow) * 480)
	if got := tl.AutoSmoothingBase(); got != want {
		t.Errorf("AutoSmoothingBase() = %v, want %v (autoSmoothingVideoWindow scaled by the base rate 480)", got, want)
	}
	// It must NOT have picked up the highlight's own slower rate: at 2x the
	// window is 0 (see the test above), so a AutoSmoothingBase that
	// accidentally read the highlight's segment would return 0, not 12s.
	if got := tl.AutoSmoothingBase(); got == 0 {
		t.Fatal("AutoSmoothingBase() returned 0; it must report the BASE rate's own window, not a highlight's")
	}
	// And it agrees with AutoSmoothingAt at a frame outside the highlight,
	// which is the property the summary's own decomposition depends on.
	if got := tl.AutoSmoothingAt(0); got != tl.AutoSmoothingBase() {
		t.Errorf("AutoSmoothingAt(0) = %v, AutoSmoothingBase() = %v; they must agree outside any highlight", got, tl.AutoSmoothingBase())
	}

	// With no highlights at all, AutoSmoothingBase is exactly what
	// AutoSmoothingAt returns at every frame -- the single-segment case is
	// the zero-highlight instance of the same arithmetic, not a special one.
	plain, err := NewTimeline(epoch, time.Hour, 30, 480)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plain.AutoSmoothingBase(), plain.AutoSmoothingAt(0); got != want {
		t.Errorf("with no highlights, AutoSmoothingBase() = %v, AutoSmoothingAt(0) = %v; want them equal", got, want)
	}
}

// TestSmoothing_ExplicitWindowIsConstantAcrossSegmentsButAutoIsNot pins the
// half of section E's promise that has no test yet: "An explicit
// --smoothing 30s stays global. An explicit instruction is not reinterpreted
// per segment." A Smoothing{Window: d} (Auto false) must resolve to exactly
// d at every frame, whatever segment that frame falls in -- unlike Auto,
// which this test also checks DOES vary, so the two are actually being told
// apart rather than the explicit case passing by coincidence on a Timeline
// where nothing varies anyway.
func TestSmoothing_ExplicitWindowIsConstantAcrossSegmentsButAutoIsNot(t *testing.T) {
	slow := Highlight{From: 10 * time.Minute, To: 11 * time.Minute, RateFactor: 2}
	tl, err := NewSegmentedTimeline(epoch, time.Hour, 30, 480, []Highlight{slow})
	if err != nil {
		t.Fatal(err)
	}
	insideFrame := tl.IndexAt(slow.From) + 1

	explicit := Smoothing{Window: 30 * time.Second}
	outside := explicit.WindowAt(tl, 0)
	inside := explicit.WindowAt(tl, insideFrame)
	if outside != 30*time.Second || inside != 30*time.Second {
		t.Errorf("an explicit window must be constant: outside the highlight %v, inside it %v, want 30s both", outside, inside)
	}

	// The contrast: Auto DOES vary between the same two frames, or this test
	// would not actually be distinguishing "explicit" from "auto" at all.
	auto := Smoothing{Auto: true}
	autoOutside, autoInside := auto.WindowAt(tl, 0), auto.WindowAt(tl, insideFrame)
	if autoOutside == autoInside {
		t.Fatalf("precondition: Auto should differ inside (%v) vs outside (%v) the highlight, or this test proves nothing about the two being handled differently",
			autoInside, autoOutside)
	}
}

// TestNewSegmentedTimeline_TouchingHighlightsAndBoundaryHighlightsAreAccepted
// pins three of section D's "adjusted, and reported" / "not an error" cases
// as seen by the TIMELINE constructor itself, rather than only by
// resolveHighlights (cmd/highlight_test.go covers the CLI-level validation;
// this is the lower layer NewSegmentedTimeline promises to accept once
// something upstream has already resolved a set): a highlight starting
// exactly at the activity's own start (From=0), two highlights that TOUCH
// (one's To equals the next's From, not an overlap), and a highlight ending
// exactly at the activity's own end (To=d).
//
// Derived by hand: fps=10, base rate 1, activity 20s. Three 5-second
// highlights with no gap between the first two: [0,5), [5,10), then a 5s
// non-highlight gap [10,15), then [15,20). Every segment is 5s at rate 1, so
// every one is exactly 50 frames (5*10) and the whole render is 200 frames
// (20*10) -- checked as the cross-check that the segments still partition
// the render with no overlap and no gap of their own.
func TestNewSegmentedTimeline_TouchingHighlightsAndBoundaryHighlightsAreAccepted(t *testing.T) {
	highlights := []Highlight{
		{Name: "Starts at zero", From: 0, To: 5 * time.Second},
		{Name: "Touches the first", From: 5 * time.Second, To: 10 * time.Second},
		{Name: "Ends at the activity's own end", From: 15 * time.Second, To: 20 * time.Second},
	}
	tl, err := NewSegmentedTimeline(epoch, 20*time.Second, 10, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline rejected touching/boundary highlights: %v", err)
	}
	if got, want := tl.Frames(), 200; got != want {
		t.Fatalf("Frames() = %d, want %d (four 5s segments at 10fps)", got, want)
	}

	// Highlight 0 starts at the activity's own first frame.
	if idx, w := tl.IntervalAt(0, 0); idx != 0 || w != 1 {
		t.Errorf("frame 0: IntervalAt = (%d, %v), want (0, 1) -- the first highlight starts exactly at the activity's start", idx, w)
	}
	// Highlight 0's last frame (49) and highlight 1's first (50) touch with
	// no gap and no overlap: consecutive frames, two different indices.
	if idx, _ := tl.IntervalAt(49, 0); idx != 0 {
		t.Errorf("frame 49 (highlight 0's own last frame): IntervalAt index = %d, want 0", idx)
	}
	if idx, _ := tl.IntervalAt(50, 0); idx != 1 {
		t.Errorf("frame 50 (highlight 1's own first frame, touching highlight 0): IntervalAt index = %d, want 1", idx)
	}
	// The 5s gap between highlight 1 and 2 belongs to neither.
	if idx, _ := tl.IntervalAt(100, 0); idx != NoHighlight {
		t.Errorf("frame 100 (the middle of the non-highlight gap): IntervalAt index = %d, want NoHighlight", idx)
	}
	if idx, _ := tl.IntervalAt(149, 0); idx != NoHighlight {
		t.Errorf("frame 149 (the gap's own last frame): IntervalAt index = %d, want NoHighlight", idx)
	}
	// Highlight 2 starts right after the gap, and its own LAST frame is the
	// render's last frame -- it ends exactly at the activity's own end.
	if idx, _ := tl.IntervalAt(150, 0); idx != 2 {
		t.Errorf("frame 150 (highlight 2's own first frame): IntervalAt index = %d, want 2", idx)
	}
	if idx, _ := tl.IntervalAt(tl.Frames()-1, 0); idx != 2 {
		t.Errorf("the render's own last frame (%d): IntervalAt index = %d, want 2 -- a highlight ending at the activity's end must reach the render's last frame",
			tl.Frames()-1, idx)
	}
}

// TestTimeline_IntervalAtRampOverlapsWhenTheHighlightIsShort pins the ramp
// arithmetic against hand-derived numbers, including the awkward case: a
// highlight short enough relative to the transition that
// the entrance and exit ramps overlap and the weight never reaches 1.
//
// Derived by hand: a 20s activity at fps=10, base rate 1, with a highlight
// spanning [5s,7s) paced to fill 1.5s of video. Its own rate is
// (7s-5s)/1.5s = 4/3, so its segment is round(2/(4/3)*10) = round(15.0) = 15
// frames; the preceding segment (5s at rate 1, fps 10) is 50 frames, so the
// highlight's own i0 is 50 and it runs frames 50 through 64. With a 1s
// transition, frame i's weight is
// min((i-50)/10, (65-i)/10) clamped to [0,1] -- which peaks at i=57
// (since=0.7, until=0.8) and i=58 (since=0.8, until=0.7), both giving 0.7,
// never reaching 1: the highlight (1.5s of video) is shorter than twice the
// 1s transition, so its entrance and exit ramps overlap before either
// finishes.
func TestTimeline_IntervalAtRampOverlapsWhenTheHighlightIsShort(t *testing.T) {
	highlight := Highlight{From: 5 * time.Second, To: 7 * time.Second, Video: 1500 * time.Millisecond}
	tl, err := NewSegmentedTimeline(epoch, 20*time.Second, 10, 1, []Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}

	const transition = time.Second
	cases := []struct {
		i       int
		wantIdx int
		wantWt  float64
	}{
		{49, NoHighlight, 0},
		{50, 0, 0},
		{51, 0, 0.1},
		{57, 0, 0.7},
		{58, 0, 0.7},
		{64, 0, 0.1},
		{65, NoHighlight, 0},
	}
	var maxWeight float64
	for _, c := range cases {
		idx, w := tl.IntervalAt(c.i, transition)
		if idx != c.wantIdx {
			t.Errorf("IntervalAt(%d): index = %d, want %d", c.i, idx, c.wantIdx)
		}
		if math.Abs(w-c.wantWt) > 1e-9 {
			t.Errorf("IntervalAt(%d): weight = %v, want %v", c.i, w, c.wantWt)
		}
		if w > maxWeight {
			maxWeight = w
		}
	}
	if maxWeight >= 1 {
		t.Errorf("the highlight's peak weight reached %v; it should stay below 1 because the highlight (1.5s of video) is shorter than twice the 1s transition", maxWeight)
	}
}

// TestNewSegmentedTimeline_FractionalFPSMatchesHandDerivedFrameCounts pins
// the headline construction arithmetic at a genuinely fractional fps (29.97,
// the value that is the whole reason FPS is a float64 -- see
// TestNewTimeline_FrameCountIsDurationTimesRate's own "ten minutes at 29.97"
// case), rather than only at the fps=30 the "30s+9s=39s" example uses.
//
// Derived by hand, using Go's own math.Round (ties away from zero): a 100s
// activity at real time (rate 1) with a highlight [40s,50s) paced to 2x --
// before the highlight, 40s at rate 1: round(40*29.97) = round(1198.8) =
// 1199 frames. The highlight itself, 10s at rate 2: round(10/2*29.97) =
// round(149.85) = 150 frames. After the highlight, 50s at rate 1:
// round(50*29.97) = round(1498.5) = 1499 frames (an exact tie, rounded away
// from zero). 1199+150+1499 = 2848 frames total.
func TestNewSegmentedTimeline_FractionalFPSMatchesHandDerivedFrameCounts(t *testing.T) {
	const fps = 29.97
	highlight := Highlight{From: 40 * time.Second, To: 50 * time.Second, RateFactor: 2}
	tl, err := NewSegmentedTimeline(epoch, 100*time.Second, fps, 1, []Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	if got, want := tl.Frames(), 1199+150+1499; got != want {
		t.Errorf("Frames() = %d, want %d (1199 before + 150 highlight + 1499 after)", got, want)
	}

	// The highlight begins at frame 1199 (the end of the "before" segment)
	// and its own last frame is 1199+150-1 = 1348.
	if idx := tl.IndexAt(highlight.From); idx != 1199 {
		t.Errorf("IndexAt(40s) = %d, want 1199", idx)
	}
	if index, _ := tl.IntervalAt(1199, 0); index != 0 {
		t.Errorf("frame 1199 does not report belonging to the highlight: IntervalAt = %d, want 0", index)
	}
	if index, _ := tl.IntervalAt(1348, 0); index != 0 {
		t.Errorf("frame 1348 (the highlight's own last frame) does not report belonging to it: IntervalAt = %d, want 0", index)
	}
	if index, _ := tl.IntervalAt(1349, 0); index != NoHighlight {
		t.Errorf("frame 1349 (the first frame after the highlight) reports belonging to it: IntervalAt = %d, want NoHighlight", index)
	}
}

// TestNewSegmentedTimeline_ADominantSlowHighlightDwarfsTheBase pins the
// opposite extreme from the "30s+9s" example: a highlight covering MOST of
// the activity, paced SLOWER than real time (RateFactor < 1, genuine slow
// motion), rather than a short one paced faster than the base.
//
// Derived by hand: a 100s activity at fps=10, base 60x, with an 80s
// highlight [10s,90s) at RateFactor=0.5 (half real time -- much slower than
// the base's own 60x). Before the highlight: 10s at 60x,
// round(10/60*10) = round(1.667) = 2 frames. The highlight: 80s at 0.5x,
// round(80/0.5*10) = round(1600) = 1600 frames. After the highlight:
// another 10s at 60x, another 2 frames. 2+1600+2 = 1604 frames, 160.4s of
// video -- a highlight covering 80% of the activity's own length dominates
// the render's total duration, exactly as the design predicts for the
// opposite case from the named example.
func TestNewSegmentedTimeline_ADominantSlowHighlightDwarfsTheBase(t *testing.T) {
	highlight := Highlight{From: 10 * time.Second, To: 90 * time.Second, RateFactor: 0.5}
	tl, err := NewSegmentedTimeline(epoch, 100*time.Second, 10, 60, []Highlight{highlight})
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	if got, want := tl.Frames(), 1604; got != want {
		t.Errorf("Frames() = %d, want %d (2 before + 1600 highlight + 2 after)", got, want)
	}
	if got, want := tl.Duration(), 160400*time.Millisecond; got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}
	if got, want := tl.BaseSpeedup(), 60.0; got != want {
		t.Errorf("BaseSpeedup() = %v, want 60 -- the highlight's own slower rate must not change it", got)
	}
	// The coarsest rate is still the BASE here: a highlight SLOWER than the
	// base is, by definition, not the coarsest segment in the render, unlike
	// every existing MaxSpeedup test, which only tries a highlight FASTER
	// than the base.
	if got, want := tl.MaxSpeedup(), 60.0; got != want {
		t.Errorf("MaxSpeedup() = %v, want 60 -- a highlight SLOWER than the base must not become the coarsest rate", got)
	}
}
