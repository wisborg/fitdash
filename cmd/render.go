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
	outputDir string
	output    string
	size      string
	fps       float64
	crf       int
	frames    bool
	frameAt   []time.Duration
	quiet     bool
	power     string
	layout    string
	theme     string
	smoothing string
	speedup   float64
	videoDur  time.Duration
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
		"compress the activity into a video of this length (e.g. 3m), whatever speedup that takes. "+
			"Mutually exclusive with --speedup")
	f.StringVar(&renderOpts.power, "power-source", "auto",
		"which power reading to show when the activity carries both a footpod (Stryd) developer field and the standard FIT power field -- "+
			"\"auto\" (default: prefer Stryd, fall back to native), \"stryd\" (force the footpod's developer field), or "+
			"\"native\" (force the standard FIT power field). The two can disagree since they are different sensors, and a forced "+
			"source that is absent shows a placeholder rather than the other sensor's number")
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
	timeline, err := panel.NewTimelineForActivity(timer, renderOpts.fps, speedup)
	if err != nil {
		return err
	}
	smoothing, err := resolveSmoothing(timeline)
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
		Track:       track,
		Report:      inspect.Build(track),
		Timer:       timer,
		Timeline:    timeline,
		Width:       w,
		Height:      h,
		FontScale:   layout.FontScale,
		Fonts:       fonts,
		PowerSource: powerSrc,
		Smoothing:   smoothing,
	}
	r, err := render.New(rctx, layout, theme)
	if err != nil {
		return err
	}

	if renderOpts.frames {
		return runFrames(cmd, r, timeline, activity, layout.Name, theme.Name, smoothing)
	}
	return runVideo(cmd, r, timeline, activity, w, h, layout.Name, theme.Name, smoothing)
}

// runVideo encodes the whole render to a video file.
func runVideo(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, activity string, w, h int, layoutName, themeName string, smoothing time.Duration) error {
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
	writeRenderSummary(cmd, r, tl, w, h, layoutName, themeName, smoothing)
	return nil
}

// smoothingAuto names the window derived from the compression.
const smoothingAuto = "auto"

// resolveSmoothing turns --smoothing into a window in activity time.
//
// "off" is spelled out rather than left to "0s", which parses as a duration and
// would work -- but a user reaching for a way to disable this should not have
// to guess that zero is it, and "off" in the summary reads better than "0s".
func resolveSmoothing(tl panel.Timeline) (time.Duration, error) {
	switch renderOpts.smoothing {
	case smoothingAuto, "":
		return tl.AutoSmoothing(), nil
	case "off":
		return 0, nil
	}
	d, err := time.ParseDuration(renderOpts.smoothing)
	if err != nil {
		return 0, fmt.Errorf("render: --smoothing %q is not %s, off, or a duration such as 30s",
			renderOpts.smoothing, smoothingAuto)
	}
	if d < 0 {
		return 0, fmt.Errorf("render: --smoothing %v is negative", d)
	}
	return d, nil
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

// speedupNote renders the compression, or nothing at all in real time -- where
// saying "(1x)" would draw attention to a fact the two equal durations beside
// it already state.
//
// Rounded to two decimals, and trimmed to none when it lands on a whole
// number. A --video-duration of three minutes over a four-hour activity works
// out at 79.99444444444444, and printing that is not more accurate, only
// harder to read -- the exact figure is a consequence of the duration the user
// asked for, which is the number they actually care about and which is printed
// beside it.
func speedupNote(s float64) string {
	if s == 1 {
		return ""
	}
	rounded := math.Round(s*100) / 100
	return fmt.Sprintf(" (%sx)", strconv.FormatFloat(rounded, 'f', -1, 64))
}

// writeRenderSummary reports what was rendered, and -- crucially -- which panels
// were left out and why.
//
// Naming the declined panels is not a nicety. A panel that vanishes because
// the activity carries nothing for it leaves "no unexplained holes" true in
// the pixels and false in the user's understanding of them: they see a
// dashboard with no power reading and have no way to tell whether their file
// lacks power, or fitdash does.
func writeRenderSummary(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, w, h int, layoutName, themeName string, smoothing time.Duration) {
	if renderOpts.quiet {
		return
	}
	out := cmd.ErrOrStderr()
	// Both durations, always. A user who asked for a three-minute video wants
	// to see that they got three minutes AND that it still covers the whole
	// activity; printing one of them leaves the other to be guessed at.
	fmt.Fprintf(out, "%d frames, %s of activity in %s of video%s, %s fps, %dx%d\n",
		tl.Frames(), panel.FormatClock(tl.ActivityDuration()), panel.FormatClock(tl.Duration()),
		speedupNote(tl.Speedup()), strconv.FormatFloat(tl.FPS(), 'f', -1, 64), w, h)

	writePanelSummary(cmd, r, layoutName, themeName, smoothing)
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
func smoothingNote(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	return d.Round(time.Second).String()
}

func writePanelSummary(cmd *cobra.Command, r *render.Renderer, layoutName, themeName string, smoothing time.Duration) {
	if renderOpts.quiet {
		return
	}
	out := cmd.ErrOrStderr()
	drew := make([]string, 0, len(r.Placed()))
	for _, p := range r.Placed() {
		drew = append(drew, p.Panel.Name())
	}
	fmt.Fprintf(out, "layout %s, theme %s, smoothing %s\n", layoutName, themeName, smoothingNote(smoothing))
	fmt.Fprintf(out, "panels: %s\n", strings.Join(drew, ", "))
	if declined := r.Declined(); len(declined) > 0 {
		fmt.Fprintf(out, "declined (this activity carries no such data): %s\n", strings.Join(declined, ", "))
	}
}

// runFrames writes selected frames as PNGs instead of encoding.
//
// It runs the IDENTICAL render path -- only the sink differs -- which is what
// makes the fast visual loop a trustworthy proxy for the real render rather
// than a second implementation free to disagree with it.
func runFrames(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, activity, layoutName, themeName string, smoothing time.Duration) error {
	indices, err := frameIndices(tl, renderOpts.frameAt)
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
	writePanelSummary(cmd, r, layoutName, themeName, smoothing)
	return nil
}

// frameIndices picks which frames --frames writes: the landmarks, plus
// anything --frame-at named.
//
// The landmarks are the first, the quarters and the last. Frame 0 and the last
// frame are there because panels that accumulate -- a progress bar, a covered
// route, a splits list -- are wrong at the boundaries far more often than in
// the middle, and frame 0 in particular hits every "nothing has happened yet"
// branch at once.
func frameIndices(tl panel.Timeline, at []time.Duration) ([]int, error) {
	n := tl.Frames()
	out := []int{0, n / 4, n / 2, (3 * n) / 4, n - 1}
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
	return out, nil
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
