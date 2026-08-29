package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/encode"
	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/progress"
	"github.com/wisborg/fitdash/internal/render"
)

type renderOptions struct {
	outputDir           string
	output              string
	size                string
	fps                 float64
	crf                 int
	frames              bool
	frameAt             []time.Duration
	frameAtVideo        []time.Duration
	quiet               bool
	power               string
	layout              string
	theme               string
	smoothing           string
	speedup             float64
	videoDur            time.Duration
	highlights          []string
	highlightStyle      string
	highlightTransition time.Duration
}

// validateRenderOptions rejects flag combinations that cannot mean what they
// appear to, before any work is done.
//
// Separate from runRender so it can be tested without an activity file. Both
// checks here were review findings: each was a flag doing something other than
// what its own help text said, silently.
func validateRenderOptions() error {
	// -o names a FILE, and --frames writes several. Reinterpreting it as a
	// directory made `-o preview.png --frames` create a DIRECTORY called
	// preview.png with frame-000000.png inside it, which is not a reading of
	// the flag anyone intended.
	if renderOpts.frames && renderOpts.output != "" {
		return fmt.Errorf("render: -o names a single file and --frames writes several; use --output-dir")
	}
	// 0 is x264's spelling of lossless and this program's spelling of "unset",
	// and it cannot be both. Passing it through silently produced the default
	// quality instead, with nothing reported.
	if renderOpts.crf == 0 {
		return fmt.Errorf("render: --crf 0 is x264's lossless, which this program's zero value already means \"unset\"; use 1 for near-lossless")
	}
	return nil
}

// powerSources maps the --power-source flag values to their enum.
//
// The vocabulary -- the flag name, the three values, and what each means -- is
// deliberately identical to videofx's. The two programs read the same files
// through the same library, and a user who has learned "--power-source stryd"
// in one should not have to learn a different spelling of the same idea in the
// other.
var powerSources = map[string]fitactivity.PowerSource{
	"auto":   fitactivity.PowerAuto,
	"stryd":  fitactivity.PowerStryd,
	"native": fitactivity.PowerNative,
}

// parsePowerSource maps a --power-source value to its enum, rejecting an
// unknown one where the user typed it rather than falling back to a default.
//
// Falling back would be the worse failure: a typo would silently render the
// activity against a different sensor, and the two disagree by enough to be
// mistaken for a bad workout rather than a bad flag.
func parsePowerSource(mode string) (fitactivity.PowerSource, error) {
	src, ok := powerSources[mode]
	if !ok {
		return 0, fmt.Errorf("render: --power-source %q is invalid; use auto, stryd, or native", mode)
	}
	return src, nil
}

var renderOpts renderOptions

// bindRenderFlags attaches the render flags to the root command.
func bindRenderFlags(c *cobra.Command) {
	f := c.Flags()
	f.StringVar(&renderOpts.outputDir, "output-dir", ".", "directory for the rendered video; the name is derived from the activity")
	f.StringVarP(&renderOpts.output, "output", "o", "", "write to this exact path instead of deriving one")
	f.StringVar(&renderOpts.size, "size", "1920x1080", "output resolution; width and height must both be even")
	f.Float64Var(&renderOpts.fps, "fps", 30, "frames per second")
	f.IntVar(&renderOpts.crf, "crf", encode.DefaultCRF,
		"H.264 quality; lower is better (18-28 is the useful range). 0 is x264's lossless and is not offered: "+
			"0 is this program's \"unset\" value internally, so it would silently mean the default. Use 1 for near-lossless")
	f.BoolVar(&renderOpts.frames, "frames", false, "write landmark frames as PNG instead of encoding a video")
	f.DurationSliceVar(&renderOpts.frameAt, "frame-at", nil, "with --frames, also write the frame at this offset into the activity (repeatable, e.g. 12m30s)")
	f.DurationSliceVar(&renderOpts.frameAtVideo, "frame-at-video", nil, "with --frames, also write the frame at this offset into the rendered VIDEO's own clock, rather than the activity's (repeatable, e.g. 9.8s) -- "+
		"what checking a highlight's entrance or exit transition needs, since --frame-at cannot land a frame a fixed distance from a boundary once the render runs at more than one rate")
	f.BoolVar(&renderOpts.quiet, "quiet", false, "suppress the progress line and the summary")
	f.StringVar(&renderOpts.layout, "layout", panel.LayoutAuto,
		"panel arrangement -- \"auto\" (default: a column for a portrait frame, a row-based one otherwise), "+
			"\"landscape\", or \"portrait\". Naming one overrides the frame's shape, which is occasionally what you want "+
			"and usually not")
	f.StringVar(&renderOpts.theme, "theme", panel.DefaultTheme().Name,
		"colour palette -- \"dark\" (default) or \"light\"")
	f.StringVar(&renderOpts.smoothing, "smoothing", smoothingAuto,
		"average the gauge readings -- heart rate, pace, power, cadence -- over this much ACTIVITY time, "+
			"so they can be read when the activity is compressed. \"auto\" (default) scales with the compression and "+
			"comes out to nothing at real time; \"off\" shows what was recorded; or a duration such as 30s. "+
			"Position, distance and elevation are never averaged")
	f.Float64Var(&renderOpts.speedup, "speedup", 1,
		"compress the activity into a shorter video: 60 turns an hour of activity into a minute of video. "+
			"The dashboard still reads ACTIVITY time, so its clock advances that much faster. Mutually exclusive with --video-duration")
	f.DurationVar(&renderOpts.videoDur, "video-duration", 0,
		"compress the activity into a video of this BASE length (e.g. 3m), whatever speedup that takes -- the "+
			"length before any --highlight is added, since a highlight adds its own video time on top rather than "+
			"squeezing the rest of the render to make room for it (see the summary's own decomposition line when "+
			"highlights are configured). Mutually exclusive with --speedup")
	f.StringVar(&renderOpts.power, "power-source", "auto",
		"which power reading to show when the activity carries both a footpod (Stryd) developer field and the standard FIT power field -- "+
			"\"auto\" (default: prefer Stryd, fall back to native), \"stryd\" (force the footpod's developer field), or "+
			"\"native\" (force the standard FIT power field). The two can disagree since they are different sensors, and a forced "+
			"source that is absent shows a placeholder rather than the other sensor's number")
	f.StringArrayVar(&renderOpts.highlights, "highlight", nil,
		"mark a stretch of the activity for its own on-screen pace and treatment (repeatable). Comma-separated "+
			"key=value fields: from=DURATION and to=DURATION (both required, Go duration syntax into the activity's "+
			"ELAPSED time, e.g. 12m30s), name=TEXT (optional, free text; a literal comma needs \\, and a literal "+
			"backslash needs \\\\), and video=DURATION or speedup=N (optional, mutually exclusive -- how much video "+
			"this stretch should take, or its own compression factor; neither means mark it without re-pacing it). "+
			"Example: --highlight 'from=12m30s,to=16m10s,video=10s,name=Hill climb'")
	f.StringVar(&renderOpts.highlightStyle, "highlight-style", panel.HighlightStyleBorder,
		"how a highlight is marked on screen -- \"border\" (default: an accent border in the frame's margin, plus "+
			"the highlight strip), \"wash\" (additionally tints the whole background -- costs a second static base "+
			"and a full-frame blend per frame), or \"none\" (re-pace only, with the highlight strip still marking "+
			"where the highlights are)")
	f.DurationVar(&renderOpts.highlightTransition, "highlight-transition", 400*time.Millisecond,
		"how much VIDEO time a highlight's on-screen mark takes to appear and to disappear at each end -- video "+
			"time, not activity time, because this is a perceptual ramp and should look the same however compressed "+
			"the render is. 0 makes it a hard cut")
}

// runRender is the root command: fitdash ACTIVITY.fit.
func runRender(cmd *cobra.Command, args []string) error {
	if err := validateRenderOptions(); err != nil {
		return err
	}
	w, h, err := parseSize(renderOpts.size)
	if err != nil {
		return err
	}

	powerSrc, err := parsePowerSource(renderOpts.power)
	if err != nil {
		return err
	}

	activity := args[0]
	track, err := fitactivity.Decode(activity)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	timer := fitactivity.BuildTimerModel(track)
	speedup, err := resolveSpeedup(cmd, timer)
	if err != nil {
		return err
	}
	highlights, err := resolveHighlights(renderOpts.highlights, timer)
	if err != nil {
		return err
	}
	highlightStyle, err := parseHighlightStyle(renderOpts.highlightStyle)
	if err != nil {
		return err
	}
	if renderOpts.highlightTransition < 0 {
		return fmt.Errorf("render: --highlight-transition %v is negative", renderOpts.highlightTransition)
	}
	// Always the highlighted constructor, even with no highlights: with an
	// empty slice it is byte-identical to NewTimelineForActivity, which is
	// the whole point of funnelling both through one segment builder rather
	// than branching here on whether any were configured.
	timeline, err := panel.NewTimelineForActivityWithHighlights(timer, renderOpts.fps, speedup, highlights)
	if err != nil {
		return err
	}
	smoothing, err := resolveSmoothing()
	if err != nil {
		return err
	}
	fonts, err := panel.NewFaceCache()
	if err != nil {
		return err
	}

	layout, err := panel.SelectLayout(renderOpts.layout, w, h)
	if err != nil {
		return err
	}
	theme, err := panel.SelectTheme(renderOpts.theme)
	if err != nil {
		return err
	}
	rctx := &panel.Context{
		Track:               track,
		Report:              inspect.Build(track),
		Timer:               timer,
		Timeline:            timeline,
		Width:               w,
		Height:              h,
		FontScale:           layout.FontScale,
		Fonts:               fonts,
		PowerSource:         powerSrc,
		Smoothing:           smoothing,
		Highlights:          highlights,
		HighlightStyle:      highlightStyle,
		HighlightTransition: renderOpts.highlightTransition,
	}
	r, err := render.New(rctx, layout, theme)
	if err != nil {
		return err
	}

	if renderOpts.frames {
		return runFrames(cmd, r, timeline, activity, layout.Name, theme.Name, smoothing, highlights)
	}
	return runVideo(cmd, r, timeline, activity, w, h, layout.Name, theme.Name, smoothing, highlights)
}

// runVideo encodes the whole render to a video file.
func runVideo(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, activity string, w, h int, layoutName, themeName string, smoothing panel.Smoothing, highlights []panel.Highlight) error {
	out, err := outputPath(activity, renderOpts.output, renderOpts.outputDir, ".mp4")
	if err != nil {
		return err
	}

	sink, err := encode.OpenVideo(cmd.Context(), encode.Config{
		OutputPath: out, Width: w, Height: h, FPS: tl.FPS(), CRF: renderOpts.crf,
	})
	if err != nil {
		return err
	}
	// Deferred for the error paths, then closed explicitly below where its
	// error matters: ffmpeg reports a failure at exit rather than at write, so
	// Close is the encode's own verdict. Both happen at most once.
	defer sink.Close()

	prog := newReporter(cmd, r.Frames())
	if err := render.Run(cmd.Context(), r, sink, func(i, n int) { prog.Update(i) }); err != nil {
		prog.Done()
		return err
	}
	prog.Done()
	if err := sink.Close(); err != nil {
		return err
	}

	// The path goes to stdout so it can be piped; everything else is
	// commentary and goes to stderr.
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out)
	writeRenderSummary(cmd, r, tl, w, h, layoutName, themeName, smoothing, highlights)
	return nil
}

// smoothingAuto names the window derived from the compression.
const smoothingAuto = "auto"

// resolveSmoothing turns --smoothing into a panel.Smoothing.
//
// "off" is spelled out rather than left to "0s", which parses as a duration and
// would work -- but a user reaching for a way to disable this should not have
// to guess that zero is it, and "off" in the summary reads better than "0s".
//
// The "auto" branch no longer resolves a number here: once a render can
// carry more than one rate, "auto" means "scale with whichever segment a
// frame falls in", which Timeline.AutoSmoothingAt answers per frame rather
// than this function answering once for the whole render. An EXPLICIT window
// is not deferred -- a user who typed --smoothing 30s gets thirty seconds
// everywhere, which is what the flag says.
func resolveSmoothing() (panel.Smoothing, error) {
	switch renderOpts.smoothing {
	case smoothingAuto, "":
		return panel.Smoothing{Auto: true}, nil
	case "off":
		return panel.Smoothing{}, nil
	}
	d, err := time.ParseDuration(renderOpts.smoothing)
	if err != nil {
		return panel.Smoothing{}, fmt.Errorf("render: --smoothing %q is not %s, off, or a duration such as 30s",
			renderOpts.smoothing, smoothingAuto)
	}
	if d < 0 {
		return panel.Smoothing{}, fmt.Errorf("render: --smoothing %v is negative", d)
	}
	return panel.Smoothing{Window: d}, nil
}

// resolveSpeedup turns --speedup or --video-duration into one compression
// factor.
//
// The two are mutually exclusive rather than one overriding the other. They
// express the same intention in opposite directions, and silently preferring
// whichever the code checks first would let a user pass both and get a video
// of a length they did not ask for, with nothing saying which flag won.
func resolveSpeedup(cmd *cobra.Command, timer *fitactivity.TimerModel) (float64, error) {
	// Ask cobra whether the flag was TYPED, rather than comparing against its
	// default. Comparing meant `--speedup 1 --video-duration 3m` slipped past
	// the exclusivity check and silently took the duration branch -- exactly
	// the "pass both and get a length you did not ask for, with nothing saying
	// which won" failure this function exists to prevent.
	explicitSpeedup := cmd.Flags().Changed("speedup")
	explicitDuration := cmd.Flags().Changed("video-duration")

	switch {
	case explicitSpeedup && explicitDuration:
		return 0, fmt.Errorf("render: --speedup and --video-duration both set; they are two ways to say the same thing, so pass one")
	case explicitDuration:
		if renderOpts.videoDur <= 0 {
			return 0, fmt.Errorf("render: --video-duration must be positive, got %v", renderOpts.videoDur)
		}
		start, end := timer.Window()
		activity := end.Sub(start)
		if activity <= 0 {
			return 0, fmt.Errorf("render: the activity has no duration to compress")
		}
		return panel.SpeedupFor(activity, renderOpts.videoDur), nil
	default:
		if renderOpts.speedup <= 0 {
			return 0, fmt.Errorf("render: --speedup must be positive, got %v", renderOpts.speedup)
		}
		return renderOpts.speedup, nil
	}
}

// newReporter builds the progress reporter, or nil under --quiet.
//
// A nil *progress.Reporter is safe to call, which is why --quiet is one
// decision here rather than a condition at every call site.
func newReporter(cmd *cobra.Command, total int) *progress.Reporter {
	if renderOpts.quiet {
		return nil
	}
	w := cmd.ErrOrStderr()
	inline := false
	if f, ok := w.(*os.File); ok {
		inline = progress.IsTerminal(f)
	}
	return progress.New(w, total, inline)
}

// formatMultiplier renders a compression factor for display, rounded to two
// decimals and trimmed to none when it lands on a whole number. Shared by
// speedupNote and the highlight table (see writeHighlightSummary) so a
// render's base rate and a highlight's own are spelled the same way.
//
// A --video-duration of three minutes over a four-hour activity works out at
// 79.99444444444444, and printing that is not more accurate, only harder to
// read -- the exact figure is a consequence of the duration the user asked
// for, which is the number they actually care about and which is printed
// beside it.
func formatMultiplier(s float64) string {
	rounded := math.Round(s*100) / 100
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// speedupNote renders the compression, or nothing at all in real time -- where
// saying "(1x)" would draw attention to a fact the two equal durations beside
// it already state.
func speedupNote(s float64) string {
	if s == 1 {
		return ""
	}
	return fmt.Sprintf(" (%sx)", formatMultiplier(s))
}

// baseSpeedupNote is speedupNote, made highlight-aware.
//
// A bare "(60x)" beside a video that runs at three different rates would be
// exactly the confident lie speedupNote's own doc comment describes avoiding
// at real time -- so once any highlight is configured, the base figure is
// named explicitly, even at a base of 1x: a highlight can still run its own
// rate while the rest of the render is real time, and an unlabelled render
// would claim the whole video was.
func baseSpeedupNote(s float64, hasHighlights bool) string {
	if !hasHighlights {
		return speedupNote(s)
	}
	return fmt.Sprintf(" (base %sx)", formatMultiplier(s))
}

// writeRenderSummary reports what was rendered, and -- crucially -- which panels
// were left out and why.
//
// Naming the declined panels is not a nicety. A panel that vanishes because
// the activity carries nothing for it leaves "no unexplained holes" true in
// the pixels and false in the user's understanding of them: they see a
// dashboard with no power reading and have no way to tell whether their file
// lacks power, or fitdash does.
func writeRenderSummary(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, w, h int, layoutName, themeName string, smoothing panel.Smoothing, highlights []panel.Highlight) {
	if renderOpts.quiet {
		return
	}
	out := cmd.ErrOrStderr()
	// Both durations, always. A user who asked for a three-minute video wants
	// to see that they got three minutes AND that it still covers the whole
	// activity; printing one of them leaves the other to be guessed at.
	//
	// BaseSpeedup, not Speedup: once a render can carry more than one rate
	// there is no longer a single scalar speedup to report, and this prints
	// the whole-activity rate the render was asked for rather than any one
	// highlight's own -- labelled "base" once any highlight exists, via
	// baseSpeedupNote, with the full base-plus-highlights decomposition
	// following in writeHighlightSummary below.
	fmt.Fprintf(out, "%d frames, %s of activity in %s of video%s, %s fps, %dx%d\n",
		tl.Frames(), panel.FormatClock(tl.ActivityDuration()), panel.FormatClock(tl.Duration()),
		baseSpeedupNote(tl.BaseSpeedup(), len(highlights) > 0), strconv.FormatFloat(tl.FPS(), 'f', -1, 64), w, h)

	writePanelSummary(cmd, r, tl, layoutName, themeName, smoothing)
	writeHighlightSummary(cmd, tl, highlights, smoothing)
}

// writePanelSummary reports the arrangement and, crucially, which panels were
// left out and why.
//
// Shared by the video and --frames paths rather than written twice. It was
// written twice, and the copies drifted immediately: --frames reported the
// panels but not the layout or theme, so the one command whose whole purpose
// is checking how a render looks said least about how it had been configured.
// smoothingNote spells a window for the summary, so a reader can tell an
// averaged readout from a recorded one -- which the video itself does not say.
//
// "auto" alone, without a number, now that a render can carry more than one
// rate and therefore more than one auto window: this prints "auto (Ns base)",
// the window the render-wide base rate implies (Timeline.AutoSmoothingBase),
// and the per-highlight figures -- which can differ from the base once a
// highlight runs its own rate -- are printed beside each highlight in the
// table writeHighlightSummary builds, not restated here.
func smoothingNote(s panel.Smoothing, tl panel.Timeline) string {
	if s.Auto {
		base := tl.AutoSmoothingBase()
		if base <= 0 {
			return smoothingAuto + " (off at base)"
		}
		return fmt.Sprintf("%s (%s base)", smoothingAuto, base.Round(time.Second).String())
	}
	if s.Window <= 0 {
		return "off"
	}
	return s.Window.Round(time.Second).String()
}

// highlightPanelName is the highlight panel's own Name(), used below to split
// it out of the ordinary "carries no such data" decline heading. Read from
// the panel itself rather than restated as a string literal, so the two
// cannot drift apart the way a hand-copied name could.
var highlightPanelName = panel.HighlightPanel{}.Name()

func writePanelSummary(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, layoutName, themeName string, smoothing panel.Smoothing) {
	if renderOpts.quiet {
		return
	}
	out := cmd.ErrOrStderr()
	drew := make([]string, 0, len(r.Placed()))
	for _, p := range r.Placed() {
		drew = append(drew, p.Panel.Name())
	}
	fmt.Fprintf(out, "layout %s, theme %s, smoothing %s\n", layoutName, themeName, smoothingNote(smoothing, tl))
	fmt.Fprintf(out, "panels: %s\n", strings.Join(drew, ", "))

	// Two decline reasons, not one. The highlight panel declines because no
	// --highlight was given at all -- a fact about the FLAGS -- and printing
	// its name under "this activity carries no such data" would tell the
	// user their FIT file lacks something no FIT file has ever carried. Every
	// other panel's decline IS a fact about the activity, so it keeps the
	// original heading.
	var dataDeclined, configDeclined []string
	for _, name := range r.Declined() {
		if name == highlightPanelName {
			configDeclined = append(configDeclined, name)
			continue
		}
		dataDeclined = append(dataDeclined, name)
	}
	if len(dataDeclined) > 0 {
		fmt.Fprintf(out, "declined (this activity carries no such data): %s\n", strings.Join(dataDeclined, ", "))
	}
	if len(configDeclined) > 0 {
		fmt.Fprintf(out, "declined (no --highlight given): %s\n", strings.Join(configDeclined, ", "))
	}
}

// writeHighlightSummary reports the base/highlight decomposition, one line
// per highlight naming its own pace, and a warning for each of the three
// surprises resolveHighlights marks as "adjusted, and reported" rather than
// fatal: a clipped end, a highlight forced down to one frame, and one lying
// wholly inside a paused stretch.
//
// Printed only when at least one highlight was configured, so an ordinary
// render without --highlight prints none of this -- keeping its summary
// identical to one from before this feature existed, the same promise the
// highlight panel's own Accepts makes for the pixels.
//
// Also where each highlight's own auto-smoothing window is reported, via
// highlightSmoothingNote -- see smoothingNote's doc comment for why that
// figure moved out of the plain "smoothing auto" line once a render could
// carry more than one window.
func writeHighlightSummary(cmd *cobra.Command, tl panel.Timeline, highlights []panel.Highlight, smoothing panel.Smoothing) {
	if renderOpts.quiet || len(highlights) == 0 {
		return
	}
	out := cmd.ErrOrStderr()

	// The base figure is what the WHOLE activity would have rendered to at
	// the base rate alone, with no highlight re-pacing anything -- exactly
	// what NewTimeline(start, d, fps, BaseSpeedup) alone would have produced.
	// Everything Duration() runs beyond that is highlight time. Both are
	// rounded the same way Timeline.Duration itself rounds, so the three
	// figures are consistent with each other and with the line above them.
	base := time.Duration(math.Round(tl.ActivityDuration().Seconds() / tl.BaseSpeedup() * float64(time.Second)))
	extra := tl.Duration() - base
	fmt.Fprintf(out, "%s base + %s of highlights = %s of video\n",
		panel.FormatClock(base), panel.FormatClock(extra), panel.FormatClock(tl.Duration()))

	lines := make([]string, len(highlights))
	for i, h := range highlights {
		rate := h.Rate(tl.BaseSpeedup())
		video := float64(highlightFrames(tl, h)) / tl.FPS()
		lines[i] = fmt.Sprintf("%s %s -> %ss (%sx)%s",
			highlightSummaryName(h), panel.FormatClock(h.From), strconv.FormatFloat(video, 'f', 1, 64), formatMultiplier(rate),
			highlightSmoothingNote(tl, h, smoothing))
	}
	fmt.Fprintf(out, "highlights: %s\n", strings.Join(lines, ", "))

	for _, h := range highlights {
		if h.Clipped {
			fmt.Fprintf(out, "highlight %s clipped to the activity's end (%s)\n",
				highlightSummaryName(h), panel.FormatClock(h.To))
		}
		if highlightFrames(tl, h) == 1 {
			fmt.Fprintf(out, "highlight %s occupies one frame; its speedup is higher than the render can show\n",
				highlightSummaryName(h))
		}
		if h.PausedThroughout {
			fmt.Fprintf(out, "highlight %s lies inside a paused stretch; the dashboard is frozen through it\n",
				highlightSummaryName(h))
		}
	}
}

// highlightSmoothingNote appends this highlight's own auto-smoothing window
// to its line in the highlight table, or nothing when there is no per-
// highlight figure to add.
//
// Only --smoothing auto varies per highlight. An EXPLICIT --smoothing 30s
// stays global (resolveSmoothing never reinterprets it per segment), so it
// is already the one number writePanelSummary's "smoothing 30s" line
// reports and restating it five times over in this table would say nothing
// new. Auto is the case E in the plan exists for: a highlight running its
// own rate gets its own window (Timeline.AutoSmoothingAt), which is exactly
// the figure that can differ sharply from the base window smoothingNote
// prints above this table -- that difference is the whole reason the window
// went per-segment rather than staying one render-wide number.
func highlightSmoothingNote(tl panel.Timeline, h panel.Highlight, smoothing panel.Smoothing) string {
	if !smoothing.Auto {
		return ""
	}
	w := tl.AutoSmoothingAt(tl.IndexAt(h.From))
	if w <= 0 {
		return ", smoothing off"
	}
	return fmt.Sprintf(", smoothing %s", w.Round(time.Second).String())
}

// highlightFrames is how many frames h's own segment actually occupies in
// tl, read back from Timeline.IndexAt rather than re-derived from
// Highlight.Rate and the construction arithmetic newTimelineFromSegments
// applies internally.
//
// That second option would be a second copy of the rounding rule that
// decides a segment's frame count -- exactly the kind of independent
// recomputation this project's whole architecture exists to avoid, since a
// summary line that disagreed with what actually rendered would be the
// two-layers-drew-and-disagreed failure wearing text instead of pixels. Using
// IndexAt instead makes this summary read back whatever Timeline itself
// decided, however it decided it.
//
// h.To minus a nanosecond is the same half-open-interval trick
// highlightLiesInPause uses: To itself belongs to whatever comes after the
// highlight, so stepping back one nanosecond is what keeps the lookup inside
// the highlight's own segment.
func highlightFrames(tl panel.Timeline, h panel.Highlight) int {
	first := tl.IndexAt(h.From)
	last := tl.IndexAt(h.To - time.Nanosecond)
	if last < first {
		last = first
	}
	return last - first + 1
}

// highlightSummaryName names a highlight for the summary: its own name,
// quoted, or its clock bounds when it has none -- the block lighting up in
// the highlight strip is the honest statement that something is playing even
// without a name, so the summary owes the same courtesy rather than printing
// a placeholder that would claim a name was expected and missing.
func highlightSummaryName(h panel.Highlight) string {
	if h.Name != "" {
		return strconv.Quote(h.Name)
	}
	return fmt.Sprintf("%s-%s", panel.FormatClock(h.From), panel.FormatClock(h.To))
}

// runFrames writes selected frames as PNGs instead of encoding.
//
// It runs the IDENTICAL render path -- only the sink differs -- which is what
// makes the fast visual loop a trustworthy proxy for the real render rather
// than a second implementation free to disagree with it.
func runFrames(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, activity, layoutName, themeName string, smoothing panel.Smoothing, highlights []panel.Highlight) error {
	indices, err := frameIndices(tl, renderOpts.frameAt, renderOpts.frameAtVideo, highlights)
	if err != nil {
		return err
	}
	dir := renderOpts.outputDir

	sink, err := encode.OpenPNGFrames(dir, indices)
	if err != nil {
		return err
	}
	defer sink.Close()

	prog := newReporter(cmd, r.Frames())
	if err := render.Run(cmd.Context(), r, sink, func(i, n int) { prog.Update(i) }); err != nil {
		prog.Done()
		return err
	}
	prog.Done()
	if err := sink.Close(); err != nil {
		return err
	}
	for _, p := range sink.Written() {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", p)
	}
	writePanelSummary(cmd, r, tl, layoutName, themeName, smoothing)
	// The fast visual loop is where a transition actually gets looked at
	// (see --frame-at-video), so it gets the same base/highlight
	// decomposition and warnings the video path prints -- otherwise the one
	// command built for checking a highlight would say nothing about it.
	writeHighlightSummary(cmd, tl, highlights, smoothing)
	return nil
}

// frameIndices picks which frames --frames writes: the landmarks, plus
// anything --frame-at or --frame-at-video named.
//
// The landmarks are the first, the quarters, the last, and -- when any
// highlight is configured -- each one's own first and last frame. Frame 0 and
// the last frame are there because panels that accumulate -- a progress bar,
// a covered route, a splits list -- are wrong at the boundaries far more
// often than in the middle, and frame 0 in particular hits every "nothing has
// happened yet" branch at once. A highlight's own boundaries are there for
// the same reason: that is exactly where a panel gets a transition wrong,
// and a highlight creates two new boundaries the quarters will not land near
// on a long render.
func frameIndices(tl panel.Timeline, at, atVideo []time.Duration, highlights []panel.Highlight) ([]int, error) {
	n := tl.Frames()
	out := []int{0, n / 4, n / 2, (3 * n) / 4, n - 1}
	for _, h := range highlights {
		out = append(out, highlightLandmarkFrames(tl, h)...)
	}
	for _, d := range at {
		// Timeline.IndexAt CLAMPS, which is right for its own callers and
		// wrong here. Clamping made `--frame-at 40m` on a 25-minute activity
		// write the final frame and exit 0, handing the user a picture of
		// 25:53 while they believed they were looking at 40:00. The sink's
		// unreached-frame error could never fire, because clamping had made
		// the index reachable.
		if d < 0 {
			return nil, fmt.Errorf("render: --frame-at %v is negative", d)
		}
		if d > tl.ActivityDuration() {
			return nil, fmt.Errorf("render: --frame-at %v is past the end of the activity, which runs %s",
				d, panel.FormatClock(tl.ActivityDuration()))
		}
		out = append(out, tl.IndexAt(d))
	}
	for _, d := range atVideo {
		// --frame-at-video is the VIDEO's own clock, not the activity's --
		// see the flag's own help text -- so it is refused past the video's
		// own Duration() rather than ActivityDuration(), the same precedent
		// --frame-at follows one clock over.
		if d < 0 {
			return nil, fmt.Errorf("render: --frame-at-video %v is negative", d)
		}
		if d > tl.Duration() {
			return nil, fmt.Errorf("render: --frame-at-video %v is past the end of the video, which runs %s",
				d, panel.FormatClock(tl.Duration()))
		}
		// Every frame is exactly 1/FPS of video time from its neighbour
		// regardless of which segment it falls in (see Timeline.IntervalAt's
		// own doc comment on why that part of the arithmetic needs no
		// segment lookup), so converting a video-time offset to a frame
		// index is a division, not a search -- unlike IndexAt, which is
		// activity time and would ask the wrong question of this value.
		idx := int(math.Round(d.Seconds() * tl.FPS()))
		if idx >= n {
			idx = n - 1
		}
		out = append(out, idx)
	}
	return out, nil
}

// highlightLandmarkFrames returns h's own first and last frame, so --frames
// always samples both ends of a highlight's transition even when no
// --frame-at-video was given.
//
// Read back from Timeline.IndexAt rather than re-derived from Highlight.Rate
// -- see highlightFrames's own doc comment for why that is the one way to
// ask this question that cannot disagree with what Timeline itself laid
// down.
func highlightLandmarkFrames(tl panel.Timeline, h panel.Highlight) []int {
	first := tl.IndexAt(h.From)
	last := first + highlightFrames(tl, h) - 1
	if last == first {
		return []int{first}
	}
	return []int{first, last}
}

// parseSize reads a WxH resolution.
//
// The even-dimension rule is enforced by the encoder, which is the one place
// that knows why it exists (yuv420p subsamples chroma by two). Repeating the
// check here would be a second copy of a rule with one owner; what this does
// is refuse a string that is not a resolution at all, which the encoder cannot
// diagnose because it never sees it.
func parseSize(s string) (w, h int, err error) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(s)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("render: --size %q is not WxH (for example 1920x1080)", s)
	}
	w, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("render: --size %q has a non-numeric width", s)
	}
	h, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("render: --size %q has a non-numeric height", s)
	}
	return w, h, nil
}

// outputPath decides where the render is written.
//
// An explicit -o wins outright. Otherwise the name is derived from the
// activity's own stem, in --output-dir.
//
// An existing file is REFUSED rather than overwritten. A render takes minutes,
// and a second one silently replacing the first is a bad trade for a flag the
// user can pass in a second; ffmpeg's own -y would clobber without asking. The
// message names the flag that resolves it, because "file exists" on its own
// leaves the user guessing.
func outputPath(activity, explicit, dir, ext string) (string, error) {
	out := explicit
	if out == "" {
		stem := strings.TrimSuffix(filepath.Base(activity), filepath.Ext(activity))
		out = filepath.Join(dir, stem+ext)
	}
	if d := filepath.Dir(out); d != "" {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", fmt.Errorf("render: creating %s: %w", d, err)
		}
	}
	if _, err := os.Stat(out); err == nil {
		return "", fmt.Errorf("render: %s already exists; pass -o to write somewhere else", out)
	}
	return out, nil
}
