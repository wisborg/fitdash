package panel

import (
	"testing"
	"time"
)

// A paced stretch takes the frames its own rate gives it, shows the
// instants it covers at that rate, and is no highlight's: nothing is named
// or marked over it.
func TestPacedTimeline_StretchRunsAtItsOwnRate(t *testing.T) {
	const fps, rate = 30.0, 600.0
	d := 10 * time.Hour
	p := Pacing{From: 30 * time.Minute, To: 40 * time.Minute, Rate: 50}
	tl, err := NewPacedTimeline(epoch, d, fps, rate, nil, []Pacing{p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := int(30*time.Minute.Seconds()/rate*fps + 0.5)    // 90 frames
	paced := int(10*time.Minute.Seconds()/50*fps + 0.5)       // 360 frames
	after := int((d-40*time.Minute).Seconds()/rate*fps + 0.5) // 1680 frames
	if got, want := tl.Frames(), before+paced+after; got != want {
		t.Fatalf("frames = %d, want %d", got, want)
	}
	// Twelve seconds of video for ten minutes of activity, where the base
	// rate would have given it one.
	if got, want := tl.PacedExtra(), 11*time.Second; got != want {
		t.Errorf("PacedExtra = %v, want %v", got, want)
	}
	mid := before + paced/2
	if got, want := tl.At(mid).Sub(epoch), 35*time.Minute; got != want {
		t.Errorf("frame %d shows %v, want %v", mid, got, want)
	}
	// 50/30 of a second, to the nanosecond At rounds to.
	if got := tl.At(mid + 1).Sub(tl.At(mid)); got < 1666666666 || got > 1666666668 {
		t.Errorf("a paced frame advances %v, want 1.667s", got)
	}
	if idx, _ := tl.IntervalAt(mid, 0); idx != NoHighlight {
		t.Errorf("a paced frame belongs to highlight %d; a pacing is nobody's highlight", idx)
	}
	if got := tl.BaseSpeedup(); got != rate {
		t.Errorf("BaseSpeedup = %v, want the base rate %v", got, rate)
	}
}

// A pacing beside a highlight leaves the highlight its own rate and index,
// and one overlapping a highlight is refused: the two would each claim the
// same stretch's rate.
func TestPacedTimeline_BesideAndOverHighlights(t *testing.T) {
	d := 4 * time.Hour
	h := Highlight{Name: "climb", From: time.Hour, To: 70 * time.Minute, Video: 10 * time.Second}
	p := Pacing{From: 10 * time.Minute, To: 20 * time.Minute, Rate: 60}
	tl, err := NewPacedTimeline(epoch, d, 30, 240, []Highlight{h}, []Pacing{p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := tl.IndexAt(65 * time.Minute)
	if idx, _ := tl.IntervalAt(i, 0); idx != 0 {
		t.Errorf("frame in the highlight belongs to %d, want 0", idx)
	}
	over := Pacing{From: 65 * time.Minute, To: 75 * time.Minute, Rate: 60}
	if _, err := NewPacedTimeline(epoch, d, 30, 240, []Highlight{h}, []Pacing{over}, nil); err == nil {
		t.Error("a pacing over a highlight was accepted")
	}
}

// Time left out at the activity's start and end -- a flight standing at the
// gate -- shortens the render from the edges and marks no seam: the render
// begins at the first instant kept.
func TestTimelineForActivityPaced_LeavesOutTheEdges(t *testing.T) {
	d := 2 * time.Hour
	leave := []Cut{{From: 0, To: 25 * time.Minute}, {From: 110 * time.Minute, To: d}}
	tl, err := NewPacedTimeline(epoch, d, 30, 60, nil, nil, unionCuts(nil, leave))
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.At(0).Sub(epoch); got != 25*time.Minute {
		t.Errorf("frame 0 shows %v, want 25m: the stand at the gate is left out", got)
	}
	if got, want := tl.ActivityDuration(), 85*time.Minute; got != want {
		t.Errorf("ActivityDuration = %v, want %v", got, want)
	}
	if n := len(tl.Cuts()); n != 0 {
		t.Errorf("%d seams marked; time left out at an edge has nothing to splice", n)
	}
}

// Pauses and other time left out are removed once where they overlap.
func TestUnionCuts(t *testing.T) {
	a := []Cut{{From: 10, To: 20}, {From: 50, To: 60}}
	b := []Cut{{From: 0, To: 15}, {From: 55, To: 70}, {From: 80, To: 90}}
	got := unionCuts(a, b)
	want := []Cut{{From: 0, To: 20}, {From: 50, To: 70}, {From: 80, To: 90}}
	if len(got) != len(want) {
		t.Fatalf("unionCuts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("unionCuts[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if len(a) != 2 || a[0].To != 20 {
		t.Errorf("unionCuts changed its argument: %v", a)
	}
}
