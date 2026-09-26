package panel

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
)

// pausedTrack is a synthetic activity that stops for three minutes, from
// 300 s to 480 s in -- far longer than fitactivity.DefaultMaxGap, so the
// gap-aware lookup alone refuses anywhere deep inside it.
func pausedTrack(t *testing.T) (*fitactivity.Track, *fitactivity.TimerModel, time.Time) {
	t.Helper()
	opts := fittest.DefaultOptions()
	opts.Count = 900
	opts.Pauses = []fittest.Pause{{Start: 300 * time.Second, End: 480 * time.Second}}
	path := filepath.Join(t.TempDir(), "paused.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	return track, fitactivity.BuildTimerModel(track), track.Samples[0].Time
}

// A bound inside a PAUSE resolves to the distance the activity stopped at.
// The watch was stopped -- or it is the gap between two files of a merged
// activity -- and no distance was added through it, so the distance there is
// known. Before this, a highlight starting between two files was not marked
// on the profile at all, and said so in the summary.
func TestTimeToDistance_ResolvesInsideAPauseToWhereItStopped(t *testing.T) {
	track, timer, start := pausedTrack(t)
	pauses := timer.Pauses()
	if len(pauses) != 1 {
		t.Fatalf("precondition: %d pauses, want 1", len(pauses))
	}
	deep := pauses[0].Start.Add(90 * time.Second).Sub(start)

	if _, ok := TimeToDistance(track, nil, start, deep); ok {
		t.Fatal("precondition: the middle of the pause resolved without the timer, so this proves nothing")
	}
	d, ok := TimeToDistance(track, timer, start, deep)
	if !ok {
		t.Fatal("a bound inside a pause did not resolve")
	}
	stopped, ok := TimeToDistance(track, nil, start, pauses[0].Start.Sub(start))
	if !ok || d != stopped {
		t.Errorf("inside the pause the distance is %v, want %v -- where it stopped", d, stopped)
	}
}

// A DROPOUT is not a pause. The device recorded nothing and said nothing
// about why, so the distance there is unknown and the answer stays no --
// with a timer to hand, which is what distinguishes the two.
func TestTimeToDistance_StillRefusesInsideADropout(t *testing.T) {
	track, start := gappedDistanceTrack(100 * time.Second)
	timer := fitactivity.BuildTimerModel(track)
	if len(timer.Pauses()) != 0 {
		t.Fatal("precondition: the dropout fixture has pauses, so this proves nothing")
	}
	if _, ok := TimeToDistance(track, timer, start, 51*time.Second); ok {
		t.Error("a bound deep in a dropout resolved; a dropout is not a pause")
	}
}

// The panel places a highlight that starts inside a pause -- the case the
// merged-activity render reported as "no distance at its bounds" -- because
// it resolves through its own ctx.Timer. Without the timer it could not,
// which is checked first so this cannot pass by the gap-aware lookup alone.
func TestElevationPanel_MarksAHighlightStartingInsideAPause(t *testing.T) {
	track, timer, start := pausedTrack(t)
	highlights := []Highlight{{Name: "Leg", From: 400 * time.Second, To: 700 * time.Second}}

	build := func(withTimer bool) *elevationPainter {
		t.Helper()
		faces, err := NewFaceCache()
		if err != nil {
			t.Fatal(err)
		}
		tl, err := NewSegmentedTimeline(start, 900*time.Second, 30, 1, highlights)
		if err != nil {
			t.Fatal(err)
		}
		ctx := elevationContext(track)
		ctx.Width, ctx.Height, ctx.FontScale, ctx.Fonts = 1200, 300, 0.05, faces
		ctx.Timeline, ctx.Highlights = tl, highlights
		if withTimer {
			ctx.Timer = timer
		}
		p, ok := ElevationPanel{}.Prepare(ctx, Box{W: 1200, H: 300}).(*elevationPainter)
		if !ok {
			t.Fatal("Prepare did not return an elevation painter")
		}
		return p
	}

	if p := build(false); len(p.marks) == 1 && p.marks[0].ok {
		t.Fatal("precondition: the mark was placed without the timer, so this proves nothing")
	}
	p := build(true)
	if len(p.marks) != 1 || !p.marks[0].ok {
		t.Fatalf("a highlight starting inside a pause was not placed: %+v", p.marks)
	}
	if p.marks[0].x1 <= p.marks[0].x0 {
		t.Errorf("the mark runs from %v to %v", p.marks[0].x0, p.marks[0].x1)
	}
}
