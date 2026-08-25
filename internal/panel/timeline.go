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
	"time"

	"github.com/wisborg/fitactivity"
)

// Timeline maps a frame index to the instant in the activity that frame shows.
//
// The map is affine: At(i) is Start + i/FPS, full stop. That is a deliberate
// choice with consequences well beyond this type, and it is the reason fitdash
// renders on ELAPSED time rather than moving time.
//
// Cutting an activity's pauses out of the video would make this map a search
// through the pause list instead of arithmetic, and would make At non-affine
// in i. Every panel that derives anything from a frame index would then need
// the pause list too, and seeking to an arbitrary instant -- which is what
// --frame-at does -- would become a lookup rather than a division. The
// rendering consequence is worse: splicing two instants together teleports the
// route dot across whatever ground was covered while the watch was stopped,
// which looks exactly like the GPS glitch this project spends its care
// avoiding. A dashboard that freezes through a pause is telling the truth
// about what the recording says.
//
// Moving time, if it is ever wanted, is a second implementation of this type
// and nothing else changes -- not a panel, not the frame loop, not the
// encoder. See docs/architecture.md.
type Timeline struct {
	start time.Time
	fps   float64
	n     int
}

// NewTimeline lays n frames over d at fps, beginning at start.
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
func NewTimeline(start time.Time, d time.Duration, fps float64) (Timeline, error) {
	if start.IsZero() {
		return Timeline{}, fmt.Errorf("panel: timeline has no start instant")
	}
	if d <= 0 {
		return Timeline{}, fmt.Errorf("panel: timeline duration must be positive, got %v", d)
	}
	if fps <= 0 || math.IsInf(fps, 0) || math.IsNaN(fps) {
		return Timeline{}, fmt.Errorf("panel: fps must be a positive finite number, got %v", fps)
	}
	n := int(math.Round(d.Seconds() * fps))
	if n < 1 {
		n = 1
	}
	return Timeline{start: start, fps: fps, n: n}, nil
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
func NewTimelineForActivity(timer *fitactivity.TimerModel, fps float64) (Timeline, error) {
	if timer == nil {
		return Timeline{}, fmt.Errorf("panel: no timer model")
	}
	start, end := timer.Window()
	if start.IsZero() || end.IsZero() {
		return Timeline{}, fmt.Errorf("panel: activity has no resolved time window; the file carried neither session timing nor samples")
	}
	d := end.Sub(start)
	if d <= 0 {
		return Timeline{}, fmt.Errorf("panel: activity spans %v; there is nothing to render", d)
	}
	return NewTimeline(start, d, fps)
}

// Frames is the number of frames in the render.
func (t Timeline) Frames() int { return t.n }

// FPS is the frame rate, which the encoder must be given the same value of --
// the container's timestamps and the dashboard's own clock have to derive from
// one number or the video drifts against the readout burned into it.
func (t Timeline) FPS() float64 { return t.fps }

// Start is the instant frame 0 shows.
func (t Timeline) Start() time.Time { return t.start }

// Duration is the span the frames cover: Frames()/FPS, which is what the
// encoded video's own duration will be.
//
// This is NOT necessarily the duration NewTimeline was given. Rounding the
// frame count to a whole number of frames can move it by up to half a frame,
// and this reports what was actually laid down rather than what was asked for
// -- a caller checking the output video's length wants the number the output
// was built from.
func (t Timeline) Duration() time.Duration {
	return time.Duration(math.Round(float64(t.n) / t.fps * float64(time.Second)))
}

// At returns the instant frame i shows.
//
// Defined for every i, not only for 0 <= i < Frames(): it is an affine map,
// and a caller computing a lookahead or a lookbehind instant should get the
// arithmetic rather than a special case. Indices outside the render simply
// name instants outside it.
//
// The offset is rounded to the nearest nanosecond rather than truncated, so
// the error against the exact instant stays bounded instead of accumulating
// with i.
func (t Timeline) At(i int) time.Time {
	offset := math.Round(float64(i) / t.fps * float64(time.Second))
	return t.start.Add(time.Duration(offset))
}

// IndexAt returns the frame showing the instant offset into the activity,
// rounded to the nearest frame and clamped to the render.
//
// This is At's inverse, and having it is most of the reason At is affine: it
// is what lets a caller ask for "the frame at 12m30s" -- which is how a render
// is inspected inside a GPS dropout or a paused stretch without rendering
// everything up to it -- with a division rather than a search.
//
// Clamping rather than erroring is deliberate here and is the opposite of what
// the PNG sink does with an unreachable frame index. The distinction: an
// offset past the end of the activity has an obvious intended meaning, the
// last frame, whereas a raw frame index past the end of the render is a
// request the caller must have computed wrongly. A caller who needs to know
// the offset was out of range can compare against Duration.
func (t Timeline) IndexAt(offset time.Duration) int {
	i := int(math.Round(offset.Seconds() * t.fps))
	if i < 0 {
		return 0
	}
	if i >= t.n {
		return t.n - 1
	}
	return i
}
