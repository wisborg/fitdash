package panel

import "time"

// NoLabel is the value Frame.Label takes for a frame that belongs to no
// label.
//
// Its own name rather than reusing NoHighlight: a reader of
// `f.Label == NoLabel` should not have to think about highlights to
// understand it. The underlying reason for a sentinel at all is the same
// one NoHighlight's own doc comment gives -- index 0 is a legitimate label,
// so a presence flag living next to the index would be a second
// declaration free to disagree with the first.
const NoLabel = -1

// Label names an instant in the activity worth calling out by name on
// screen, and how long that name stays there.
//
// A Label is an instant with a screen duration, not a range with a rate --
// the opposite shape from Highlight -- and is deliberately its own type
// rather than a variant of Highlight or a struct the two embed to share
// their Name field. They share that one string and nothing else: Highlight
// paces a stretch of the render and marks a range; a Label paces nothing
// and marks an instant. Distance- and lap-bounded highlights are on
// Highlight's own roadmap and have no meaning for a Label at all, so the
// two are about to diverge further, not converge.
//
// A Label reaching Context.Labels has already been through resolveLabels
// (cmd/label.go): sorted by At ascending, no two sharing the same At, an
// earlier label's Video truncated to end where the next begins whenever the
// two would otherwise overlap on screen, and FirstFrame/LastFrame resolved
// against the render's own Timeline. Nothing downstream re-derives or
// re-validates any of that.
type Label struct {
	// Name is the text drawn for this label. Required -- unlike
	// Highlight.Name, which may be empty because a highlight still lights a
	// block and marks the margin with no name at all. A label IS its name;
	// one with no name draws nothing, so resolveLabels refuses it outright
	// rather than accepting a label that is nothing.
	Name string

	// At is the instant this label marks, in the activity's ELAPSED time --
	// the same clock Highlight.From and Highlight.To use, for the same
	// reasons (see Highlight's own doc comment).
	At time.Duration

	// Video is how much VIDEO time this label's name stays on screen,
	// starting AT the instant rather than centred on it -- "when to add the
	// label" names the start, not the middle. Defaults to
	// cmd/label.go's defaultLabelVideo when --label gave no video= of its
	// own, and is narrowed by resolveLabels when Truncated (below) is true,
	// so this always reflects what the label actually occupies rather than
	// what was asked for.
	Video time.Duration

	// Truncated records whether Video was cut short of what --label asked
	// for because the next label's own span would otherwise have begun
	// before this one ended. See cmd/label.go's resolveLabels for why label
	// overlap is truncated and reported rather than refused outright the
	// way highlight overlap is -- the two rules differ on purpose.
	Truncated bool

	// FirstFrame, LastFrame are this label's own resolved frame bounds --
	// the frames its (possibly truncated) Video actually occupies once
	// resolved against the render's Timeline.
	//
	// Exported, and resolved by cmd/label.go rather than by a Timeline
	// method, because Timeline itself carries no notion of a label at all:
	// a label paces nothing, so a Timeline.LabelAt would be exactly the
	// speculative API this project's last review round refused for
	// SpeedupAt. See LabelAt, the free function that reads these bounds
	// back to decide which label, if any, a given frame belongs to.
	FirstFrame, LastFrame int
}

// LabelAt reports which label, if any, frame i falls inside, and how far
// through its entrance or exit ramp that frame sits -- the same 0->1->0
// transition Timeline.IntervalAt applies to a highlight, built from the
// same rampWeight arithmetic so a highlight's ramp and a label's cannot
// drift a frame apart by drifting into two separately-maintained copies of
// it.
//
// A free function beside Label, not a Timeline method: Timeline carries no
// notion of a label (see Label's own doc comment) and gains none here --
// the render loop, the one caller with both a frame index and the resolved
// label slice already in hand, calls this directly. fps is threaded
// through as a plain float64 rather than a *Timeline for the same reason:
// it is Timeline.FPS(), an ordinary accessor that already exists for
// reasons having nothing to do with labels, not a label-specific addition
// to the type.
//
// labels must already be resolved -- sorted by FirstFrame ascending, no two
// overlapping in frame range (see cmd/label.go's resolveLabels) -- what
// this does is arithmetic on whatever it is given, not a second validation
// aimed at the user.
//
// A linear scan, not a binary search: a render carries at most a handful of
// labels (nobody types dozens of --label), so the search cost is
// irrelevant here, unlike Timeline.segmentFor's search over up to 2k+1
// segments.
func LabelAt(labels []Label, i int, fps float64, transition time.Duration) (index int, weight float64) {
	for idx, l := range labels {
		if i < l.FirstFrame || i > l.LastFrame {
			continue
		}
		return idx, rampWeight(i, l.FirstFrame, l.LastFrame-l.FirstFrame+1, fps, transition)
	}
	return NoLabel, 0
}
