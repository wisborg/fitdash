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
		if got := tl.Speedup(); got != speedup {
			t.Errorf("Speedup() = %v, want %v", got, speedup)
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
