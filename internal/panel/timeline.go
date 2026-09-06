// Package panel defines what a dashboard panel is and how panels are arranged
// into a frame. It also owns the types a panel is handed -- Context, Frame and
// Timeline -- because the types that define a contract belong with the
// contract, and because the other direction would be an import cycle:
// internal/render drives panels, so it imports this package rather than the
// reverse.
package panel

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wisborg/fitactivity"
)

// Timeline maps a frame index to the instant in the activity that frame
// shows.
//
// PIECEWISE affine: within one segment, At(i) is
// segment.start + (i-segment.i0)/FPS*segment.rate -- exactly the single-rate
// arithmetic this type always used, with a segment's own origin standing in
// for the render's. Most Timelines have exactly one segment, spanning the
// whole activity at one rate, and that is not a special case living beside
// the general one: every constructor funnels through the same segment
// builder (see newTimelineFromSegments), so a highlight-free render is the
// zero-highlight instance of NewSegmentedTimeline rather than a different
// code path. See docs/architecture.md's "Timeline: elapsed" section for why
// giving up a single affine map was worth it and what it cost.
//
// Cutting an activity's pauses out of the video is what --pauses skip asks
// for, and it is OPT-IN rather than the default, because the objection to it
// is real and unchanged: a cut splices two instants together and teleports
// the route dot across whatever ground was covered while the watch was
// stopped, which looks exactly like the GPS glitch this project spends its
// care avoiding. Under the default -- PausesFreeze -- every segment covers
// real recorded time and nothing is spliced.
//
// What made the opt-in cheap is this type already being piecewise: a
// pause-skipping Timeline is the same segment list with the paused stretches
// left out of it, built by the same builder, not a second implementation of
// the type. What it costs is stated rather than hidden, and paid for on
// screen: every seam is recorded as a Cut, and the render draws a notice
// naming how much activity time went missing there, so a video where four
// minutes silently vanish is exactly what this does NOT produce. See
// docs/architecture.md's "Timeline: elapsed" for the original argument and
// what changed.
type Timeline struct {
	fps  float64
	segs []segment

	// origin is the activity instant elapsed offsets are measured from --
	// the start the constructor was given, which is NOT necessarily the
	// instant frame 0 shows once --pauses skip has removed a pause that
	// began at the activity's own start. IndexAt measures against this
	// rather than against Start() for exactly that case; with no cuts, or
	// no leading one, the two are the same instant and every existing
	// timeline behaves identically.
	origin time.Time

	// span is the whole elapsed extent the frames were laid over -- the d
	// the constructor was given -- INCLUDING anything the cuts removed. It
	// is the clock every offset a user types is measured on, which
	// ActivityDuration below stops being the moment a cut lands: that
	// reports how much activity the frames actually cover, and the two
	// differ by exactly the time skipped. See ActivitySpan.
	span time.Duration

	// cuts are the seams --pauses skip left in the render: the stretches of
	// activity that were removed, each with the frames its on-screen notice
	// occupies. Empty under PausesFreeze, and empty under PausesSkip on an
	// activity that never paused. See Cut and CutAt.
	cuts []Cut

	// frames is the sum of every segment's frame count, cached at
	// construction rather than summed on every Frames() call -- cheap either
	// way at <= 2k+1 segments, but Frames() is read every frame of the loop.
	frames int

	// base is the whole-activity rate the caller asked for, before any
	// highlight's own pacing was layered on. See BaseSpeedup.
	base float64

	// maxRate is the coarsest rate any segment runs at, cached for the same
	// reason frames is. See MaxSpeedup.
	maxRate float64
}

// segment is one stretch of the render at one rate.
//
// Unexported and never handed to a panel: no panel derives anything from a
// frame index today (Frame.Index has zero readers outside internal/render),
// so there is no population this would need to protect by staying hidden --
// it stays hidden anyway, because a segment list becoming part of the public
// contract is how the next highlight-adjacent feature ends up with its own
// copy of this arithmetic instead of a call into it.
type segment struct {
	// start is the true activity-time instant this segment begins at (a0 in
	// the construction arithmetic's own terms) -- NOT wherever the previous
	// segment's rounded last frame happened to land. Anchoring here rather
	// than chaining onto the previous segment's own rounding is what keeps
	// rounding error from accumulating across segments: a highlight late in
	// a long render starts exactly where it was asked to, not a
	// fraction-of-a-second later for every seam before it.
	start time.Time

	// rate is this segment's own speedup.
	rate float64

	// i0 is the first frame index this segment owns.
	i0 int

	// n is how many frames this segment owns, floored at 1: a highlight
	// whose own rate rounds its frame count to zero still gets one frame
	// rather than silently occupying no video at all.
	n int

	// highlight is this segment's position in the highlights slice
	// newTimelineFromSegments was built from, or NoHighlight for a segment
	// that is not a highlight's own -- the ordinary stretch of the render
	// around and between them.
	highlight int
}

// --pauses' legal values -- what the render does with the activity's paused
// stretches. Exported beside the type that implements the choice, so the
// CLI's own validation, Context.Pauses and this constructor compare against
// one pair of strings rather than each defining its own.
const (
	// PausesFreeze is the default and the zero value's behaviour: a paused
	// stretch is rendered like any other, with the dashboard frozen through
	// it. Every recorded instant appears, in order, at a positive rate.
	PausesFreeze = "freeze"

	// PausesSkip cuts the paused stretches out, so video time advances only
	// while the timer was running. See the Timeline doc comment for what
	// that costs and Cut for what marks each seam.
	PausesSkip = "skip"
)

// NoCut is the value Frame.Cut takes for a frame that is not showing a cut's
// notice -- its own name rather than reusing NoHighlight or NoLabel, for the
// reason NoLabel's own doc comment gives: a reader of `f.Cut == NoCut`
// should not have to think about highlights to understand it.
const NoCut = -1

// Cut is one stretch of the activity a --pauses skip render leaves out, and
// the frames of video where it says so.
//
// From and To are the removed span in the activity's ELAPSED time -- the
// same clock Highlight.From and Label.At use -- so a Cut can be read
// against the flags the user typed rather than against frame indices.
// FirstFrame and LastFrame are resolved by the constructor, not by a
// caller: they are the frames the seam's on-screen notice occupies, and
// they exist only for a cut with running activity on BOTH sides. A pause at
// the very start or the very end of an activity is still removed, and gets
// no Cut here, because there is no seam: nothing was spliced to anything,
// the render simply begins later or ends earlier.
type Cut struct {
	// From, To bound the removed stretch, as offsets from the activity's
	// own start. Set by whoever builds the timeline; the rest is resolved.
	From, To time.Duration

	// FirstFrame, LastFrame are the frames this cut's notice occupies.
	// FirstFrame is the first frame AFTER the removed stretch -- the frame
	// the splice lands on -- and LastFrame is cutNoticeVideo of video time
	// later, truncated where a following cut's own notice would otherwise
	// begin first.
	FirstFrame, LastFrame int
}

// Removed is how much activity time this cut leaves out. A method rather
// than a third field, so it cannot disagree with the bounds it is derived
// from.
func (c Cut) Removed() time.Duration { return c.To - c.From }

// cutNoticeVideo is how much VIDEO time a cut's notice stays on screen,
// starting at the seam.
//
// Video time, not activity time, for the reason --highlight-transition is
// also video time: this is a perceptual thing, and it should read the same
// however compressed the render is. A second and a half is long enough to
// read four words at a glance and short enough not to sit over the
// dashboard as if it were part of it. It is deliberately not a flag: a
// render that cuts pauses has to say so, so there is no value here worth
// setting to zero, and one more knob on an already long flag list buys
// nothing.
const cutNoticeVideo = 1500 * time.Millisecond

// NewTimeline lays frames over d at fps, beginning at start, compressing the
// activity by speedup.
//
// A speedup of 1 renders in real time: a four-hour ride becomes a four-hour
// video, which is faithful and almost nobody will watch. At 60 it becomes four
// minutes. The activity's own clock still reads activity time -- the video is
// compressed, the data is not relabelled -- so the elapsed readout advances
// sixty seconds per second of video, which is what a time-lapse should look
// like.
//
// What compression costs is stated rather than hidden: each frame samples the
// activity 'speedup/fps' seconds further on, so at 60x and 30 fps a frame
// covers two seconds and a heart-rate spike shorter than that falls between
// frames entirely. Nothing is averaged over the interval, because averaging
// would invent a reading nobody recorded; frames simply land where they land.
//
// Frames cover the HALF-OPEN interval [start, start+d): At(0) is start and
// At(Frames()-1) is one frame short of the end. That is what makes the video's
// duration come out as d -- a frame occupies the time until the next one, so a
// final frame sitting exactly on the end instant would make the video one
// frame longer than the activity it shows.
//
// The frame count is rounded rather than truncated, so a duration that is not
// a whole number of frames lands on the nearest one instead of always losing
// the remainder. A positive duration always yields at least one frame, even
// when d*fps rounds to zero: a ten-millisecond activity at 30 fps is a
// one-frame video, which is odd but honest, where a zero-frame video is a file
// no player will open.
//
// This is exactly NewSegmentedTimeline(start, d, fps, speedup, nil): a single
// segment spanning the whole window at one rate.
func NewTimeline(start time.Time, d time.Duration, fps, speedup float64) (Timeline, error) {
	return NewSegmentedTimeline(start, d, fps, speedup, nil)
}

// NewSegmentedTimeline is NewTimeline generalised to a piecewise rate: every
// highlight gets its own segment at its own rate (see Highlight.Rate), and
// everything between and around them runs at rate. See the Timeline doc
// comment for what a piecewise map costs against a single affine one, and
// newTimelineFromSegments for the construction arithmetic itself.
//
// highlights must already be resolved -- sorted by From, each clipped to
// [0,d), and free of overlap -- which is cmd/highlight.go's job, with an
// error message aimed at whatever the user typed. What is checked again here
// is the last line of defense against a caller inside this module passing
// something that was never resolved, not a second copy of that validation
// aimed at a user.
func NewSegmentedTimeline(start time.Time, d time.Duration, fps, rate float64, highlights []Highlight) (Timeline, error) {
	return NewCutTimeline(start, d, fps, rate, highlights, nil)
}

// NewCutTimeline is NewSegmentedTimeline with stretches of the activity
// removed outright: every Cut's [From, To) is left out of the render, and
// the frames close up around it.
//
// It is where the window, fps and rate checks actually live, and
// NewSegmentedTimeline is exactly this called with no cuts -- the same
// delegation NewTimeline already makes to NewSegmentedTimeline, and for the
// same reason: two constructors each restating the same four refusals is two
// places for them to drift.
//
// cuts must be sorted ascending, non-overlapping and within [0, d] -- which
// is exactly what fitactivity's TimerModel.Pauses already guarantees, the
// one producer this has. What is checked again here is the last line of
// defence against a caller inside this module passing something that was
// never resolved, not a second copy of a validation aimed at a user.
//
// A Cut is not the same shape of thing as a Highlight and is deliberately
// not expressed as one with a rate of zero: a zero rate is not a rate, the
// frame count arithmetic divides by it, and the floor-at-one-frame rule
// that keeps a named highlight from silently occupying no video is exactly
// the rule a cut must NOT get.
func NewCutTimeline(start time.Time, d time.Duration, fps, rate float64, highlights []Highlight, cuts []Cut) (Timeline, error) {
	if start.IsZero() {
		return Timeline{}, fmt.Errorf("panel: timeline has no start instant")
	}
	if d <= 0 {
		return Timeline{}, fmt.Errorf("panel: timeline duration must be positive, got %v", d)
	}
	if err := checkFPS(fps); err != nil {
		return Timeline{}, err
	}
	if err := checkRate(rate); err != nil {
		return Timeline{}, err
	}
	for i, c := range cuts {
		if c.From < 0 || c.To > d || c.To <= c.From {
			return Timeline{}, fmt.Errorf("panel: cut %d [%v,%v) is out of the timeline's own bounds [0,%v)", i, c.From, c.To, d)
		}
		if i > 0 && c.From < cuts[i-1].To {
			return Timeline{}, fmt.Errorf("panel: cut %d overlaps the previous one", i)
		}
	}
	return newTimelineFromSegments(start, d, fps, rate, highlights, cuts)
}

// checkFPS and checkRate are the two input checks NewCutTimeline makes -- and
// so, through it, every constructor on this type -- with nothing else needing
// its own wording for the same failure.
func checkFPS(fps float64) error {
	if fps <= 0 || math.IsInf(fps, 0) || math.IsNaN(fps) {
		return fmt.Errorf("panel: fps must be a positive finite number, got %v", fps)
	}
	return nil
}

func checkRate(rate float64) error {
	if rate <= 0 || math.IsInf(rate, 0) || math.IsNaN(rate) {
		return fmt.Errorf("panel: speedup must be a positive finite number, got %v", rate)
	}
	return nil
}

// newTimelineFromSegments is the one place every constructor builds a
// Timeline's segment list, so the whole-activity case -- one segment
// spanning [0,d) at rate -- is the zero-highlight instance of the general
// construction rather than a special case living beside it.
//
// Construction, per the design this implements: partition [0,d) at each
// highlight's own boundaries; a highlight segment gets Highlight.Rate(rate),
// everything else gets rate; a segment's frame count is
// round(length/segmentRate*fps), floored at 1; a segment's first frame index
// is the running sum of every earlier segment's count; and a segment's start
// is the TRUE boundary offset from start, not wherever the previous
// segment's rounding happened to land -- see the segment.start field comment
// for why that anchoring is what keeps rounding error from accumulating
// across segments.
func newTimelineFromSegments(start time.Time, d time.Duration, fps, rate float64, highlights []Highlight, cuts []Cut) (Timeline, error) {
	for i, h := range highlights {
		if h.From < 0 || h.To > d || h.To <= h.From {
			return Timeline{}, fmt.Errorf("panel: highlight %d (%q) [%v,%v) is out of the timeline's own bounds [0,%v)",
				i, h.Name, h.From, h.To, d)
		}
		if i > 0 && h.From < highlights[i-1].To {
			return Timeline{}, fmt.Errorf("panel: highlight %d (%q) overlaps the previous one", i, h.Name)
		}
	}

	var bounds []bound
	cursor := time.Duration(0)
	for i, h := range highlights {
		if h.From > cursor {
			bounds = append(bounds, bound{from: cursor, to: h.From, rate: rate, highlight: NoHighlight, cut: NoCut})
		}
		bounds = append(bounds, bound{from: h.From, to: h.To, rate: h.Rate(rate), highlight: i, cut: NoCut})
		cursor = h.To
	}
	if cursor < d {
		bounds = append(bounds, bound{from: cursor, to: d, rate: rate, highlight: NoHighlight, cut: NoCut})
	}
	if len(bounds) == 0 {
		// d > 0 is already checked by every caller, so this is only the
		// no-highlights case: one segment, the whole window, at rate.
		bounds = append(bounds, bound{from: 0, to: d, rate: rate, highlight: NoHighlight, cut: NoCut})
	}

	// The cuts are subtracted AFTER the highlights have partitioned the
	// window, not before, and the order is not interchangeable. A highlight
	// is a range the user typed in the activity's own elapsed time; clipping
	// it against the cuts first would leave the rest of the construction
	// working from bounds that no longer say which stretch the user actually
	// named, and a highlight that straddles a pause would come out as two
	// highlights rather than one interrupted one.
	if len(cuts) > 0 {
		bounds = subtractCuts(bounds, cuts)
		if len(bounds) == 0 {
			return Timeline{}, fmt.Errorf("panel: every stretch of this activity is paused, so skipping the pauses leaves nothing to render")
		}
	}

	segs := make([]segment, 0, len(bounds))
	// kept is the cuts that actually became a seam -- one with running
	// activity on both sides. A cut at the very start or the very end of
	// the activity is still removed and simply has nothing to mark; see
	// Cut's own doc comment.
	kept := make([]Cut, 0, len(cuts))
	frameCursor := 0
	var maxRate float64
	for _, b := range bounds {
		// A cut preceding the FIRST surviving bound removed a stretch that
		// began at the activity's own start: there is no earlier frame for
		// it to be a seam between, so it is dropped rather than recorded
		// as a notice sitting over frame 0 announcing a splice that never
		// happened.
		if b.cut != NoCut && len(segs) > 0 {
			c := cuts[b.cut]
			c.FirstFrame = frameCursor
			kept = append(kept, c)
		}
		length := b.to - b.from
		n := int(math.Round(length.Seconds() / b.rate * fps))
		if n < 1 {
			// A segment -- in practice, a highlight -- that rounds to zero
			// frames still gets one. A named highlight silently occupying no
			// video is the same class of failure as a silently-missing
			// panel; see resolveHighlights, which is where this gets
			// reported to the user rather than merely tolerated here.
			n = 1
		}
		segs = append(segs, segment{
			start:     start.Add(b.from),
			rate:      b.rate,
			i0:        frameCursor,
			n:         n,
			highlight: b.highlight,
		})
		frameCursor += n
		if b.rate > maxRate {
			maxRate = b.rate
		}
	}

	return Timeline{
		fps:     fps,
		segs:    segs,
		frames:  frameCursor,
		base:    rate,
		maxRate: maxRate,
		origin:  start,
		span:    d,
		cuts:    resolveCutNotices(kept, fps, frameCursor),
	}, nil
}

// bound is one stretch of the window before it becomes a segment: the
// highlight partition, then whatever survives the cuts.
type bound struct {
	from, to  time.Duration
	rate      float64
	highlight int

	// cut is the index into the cuts slice of the stretch removed
	// IMMEDIATELY before this bound, or NoCut when this bound follows on
	// from the previous one with nothing taken out between them.
	cut int
}

// subtractCuts removes every cut's span from bounds, splitting a bound that
// a cut falls inside and dropping one a cut swallows whole, and records on
// each survivor which cut (if any) was removed immediately before it.
//
// O(bounds x cuts) rather than a merge walk over the two sorted lists. A
// cut can span several bounds and a bound can contain several cuts, and the
// index bookkeeping a single walk needs to get both right is where this
// would acquire an off-by-one that only shows on a file with a pause
// crossing a highlight's own boundary. Both lists are short -- at most a
// couple of thousand bounds against a recording's handful of pauses -- and
// this runs once, at construction, not per frame.
func subtractCuts(bounds []bound, cuts []Cut) []bound {
	out := make([]bound, 0, len(bounds)+len(cuts))
	// pending carries a cut across the bound boundary it may straddle: a
	// pause covering the end of one highlight and the start of the next
	// removes a piece of two bounds and is still ONE seam, marked on
	// whichever bound finally emits.
	pending := NoCut
	for _, b := range bounds {
		cursor := b.from
		for i, c := range cuts {
			if c.To <= cursor || c.From >= b.to {
				continue // entirely before what is left of this bound, or after it
			}
			if c.From > cursor {
				out = append(out, bound{from: cursor, to: c.From, rate: b.rate, highlight: b.highlight, cut: pending})
				pending = NoCut
			}
			pending = i
			if c.To > cursor {
				cursor = c.To
			}
			if cursor >= b.to {
				break
			}
		}
		if cursor < b.to {
			out = append(out, bound{from: cursor, to: b.to, rate: b.rate, highlight: b.highlight, cut: pending})
			pending = NoCut
		}
	}
	return out
}

// resolveCutNotices gives each seam the frames its on-screen notice
// occupies: cutNoticeVideo of video time from the seam onward, truncated
// where the next seam's own notice would otherwise begin first, and clamped
// to the render.
//
// Truncated rather than refused, and rather than allowed to overlap: two
// notices on screen at once, one of them describing a splice that already
// passed, is worse than a short one. This is the same rule resolveLabels
// applies to two --label spans that would collide, deliberately -- a reader
// who has understood one has understood the other.
func resolveCutNotices(cuts []Cut, fps float64, frames int) []Cut {
	if len(cuts) == 0 {
		return nil
	}
	span := int(math.Round(cutNoticeVideo.Seconds() * fps))
	if span < 1 {
		span = 1
	}
	for i := range cuts {
		last := cuts[i].FirstFrame + span - 1
		if i+1 < len(cuts) && last >= cuts[i+1].FirstFrame {
			last = cuts[i+1].FirstFrame - 1
		}
		if last > frames-1 {
			last = frames - 1
		}
		if last < cuts[i].FirstFrame {
			last = cuts[i].FirstFrame
		}
		cuts[i].LastFrame = last
	}
	return cuts
}

// SpeedupFor returns the compression that fits an activity of length activity
// into a video of length target.
//
// It is the inverse of the same arithmetic NewTimeline applies, kept here so a
// caller offering a target duration and a caller offering a factor go through
// one rule. Both zero or negative inputs yield 1, which renders in real time --
// the caller validates; this does not silently invent a compression.
func SpeedupFor(activity, target time.Duration) float64 {
	if activity <= 0 || target <= 0 {
		return 1
	}
	return activity.Seconds() / target.Seconds()
}

// activityWindow resolves an activity's start and end from its TimerModel,
// with the error messages NewTimelineForActivity and
// NewTimelineForActivityWithHighlights share rather than each restating.
func activityWindow(timer *fitactivity.TimerModel) (start, end time.Time, err error) {
	if timer == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("panel: no timer model")
	}
	start, end = timer.Window()
	if start.IsZero() || end.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("panel: activity has no resolved time window; the file carried neither session timing nor samples")
	}
	return start, end, nil
}

// NewTimelineForActivity lays a timeline over the whole of an activity.
//
// The window comes from the TimerModel rather than from the track's samples,
// and that is not interchangeable. The model resolves what an activity's start
// and end ARE -- the session's start_time when the file carried one, its
// declared total elapsed time when the session carried totals -- and those can
// differ from the first and last records by seconds. Measuring the timeline
// against one window while Frame.Elapsed is measured against another produces
// a final frame whose clock reads something other than the activity's total,
// with nothing reporting a problem. One window, from the type that owns it.
func NewTimelineForActivity(timer *fitactivity.TimerModel, fps, speedup float64) (Timeline, error) {
	start, end, err := activityWindow(timer)
	if err != nil {
		return Timeline{}, err
	}
	d := end.Sub(start)
	if d <= 0 {
		return Timeline{}, fmt.Errorf("panel: activity spans %v; there is nothing to render", d)
	}
	return NewTimeline(start, d, fps, speedup)
}

// NewTimelineForActivityWithHighlights is NewTimelineForActivity with a
// resolved, sorted, non-overlapping set of highlights layered on -- see
// Context.Highlights and resolveHighlights, which is what produces that set.
// NewTimelineForActivity is exactly this function called with no highlights;
// both funnel through NewSegmentedTimeline.
func NewTimelineForActivityWithHighlights(timer *fitactivity.TimerModel, fps, speedup float64, highlights []Highlight) (Timeline, error) {
	return NewTimelineForActivityWithPauses(timer, fps, speedup, highlights, PausesFreeze)
}

// NewTimelineForActivityWithPauses is NewTimelineForActivityWithHighlights
// told what to do with the activity's paused stretches: PausesFreeze renders
// them like any other stretch, PausesSkip cuts them out. The two-argument
// form above is exactly this called with PausesFreeze, so a render that
// never mentions the flag goes through the identical construction it always
// has.
//
// The pause list comes from the TimerModel, which owns it, rather than being
// derived here from Active or sampled from Paused -- see
// TimerModel.Pauses for why a second derivation of where the pauses are is
// the thing this must not become.
//
// An unknown mode is refused rather than defaulting to freeze. A typo
// reaching here would produce a render that quietly ignored the flag, which
// is the failure every other value-taking flag in this project refuses at
// the point the user typed it.
func NewTimelineForActivityWithPauses(timer *fitactivity.TimerModel, fps, speedup float64, highlights []Highlight, pauses string) (Timeline, error) {
	start, end, err := activityWindow(timer)
	if err != nil {
		return Timeline{}, err
	}
	d := end.Sub(start)
	if d <= 0 {
		return Timeline{}, fmt.Errorf("panel: activity spans %v; there is nothing to render", d)
	}
	switch pauses {
	case "", PausesFreeze:
		return NewSegmentedTimeline(start, d, fps, speedup, highlights)
	case PausesSkip:
		return NewCutTimeline(start, d, fps, speedup, highlights, cutsForPauses(timer, start))
	}
	return Timeline{}, fmt.Errorf("panel: unknown pause handling %q; use %s or %s", pauses, PausesFreeze, PausesSkip)
}

// cutsForPauses turns the activity's paused intervals into cuts, as offsets
// from the activity's own start.
//
// TimerModel.Pauses already returns them ascending, non-overlapping and
// clipped to the same window activityWindow resolved, which is exactly what
// NewCutTimeline requires -- so this converts instants to offsets and
// nothing else. Any sorting, merging or clipping added here would be a
// second copy of a rule that already has an owner.
func cutsForPauses(timer *fitactivity.TimerModel, start time.Time) []Cut {
	pauses := timer.Pauses()
	if len(pauses) == 0 {
		return nil
	}
	cuts := make([]Cut, len(pauses))
	for i, p := range pauses {
		cuts[i] = Cut{From: p.Start.Sub(start), To: p.End.Sub(start)}
	}
	return cuts
}

// autoSmoothingVideoWindow is how much VIDEO time the automatic smoothing
// window covers.
//
// Video time rather than activity time, because the problem it solves is
// perceptual: a number that changes wildly from frame to frame cannot be read,
// and how fast that is depends on the frame rate and on nothing else. Fixing
// the window in activity time instead would smooth a real-time render as hard
// as a 480x one, and would smooth a 60 fps render half as much as a 30 fps one
// for no reason a viewer could see.
//
// A third of a second is a judgement rather than a derivation: long enough that
// consecutive frames' windows overlap heavily and the readout drifts instead of
// flickering, short enough that a hard effort still shows as a rise. It is what
// --smoothing exists to override.
const autoSmoothingVideoWindow = 300 * time.Millisecond

// minSmoothingWindow is the shortest window worth applying.
//
// The activities this targets are recorded at 1 Hz, so a window narrower than
// a second spans at most one recorded sample and there is nothing in it to
// average. It is not a no-op, though, and assuming it was is a mistake this
// constant exists to stop repeating: Track.Window INTERPOLATES its endpoints,
// so a 300ms window returns three points -- two invented -- whose mean is not
// the recorded reading. Measured, it moved a real-time heart rate from 147 to
// 149: harmless, and still a value nobody recorded, shown where the user asked
// for no smoothing in all but name.
const minSmoothingWindow = time.Second

// AutoSmoothingAt is the smoothing window frame i's own segment implies, in
// ACTIVITY time, or zero when that segment's compression is mild enough not
// to need any -- autoSmoothingVideoWindow scaled by the segment's own rate,
// floored by minSmoothingWindow, exactly the arithmetic AutoSmoothing used to
// apply render-wide.
//
// Replaces AutoSmoothing now that a render can run at more than one rate.
// Deriving the window from the render-wide BASE rate instead would make the
// feature actively counterproductive: a highlight slowed to fill ten seconds
// of video with one second of activity would still get a window sized for
// the base compression, averaging that one second's worth of frames over a
// span many times wider than the highlight itself -- smoothing away the
// detail the highlight exists to show, in the exact moment the viewer cared
// about it.
func (t Timeline) AutoSmoothingAt(i int) time.Duration {
	return autoSmoothingFor(t.segmentFor(i).rate)
}

// AutoSmoothingBase is the auto-smoothing window the RENDER-WIDE base rate
// implies -- the same arithmetic AutoSmoothingAt applies to whichever
// segment a frame falls in, applied here to BaseSpeedup instead.
//
// It exists for the summary, not the frame loop: cmd/render.go's
// smoothingNote must say something more useful than the bare word "auto" now
// that a render can carry more than one window, and the one render-wide
// figure that still means something without naming every highlight is the
// window the base rate alone would produce. Per-highlight figures belong in
// the highlight table instead (see cmd/render.go's writeHighlightSummary),
// each read back from AutoSmoothingAt at that highlight's own first frame,
// which is what makes the FULL picture the base line plus the table rather
// than this method trying to say it alone.
func (t Timeline) AutoSmoothingBase() time.Duration {
	return autoSmoothingFor(t.base)
}

// autoSmoothingFor is the shared arithmetic AutoSmoothingAt and
// AutoSmoothingBase both apply to whichever rate they were given:
// autoSmoothingVideoWindow scaled by the rate, floored to zero (meaning off)
// below minSmoothingWindow.
func autoSmoothingFor(rate float64) time.Duration {
	w := time.Duration(float64(autoSmoothingVideoWindow) * rate)
	if w < minSmoothingWindow {
		return 0
	}
	return w
}

// Frames is the number of frames in the render.
func (t Timeline) Frames() int { return t.frames }

// FPS is the frame rate, which the encoder must be given the same value of --
// the container's timestamps and the dashboard's own clock have to derive from
// one number or the video drifts against the readout burned into it.
func (t Timeline) FPS() float64 { return t.fps }

// BaseSpeedup is the whole-activity rate the caller asked for: 1 is real
// time, 60 turns an hour of activity into a minute of video. It is the rate
// every stretch of the render runs at BEFORE a highlight's own pacing is
// layered on top of it -- see MaxSpeedup for the figure a Painter that must
// pick a single render-wide number, rather than one per segment, should read
// instead.
//
// Renamed from Speedup, deliberately: once a render can carry more than one
// rate there is no longer a single scalar answer to "how fast is this
// render", and letting a caller keep asking Speedup() and silently getting
// the base back would hand callers like readout.go's distance-precision
// rule a number that no longer means what it used to. The rename forces
// every call site through the compiler instead.
func (t Timeline) BaseSpeedup() float64 { return t.base }

// MaxSpeedup is the coarsest rate any segment of this timeline runs at:
// BaseSpeedup when there are no highlights, or a highlight's own rate when
// one is coarser than the base.
//
// This is the number bindDistancePrecision (internal/panel/readout.go) picks
// the distance readout's decimal precision from, once, in Prepare -- and the
// unit that decision is drawn against is in the STATIC layer, so it has to
// be one render-wide answer. Choosing the coarsest rate means a slowed
// highlight shows one decimal where two would fit, which is the safe
// direction to be wrong in: choosing the base or the finest rate would leave
// the fast stretches -- most of the render -- churning both decimals every
// frame, which is the exact flicker coarseDistanceStep exists to prevent.
func (t Timeline) MaxSpeedup() float64 { return t.maxRate }

// ActivityDuration is how much of the ACTIVITY the frames span, as opposed to
// how long the video runs.
//
// The two are the same only at a speedup of 1, and reporting just one of them
// would be the confusing half: a user who asked for a three-minute video wants
// to see that they got three minutes AND that it still covers the whole
// four-hour ride.
//
// Summed per segment -- n/fps*rate for each -- rather than derived from one
// rate and the total frame count, which is what a piecewise timeline
// requires; for a single-segment Timeline this is the exact arithmetic
// ActivityDuration always used, in the same order of operations, so it comes
// out bit-for-bit identical.
func (t Timeline) ActivityDuration() time.Duration {
	var total float64
	for _, s := range t.segs {
		total += float64(s.n) / t.fps * s.rate
	}
	return time.Duration(math.Round(total * float64(time.Second)))
}

// ActivitySpan is the activity's whole elapsed extent, including any stretch
// --pauses skip removed from the render.
//
// Distinct from ActivityDuration, and the distinction is the one a caller
// validating a user's offset has to get right: ActivityDuration is how much
// activity the FRAMES cover, this is how long the activity RAN. They are
// equal under the default and differ by exactly the skipped time under
// --pauses skip -- so bounding a --frame-at against ActivityDuration would
// refuse an offset that is plainly inside the activity, telling the user
// their twenty-eight-minute mark is "past the end of the activity, which
// runs 0:25:56".
func (t Timeline) ActivitySpan() time.Duration { return t.span }

// Start is the instant frame 0 shows.
func (t Timeline) Start() time.Time { return t.segs[0].start }

// Duration is how long the encoded VIDEO runs: Frames()/FPS.
//
// Not how much activity it covers -- see ActivityDuration. The two diverge as
// soon as the render is sped up, and confusing them would put a four-hour
// figure on a three-minute file.
//
// This is NOT necessarily the duration NewTimeline was given. Rounding the
// frame count to a whole number of frames can move it by up to half a frame,
// and this reports what was actually laid down rather than what was asked for
// -- a caller checking the output video's length wants the number the output
// was built from.
func (t Timeline) Duration() time.Duration {
	return time.Duration(math.Round(float64(t.frames) / t.fps * float64(time.Second)))
}

// At returns the instant frame i shows.
//
// Defined for every i, not only for 0 <= i < Frames(): a caller computing a
// lookahead or lookbehind instant should get arithmetic rather than a
// special case. Below 0 it extrapolates at the first segment's rate; at or
// beyond Frames() it extrapolates at the last segment's -- a boundary no
// real render crosses, but the same courtesy the single-rate version always
// extended to every i.
//
// Within a segment the arithmetic is exactly what a single affine Timeline
// always used -- the offset from the segment's OWN start, rounded to the
// nearest nanosecond so the error against the exact instant stays bounded
// instead of accumulating with i -- with the segment's start standing in for
// the render's. See the segment.start field comment for why that start is
// anchored at the true boundary rather than chained from the previous
// segment.
func (t Timeline) At(i int) time.Time {
	seg := t.segmentFor(i)
	offset := math.Round(float64(i-seg.i0) / t.fps * seg.rate * float64(time.Second))
	return seg.start.Add(time.Duration(offset))
}

// segmentFor returns the segment frame i belongs to, extrapolating at the
// first segment's rate below 0 and the last segment's at or beyond Frames().
// A binary search over <= 2k+1 segments, not a search through the activity's
// own timer events -- see the Timeline doc comment.
func (t Timeline) segmentFor(i int) segment {
	if i < 0 {
		return t.segs[0]
	}
	if i >= t.frames {
		return t.segs[len(t.segs)-1]
	}
	idx := sort.Search(len(t.segs), func(k int) bool {
		return t.segs[k].i0+t.segs[k].n > i
	})
	return t.segs[idx]
}

// IndexAt returns the frame showing the instant offset into the ACTIVITY,
// rounded to the nearest frame and clamped to the render.
//
// Activity time, not video time: a user asking for the frame at 12m30s means
// twelve and a half minutes into their run, which at a speedup of 10 is
// seventy-five seconds into the video. Interpreting the offset as video time
// would answer a question nobody asked.
//
// This is At's inverse within a segment, and having it is most of the reason
// At stays affine there: it is what lets a caller ask for "the frame at
// 12m30s" -- which is how a render is inspected inside a GPS dropout or a
// paused stretch without rendering everything up to it -- with a division
// rather than a search through the activity's own timer events. Locating the
// segment IS a search, but over the render's own <= 2k+1 segments rather than
// that -- see the Timeline doc comment.
//
// Clamping rather than erroring is deliberate here and is the opposite of
// what the PNG sink does with an unreachable frame index. The distinction:
// an offset past the end of the activity has an obvious intended meaning,
// the last frame, whereas a raw frame index past the end of the render is a
// request the caller must have computed wrongly. A caller who needs to know
// the offset was out of range can compare against ActivityDuration.
func (t Timeline) IndexAt(offset time.Duration) int {
	// Measured from the ACTIVITY's own start, not from Start(): under
	// --pauses skip a pause at the very beginning is removed, so frame 0
	// shows an instant some way into the activity and the two differ. Every
	// caller passes an offset the user typed into a flag, which is elapsed
	// time from the activity's start whatever the render then does with it.
	target := t.origin.Add(offset)
	// Segments partition the activity's elapsed time, with a gap exactly
	// where a cut removed one (and none at all without --pauses skip), so
	// the last segment whose own start is at or before target is the one
	// that contains it -- or, for a target inside a cut, the one just
	// before the seam, which the clamp below then resolves to that
	// segment's last frame. An instant that was cut out of the render has
	// no frame of its own; the frame the splice lands on is the closest
	// true answer available.
	idx := sort.Search(len(t.segs), func(k int) bool {
		return t.segs[k].start.After(target)
	}) - 1
	if idx < 0 {
		idx = 0
	}
	seg := t.segs[idx]

	within := target.Sub(seg.start)
	i := seg.i0 + int(math.Round(within.Seconds()/seg.rate*t.fps))

	// Clamp to the segment first...
	if i < seg.i0 {
		i = seg.i0
	}
	if last := seg.i0 + seg.n - 1; i > last {
		i = last
	}
	// ...then to the render, which differs from the segment clamp only at
	// the very first or last segment, and only when offset itself falls
	// outside the activity.
	if i < 0 {
		i = 0
	}
	if i >= t.frames {
		i = t.frames - 1
	}
	return i
}

// Cuts returns the seams --pauses skip left in this render, ascending, each
// with the stretch of activity it removed and the frames its notice
// occupies. Empty under PausesFreeze, and empty under PausesSkip on an
// activity that never paused.
//
// Returns the slice itself rather than a copy, matching every other
// accessor on this type: a Timeline is passed by value and read all over
// the render, and nothing in this module writes to what it is handed.
func (t Timeline) Cuts() []Cut { return t.cuts }

// CutRemoved is how much activity time this render leaves out in total --
// the sum of every SEAM's own removed span.
//
// Deliberately not the activity's whole paused total: a pause at the very
// start or end of the activity is removed too but is not a seam (see Cut),
// so a caller reporting "removed" from this number is reporting what was
// spliced OUT OF the video, not what the watch spent stopped. The second
// figure belongs to the activity and is TimerModel.PausedTotal's to
// answer; conflating them would put a number on screen that no seam
// accounts for.
func (t Timeline) CutRemoved() time.Duration {
	var total time.Duration
	for _, c := range t.cuts {
		total += c.Removed()
	}
	return total
}

// CutAt reports which cut's notice, if any, frame i falls inside, and how
// far through its entrance or exit ramp that frame sits -- the label
// analogue for a seam, built from the same rampWeight arithmetic (see
// LabelAt) so a cut's notice and a label's name cannot drift a frame apart.
//
// A linear scan for the reason LabelAt scans: a recording carries a handful
// of pauses, not thousands, so the search cost is irrelevant here.
func (t Timeline) CutAt(i int, transition time.Duration) (index int, weight float64) {
	for idx, c := range t.cuts {
		if i < c.FirstFrame || i > c.LastFrame {
			continue
		}
		return idx, rampWeight(i, c.FirstFrame, c.LastFrame-c.FirstFrame+1, t.fps, transition)
	}
	return NoCut, 0
}

// IntervalAt reports which highlight, if any, frame i belongs to, and how
// far through its entrance or exit transition that frame sits.
//
// index is NoHighlight outside every highlight, or a highlight's own
// position in the slice this Timeline was built from by
// NewTimelineForActivityWithHighlights -- which is exactly Context.Highlights,
// so a Painter that captured that slice in Prepare can look a name back up
// by this index without Timeline ever handing out a Highlight itself.
//
// weight ramps 0->1 across the first `transition` of VIDEO time inside the
// highlight, holds at 1 through the body, and ramps 1->0 across the last
// `transition`. Video time, not activity time: the ramp is a perceptual
// effect, and --highlight-transition promises it looks the same however
// compressed the render is. Every frame is exactly 1/FPS of video time from
// its neighbour regardless of which segment it falls in, so that part of the
// arithmetic needs no segment lookup of its own -- only which segment i
// belongs to does. transition <= 0 is a hard cut: weight is 1 for every
// frame inside the highlight and 0 everywhere else.
func (t Timeline) IntervalAt(i int, transition time.Duration) (index int, weight float64) {
	if i < 0 || i >= t.frames {
		return NoHighlight, 0
	}
	seg := t.segmentFor(i)
	if seg.highlight == NoHighlight {
		return NoHighlight, 0
	}
	return seg.highlight, rampWeight(i, seg.i0, seg.n, t.fps, transition)
}

// rampWeight is the 0->1->0 transition arithmetic IntervalAt applies to a
// highlight and LabelAt applies to a label, pulled out into one place so the
// two ramps cannot drift a frame apart by drifting into two hand-maintained
// copies of the same five lines -- exactly the duplicated-primitive shape
// this project's own review process exists to catch.
//
// i0 and n are the owning segment's (or label's) own first frame and frame
// count; see IntervalAt's own doc comment for what the ramp means, why it
// runs in VIDEO time via fps rather than activity time, and why transition
// <= 0 is a hard cut: weight is 1 for every frame in [i0, i0+n) and neither
// caller reaches this function for a frame outside that range at all.
func rampWeight(i, i0, n int, fps float64, transition time.Duration) float64 {
	if transition <= 0 {
		return 1
	}

	trans := transition.Seconds()
	since := float64(i-i0) / fps
	until := float64(i0+n-i) / fps
	w := since / trans
	if u := until / trans; u < w {
		w = u
	}
	if w > 1 {
		w = 1
	} else if w < 0 {
		w = 0
	}
	return w
}

// Smoothing is how much a gauge reading is averaged over, deferred rather
// than resolved to a single duration up front -- see WindowAt.
//
// One type, not a Window time.Duration alongside a separate SmoothingAuto
// bool: that shape is a second declaration free to disagree with the first,
// the same trap the Panel contract's Accepts/Prepare split exists to avoid
// at a different seam.
type Smoothing struct {
	// Auto scales the window with the frame's own segment rate; see
	// WindowAt and Timeline.AutoSmoothingAt. An explicit Window is never
	// reinterpreted per segment -- a user who typed --smoothing 30s gets
	// thirty seconds everywhere, which is what the flag says.
	Auto bool

	// Window is the smoothing window in ACTIVITY time, used as given when
	// Auto is false. Zero means off: the reading shown is exactly what was
	// recorded.
	Window time.Duration
}

// WindowAt resolves the smoothing window for frame i of tl -- Auto scales
// per that frame's own segment, an explicit Window applies everywhere
// unchanged.
func (s Smoothing) WindowAt(tl Timeline, i int) time.Duration {
	if s.Auto {
		return tl.AutoSmoothingAt(i)
	}
	return s.Window
}
