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
	start   time.Time
	fps     float64
	speedup float64
	n       int
}

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
func NewTimeline(start time.Time, d time.Duration, fps, speedup float64) (Timeline, error) {
	if start.IsZero() {
		return Timeline{}, fmt.Errorf("panel: timeline has no start instant")
	}
	if d <= 0 {
		return Timeline{}, fmt.Errorf("panel: timeline duration must be positive, got %v", d)
	}
	if fps <= 0 || math.IsInf(fps, 0) || math.IsNaN(fps) {
		return Timeline{}, fmt.Errorf("panel: fps must be a positive finite number, got %v", fps)
	}
	if speedup <= 0 || math.IsInf(speedup, 0) || math.IsNaN(speedup) {
		return Timeline{}, fmt.Errorf("panel: speedup must be a positive finite number, got %v", speedup)
	}
	n := int(math.Round(d.Seconds() / speedup * fps))
	if n < 1 {
		n = 1
	}
	return Timeline{start: start, fps: fps, speedup: speedup, n: n}, nil
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
	return NewTimeline(start, d, fps, speedup)
}

// Frames is the number of frames in the render.
func (t Timeline) Frames() int { return t.n }

// FPS is the frame rate, which the encoder must be given the same value of --
// the container's timestamps and the dashboard's own clock have to derive from
// one number or the video drifts against the readout burned into it.
func (t Timeline) FPS() float64 { return t.fps }

// Speedup is how much the activity is compressed into the video: 1 is real
// time, 60 turns an hour into a minute.
func (t Timeline) Speedup() float64 { return t.speedup }

// ActivityDuration is how much of the ACTIVITY the frames span, as opposed to
// how long the video runs.
//
// The two are the same only at a speedup of 1, and reporting just one of them
// would be the confusing half: a user who asked for a three-minute video wants
// to see that they got three minutes AND that it still covers the whole
// four-hour ride.
func (t Timeline) ActivityDuration() time.Duration {
	return time.Duration(math.Round(float64(t.n) / t.fps * t.speedup * float64(time.Second)))
}

// Start is the instant frame 0 shows.
func (t Timeline) Start() time.Time { return t.start }

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
	offset := math.Round(float64(i) / t.fps * t.speedup * float64(time.Second))
	return t.start.Add(time.Duration(offset))
}

// IndexAt returns the frame showing the instant offset into the ACTIVITY,
// rounded to the nearest frame and clamped to the render.
//
// Activity time, not video time: a user asking for the frame at 12m30s means
// twelve and a half minutes into their run, which at a speedup of 10 is
// seventy-five seconds into the video. Interpreting the offset as video time
// would answer a question nobody asked.
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
	i := int(math.Round(offset.Seconds() / t.speedup * t.fps))
	if i < 0 {
		return 0
	}
	if i >= t.n {
		return t.n - 1
	}
	return i
}
