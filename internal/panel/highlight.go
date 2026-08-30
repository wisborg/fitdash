package panel

import (
	"image/color"
	"time"
)

// NoHighlight is the value Frame.Interval takes for a frame that belongs to
// no highlight, and the value a Timeline segment carries when it is not a
// highlight's own segment.
//
// -1 rather than a HasInterval bool alongside Interval int: index 0 is a
// legitimate highlight, so a presence flag living next to the index would be
// a second declaration free to disagree with the first -- the same trap the
// Panel contract's Accepts/Prepare split exists to avoid at a different
// seam. One field, one documented sentinel, nothing to disagree with itself.
const NoHighlight = -1

// Highlight names a stretch of the activity that gets its own pace and its
// own on-screen treatment.
//
// From and To are offsets into the activity's ELAPSED time -- Timer.Elapsed's
// clock, not Active's and not video time. Elapsed because Timeline already
// runs on it (see the Timeline doc comment), because it is the number on the
// rendered clock and in `fitdash inspect`, and because distance- or
// lap-bounded highlights need accessors fitactivity does not have yet (see
// cmd/highlight.go's resolveHighlights for why that is deliberately out of
// v1 rather than reimplemented locally).
//
// A Highlight reaching Context.Highlights has already been through
// resolveHighlights: From and To are sorted ascending across the slice, To
// has been clipped to the activity's own end wherever it ran past it, and no
// two highlights overlap (touching endpoints -- one ending exactly where the
// next begins -- are not an overlap). Nothing downstream -- Timeline
// construction, a Painter's Prepare -- re-derives or re-validates any of
// that; it is resolved exactly once.
type Highlight struct {
	// Name is free text, shown beside the lit block in a highlight strip.
	// Empty is legal: a block lighting up is itself the honest statement
	// that a highlight is active, and printing a placeholder in the name's
	// place would claim the program failed to find a name the user simply
	// chose not to give.
	Name string

	// From, To bound the highlight in the activity's elapsed time. The
	// interval is half-open, [From, To), matching Timeline's own frame
	// convention.
	From, To time.Duration

	// Video is the video-time length this highlight is paced to fill, or
	// zero when unset. Mutually exclusive with RateFactor -- resolveHighlights
	// refuses a highlight with both set. Neither set means "mark it, do not
	// re-pace it": a real use, calling out a climb without stretching the
	// video around it.
	Video time.Duration

	// RateFactor is this highlight's own compression factor, or zero when
	// unset.
	RateFactor float64

	// Clipped records whether To was pulled back to the activity's own end.
	Clipped bool

	// PausedThroughout records whether the whole highlight lies inside one
	// of the activity's paused stretches. The dashboard freezes through a
	// pause by design (see the Timeline doc comment), so a highlight slowed
	// down over one renders many seconds of a frozen dashboard -- which is
	// correct and will look like a bug unless it is reported. See
	// resolveHighlights.
	PausedThroughout bool

	// Background is the exact colour this highlight's own background=
	// resolved to, meaningful only under --highlight-style wash -- every
	// other style refuses background= outright (see cmd/highlight.go's
	// resolveHighlights) rather than silently ignoring it or repurposing it
	// as a border colour.
	//
	// Stored as a concrete color.NRGBA rather than the color.Color
	// interface for two reasons, one idiomatic and one load-bearing: this
	// project's presence-flag rule (see HasBackground), and because
	// internal/render keys its colour-indexed table of static wash bases on
	// this value -- color.Color is an interface with no comparability
	// guarantee, and a map key needs one.
	Background color.NRGBA

	// HasBackground records whether Background was set at all. A zero
	// color.NRGBA (transparent black) is a legitimate colour a user could
	// type, so it cannot double as "unset" -- the same absence-is-not-zero
	// rule every sensor reading in this project already follows.
	HasBackground bool
}

// Rate resolves this highlight's own compression factor given the render's
// base speedup.
//
// RateFactor wins when it is set. Otherwise Video wins, converted the same
// way NewTimeline's own speedup is defined: the length of activity this
// highlight covers over the video length it should take, SpeedupFor's own
// arithmetic applied to one highlight instead of the whole render.
// Otherwise base -- which is what "mark it, do not re-pace it" means in
// practice: the segment this highlight becomes runs at the same rate as the
// stretches on either side of it, distinguished only by what draws over it.
func (h Highlight) Rate(base float64) float64 {
	switch {
	case h.RateFactor > 0:
		return h.RateFactor
	case h.Video > 0:
		return (h.To - h.From).Seconds() / h.Video.Seconds()
	default:
		return base
	}
}

// --highlight-style's legal values. Exported so the CLI's own validation and
// a future MarkerPanel or border overlay compare against one set of strings
// rather than each defining its own.
const (
	// HighlightStyleBorder marks a highlight with an accent border in the
	// frame's margin plus the highlight strip. The default: it touches no
	// theme and needs no second static base (see docs/architecture.md's note
	// on why a Painter cannot change the background colour).
	HighlightStyleBorder = "border"

	// HighlightStyleWash additionally tints the whole background -- one
	// static base per distinct colour, blended per frame by
	// Frame.IntervalWeight, opt-in because it is the one style that can
	// break the static/dynamic invariant every other panel in this project
	// is built around. The colour is a highlight's own background= when it
	// has one, or the theme's own derived tint otherwise -- see
	// internal/render's washColorFor. Only this style gives background= any
	// meaning at all; every other style refuses it.
	HighlightStyleWash = "wash"

	// HighlightStyleNone re-paces the highlighted stretches without marking
	// them on screen; the highlight strip still shows where they are.
	HighlightStyleNone = "none"
)
