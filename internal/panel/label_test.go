package panel

import (
	"math"
	"testing"
	"time"
)

// TestLabelAt_ReturnsNoLabelOutsideEveryLabel pins the boundary behaviour a
// reader would otherwise have to infer from LabelAt's own arithmetic: a
// frame strictly before a label's FirstFrame or strictly after its
// LastFrame belongs to no label at all, even when other labels exist
// elsewhere in the slice.
func TestLabelAt_ReturnsNoLabelOutsideEveryLabel(t *testing.T) {
	labels := []Label{
		{Name: "Start", FirstFrame: 10, LastFrame: 19},
		{Name: "Lighthouse", FirstFrame: 40, LastFrame: 49},
	}

	cases := []struct {
		i       int
		wantIdx int
	}{
		{0, NoLabel},
		{9, NoLabel},
		{10, 0},
		{19, 0},
		{20, NoLabel},
		{39, NoLabel},
		{40, 1},
		{49, 1},
		{50, NoLabel},
	}
	for _, c := range cases {
		idx, w := LabelAt(labels, c.i, 30, 0)
		if idx != c.wantIdx {
			t.Errorf("LabelAt(%d): index = %d, want %d", c.i, idx, c.wantIdx)
		}
		if c.wantIdx == NoLabel && w != 0 {
			t.Errorf("LabelAt(%d): weight = %v, want 0 outside every label", c.i, w)
		}
	}
}

// TestLabelAt_RampMatchesIntervalAtOnTheSameNumbers proves the two ramps
// cannot drift apart by construction, rather than merely by inspection: it
// feeds LabelAt exactly the segment geometry
// TestTimeline_IntervalAtRampOverlapsWhenTheHighlightIsShort feeds
// IntervalAt (a highlight [5s,7s) paced to 1.5s of video inside a 20s, 10fps
// timeline, which resolves to a segment starting at frame 50 spanning 15
// frames) and checks the identical table of expected weights. Both
// functions now call the same unexported rampWeight, so this is a
// regression test for the extraction itself: if a future edit reintroduces
// a second copy of the ramp arithmetic for one of the two callers, this
// test and the highlight one it mirrors will disagree with each other
// exactly when the values it pins were derived from the shared arithmetic
// being genuinely shared.
func TestLabelAt_RampMatchesIntervalAtOnTheSameNumbers(t *testing.T) {
	const fps = 10.0
	const transition = time.Second
	labels := []Label{{Name: "Climb", FirstFrame: 50, LastFrame: 64}}

	cases := []struct {
		i       int
		wantIdx int
		wantWt  float64
	}{
		{49, NoLabel, 0},
		{50, 0, 0},
		{51, 0, 0.1},
		{57, 0, 0.7},
		{58, 0, 0.7},
		{64, 0, 0.1},
		{65, NoLabel, 0},
	}
	var maxWeight float64
	for _, c := range cases {
		idx, w := LabelAt(labels, c.i, fps, transition)
		if idx != c.wantIdx {
			t.Errorf("LabelAt(%d): index = %d, want %d", c.i, idx, c.wantIdx)
		}
		if math.Abs(w-c.wantWt) > 1e-9 {
			t.Errorf("LabelAt(%d): weight = %v, want %v", c.i, w, c.wantWt)
		}
		if w > maxWeight {
			maxWeight = w
		}
	}
	if maxWeight >= 1 {
		t.Errorf("the label's peak weight reached %v; it should stay below 1 because the label (1.5s of video) is shorter than twice the 1s transition", maxWeight)
	}
}

// TestLabelAt_HardCutWhenTransitionIsZero pins rampWeight's transition <= 0
// branch as reached through LabelAt: every frame inside the label is full
// weight and nothing outside it is, with no ramp in between.
func TestLabelAt_HardCutWhenTransitionIsZero(t *testing.T) {
	labels := []Label{{Name: "Aid station", FirstFrame: 100, LastFrame: 129}}

	for _, i := range []int{100, 110, 129} {
		if idx, w := LabelAt(labels, i, 30, 0); idx != 0 || w != 1 {
			t.Errorf("LabelAt(%d) with transition=0: got (%d, %v), want (0, 1)", i, idx, w)
		}
	}
	for _, i := range []int{99, 130} {
		if idx, w := LabelAt(labels, i, 30, 0); idx != NoLabel || w != 0 {
			t.Errorf("LabelAt(%d) with transition=0: got (%d, %v), want (%d, 0)", i, idx, w, NoLabel)
		}
	}
}

// TestLabelAt_ChoosesTheContainingLabelNotTheFirst guards against an
// off-by-one that would make LabelAt fall through the first label's own
// range check and match a later label instead, or the reverse -- a linear
// scan is only as correct as its exit condition.
func TestLabelAt_ChoosesTheContainingLabelNotTheFirst(t *testing.T) {
	labels := []Label{
		{Name: "First", FirstFrame: 0, LastFrame: 9},
		{Name: "Second", FirstFrame: 10, LastFrame: 19},
		{Name: "Third", FirstFrame: 20, LastFrame: 29},
	}
	for i, want := range map[int]int{5: 0, 15: 1, 25: 2} {
		if idx, _ := LabelAt(labels, i, 30, 0); idx != want {
			t.Errorf("LabelAt(%d): index = %d, want %d", i, idx, want)
		}
	}
}
