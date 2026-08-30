package cmd

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/wisborg/fitdash/internal/panel"
)

// defaultLabelVideo is how long a label's name stays on screen when
// --label gave no video= of its own.
//
// Three seconds is a judgement, not a derivation, in the same voice as
// highlightWashStrength and autoSmoothingVideoWindow: long enough to be
// read once at a comfortable, unhurried pace, short enough that a handful
// of labels do not eat a large fraction of a 30-60 second video. --label's
// own video= exists for whoever wants a different answer for one label.
const defaultLabelVideo = 3 * time.Second

// parseLabel parses one --label value into a panel.Label.
//
// Grammar: comma-separated key=value fields, sharing cmd/fields.go's lexer
// with --highlight -- see parseHighlight's own doc comment for the two
// escapes and the first-"=" rule, which apply here unchanged. at and name
// are both required: at is a Go duration into the activity's ELAPSED time,
// the same clock --highlight from/to use; name is free text, and -- unlike
// Highlight.Name -- required, because a label with no name draws nothing at
// all, where a highlight with no name still lights a block. video is
// optional, defaults to defaultLabelVideo, and is VIDEO time -- the same
// clock --video-duration and --highlight video= already use -- naming how
// long the name stays on screen starting AT the instant, not centred on
// it.
//
// There is no for= key: "for" does not itself name a clock, and a user
// reading at=12m30s,for=5s would reasonably assume both fields share one.
// There is also no per-label style or transition knob -- see
// writeLabelSummary's own doc comment for why --highlight-transition
// governs a label's fade too.
//
// Bounds against the activity and against other labels -- at at or past
// the activity's end, two labels colliding in video time -- are NOT
// checked here, for the same reason parseHighlight does not check them:
// this function sees one value in isolation. See resolveLabels.
func parseLabel(raw string) (panel.Label, error) {
	const flag = "--label"
	fields, err := parseFields(raw, flag)
	if err != nil {
		return panel.Label{}, err
	}

	l := panel.Label{Video: defaultLabelVideo}
	var hasAt bool
	for key, value := range fields {
		switch key {
		case "at":
			d, err := parseDurationField(flag, raw, key, value)
			if err != nil {
				return panel.Label{}, err
			}
			l.At, hasAt = d, true
		case "name":
			l.Name = value
		case "video":
			d, err := parseDurationField(flag, raw, key, value)
			if err != nil {
				return panel.Label{}, err
			}
			if d <= 0 {
				return panel.Label{}, fmt.Errorf("render: --label %q: video must be positive, got %v", raw, d)
			}
			l.Video = d
		default:
			return panel.Label{}, fmt.Errorf("render: --label %q: unknown field %q", raw, key)
		}
	}

	if !hasAt || l.Name == "" {
		return panel.Label{}, fmt.Errorf("render: --label %q: at and name are both required; a nameless label draws nothing at all", raw)
	}
	if l.At < 0 {
		return panel.Label{}, fmt.Errorf("render: --label %q: at %v is negative", raw, l.At)
	}
	return l, nil
}

// resolveLabels parses every --label value and resolves the whole set
// against the render's own Timeline: sorting by at ascending, refusing two
// labels at the same instant as a duplicate, resolving each label's own
// FirstFrame/LastFrame, truncating an earlier label's on-screen span to end
// where the next begins whenever the two would otherwise overlap, and
// clamping the LAST label's own span to the render's final frame when its
// video= would otherwise run past it.
//
// tl, not the raw *fitactivity.TimerModel: the overlap this function checks
// for is in VIDEO time -- a label's video= names how long its name stays on
// screen, and two labels can collide there even when the activity-time
// instants they name (at) do not, once any compression or highlight pacing
// is in play. Video time only exists once the render's Timeline is built --
// which is why runRender resolves labels AFTER building tl, rather than
// beside resolveHighlights as the surface symmetry of the two flags would
// suggest: at the point highlights are resolved there is no Timeline yet to
// convert an activity-time at into a frame, let alone to compare two
// labels' frame ranges against each other.
//
// Returns nil, nil when raw is empty: no --label given at all is not an
// error.
func resolveLabels(raw []string, tl panel.Timeline) ([]panel.Label, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	labels := make([]panel.Label, 0, len(raw))
	for _, r := range raw {
		l, err := parseLabel(r)
		if err != nil {
			return nil, err
		}
		// Mirrors resolveHighlights' identical refusal for --highlight
		// from: there is nothing sensible to clip an out-of-range instant
		// TO, unlike a "to" running past the end, which has an obvious
		// target to clip to.
		if l.At >= tl.ActivityDuration() {
			return nil, fmt.Errorf("render: --label %q: at %v is at or past the activity's end, which runs %s",
				r, l.At, panel.FormatClock(tl.ActivityDuration()))
		}
		labels = append(labels, l)
	}

	sort.Slice(labels, func(i, j int) bool { return labels[i].At < labels[j].At })

	for i := 1; i < len(labels); i++ {
		if labels[i].At == labels[i-1].At {
			return nil, fmt.Errorf("render: two labels are both at %v (%s and %s); two labels at the same instant is a typo, not a range to compose",
				labels[i].At, labelSummaryName(labels[i-1]), labelSummaryName(labels[i]))
		}
	}

	for i := range labels {
		labels[i].FirstFrame = tl.IndexAt(labels[i].At)
		frames := framesForVideoDuration(labels[i].Video, tl.FPS())
		labels[i].LastFrame = labels[i].FirstFrame + frames - 1
	}

	// Truncate an earlier label's on-screen span to end where the next
	// begins, rather than refusing the overlap the way resolveHighlights
	// refuses two overlapping highlights. The two rules differ on purpose
	// -- see writeLabelSummary's own doc comment for the reasoning, kept
	// there rather than here so it sits beside the warning line it
	// explains.
	for i := 0; i < len(labels)-1; i++ {
		if labels[i].LastFrame < labels[i+1].FirstFrame {
			continue
		}
		last := labels[i+1].FirstFrame - 1
		if last < labels[i].FirstFrame {
			// Floored to one frame, matching newTimelineFromSegments'
			// identical floor for a highlight whose own rate rounds its
			// frame count to zero: a label silently occupying no video at
			// all is the same class of failure as a highlight that does,
			// and both are reported rather than merely tolerated.
			last = labels[i].FirstFrame
		}
		labels[i].LastFrame = last
		labels[i].Video = time.Duration(math.Round(float64(labels[i].LastFrame-labels[i].FirstFrame+1) / tl.FPS() * float64(time.Second)))
		labels[i].Truncated = true
	}

	// Only the LAST label in this (now At-sorted) list can still run past
	// the render's own final frame. Every earlier label's LastFrame is
	// bounded above by the next label's own FirstFrame in the loop just
	// above, and every FirstFrame comes from tl.IndexAt, which already
	// clamps to [0, tl.Frames()-1] -- so no non-last label can ever exceed
	// the render's own total. The last one has no following label to bound
	// it, and a label placed close enough to the activity's end (its own
	// `at` need only be BEFORE the end, not its whole on-screen video=) can
	// still compute a LastFrame the render never reaches: at the default
	// video= of three seconds, a label inside the last couple of seconds of
	// a compressed render is enough to trigger this.
	//
	// Left unclamped, this is two distinct failures wearing one bug: the
	// PNG sink refuses to write a frame index past the render's own total
	// (see frameIndices and labelLandmarkFrames, which read this label's
	// bounds back verbatim), so --frames fails outright; and even where
	// nothing reads the landmark, the summary would go on reporting the
	// video= that was asked for rather than the shorter span the label
	// actually got, silently repeating the exact lie writeLabelSummary's
	// opacity warning exists to prevent for the transition ramp.
	//
	// Truncated is reused rather than a second field: this and the overlap
	// case above are mutually exclusive by construction (the overlap loop
	// never touches the last index, and this block touches nothing else),
	// so writeLabelSummary can tell which happened from the label's
	// POSITION in the slice alone, with no extra state to carry.
	if n := len(labels); n > 0 {
		last := &labels[n-1]
		if final := tl.Frames() - 1; last.LastFrame > final {
			last.LastFrame = final
			last.Video = time.Duration(math.Round(float64(last.LastFrame-last.FirstFrame+1) / tl.FPS() * float64(time.Second)))
			last.Truncated = true
		}
	}

	return labels, nil
}

// framesForVideoDuration is how many frames a VIDEO-time duration occupies
// at fps, floored at 1 for the same reason newTimelineFromSegments floors a
// highlight segment's own frame count: a label silently occupying no video
// at all is a worse failure than one occupying a single frame it did not
// quite earn.
//
// Every frame is exactly 1/fps of video time from its neighbour regardless
// of which Timeline segment it falls in (see Timeline.IntervalAt's own doc
// comment), so converting a video-time duration to a frame count is a
// division, not a segment lookup -- the same arithmetic frameIndices
// already applies to --frame-at-video.
func framesForVideoDuration(d time.Duration, fps float64) int {
	n := int(math.Round(d.Seconds() * fps))
	if n < 1 {
		n = 1
	}
	return n
}

// labelSummaryName quotes a label's name for an error or summary line.
// Unlike highlightSummaryName, a label always has one -- parseLabel refuses
// a nameless label outright -- so there is no fallback-to-bounds case to
// handle.
func labelSummaryName(l panel.Label) string {
	return strconv.Quote(l.Name)
}

// labelLandmarkFrames returns l's own first and last frame, so --frames
// always samples both ends of a label's fade even when no --frame-at-video
// was given -- the label analogue of highlightLandmarkFrames.
//
// Read straight from the already-resolved FirstFrame/LastFrame rather than
// re-derived from At and Video: resolveLabels is the one place that
// arithmetic runs (including the truncation that can shorten LastFrame),
// and re-deriving it here would risk landmark frames disagreeing with the
// span writeLabelSummary reports.
func labelLandmarkFrames(l panel.Label) []int {
	if l.LastFrame == l.FirstFrame {
		return []int{l.FirstFrame}
	}
	return []int{l.FirstFrame, l.LastFrame}
}
