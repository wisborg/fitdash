package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitdash/internal/panel"
)

// TestParseLabel_ParsesTheGrammar pins the comma-separated key=value
// grammar --label shares with --highlight, and the default video=.
func TestParseLabel_ParsesTheGrammar(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want panel.Label
	}{
		{
			name: "every field",
			in:   "at=12m30s,name=Lighthouse,video=3s",
			want: panel.Label{At: 12*time.Minute + 30*time.Second, Name: "Lighthouse", Video: 3 * time.Second},
		},
		{
			name: "no video= falls back to the three-second default",
			in:   "at=1m,name=Start",
			want: panel.Label{At: time.Minute, Name: "Start", Video: defaultLabelVideo},
		},
		{
			name: "field order does not matter",
			in:   "name=Start,video=5s,at=1m",
			want: panel.Label{At: time.Minute, Name: "Start", Video: 5 * time.Second},
		},
		{
			name: "an escaped comma is literal inside the name",
			in:   `at=1m,name=Aid\, station 1`,
			want: panel.Label{At: time.Minute, Name: "Aid, station 1", Video: defaultLabelVideo},
		},
		{
			name: "the first = splits the field; a value may contain = freely",
			in:   "at=1m,name=Pace=Sub 4",
			want: panel.Label{At: time.Minute, Name: "Pace=Sub 4", Video: defaultLabelVideo},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseLabel(c.in)
			if err != nil {
				t.Fatalf("parseLabel(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseLabel(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// TestParseLabel_RejectsEveryFatalCase pins --label's own refusals,
// including the deliberate asymmetry with --highlight: name is required
// here, where a highlight's name may be empty.
func TestParseLabel_RejectsEveryFatalCase(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"missing at", "name=Start", "both required"},
		{"missing name", "at=1m", "both required"},
		{"name explicitly empty", "at=1m,name=", "both required"},
		{"missing both", "video=3s", "both required"},
		{"an unparseable at", "at=soon,name=Start", "not a duration"},
		{"at is negative", "at=-1m,name=Start", "negative"},
		{"video is zero", "at=1m,name=Start,video=0s", "video must be positive"},
		{"video is negative", "at=1m,name=Start,video=-1s", "video must be positive"},
		{"an unknown key", "at=1m,name=Start,for=5s", "unknown field"},
		{"a repeated key", "at=1m,at=2m,name=Start", "repeated"},
		{"a field with no equals sign", "at=1m,name=Start,broken", `has no "="`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseLabel(c.in)
			if err == nil {
				t.Fatalf("parseLabel(%q) accepted it", c.in)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("parseLabel(%q) error = %v, want it to mention %q", c.in, err, c.wantErr)
			}
		})
	}
}

// labelTestTimeline builds a plain, unhighlighted Timeline for resolveLabels
// tests -- a real Timeline rather than a mock, since resolveLabels' whole
// point is to read frame bounds back from one.
func labelTestTimeline(t *testing.T, activity time.Duration, fps, speedup float64) panel.Timeline {
	t.Helper()
	tl, err := panel.NewTimeline(highlightEpoch, activity, fps, speedup)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	return tl
}

// TestResolveLabels_NoneGivenIsNotAnError mirrors
// TestResolveHighlights_NoneGivenIsNotAnError: no --label at all is a fact
// about the flags, not a failure.
func TestResolveLabels_NoneGivenIsNotAnError(t *testing.T) {
	tl := labelTestTimeline(t, 30*time.Minute, 30, 1)
	got, err := resolveLabels(nil, tl)
	if err != nil {
		t.Fatalf("resolveLabels(nil, ...): %v", err)
	}
	if got != nil {
		t.Errorf("resolveLabels(nil, ...) = %v, want nil", got)
	}
}

// TestResolveLabels_SortsByAtAndResolvesFrameBounds pins that labels come
// back ordered by at, and that FirstFrame/LastFrame are read back from the
// Timeline rather than left zero.
//
// At 30fps real time, "at=1m" is frame 1800 and the default 3s video= is
// 90 frames, so FirstFrame=1800, LastFrame=1889.
func TestResolveLabels_SortsByAtAndResolvesFrameBounds(t *testing.T) {
	tl := labelTestTimeline(t, 30*time.Minute, 30, 1)
	raw := []string{
		"at=5m,name=Second",
		"at=1m,name=First",
	}
	got, err := resolveLabels(raw, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resolveLabels returned %d labels, want 2", len(got))
	}
	if got[0].Name != "First" || got[1].Name != "Second" {
		t.Fatalf("resolveLabels did not sort by At: got %q then %q", got[0].Name, got[1].Name)
	}
	if got[0].FirstFrame != 1800 || got[0].LastFrame != 1889 {
		t.Errorf("First's frame bounds = [%d,%d], want [1800,1889]", got[0].FirstFrame, got[0].LastFrame)
	}
}

// TestResolveLabels_FrameBoundsMatchAHighlightSegmentedTimeline pins that
// resolveLabels reads frame bounds back from WHATEVER Timeline it is
// given, rather than from an assumption that fps and speedup are uniform
// across the whole render. Every other resolveLabels test in this file
// builds its Timeline through labelTestTimeline -- a single, UNhighlighted
// segment -- so a resolveLabels that hardcoded plain arithmetic
// (frame = at.Seconds()*fps, say, instead of calling tl.IndexAt(at)) would
// compile and pass every one of them. This is the one built to catch such
// a mistake, by handing resolveLabels a Timeline whose frame bounds cannot
// be produced by that simpler formula at all.
//
// The Timeline carries one highlight, [10s,20s) at RateFactor 0.1 -- ten
// times SLOWER than the base rate of 1 -- over a 60s activity at 30fps.
// Hand-derived from newTimelineFromSegments' own construction rule
// (frames = round(length/rate*fps) per segment, floored at 1, each
// segment's own i0 the running total of every earlier segment's count):
//
//	[0,10s)  @ rate 1:   round(10/1*30)   =  300 frames, i0=0
//	[10,20s) @ rate 0.1: round(10/0.1*30) = 3000 frames, i0=300
//	[20,60s) @ rate 1:   round(40/1*30)   = 1200 frames, i0=3300
//	total: 4500 frames
//
// A PLAIN 60s@30fps timeline has 1800 frames total, well under half of
// this one -- the "differ substantially" this fixture is built for, so a
// resolveLabels call that silently read frame bounds off the wrong
// (plain) Timeline lands on a number the two timelines could agree on
// only by coincidence, rather than one that happens to be valid on both.
//
// The label sits at at=15s, five seconds into the highlighted segment.
// Timeline.IndexAt's own arithmetic (see its doc comment) is
// seg.i0 + round((target-seg.start).Seconds()/seg.rate*fps):
// 300 + round(5/0.1*30) = 300 + 1500 = 1800. The default 3s video= is
// overridden to 1s here so the frame count (round(1*30) = 30 frames)
// stays easy to hand-check: LastFrame = 1800+29 = 1829.
func TestResolveLabels_FrameBoundsMatchAHighlightSegmentedTimeline(t *testing.T) {
	highlights := []panel.Highlight{{Name: "Slow bit", From: 10 * time.Second, To: 20 * time.Second, RateFactor: 0.1}}
	tl, err := panel.NewSegmentedTimeline(highlightEpoch, 60*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	if got := tl.Frames(); got != 4500 {
		t.Fatalf("precondition: segmented timeline has %d frames, want 4500 -- the hand-derived construction no longer matches newTimelineFromSegments", got)
	}
	plain, err := panel.NewTimeline(highlightEpoch, 60*time.Second, 30, 1)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	if plain.Frames() == tl.Frames() {
		t.Fatalf("precondition: the plain timeline has the same frame count (%d) as the segmented one; this fixture cannot tell the two apart", plain.Frames())
	}

	got, err := resolveLabels([]string{"at=15s,name=Mid-climb,video=1s"}, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("resolveLabels returned %d labels, want 1", len(got))
	}
	if want := 1800; got[0].FirstFrame != want {
		t.Errorf("FirstFrame = %d, want %d (hand-derived against the highlight-segmented timeline, not a plain one)", got[0].FirstFrame, want)
	}
	if want := 1829; got[0].LastFrame != want {
		t.Errorf("LastFrame = %d, want %d", got[0].LastFrame, want)
	}
}

// TestResolveLabels_RejectsAtAtOrPastTheEnd mirrors
// TestResolveHighlights_RejectsFromAtOrPastTheEnd: a point at or past the
// activity's own end has no defined frame to land on.
func TestResolveLabels_RejectsAtAtOrPastTheEnd(t *testing.T) {
	tl := labelTestTimeline(t, 25*time.Minute, 30, 1)
	if _, err := resolveLabels([]string{"at=25m,name=End"}, tl); err == nil {
		t.Fatal("a label at exactly the activity's end was accepted")
	}
	if _, err := resolveLabels([]string{"at=40m,name=Beyond"}, tl); err == nil {
		t.Fatal("a label well past the activity's end was accepted")
	}
}

// TestResolveLabels_RefusesDuplicateAt pins the one label collision that is
// refused rather than composed: two labels naming the exact same instant
// have nothing to truncate to and are a typo, not a range.
func TestResolveLabels_RefusesDuplicateAt(t *testing.T) {
	tl := labelTestTimeline(t, 30*time.Minute, 30, 1)
	_, err := resolveLabels([]string{
		"at=5m,name=A",
		"at=5m,name=B",
	}, tl)
	if err == nil {
		t.Fatal("two labels at the same instant were accepted")
	}
	if !strings.Contains(err.Error(), "A") || !strings.Contains(err.Error(), "B") {
		t.Errorf("the duplicate error should name both labels; got: %v", err)
	}
}

// TestResolveLabels_TruncatesAnOverlappingEarlierLabel pins the rule that
// deliberately differs from highlight overlap: the earlier label's on-screen
// span is cut short to end exactly where the next one's begins, not
// refused.
//
// Both labels default to a 3s video=, and at a 1x speedup and 30fps that is
// 90 frames. "at=10s" resolves to frame 300; "at=11s" (one second, 30
// frames, later) resolves to frame 330. Label A would naturally run
// [300,389], which overlaps B's [330,419], so A must be truncated to end at
// frame 329 -- 30 frames, exactly 1s of video.
func TestResolveLabels_TruncatesAnOverlappingEarlierLabel(t *testing.T) {
	tl := labelTestTimeline(t, time.Minute, 30, 1)
	got, err := resolveLabels([]string{
		"at=10s,name=A",
		"at=11s,name=B",
	}, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resolveLabels returned %d labels, want 2", len(got))
	}
	a, b := got[0], got[1]
	if !a.Truncated {
		t.Error("the earlier, overlapping label was not marked Truncated")
	}
	if b.Truncated {
		t.Error("the later label was truncated; only the earlier one should be")
	}
	if want := 300; a.FirstFrame != want {
		t.Errorf("A.FirstFrame = %d, want %d", a.FirstFrame, want)
	}
	if want := 329; a.LastFrame != want {
		t.Errorf("A.LastFrame = %d, want %d (truncated to end exactly where B begins)", a.LastFrame, want)
	}
	if want := time.Second; a.Video != want {
		t.Errorf("A.Video after truncation = %v, want %v", a.Video, want)
	}
	if want := 330; b.FirstFrame != want {
		t.Errorf("B.FirstFrame = %d, want %d (unaffected by the truncation)", b.FirstFrame, want)
	}
}

// TestResolveLabels_TruncationBelowOneFrameIsFlooredToOne pins the
// zero-frame floor, mirroring newTimelineFromSegments' identical floor for
// a highlight: two labels resolving to the very same frame would otherwise
// truncate the earlier one to zero frames, so it is floored to one instead,
// even though that one frame still coincides with the next label's own
// first frame.
func TestResolveLabels_TruncationBelowOneFrameIsFlooredToOne(t *testing.T) {
	tl := labelTestTimeline(t, time.Minute, 30, 1)
	// 10.0s and 10.01s both round to frame 300 at 30fps, so both labels
	// share the same FirstFrame.
	got, err := resolveLabels([]string{
		"at=10s,name=A",
		"at=10.01s,name=B",
	}, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	a := got[0]
	if !a.Truncated {
		t.Fatal("the earlier label was not marked Truncated")
	}
	if a.LastFrame != a.FirstFrame {
		t.Errorf("A's span = [%d,%d], want exactly one frame [%d,%d]", a.FirstFrame, a.LastFrame, a.FirstFrame, a.FirstFrame)
	}
}

// TestResolveLabels_ClampsTheLastLabelToTheRendersOwnEnd pins the blocker
// this clamp exists to fix: a label near the end of the render whose video=
// would otherwise run past the render's own last frame is clamped to it,
// rather than left to overrun -- which broke --frames outright (the PNG
// sink refuses a requested frame index the render never reaches) and left
// the summary reporting the video= that was asked for rather than what the
// label actually got. See resolveLabels' own doc comment for why only the
// LAST label in the sorted list can ever need this.
//
// At 30fps real time over a one-minute render (1800 frames, last index
// 1799), "at=59.9s" resolves to frame 1797 (59.9*30) and the default 3s
// video= is 90 frames, so the label would naturally run to frame 1886 -- 87
// frames past the render's own end. Clamped, it runs [1797,1799]: 3 frames,
// exactly 100ms of video.
func TestResolveLabels_ClampsTheLastLabelToTheRendersOwnEnd(t *testing.T) {
	tl := labelTestTimeline(t, time.Minute, 30, 1)
	got, err := resolveLabels([]string{"at=59.9s,name=Finish"}, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("resolveLabels returned %d labels, want 1", len(got))
	}
	l := got[0]
	if !l.Truncated {
		t.Error("a label clipped to the render's own end was not marked Truncated")
	}
	if want := 1797; l.FirstFrame != want {
		t.Errorf("FirstFrame = %d, want %d", l.FirstFrame, want)
	}
	if want := 1799; l.LastFrame != want {
		t.Errorf("LastFrame = %d, want %d (the render's own last frame)", l.LastFrame, want)
	}
	if want := 100 * time.Millisecond; l.Video != want {
		t.Errorf("Video after clamping = %v, want %v", l.Video, want)
	}
	if l.LastFrame >= tl.Frames() {
		t.Errorf("LastFrame %d reaches or exceeds the render's own frame count %d, which is exactly what broke --frames", l.LastFrame, tl.Frames())
	}
}

// TestWriteLabelSummary_ReportsClippingToTheRendersEnd pins the message
// naming the OTHER truncation cause writeLabelSummary must tell apart from
// an overlap with the next label -- see that function's own doc comment for
// how it distinguishes the two from a Truncated label's position alone.
func TestWriteLabelSummary_ReportsClippingToTheRendersEnd(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	tl := labelTestTimeline(t, time.Minute, 30, 1)
	labels, err := resolveLabels([]string{"at=59.9s,name=Finish"}, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeLabelSummary(c, labels, 400*time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, `label "Finish" clipped to 100ms so it ends at the render's last frame`) {
		t.Errorf("summary is missing the render-end clipping warning; got:\n%s", out)
	}
	if strings.Contains(out, "so it ends where the next label begins") {
		t.Errorf("the render-end clip was reported with the overlap message instead; got:\n%s", out)
	}
}

// TestWriteLabelSummary_ReportsEachLabelAndTruncation pins the summary line
// format and the truncation warning.
func TestWriteLabelSummary_ReportsEachLabelAndTruncation(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	tl := labelTestTimeline(t, time.Minute, 30, 1)
	labels, err := resolveLabels([]string{
		"at=10s,name=A",
		"at=11s,name=B",
	}, tl)
	if err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeLabelSummary(c, labels, 400*time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, `labels: "A" 0:00:10 -> 1s, "B" 0:00:11 -> 3s`) {
		t.Errorf("summary is missing the label table line; got:\n%s", out)
	}
	if !strings.Contains(out, `label "A" truncated to 1s so it ends where the next label begins`) {
		t.Errorf("summary is missing the truncation warning; got:\n%s", out)
	}
}

// TestWriteLabelSummary_WarnsWhenTheSpanNeverReachesFullOpacity pins the
// warning for a label whose own video= is shorter than twice
// --highlight-transition: rampWeight takes the min of the entrance and exit
// ramps, so such a label's name never reaches weight 1.
func TestWriteLabelSummary_WarnsWhenTheSpanNeverReachesFullOpacity(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	labels := []panel.Label{{Name: "Blip", At: time.Second, Video: 500 * time.Millisecond, FirstFrame: 0, LastFrame: 14}}

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeLabelSummary(c, labels, 400*time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, `label "Blip" is on screen for 500ms, shorter than twice --highlight-transition (400ms); its name never reaches full opacity`) {
		t.Errorf("summary is missing the opacity warning; got:\n%s", out)
	}
}

// TestWriteLabelSummary_PrintsNothingWithNoLabels pins that an ordinary
// render without --label leaves the summary untouched by this feature.
func TestWriteLabelSummary_PrintsNothingWithNoLabels(t *testing.T) {
	defer func(v bool) { renderOpts.quiet = v }(renderOpts.quiet)
	renderOpts.quiet = false

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&buf)
	writeLabelSummary(c, nil, 400*time.Millisecond)

	if buf.Len() != 0 {
		t.Errorf("writeLabelSummary with no labels wrote %q, want nothing", buf.String())
	}
}
