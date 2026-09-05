package panel

import (
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// Panel is one dashboard element, in three phases.
//
// The phases are not decoration. A HUD that composites over footage can get
// away with one Draw method plus an optional DrawStatic taking the same
// per-frame type, and this project's sibling does exactly that -- and it is the
// arrangement that failed there. The static call receives a per-frame value
// with everything but the dimensions left at zero, which makes "do not read
// anything else from it" a convention held up by a doc comment. When it was
// broken, an axis was labelled against one origin while the playhead drawn on
// top of it used another; both layers drew, they disagreed, and nothing errored.
//
// Splitting Prepare out makes that a compile error instead of a rule:
//
//   - Accepts decides whether the panel is placed at all, before any box
//     exists. This is where an activity-level absence is answered -- a pool
//     swim has no GPS, an indoor ride has no elevation -- and a panel that
//     declines is pruned from the layout so its siblings grow into its space.
//   - Prepare binds the panel to one render and one box, returning a Painter
//     that holds everything fixed for the whole video: the box, a projection,
//     an axis range, resolved font sizes.
//   - The Painter draws, in two passes, from that one set of fields.
type Panel interface {
	// Name identifies the panel in diagnostics and in the render summary that
	// reports which panels drew and which declined.
	Name() string

	// Accepts reports whether this panel has anything to show for this
	// activity. Returning false is a decision, not a failure: the layout
	// closes up around it.
	//
	// This is ALSO the panel's declaration of its absent-data policy, and
	// deliberately the only one. A panel returning true unconditionally has
	// chosen to draw a placeholder when a reading is missing; one that
	// inspects ctx has chosen to decline. A separate Policy method would be a
	// second declaration free to disagree with the behaviour.
	Accepts(ctx *Context) bool

	// Prepare binds the panel to one render. Everything invariant across the
	// video is computed here, once, and kept on the returned Painter.
	Prepare(ctx *Context, box Box) Painter
}

// Painter is one panel bound to one render.
type Painter interface {
	// Static draws what does not change: chrome, axes, labels, the dim
	// outline of a whole route. It is called ONCE per render, and it has no
	// Frame in scope -- not a zeroed one, none -- so there is no per-frame
	// value that could be silently absent here.
	Static(c *Canvas)

	// Dynamic draws what changes, over the static layer, once per frame.
	//
	// It must NOT mutate the Painter. Nothing today depends on that, and it
	// is what keeps parallel frame rendering possible later; a memo cache
	// written during drawing is why this project's sibling needs a mutex
	// around one.
	Dynamic(c *Canvas, f Frame)
}

// LabelOverlapReporter is an OPTIONAL capability a Painter may implement
// when it places --label ticks on an axis of its own, with a bounded nudge
// to pull coincident ones apart (see ElevationPanel's D.4 policy in
// elevation.go). OverlappingLabels reports which configured labels, by
// index into Context.Labels, still sit too close to a neighbour once that
// nudge has run and the bound has stopped it from going further.
//
// It is not part of Painter itself because most panels place no labels at
// all and would have nothing honest to return; internal/render type-asserts
// for it after Prepare rather than adding a no-op method every other Painter
// would have to carry. The render summary is what turns this list into
// words, mirroring the "not marked" line a mark with no resolvable distance
// already gets -- see cmd/render.go's writeLabelSummary.
type LabelOverlapReporter interface {
	OverlappingLabels() []int
}

// NoStatic is embedded by a panel with nothing invariant to draw.
//
// Embedding it is a deliberate, greppable declaration that the question was
// asked and the answer was none, where simply omitting the method would be
// indistinguishable from not having considered it. The panel contract asks
// which part of a panel is invariant across the whole render; making the
// method required means that question cannot be skipped.
type NoStatic struct{}

// Static draws nothing.
func (NoStatic) Static(*Canvas) {}

// Context is everything computed once and identical on every frame of one
// render.
//
// Panels read it in Accepts and Prepare and then let it go: a Painter keeps
// what it needs as its own fields. Frame deliberately holds no pointer back to
// here, so there is no route by which per-render state can arrive during the
// dynamic pass pretending to be per-frame, or the reverse.
type Context struct {
	// Track is the decoded activity.
	Track *fitactivity.Track

	// Report is what the activity actually contains, metric by metric, with
	// coverage rather than mere presence.
	//
	// It is here so fitdash has exactly ONE rule for "does this activity carry
	// power", shared by the inspect command and by every panel's Accepts. A
	// panel deriving it by walking the samples would be free to disagree with
	// the report the user just printed -- same file, two answers, and the
	// disagreement shows up only as a panel missing when inspect said it
	// should be there.
	Report inspect.Report

	// Elevation is the whole-activity elevation-vs-distance model, built
	// ONCE by whoever constructs this Context -- cmd/render.go for a real
	// render, a test fixture for a unit test -- via BuildElevation, and only
	// ever READ from here, never rebuilt.
	//
	// Before this field existed, ElevationPanel's own Accepts, its own
	// Prepare, and internal/render's profileTakesTheBand each built an
	// independent copy of the same model from the same track, three builds
	// of something that is not free: a target-tuned build (see
	// cmd's resolveElevationTuning, which resolves the target) runs up to
	// forty full Gaussian smoothing passes hunting for the sigma that
	// matches a known gain/loss total, measured at roughly fifty times an
	// untuned build on a short run. Three builds a render is one thing; the
	// climb and gradient panels this field exists to let land without
	// raising that count to eight are the reason it could not stay three
	// either.
	//
	// nil on a Context nobody populated it on -- Accepts and Prepare both
	// treat that exactly like an empty model (nothing to plot, so the panel
	// declines), never as licence to build one themselves: a second build
	// site is a second place the SAME track and the SAME tuning could
	// silently stop agreeing, which is the one failure this field exists to
	// close off.
	//
	// The tuning that produced it (--elevation-smoothing / --elevation-gain
	// and -loss / the file's own totals / the library default) is NOT kept
	// on this Context: nothing here reads it back, and the render summary's
	// "which of the four levels produced this" is carried separately, as a
	// plain string threaded through cmd's own renderInputs (see
	// resolveElevationTuning and writeElevationSummary), which is where a
	// future consumer belongs too rather than a second, unread field here.
	Elevation *fitactivity.ElevationModel

	// Timer resolves the activity's window and its elapsed-versus-active time.
	Timer *fitactivity.TimerModel

	// Timeline maps a frame index to an instant. See Timeline.
	Timeline Timeline

	// Width, Height are the output frame's pixel dimensions.
	Width, Height int

	// FontScale is the layout's base text size as a fraction of the frame's
	// smaller dimension, carried here so a Painter can resolve its own sizes
	// during Prepare rather than during drawing.
	FontScale float64

	// PowerSource selects which power reading the power panel shows when the
	// activity carries both a footpod's developer field and the standard FIT
	// power field. The zero value is fitactivity.PowerAuto: prefer the
	// footpod, fall back to native.
	//
	// The two are different sensors and routinely disagree, by enough to
	// change what the gauge says at the same instant -- so which one is
	// shown is a real choice rather than a formatting preference.
	PowerSource fitactivity.PowerSource

	// Smoothing controls how much a gauge reading is averaged over. See
	// Smoothing and Timeline.AutoSmoothingAt: an explicit window applies
	// everywhere, but "auto" is resolved per FRAME rather than once, because
	// a render can run at more than one rate once highlights are in play,
	// and a window derived from the render-wide base rate would average a
	// highlight slowed down to show a rep's detail over a window many times
	// too wide -- smoothing away the very detail the highlight exists to
	// show. See render's smoothSample for which readings are averaged and
	// which are deliberately left alone.
	Smoothing Smoothing

	// Highlights is the resolved, sorted, clipped set of --highlight ranges,
	// or nil when none were given. Panels read it in Accepts (a highlight
	// panel declines outright with none configured, which is a fact about
	// the flags rather than about the activity) and in Prepare, to size and
	// place whatever they draw from the whole set once rather than per
	// frame.
	Highlights []Highlight

	// HighlightStyle selects how a highlight is marked on screen --
	// HighlightStyleBorder, HighlightStyleWash or HighlightStyleNone.
	//
	// Read by internal/render and by no panel, which is not an oversight:
	// both treatments it selects are render-WIDE -- the border drawn in the
	// margin Layout.Resolve leaves empty, and the second static base the
	// wash blends toward -- so neither belongs to any box in the layout
	// tree, and neither could be a Painter's decision without that Painter
	// drawing outside its own Box. A panel that needs to know a highlight is
	// active reads Frame.Interval instead, which says that it is without
	// saying anything about how it is being marked.
	HighlightStyle string

	// HighlightTransition is how much VIDEO time a highlight's on-screen
	// mark takes to ramp in and out. See Frame.IntervalWeight, which is
	// where the render loop actually applies it.
	HighlightTransition time.Duration

	// Fonts measures text. It is here because a panel must resolve its text
	// sizes in Prepare, which has no Canvas -- see FaceCache.FitSize for why
	// sizing during drawing is wrong rather than merely inconvenient.
	//
	// Whoever builds the Context builds this, and the renderer requires it
	// rather than quietly creating one: a second cache would rebuild every
	// face the first already holds, and at 45,000 frames that is not a
	// rounding error.
	Fonts *FaceCache

	// Labels is the resolved, sorted set of --label instants, or nil when
	// none were given -- the label analogue of Highlights, for the same
	// reason: MarkerPanel reads it in Accepts (nil or empty means nothing to
	// show, unless a highlight is also configured) and in Prepare, to place
	// every label's tick and size its name once rather than per frame.
	Labels []Label

	// BottomBand selects which of the bottom strip's two Alt candidates
	// (layouts.go) the user gets: BottomBandProfile (the zero value's
	// behaviour too, so an unset Context renders exactly as it always has)
	// or BottomBandDistance, which omits the elevation profile outright so
	// the distance readout takes the band even on an activity that carries
	// elevation.
	//
	// Read by internal/render's keep filter, deliberately NOT by
	// ElevationPanel's or Readout's own Accepts. Accepts answers whether the
	// ACTIVITY carries the data; this answers whether the USER asked not to
	// show it anyway. Folding the two into one method is exactly the trap
	// docs/architecture.md's "one band, two candidates" section names: a
	// strip-variant of Distance whose Accepts also called
	// ElevationPanel.Accepts would have its own Accepts decline right
	// alongside the profile's, and the Alt slot would prune both, deleting
	// distance from the render entirely -- the one outcome the Alt
	// mechanism exists to prevent. Rejecting the profile in the keep filter
	// instead lets the Alt slot fall through to the readout for free, exactly
	// as it already does when the activity itself has no elevation.
	BottomBand string

	// GaugeStyle selects how a fluctuating Readout -- heart rate, pace,
	// power, cadence -- draws its reading: GaugeStylePlain (the zero value,
	// so an unset Context renders exactly as it always has), GaugeStyleTrack
	// (a scale track beneath the number), or GaugeStyleDial (the same scale
	// beside the number, as a semicircular arc and needle). See GaugeStyle's
	// own doc comment (gauge.go) for the design and Readout.scale for how
	// each metric's range is derived.
	//
	// --gauge-style sets this (SelectGaugeStyle resolves the flag's value),
	// defaulting to GaugeStylePlain -- so every existing caller that leaves
	// it unset, and every render before --gauge-style existed, keeps drawing
	// the plain style it always has.
	GaugeStyle GaugeStyle
}

// captionOffset is how far above a panel's own centre a caption sits, as a
// fraction of a "capUnit" each caller resolves for itself -- see
// ElapsedPanel.Prepare and Readout.Prepare, both of which use this same
// fraction so that a panel of either kind placed beside one of the other
// draws its caption on the same baseline.
//
// It used to be two independent constants, 0.34 on ElapsedPanel and 0.30 on
// Readout, each multiplying that panel's OWN box's unit (min(box.W,
// box.H)), chosen when the two never sat in the same row and so never had
// to agree. Once the clock and the distance readout became a deliberate
// pair (layouts.go), the mismatch showed up as DISTANCE's caption sitting
// visibly lower than ELAPSED's, and sharing only the fraction -- one
// constant, still multiplying each panel's own box's unit -- fixed it only
// conditionally. Row siblings are guaranteed the same box HEIGHT (the
// layout engine only ever varies a row's X and W dividing it), so the two
// units still agreed as long as both boxes stayed wider than that shared
// height. Forcing LandscapeLayout onto a portrait frame -- SelectLayout's
// own documented escape hatch for a shape the auto choice would not have
// picked -- narrows the distance box past its own height, the unit silently
// switches from the shared height to that panel's own narrower width, and
// the captions separate again: the exact defect sharing the fraction alone
// claimed to have closed.
//
// The premise the fix now depends on: ElapsedPanel, and the one Readout
// that is paired with it (Distance, via its rule field), both resolve
// their capUnit -- the quantity captionOffset actually multiplies -- as
// box.H rather than min(box.W, box.H). Row siblings are guaranteed an
// IDENTICAL box.H by construction (the layout engine only ever divides a
// row's WIDTH between its children, never its height), so two panels that
// both read it agree always, not "while both boxes stay wider than their
// shared height". box.H is never less than min(box.W, box.H), so this can
// only move a caption FURTHER from its panel's own centre than the old
// basis did, never closer -- it cannot reintroduce the collision
// TestReadout_RowsClearOneAnother guards, only widen the margin -- and it
// equals the old basis outright in the ordinary case either panel is
// placed in, where the box reads wider than it is tall.
//
// Every OTHER Readout (HeartRate, Pace, Power, Cadence) keeps capUnit at
// min(box.W, box.H), unchanged: none of the four is placed beside anything
// it needs to agree with, box.H there is free to grow on its own account
// (a sibling's decline, in a Col rather than a Row, can grow a box's height
// with its width held fixed), and TestReadout_RowsClearOneAnother is what
// keeps their own three rows a group as that happens. Applying box.H to
// every Readout indiscriminately was tried and reverted -- it separated
// HeartRate's own label from its value the moment its box's height
// exceeded its width, which is the ordinary shape a gauge-column readout
// takes whenever a neighbouring gauge has declined. The distinction this
// constant's callers now have to make -- "is my box's height a quantity a
// SIBLING also has, or one that can move independently of my own width" --
// is exactly the Row-versus-Col distinction the layout engine already
// enforces, so it is answerable per caller rather than guessed at.
const captionOffset = 0.30

// captionRuleGeometry is the rule drawn beneath a caption already placed at
// labelY by captionOffset: an 8% gap under the caption, a thickness of 1.2%
// of capUnit (never below 1px) -- the SAME capUnit the caller resolved for
// its own captionOffset (see that constant's doc comment), passed in rather
// than recomputed, so a caller cannot use one basis for the caption and
// another for the rule beneath it. Two rules that scaled their gap or
// thickness off their own box's unit independently would separate by
// exactly the amount ELAPSED's and DISTANCE's captions separated by before
// captionOffset shared its own basis.
//
// width is deliberately NOT part of capUnit: it is 72% of the CALLER's own
// box width, because a rule reads as proportional to the box it decorates,
// not as a fixed length matching a neighbour's. ElapsedPanel's box and
// Distance's box are a 2:1 split (see layouts.go), so their rules are
// correspondingly different lengths by design -- only the gap beneath the
// caption and the stroke's thickness are shared chrome.
//
// Both ElapsedPanel and the one Readout that draws a rule (Distance) call
// this from Prepare rather than each restating the same three numbers,
// which is what let the two disagree the first time distance-beside-elapsed
// landed: the geometry was copied, not shared, related only by a comment
// saying one mirrored the other.
func captionRuleGeometry(capUnit float64, box Box, labelY float64) (y, w, h float64) {
	y = labelY + capUnit*0.08
	w = box.W * 0.72
	h = capUnit * 0.012
	if h < 1 {
		h = 1
	}
	return y, w, h
}

// BasePx is the layout's base text size in pixels for this frame size.
func (c *Context) BasePx() float64 {
	unit := float64(c.Width)
	if c.Height < c.Width {
		unit = float64(c.Height)
	}
	return c.FontScale * unit
}

// Frame is the per-frame state, and carries nothing that is constant across
// the render.
type Frame struct {
	// Index is the 0-based frame number.
	Index int

	// At is the instant in the activity this frame shows, on the activity's
	// own clock.
	At time.Time

	// Elapsed and Active are the two clocks at At: wall-clock time since the
	// activity began, and moving time with pauses subtracted.
	//
	// Both, always, even when they are equal. A dashboard has to choose which
	// one its timeline runs on, and a reader should see the pair and the
	// choice rather than one number that answered the question for them.
	// Computed once here rather than N times in N panels.
	//
	// When the file carried no timer events, Active equals Elapsed because
	// there were no pauses to find -- which is NOT the same as an activity
	// that never stopped. A panel showing active time must consult
	// Context.Timer.HasTimerEvents and say so, rather than printing a number
	// that merely looks like a measurement.
	Elapsed, Active time.Duration

	// Sample is the activity interpolated to At.
	//
	// INVARIANT: when HasSample is false, this is the zero Sample. That is
	// what lets a panel write one presence check --
	// `if f.Sample.HasHeartRate { ... } else { placeholder }` -- and be
	// correct inside a dropout without a second condition, because every
	// presence flag on a zero Sample is false.
	//
	// The tempting change is to hold the last good sample so the dashboard
	// does not flicker. That is the confident lie in its purest form: a heart
	// rate frozen at 148 through a two-minute dropout is indistinguishable
	// from a live reading.
	Sample    fitactivity.Sample
	HasSample bool

	// HasTimerEvents reports whether the file carried any `timer` event at
	// all, and therefore whether Active is a measurement or merely equal to
	// Elapsed for want of anything to subtract.
	//
	// It is a per-frame field despite being constant across the render, and
	// that is deliberate. A panel cannot reach Context from Dynamic -- there
	// is no pointer back, on purpose -- so a fact the dynamic pass needs must
	// arrive on the Frame. Copying one bool per frame is the price of the
	// direction of that dependency, and the direction is what keeps per-render
	// state out of the dynamic pass.
	HasTimerEvents bool

	// Paused reports whether At falls inside one of the activity's paused
	// intervals. What a panel does with it -- dim a readout, freeze a pace,
	// print a marker -- is the panel's decision; reporting the fact is the
	// renderer's.
	Paused bool

	// Interval is this frame's position in Context.Highlights, or
	// NoHighlight when it belongs to none.
	//
	// Deliberately an index, not the Highlight value itself: Dynamic has no
	// route back to Context by design (see the Context doc comment), so this
	// is how the loop hands a Painter the one fact it needs in order to look
	// the highlight back up -- which is what Prepare, not Dynamic, is for.
	// And deliberately an index alone, not an index plus a HasInterval bool:
	// index 0 is a legitimate highlight, so the two fields could disagree,
	// which is exactly the "second declaration free to disagree with the
	// first" the Panel contract's Accepts/Prepare split exists to prevent at
	// a different seam. NoHighlight is the one documented meaning of the
	// sentinel, chosen because it reads as a violation of the absence rule
	// otherwise: this is the one case in the project where the safer risk is
	// a sentinel rather than a second flag.
	Interval int

	// IntervalWeight ramps 0->1 across a highlight's entrance transition,
	// holds at 1 through its body, and ramps 1->0 across its exit -- 0
	// outside every highlight. See Timeline.IntervalAt for the arithmetic.
	//
	// Computed once, here in the loop, in VIDEO time -- the ramp is a
	// perceptual effect, so --highlight-transition promises it looks the
	// same however compressed the render is -- rather than left for each
	// consumer to derive on its own. The border overlay and the highlight
	// strip both animate on this one number, and two independent
	// recomputations landing a frame apart is exactly the
	// two-layers-drew-and-disagreed failure this architecture exists to
	// prevent.
	IntervalWeight float64

	// Label is this frame's position in Context.Labels, or NoLabel when it
	// belongs to none -- the label analogue of Interval, for the same
	// reason given on that field's own comment: an index alone, not an
	// index plus a HasLabel bool, because index 0 is a legitimate label and
	// the two fields could otherwise disagree.
	Label int

	// LabelWeight is the label analogue of IntervalWeight: the same 0->1->0
	// ramp across a label's own entrance and exit transition, built from
	// the same shared rampWeight arithmetic (see LabelAt) so a highlight's
	// ramp and a label's cannot drift a frame apart.
	LabelWeight float64
}
