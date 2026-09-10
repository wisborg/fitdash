package cmd

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/output/progress"

	"github.com/wisborg/fitdash/internal/encode"
	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/render"
	"github.com/wisborg/fitdash/internal/route"
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
	dryRun              bool
	power               string
	layout              string
	bottomBand          string
	theme               string
	gaugeStyle          string
	gauges              string
	smoothing           string
	speedup             float64
	videoDur            time.Duration
	highlights          []string
	highlightStyle      string
	highlightTransition time.Duration
	labels              []string
	elevationSmoothing  float64
	elevationGain       float64
	elevationLoss       float64
	clock               string
	pauses              string
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
	// --dry-run's entire output IS the summary, and --quiet suppresses the
	// summary. Together they resolve the whole render and then print nothing
	// at all, which is not a reading of either flag anyone intended -- the
	// same class of contradiction the -o/--frames check above refuses.
	if renderOpts.dryRun && renderOpts.quiet {
		return fmt.Errorf("render: --dry-run prints the summary and --quiet suppresses it; pass one")
	}
	// 0 is x264's spelling of lossless and this program's spelling of "unset",
	// and it cannot be both. Passing it through silently produced the default
	// quality instead, with nothing reported.
	if renderOpts.crf == 0 {
		return fmt.Errorf("render: --crf 0 is x264's lossless, which this program's zero value already means \"unset\"; use 1 for near-lossless")
	}
	// A negative value in any of the three means "auto" inside fitactivity's
	// own ElevationOptions (see resolveElevationTuning), so passing one
	// through unchecked would silently do something other than what was
	// typed -- the same class of defect the --crf 0 check above guards
	// against.
	if renderOpts.elevationSmoothing < 0 {
		return fmt.Errorf("render: --elevation-smoothing %v is negative; 0 already means auto", renderOpts.elevationSmoothing)
	}
	if renderOpts.elevationGain < 0 {
		return fmt.Errorf("render: --elevation-gain %v is negative", renderOpts.elevationGain)
	}
	if renderOpts.elevationLoss < 0 {
		return fmt.Errorf("render: --elevation-loss %v is negative", renderOpts.elevationLoss)
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

// bottomBands enumerates --bottom-band's legal values. The values themselves
// live in panel (BottomBandProfile, BottomBandDistance) because
// internal/render compares Context.BottomBand against them -- to decide
// whether to reject ElevationPanel in New's own keep filter -- and a string
// literal repeated across two packages is one typo away from a value that
// validates here and silently does nothing there. The SET belongs here
// because only the CLI's own validation and its error message need it, the
// same split highlightStyles draws in cmd/highlight.go.
var bottomBands = []string{panel.BottomBandProfile, panel.BottomBandDistance}

// parseBottomBand validates --bottom-band, refusing an unknown value where
// the user typed it rather than silently rendering with the default -- the
// same precedent parsePowerSource, parseHighlightStyle, SelectLayout and
// SelectTheme all follow: a typo should not cost a whole render.
func parseBottomBand(band string) (string, error) {
	for _, b := range bottomBands {
		if b == band {
			return band, nil
		}
	}
	return "", fmt.Errorf("render: --bottom-band %q is invalid; use %s", band, strings.Join(bottomBands, ", "))
}

// gaugeSelections enumerates --gauges' legal values, for the identical reason
// bottomBands lists --bottom-band's: the SET belongs here because only the
// CLI's own validation and its error message need it, where the values
// themselves (panel.GaugesMetrics, panel.GaugesBalance) live in panel because
// internal/render's keep filter compares Context.Gauges against them too.
var gaugeSelections = []string{panel.GaugesMetrics, panel.GaugesBalance}

// clocks and pauseModes enumerate --clock's and --pauses' legal values. The
// values themselves live in internal/panel, beside the code that acts on
// them, for the reason bottomBands gives: a string literal repeated across
// two packages is one typo away from a value that validates here and
// silently does nothing there.
var (
	clocks     = []string{panel.ClockElapsed, panel.ClockActive}
	pauseModes = []string{panel.PausesFreeze, panel.PausesSkip}
)

// parseClock validates --clock, and parsePauses --pauses, refusing an unknown
// value where the user typed it rather than rendering with the default -- the
// precedent every other value-taking flag here follows.
func parseClock(name string) (string, error) {
	if slices.Contains(clocks, name) {
		return name, nil
	}
	return "", fmt.Errorf("render: --clock %q is invalid; use %s", name, strings.Join(clocks, ", "))
}

func parsePauses(name string) (string, error) {
	if slices.Contains(pauseModes, name) {
		return name, nil
	}
	return "", fmt.Errorf("render: --pauses %q is invalid; use %s", name, strings.Join(pauseModes, ", "))
}

// parseGauges validates --gauges, refusing an unknown value rather than
// silently rendering with the default -- the same precedent parseBottomBand,
// parseHighlightStyle, SelectLayout and SelectTheme all follow.
func parseGauges(g string) (string, error) {
	for _, v := range gaugeSelections {
		if v == g {
			return g, nil
		}
	}
	return "", fmt.Errorf("render: --gauges %q is invalid; use %s", g, strings.Join(gaugeSelections, ", "))
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
	f.BoolVar(&renderOpts.dryRun, "dry-run", false,
		"resolve everything and print the summary, but write no video and no frames -- the fast way to check "+
			"a --label or --highlight lands where you meant before paying for an encode, and to read off where "+
			"each file of a merged activity starts")
	f.StringVar(&renderOpts.layout, "layout", panel.LayoutAuto,
		"panel arrangement -- \"auto\" (default: a column for a portrait frame, a row-based one otherwise), "+
			"\"landscape\", or \"portrait\". Naming one overrides the frame's shape, which is occasionally what you want "+
			"and usually not")
	f.StringVar(&renderOpts.bottomBand, "bottom-band", panel.BottomBandProfile,
		"what occupies the bottom band -- \"profile\" (default: the elevation profile, filled to the playhead, "+
			"and -- when at least one --highlight or --label is configured -- carrying their marks too, in place "+
			"of the standalone marker strip) or \"distance\" (omit the profile so the standalone distance readout "+
			"takes the band instead, even on an activity that carries elevation, and restores the standalone "+
			"marker strip alongside it). An activity carrying no elevation falls back to the readout under "+
			"\"profile\" too, exactly as before this flag existed -- this flag adds a second way to reach that "+
			"same fallback, not a different one")
	f.StringVar(&renderOpts.clock, "clock", panel.ClockElapsed,
		"which of the two clocks the time panel shows LARGE -- \"elapsed\" (default: wall-clock time since the "+
			"activity began, INCLUDING any pauses -- Garmin/Strava's \"elapsed time\") or \"active\" (the same, "+
			"EXCLUDING paused stretches -- Garmin's \"time\", Stryd/Strava's \"moving time\"). Both are always on "+
			"screen; this chooses the order. A file carrying no timer events cannot measure active time at all, and "+
			"shows a placeholder for it wherever it is drawn -- including as the large readout under \"active\" -- "+
			"rather than a figure equal to elapsed")
	f.StringVar(&renderOpts.pauses, "pauses", panel.PausesFreeze,
		"what the render does with the stretches where the activity was paused -- \"freeze\" (default: render them "+
			"like any other stretch, with the dashboard frozen through each one) or \"skip\" (cut them out, so video "+
			"time advances only while the timer was running). Skipping splices two instants together, so the route "+
			"dot jumps across whatever ground was covered while the watch was stopped; every seam draws a notice "+
			"naming how much activity time went missing there, and the summary reports the total. Pairs naturally "+
			"with --clock active, which is not implied: neither flag turns the other on")
	f.StringVar(&renderOpts.theme, "theme", panel.DefaultTheme().Name,
		"colour palette -- \"dark\" (default) or \"light\"")
	f.StringVar(&renderOpts.gaugeStyle, "gauge-style", panel.GaugeStyleNamePlain,
		"how the fluctuating gauges -- heart rate, pace, power, cadence -- show their reading: \"plain\" (default: the number "+
			"alone), \"track\" (adds a scale beneath it, marked at the current value, with both ends labelled), or \"dial\" "+
			"(the same scale beside it instead, as a semicircular arc with a needle in place of the track and its marker). "+
			"\"track\" and \"dial\" resolve the IDENTICAL range -- this activity's own typical span, snapped outward to "+
			"round numbers -- so they differ in shape and nothing else. A reading beyond either end is marked as "+
			"off-scale rather than hidden, and the number itself is never clipped. A gauge whose activity carries too "+
			"few readings to scale falls back to \"plain\", and the summary names it")
	f.StringVar(&renderOpts.gauges, "gauges", panel.GaugesMetrics,
		"what the gauge block shows: \"metrics\" (default: heart rate, pace, power and cadence) or \"balance\" "+
			"(the left/right balance bars, with pace and step length kept alongside since balance varies with effort). Every "+
			"balance bar uses one fixed scale centred on even, so the bars can be compared with each other and "+
			"across renders. An activity carrying no balance data falls back to \"metrics\" and the summary says "+
			"so -- \"fitdash inspect\" lists the developer fields a file actually carries, which is where to look "+
			"when a device names them differently")
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
	f.Float64Var(&renderOpts.elevationSmoothing, "elevation-smoothing", 0,
		"Gaussian smoothing width (in FIT samples, ~seconds) applied to the noisy GPS/barometric elevation before the "+
			"profile, the gain/loss bars and the gradient use it. 0 (default) = auto: tuned from --elevation-gain / "+
			"--elevation-loss, or the FIT device's own totals, or a mild default")
	f.Float64Var(&renderOpts.elevationGain, "elevation-gain", 0,
		"known total elevation GAIN (metres) for the activity -- the smoothing is auto-tuned so the computed total "+
			"matches (GPS elevation overcounts, so a known figure is the most reliable target). 0 = use the FIT's own "+
			"total. Paired with --elevation-loss. Ignored when --elevation-smoothing is set")
	f.Float64Var(&renderOpts.elevationLoss, "elevation-loss", 0,
		"known total elevation LOSS (metres) for the activity; see --elevation-gain. 0 = use the FIT's own total. "+
			"Ignored when --elevation-smoothing is set")
	f.StringArrayVar(&renderOpts.highlights, "highlight", nil,
		"mark a stretch of the activity for its own on-screen pace and treatment (repeatable). Comma-separated "+
			"key=value fields: from=DURATION and to=DURATION (both required, Go duration syntax into the activity's "+
			"ELAPSED time, e.g. 12m30s), name=TEXT (optional, free text; a literal comma needs \\, and a literal "+
			"backslash needs \\\\), video=DURATION or speedup=N (optional, mutually exclusive -- how much video "+
			"this stretch should take, or its own compression factor; neither means mark it without re-pacing it), "+
			"zoom=true (optional, default false -- the route panel reframes its map onto this highlight's own "+
			"stretch of course while it plays, easing in and out over --highlight-transition; a highlight whose "+
			"span carries no GPS keeps the whole-course view and the summary says so), "+
			"and background=#RRGGBB or #RGB (optional, hex only, opaque only -- the exact colour the frame washes "+
			"to while this highlight is active; only meaningful under --highlight-style wash, and refused under "+
			"any other style). "+
			"Example: --highlight 'from=12m30s,to=16m10s,video=10s,name=Hill climb,zoom=true'")
	f.StringVar(&renderOpts.highlightStyle, "highlight-style", panel.HighlightStyleBorder,
		"how a highlight is marked on screen -- \"border\" (default: an accent border in the frame's margin, plus "+
			"the marker strip), \"wash\" (additionally tints the whole background toward the highlight's own "+
			"background=, or the theme's own tint when it has none -- costs one static base per DISTINCT wash "+
			"colour, plus a full-frame blend per frame), or \"none\" (re-pace only, with the marker strip still "+
			"marking where the highlights are; background= is refused under this style too)")
	f.DurationVar(&renderOpts.highlightTransition, "highlight-transition", 400*time.Millisecond,
		"how much VIDEO time a highlight's on-screen mark takes to appear and to disappear at each end -- video "+
			"time, not activity time, because this is a perceptual ramp and should look the same however compressed "+
			"the render is. Also governs a --label's own fade in and out. 0 makes it a hard cut")
	f.StringArrayVar(&renderOpts.labels, "label", nil,
		"call out an instant in the activity by name (repeatable). Comma-separated key=value fields: "+
			"at=DURATION and name=TEXT (both required -- Go duration syntax into the activity's ELAPSED time for at, "+
			"e.g. 12m30s; a literal comma in name needs \\, and a literal backslash needs \\\\; a label with no name "+
			"draws nothing at all, unlike a --highlight), and video=DURATION (optional, VIDEO time, default 3s -- "+
			"how long the name stays on screen, starting AT the instant rather than centred on it). Two labels "+
			"whose on-screen spans would overlap are not refused: the earlier one is truncated to end where the "+
			"next begins, and it is reported. --highlight-transition governs a label's fade too. "+
			"Example: --label 'at=12m30s,name=Lighthouse,video=3s'")
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
	bottomBand, err := parseBottomBand(renderOpts.bottomBand)
	if err != nil {
		return err
	}
	gauges, err := parseGauges(renderOpts.gauges)
	if err != nil {
		return err
	}
	clock, err := parseClock(renderOpts.clock)
	if err != nil {
		return err
	}
	pauses, err := parsePauses(renderOpts.pauses)
	if err != nil {
		return err
	}

	// One Track regardless of how many files produced it, and unconditionally
	// rather than behind a check on len(args). Nothing downstream -- no
	// panel, no model, no timeline -- learns that an activity was assembled
	// from several files, which is what keeps this feature one call here
	// instead of a flag threaded through the render path. sources is the one
	// thing that does not survive the merge and is wanted anyway: where each
	// file begins, for the summary. See decodeActivities.
	track, sources, err := decodeActivities(args)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	activity := track.SourcePath

	timer := fitactivity.BuildTimerModel(track)
	speedup, err := resolveSpeedup(cmd, timer, pauses)
	if err != nil {
		return err
	}
	// Resolved BEFORE resolveHighlights, rather than after as it used to be:
	// a background= only has meaning under --highlight-style wash, and
	// refusing it under any other style (see backgroundStyleError) needs the
	// style in scope at the point each highlight is resolved.
	highlightStyle, err := parseHighlightStyle(renderOpts.highlightStyle)
	if err != nil {
		return err
	}
	highlights, err := resolveHighlights(renderOpts.highlights, timer, highlightStyle, pauses)
	if err != nil {
		return err
	}
	if renderOpts.highlightTransition < 0 {
		return fmt.Errorf("render: --highlight-transition %v is negative", renderOpts.highlightTransition)
	}
	// Always the fullest constructor, even with no highlights and under the
	// default --pauses freeze: with an empty slice and PausesFreeze it is
	// byte-identical to NewTimelineForActivity, which is the whole point of
	// funnelling every case through one segment builder rather than
	// branching here on which features were asked for.
	timeline, err := panel.NewTimelineForActivityWithPauses(timer, renderOpts.fps, speedup, highlights, pauses)
	if err != nil {
		return err
	}
	// Labels are resolved HERE, after the Timeline exists, rather than
	// beside resolveHighlights just above despite the obvious surface
	// symmetry of the two flags. See resolveLabels' own doc comment for
	// why: the overlap it checks for is in VIDEO time, and video time does
	// not exist until this Timeline -- itself built from the highlights --
	// has been constructed.
	labels, err := resolveLabels(renderOpts.labels, timeline)
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
	gaugeStyle, err := panel.SelectGaugeStyle(renderOpts.gaugeStyle)
	if err != nil {
		return err
	}
	// Resolved before Context is built, the way Report already is here, so
	// ElevationPanel (and the panels this field exists to let land) read a
	// model already built rather than each building their own copy of it --
	// see panel.Context.Elevation's own doc comment.
	elevTuning, elevSource := resolveElevationTuning(track)
	rctx := &panel.Context{
		Track:               track,
		Report:              inspect.Build(track),
		Elevation:           panel.BuildElevation(track, elevTuning),
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
		Labels:              labels,
		BottomBand:          bottomBand,
		GaugeStyle:          gaugeStyle,
		Gauges:              gauges,
		Clock:               clock,
		Pauses:              pauses,
	}
	r, err := render.New(rctx, layout, theme)
	if err != nil {
		return err
	}

	in := renderInputs{
		track: track, sources: sources, tl: timeline, w: w, h: h,
		layoutName: layout.Name, theme: theme, smoothing: smoothing,
		highlights: highlights, labels: labels,
		elevation: rctx.Elevation, elevationSource: elevSource,
	}
	if renderOpts.dryRun {
		return runDryRun(cmd, r, activity, rctx, in)
	}
	if renderOpts.frames {
		return runFrames(cmd, r, rctx, in)
	}
	return runVideo(cmd, r, activity, rctx, in)
}

// renderInputs bundles the values runRender resolves before building the
// Renderer, so runVideo, runFrames and writeRenderSummary take one value
// instead of a long positional list threaded through unchanged from
// runRender. Introduced because runVideo and writeRenderSummary had each
// grown past ten parameters, most of them the same bag threaded straight
// through, and several adjacent same-typed parameters (two ints, a
// []panel.Highlight next to a []panel.Label) made a transposition at a
// call site something the compiler would wave through.
//
// A plain data holder, not a type with behaviour of its own: everything in
// it was already validated by whichever function produced it --
// resolveHighlights, resolveLabels, panel.SelectLayout, panel.SelectTheme,
// resolveSmoothing, parseSize -- and a method here would blur which of
// those owns the rule.
//
// Deliberately NOT *panel.Context, though the two overlap (both carry the
// track, the highlights and the labels): writeHighlightSummary,
// writePanelSummary and writeLabelSummary are each exercised directly by
// tests that build a bare Timeline with no Context at all, and folding this
// into Context would drag those tests into constructing one just to call a
// summary function. This bundles only runVideo, runFrames and
// writeRenderSummary -- the three functions with no such test and no
// business being called with anything but runRender's own resolved values.
type renderInputs struct {
	track      *fitactivity.Track
	sources    []activitySource
	tl         panel.Timeline
	w, h       int
	layoutName string
	theme      panel.Theme
	smoothing  panel.Smoothing
	highlights []panel.Highlight
	labels     []panel.Label

	// elevation and elevationSource are rctx.Elevation and
	// resolveElevationTuning's own source string, carried here for
	// writeElevationSummary rather than through *panel.Context itself -- see
	// this type's own doc comment for why Context is deliberately not reused
	// as this bundle.
	elevation       *fitactivity.ElevationModel
	elevationSource string
}

// plannedOutputPath is where a render WOULD write, with no side effect: it
// creates no directory and does not care whether the file already exists.
//
// Split out of outputPath for --dry-run, which has to name the destination
// without touching the filesystem -- a dry run that created an --output-dir,
// or that failed because the file it was never going to write already
// existed, would be doing something. Both callers derive the name here so the
// path a dry run reports is the path a real render then writes.
func plannedOutputPath(activity, explicit, dir, ext string) string {
	if explicit != "" {
		return explicit
	}
	stem := strings.TrimSuffix(filepath.Base(activity), filepath.Ext(activity))
	return filepath.Join(dir, stem+ext)
}

// runDryRun resolves the whole render and reports it, without writing a video
// or a single frame.
//
// Everything that decides what the output looks like has already happened by
// the time this is called: the activity is decoded and merged, the timeline
// and its highlights and labels are resolved, and render.New has run every
// panel's Prepare -- which is what settles the layout and what each panel
// declined to draw. So the summary a dry run prints is not a prediction, it
// is the same summary a real render prints, produced by the same functions
// from the same values. Only the frame loop and the encoder are skipped.
//
// That is what makes this useful for authoring a --label or a --highlight:
// the refusals (an instant past the end of the activity, two labels at the
// same moment, a marker with no distance to sit at on the profile) all fire
// during resolution, so they are reported here in a second rather than after
// an encode.
//
// Everything goes to stderr, including the destination. runVideo puts the
// output path on stdout so it can be piped into the next command, and a dry
// run must not do that: the file is not there, and a script reading it would
// be handed a path to something that was never written.
func runDryRun(cmd *cobra.Command, r *render.Renderer, activity string, ctx *panel.Context, in renderInputs) error {
	out := cmd.ErrOrStderr()
	fmt.Fprintf(out, "dry run: resolved the whole render; no video and no frames were written\n")

	if renderOpts.frames {
		indices, err := frameIndices(in.tl, r.LastFrameWithSample(), renderOpts.frameAt, renderOpts.frameAtVideo, in.highlights, in.labels)
		if err != nil {
			return err
		}
		// Sorted, because the real run reports what the sink actually
		// wrote and the sink writes as the frame loop passes each index --
		// ascending. frameIndices returns the --frame-at/--frame-at-video
		// extras after the evenly spaced ones, so listing it unsorted would
		// have a dry run and a real run name the same files in different
		// orders, for no reason a reader could work out.
		sorted := append([]int(nil), indices...)
		sort.Ints(sorted)
		fmt.Fprintf(out, "would write %d frame%s to %s:\n", len(sorted), plural(len(sorted)), renderOpts.outputDir)
		for _, i := range sorted {
			// encode.FrameName, not a format string spelled again here:
			// a dry run naming files the sink would not create is worse
			// than one that says nothing.
			fmt.Fprintf(out, "  %s\n", encode.FrameName(renderOpts.outputDir, i))
		}
	} else {
		path := plannedOutputPath(activity, renderOpts.output, renderOpts.outputDir, ".mp4")
		fmt.Fprintf(out, "would write %s\n", path)
		// Reported, not refused. A real render fails on an existing output
		// (see outputPath), and a dry run whose whole job is to say what
		// would happen should say that this is what would happen -- rather
		// than either erroring, which stops the summary the user came for,
		// or staying silent about a render that cannot succeed.
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(out, "  note: %s already exists; a real render would refuse it and ask for -o\n", path)
		}
	}

	writeRenderSummary(cmd, r, ctx, in)
	return nil
}

// runVideo encodes the whole render to a video file.
//
// ctx is rctx, the exact *panel.Context render.New built r from -- passed
// straight through rather than folded into renderInputs (see that type's own
// doc comment), because writeGaugeSummary's only route to a gauge's resolved
// range is Readout.GaugeRangeText(ctx), and reading anything but the render's
// own Context there would risk it disagreeing with what Prepare actually drew.
func runVideo(cmd *cobra.Command, r *render.Renderer, activity string, ctx *panel.Context, in renderInputs) error {
	out, err := outputPath(activity, renderOpts.output, renderOpts.outputDir, ".mp4")
	if err != nil {
		return err
	}

	sink, err := encode.OpenVideo(cmd.Context(), encode.Config{
		OutputPath: out, Width: in.w, Height: in.h, FPS: in.tl.FPS(), CRF: renderOpts.crf,
	})
	if err != nil {
		return err
	}
	// Deferred for the error paths, then closed explicitly below where its
	// error matters: ffmpeg reports a failure at exit rather than at write, so
	// Close is the encode's own verdict. Both happen at most once.
	defer sink.Close()

	// Stopped before anything else is written, always: the display owns the
	// bottom of the terminal until it is told otherwise, and a summary
	// printed underneath a live bar lands inside the region the next redraw
	// would erase. Stop is idempotent, so the deferred call covers the paths
	// that return early.
	d, bar := newProgress(cmd, r.Frames())
	defer d.Stop()
	if err := render.Run(cmd.Context(), r, sink, func(i, n int) { bar.Set(int64(i)) }); err != nil {
		d.Stop()
		return err
	}
	d.Stop()
	if err := sink.Close(); err != nil {
		return err
	}

	// The path goes to stdout so it can be piped; everything else is
	// commentary and goes to stderr.
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out)
	writeRenderSummary(cmd, r, ctx, in)
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

// elevationTuningSource* name the four levels resolveElevationTuning can
// resolve to, in the exact wording the render summary prints (see
// writeElevationSummary). Named constants rather than string literals
// scattered across the switch below and the summary, so the two cannot
// silently say different things about the same case.
const (
	elevationTuningSourceExplicit = "set by --elevation-smoothing"
	elevationTuningSourceTargets  = "tuned to --elevation-gain/--elevation-loss"
	elevationTuningSourceFile     = "tuned to the file's own totals"
	elevationTuningSourceDefault  = "default (no totals in the file)"
)

// resolveElevationTuning is the ONE place --elevation-smoothing,
// --elevation-gain and --elevation-loss are turned into the
// fitactivity.ElevationOptions BuildElevation is given, in the four-level
// precedence the flags' own help text promises. Resolving it once here,
// rather than letting each of the elevation profile, the climb bars and the
// gradient reach their own conclusion from the same three flags, is what
// keeps three panels from disagreeing about the same activity's smoothing --
// the identical argument Context.Elevation's own doc comment makes for
// building the model once, extended here to cover the flags that choose its
// tuning.
//
// 1. An explicit --elevation-smoothing wins outright; --elevation-gain and
// --elevation-loss are ignored, matching what the library itself already
// does once Sigma > 0 -- this just documents it rather than leaving a user
// to discover it by trial.
// 2. Otherwise a known --elevation-gain or --elevation-loss (either one) sets
// the targets the smoothing is tuned to match.
// 3. Otherwise the FIT file's own reported totals, when it carried them --
// today's silent default, unchanged in effect, now named in the summary
// rather than left for a user to infer from a profile that looks a
// particular amount of smooth.
// 4. Otherwise fitactivity's own library default (a mild, untuned sigma).
//
// track may be nil (a render whose Decode already failed never reaches this
// point in practice, but the function makes no assumption of its own) --
// case 3 simply never matches a nil track, falling through to the default.
func resolveElevationTuning(track *fitactivity.Track) (fitactivity.ElevationOptions, string) {
	switch {
	case renderOpts.elevationSmoothing > 0:
		return fitactivity.ElevationOptions{Sigma: renderOpts.elevationSmoothing}, elevationTuningSourceExplicit
	case renderOpts.elevationGain > 0 || renderOpts.elevationLoss > 0:
		return fitactivity.ElevationOptions{TargetGain: renderOpts.elevationGain, TargetLoss: renderOpts.elevationLoss}, elevationTuningSourceTargets
	case track != nil && track.HasElevationTotals:
		return panel.DefaultElevationTuning(track), elevationTuningSourceFile
	default:
		return panel.DefaultElevationTuning(track), elevationTuningSourceDefault
	}
}

// resolveSpeedup turns --speedup or --video-duration into one compression
// factor.
//
// The two are mutually exclusive rather than one overriding the other. They
// express the same intention in opposite directions, and silently preferring
// whichever the code checks first would let a user pass both and get a video
// of a length they did not ask for, with nothing saying which flag won.
func resolveSpeedup(cmd *cobra.Command, timer *fitactivity.TimerModel, pauses string) (float64, error) {
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
		// Under --pauses skip the render never lays a frame over a paused
		// stretch, so the length being compressed into --video-duration is
		// the RUNNING time, not the elapsed span. Compressing the elapsed
		// span instead would come out short by however long the watch was
		// stopped -- a three-minute video of an activity with twelve
		// minutes of rest stops would run closer to two and a half -- and
		// nothing would say why.
		if pauses == panel.PausesSkip {
			activity -= timer.PausedTotal()
		}
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

// newProgress builds the live progress display and its bar, or nils under
// --quiet.
//
// A nil *progress.Display and a nil *progress.Bar are both usable and do
// nothing, which is why --quiet is one decision here rather than a condition
// at every call site and in the render loop's per-frame callback.
//
// Whether the bar renders in place or as periodic plain lines is the
// display's own decision, made from the writer: this used to be fitdash's
// judgement (an os.Stat for a character device), and it was wrong for
// /dev/null, which IS one -- so `fitdash 2>/dev/null` took the inline path
// and wrote escape sequences into it.
func newProgress(cmd *cobra.Command, total int) (*progress.Display, *progress.Bar) {
	if renderOpts.quiet {
		return nil, nil
	}
	// A gradient, which is safe to ask for unconditionally: the display
	// emits colour only on a terminal that will take it, and never when
	// NO_COLOR or TERM=dumb says otherwise.
	d := progress.New(cmd.ErrOrStderr(), progress.Options{Palette: progress.DefaultGradient()})
	return d, d.Bar(progress.BarSpec{Label: "rendering", Total: int64(total), Unit: "frames"})
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
func writeRenderSummary(cmd *cobra.Command, r *render.Renderer, ctx *panel.Context, in renderInputs) {
	if renderOpts.quiet {
		return
	}
	out := cmd.ErrOrStderr()
	writeMergeSummary(cmd, in.track, in.sources)
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
		in.tl.Frames(), panel.FormatClock(in.tl.ActivityDuration()), panel.FormatClock(in.tl.Duration()),
		baseSpeedupNote(in.tl.BaseSpeedup(), len(in.highlights) > 0), strconv.FormatFloat(in.tl.FPS(), 'f', -1, 64), in.w, in.h)

	writePanelSummary(cmd, r, in.tl, in.layoutName, in.theme.Name, in.smoothing)
	writeClockSummary(cmd, ctx)
	writePauseSummary(cmd, ctx, in)
	writeGaugeSummary(cmd, r, ctx)
	writeGaugeSelectionSummary(cmd, r, ctx)
	writeElevationSummary(cmd, in.elevation, in.elevationSource)
	markersOnProfile := markersAbsorbedIntoProfile(r)
	writeHighlightSummary(cmd, in.tl, in.track, in.highlights, in.smoothing, in.theme, markersOnProfile)
	writeLabelSummary(cmd, in.tl, in.track, in.labels, renderOpts.highlightTransition, markersOnProfile, r.OverlappingLabels())
}

// writeGaugeSummary reports, for a render styled GaugeStyleTrack or
// GaugeStyleDial, each placed gauge's own resolved range -- the same range
// Prepare will actually draw the track or the dial against, read back
// through Readout.GaugeRangeText rather than re-derived here, so this line
// can never disagree with what was drawn on screen -- and names any gauge
// whose activity range came out unusable, since Prepare's own silent
// fallback to the plain readout would otherwise leave one gauge's
// instrument missing for a reason nothing on screen explains.
//
// Printed only under GaugeStyleTrack or GaugeStyleDial: an ordinary
// GaugeStylePlain render (today's default) prints nothing here, the same
// discipline writeHighlightSummary and writeElevationSummary already apply
// to their own features, so a render that never asked for a gauge
// instrument sees a summary identical to one from before this flag existed.
// The line names which of the two styles actually drew, via
// panel.GaugeStyleName(ctx.GaugeStyle) -- both share the identical range
// arithmetic (GaugeStyle's own doc comment, gauge.go), so nothing else on
// this line needs to differ between them.
//
// Iterates r.Placed() -- the panels THIS render actually placed, in the
// order the layout tree resolved them -- rather than a fixed list of the
// four gauge metrics, so a metric the activity carries nothing for at all
// (Accepts declined it, and it is simply not in the layout) is never
// reported here. That is a different fact from a placed gauge whose own
// range Quantiles refused, which IS what "no usable range - plain" reports.
// panel.Readout.HasGaugeRule tells the two apart without needing ctx: only
// HeartRate, Pace, Power and Cadence set a scale rule at all, so filtering
// on it first is what keeps Distance and ElapsedPanel -- placed, but with no
// gauge variant to speak of -- out of this line entirely.
func writeGaugeSummary(cmd *cobra.Command, r *render.Renderer, ctx *panel.Context) {
	if renderOpts.quiet || ctx == nil ||
		(ctx.GaugeStyle != panel.GaugeStyleTrack && ctx.GaugeStyle != panel.GaugeStyleDial) {
		return
	}
	var parts []string
	for _, p := range r.Placed() {
		ro, ok := p.Panel.(panel.Readout)
		if !ok || !ro.HasGaugeRule() {
			continue
		}
		name := strings.ReplaceAll(ro.Name(), "-", " ")
		if text, ok := ro.GaugeRangeText(ctx); ok {
			parts = append(parts, fmt.Sprintf("%s %s", name, text))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s no usable range - plain", name))
	}
	if len(parts) == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "gauges: %s; %s\n", panel.GaugeStyleName(ctx.GaugeStyle), strings.Join(parts, ", "))
}

// writeGaugeSelectionSummary reports --gauges balance's own two outcomes --
// see that flag's own help text (bindRenderFlags, above) and
// panel.IsBalancePanel/hasAnyBalanceMetric (internal/panel/gauges.go) for the
// mechanism this reads back from rather than re-derives: which panels the
// gauge Alt slot actually placed is read from r.Placed(), never recomputed
// here, so this line can never disagree with the pixels.
//
// The Alt mechanism's own documented gap (Box's Alt doc comment,
// internal/panel/layout.go) means neither outcome shows up in Declined() or
// Omitted() on its own: whichever candidate loses is simply never asked
// about, and three panels (heart rate, power, cadence) vanishing from a
// render with no line explaining why would be exactly the "unexplained hole"
// this project's whole absent-data discipline exists to prevent -- worse
// than the single readout the Alt doc comment's own gap was originally
// written about, which is why this closes it locally rather than waiting on
// the engine's own "superseded" machinery.
//
// Printed only under --gauges balance: an ordinary metrics render (the
// default) prints nothing here, keeping its summary identical to one from
// before this flag existed, the same discipline every other flag-gated
// summary line in this file already follows.
func writeGaugeSelectionSummary(cmd *cobra.Command, r *render.Renderer, ctx *panel.Context) {
	if renderOpts.quiet || ctx == nil || ctx.Gauges != panel.GaugesBalance {
		return
	}
	out := cmd.ErrOrStderr()
	for _, p := range r.Placed() {
		if panel.IsBalancePanel(p.Panel) {
			fmt.Fprintln(out, "gauges: balance -- pace and step length are kept alongside the bars for effort context; "+
				"heart rate, power and cadence are not shown")
			return
		}
	}
	fmt.Fprintln(out, "gauges: balance was requested, but this activity carries none of the four balance "+
		"metrics; showing the ordinary gauges instead")
}

// writeClockSummary names the clock order, and only when it is not the
// default.
//
// Printed conditionally rather than folded into the layout/theme line for
// the reason the "omitted (--bottom-band ...)" line is its own line: a
// render that does not use the flag should print exactly the summary it
// printed before the flag existed, so a user comparing two runs sees a
// difference only where there is one.
func writeClockSummary(cmd *cobra.Command, ctx *panel.Context) {
	if renderOpts.quiet || ctx.Clock != panel.ClockActive {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "clock: %s is the large readout, %s beneath it\n", panel.ClockActive, panel.ClockElapsed)
}

// writePauseSummary reports what --pauses skip actually did, and is silent
// under the default.
//
// Every branch here is a case where the flag did something other than what a
// user would assume from having typed it, which is the whole reason it is
// worth printing: a file with no timer events cannot locate a pause at all,
// an activity that never stopped has nothing to cut, and a pause at the very
// start or end of a recording is removed without leaving a seam for the
// on-screen notice to mark. Reporting only the happy case would leave all
// three looking identical to a flag that worked.
//
// The two figures are deliberately from two different owners.
// Timeline.CutRemoved is what was spliced out of the VIDEO, and every second
// of it is announced on screen; TimerModel.PausedTotal is what the ACTIVITY
// spent stopped, and the difference between them is time removed at an edge
// where there was nothing to announce. Printing one number for both would
// make the on-screen notices fail to add up to the summary's own total, with
// nothing saying why.
func writePauseSummary(cmd *cobra.Command, ctx *panel.Context, in renderInputs) {
	if renderOpts.quiet || ctx.Pauses != panel.PausesSkip {
		return
	}
	out := cmd.ErrOrStderr()

	if !ctx.Timer.HasTimerEvents() {
		fmt.Fprintf(out, "pauses: --pauses %s asked for, but this file carries no timer events, so no pause can be located; nothing was skipped\n",
			panel.PausesSkip)
		return
	}
	total := ctx.Timer.PausedTotal()
	if total <= 0 {
		fmt.Fprintf(out, "pauses: --pauses %s asked for; this activity never stopped, so nothing was skipped\n", panel.PausesSkip)
		return
	}

	cuts := in.tl.Cuts()
	if len(cuts) > 0 {
		fmt.Fprintf(out, "pauses: %s cut out at %d seam%s, each named on screen as it passes\n",
			panel.FormatClock(in.tl.CutRemoved()), len(cuts), plural(len(cuts)))
	}
	if edge := total - in.tl.CutRemoved(); edge > 0 {
		fmt.Fprintf(out, "pauses: a further %s at the activity's own start or end, left out with no seam to mark -- the render begins later or ends earlier instead\n",
			panel.FormatClock(edge))
	}

	// A label naming an instant that no longer has a frame. Reported here
	// rather than beside the other label warnings because it is a
	// consequence of THIS flag: without --pauses skip the instant is
	// rendered like any other and there is nothing to say.
	start, _ := ctx.Timer.Window()
	for _, l := range in.labels {
		if ctx.Timer.Paused(start.Add(l.At)) {
			fmt.Fprintf(out, "label %s names an instant inside a skipped pause; it is shown at the seam instead\n", labelSummaryName(l))
		}
	}
}

// plural is the "s" a count needs, for the summary lines that name one.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// writeElevationSummary reports the smoothing sigma BuildElevation actually
// used and which of the four --elevation-smoothing / --elevation-gain and
// --elevation-loss / the file's own totals / the library's default precedence
// levels produced it (see resolveElevationTuning) -- today entirely silent,
// so a user looking at a flatter-than-expected profile has no way to tell
// whether that is the terrain or the tuning.
//
// Printed only when the activity actually carries an elevation model:
// nothing to report on a track with no elevation, or no distance, or too few
// samples carrying both -- the same discipline writeHighlightSummary and
// writeLabelSummary already apply to their own flags, printing nothing at
// all rather than a line about a feature that never engaged.
func writeElevationSummary(cmd *cobra.Command, m *fitactivity.ElevationModel, source string) {
	if renderOpts.quiet || m == nil || m.Empty() {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "elevation: smoothing sigma %.1f samples, %s\n", m.Sigma(), source)
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

// markerPanelName is the marker panel's own Name(), used below to split it
// out of the ordinary "carries no such data" decline heading. Read from the
// panel itself rather than restated as a string literal, so the two cannot
// drift apart the way a hand-copied name could.
var markerPanelName = panel.MarkerPanel{}.Name()

// markersAbsorbedIntoProfile is true when the elevation profile is drawing
// this render's configured highlights and labels as marks on its own
// distance axis, rather than MarkerPanel drawing them on the standalone
// strip -- see render.Renderer.Absorbed's own doc comment and
// internal/render's profileTakesTheBand. Computed once per render and passed
// into both writeHighlightSummary and writeLabelSummary, which use it to
// gate the D.1 "has no distance" line: that line is only true news once
// something was actually trying to place a mark on the profile's axis.
func markersAbsorbedIntoProfile(r *render.Renderer) bool {
	return slices.Contains(r.Absorbed(), markerPanelName)
}

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

	// Two decline reasons, not one. The marker panel declines because
	// neither --highlight nor --label was given at all -- a fact about the
	// FLAGS -- and printing its name under "this activity carries no such
	// data" would tell the user their FIT file lacks something no FIT file
	// has ever carried. Every other panel's decline IS a fact about the
	// activity, so it keeps the original heading.
	var dataDeclined, configDeclined []string
	for _, name := range r.Declined() {
		if name == markerPanelName {
			configDeclined = append(configDeclined, name)
			continue
		}
		// A name that was ALSO placed is an Alt loser, not a fact about the
		// activity. --gauges balance seats pace in the balance branch as well
		// as in the metrics branch; when the balance branch loses -- because
		// the file carries none of the four balance metrics -- that copy
		// declines while the metrics branch's own pace draws. Reporting the
		// loser here printed `pace` under "carries no such data" on the same
		// render whose `panels:` line listed pace as drawn, which is a
		// self-contradiction and false besides.
		//
		// The rule is deliberately about the two lists rather than about
		// pace: if a panel of that name drew, the activity demonstrably
		// carries what it needs, so no decline of that name can be a
		// statement about missing data. The fallback itself is not left
		// unexplained -- writeGaugeSelectionSummary says outright that
		// balance was requested, the activity has none, and the ordinary
		// gauges are showing instead.
		if slices.Contains(drew, name) {
			continue
		}
		dataDeclined = append(dataDeclined, name)
	}
	if len(dataDeclined) > 0 {
		fmt.Fprintf(out, "declined (this activity carries no such data): %s\n", strings.Join(dataDeclined, ", "))
	}
	if len(configDeclined) > 0 {
		fmt.Fprintf(out, "declined (no --highlight or --label given): %s\n", strings.Join(configDeclined, ", "))
	}

	// A THIRD reason, deliberately not a third "declined" heading: nothing
	// here was missing and no panel refused anything. render.New's keep
	// filter removed ElevationPanel before its own Accepts was ever asked
	// (see Renderer.Omitted's own doc comment), so folding this into either
	// decline heading above would say something false -- either that the
	// activity lacks elevation, which render.New never actually checked, or
	// that no flag was given, when one plainly was. "panels: ..." already
	// shows distance rather than the profile, so this line is not the only
	// signal; it is the one that survives being read on its own, by someone
	// who was not the one who typed the command.
	if omitted := r.Omitted(); len(omitted) > 0 {
		fmt.Fprintf(out, "omitted (--bottom-band %s): %s\n", panel.BottomBandDistance, strings.Join(omitted, ", "))
	}

	// A FOURTH reason, and again deliberately not a decline: the marker
	// panel had something to show -- render.New's keep filter only reaches
	// this branch once at least one --highlight or --label is configured --
	// and it IS on screen, just not in its own box. It is drawn as marks on
	// the elevation profile's own distance axis instead (see
	// Renderer.Absorbed's own doc comment and internal/panel/elevation.go's
	// "the name rows"). Folding this into "declined" would say the opposite
	// of what happened: nothing was left out, it moved.
	if absorbed := r.Absorbed(); len(absorbed) > 0 {
		fmt.Fprintf(out, "absorbed into the elevation profile: %s\n", strings.Join(absorbed, ", "))
	}
}

// writeHighlightSummary reports the base/highlight decomposition, one line
// per highlight naming its own pace, a warning for each of the three
// surprises resolveHighlights marks as "adjusted, and reported" rather than
// fatal (a clipped end, a highlight forced down to one frame, one lying
// wholly inside a paused stretch), and -- for a highlight with its own
// background= -- the resolved colour and a legibility warning against
// theme.
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
//
// track is here for two things: reporting a highlight whose span has no GPS
// fix anywhere near it, so RoutePanel could not mark it on the map (see
// route.SpanIndices), and -- when markersOnProfile is true -- reporting one
// whose bounds have no distance, so ElevationPanel could not mark it on the
// profile either (see panel.TimeToDistance's own D.1 policy in
// internal/panel/elevation.go). track may be nil in a test exercising the
// rest of this table, in which case route.FromTrack(nil, ...) returns no
// points and hasProfileDistance(nil, ...) returns false, so both checks are
// silently skipped -- correct, since a nil track carries neither a route nor
// a distance stream to report against, not a bug in either check.
//
// markersOnProfile is true exactly when render.Renderer.Absorbed() names the
// marker panel -- i.e. ElevationPanel is drawing these highlights as marks
// on its own axis instead of MarkerPanel drawing them on the standalone
// strip (see internal/render's own profileTakesTheBand). It gates the
// distance check for the identical reason canMarkRoute gates the GPS one: a
// highlight whose bounds have no distance is only unplaceable news when
// something was actually trying to place it there. When the strip is
// standalone -- the ordinary case, and also --bottom-band distance's own
// escape hatch -- every highlight is placeable on it regardless of distance
// (MarkerPanel positions blocks in VIDEO time, never distance), so nothing
// here would be true to report.
// highlightZoomNote marks a highlight that reframes the route panel's map, so
// the highlights line says which ones do. Silent for the default, which is
// every highlight until somebody types zoom=true.
//
// It reports what was ASKED for, not what the panel resolved -- the two differ
// only when there is no GPS to zoom to, and that case gets a line of its own
// saying exactly that, rather than being folded into a silently absent word
// here.
func highlightZoomNote(h panel.Highlight) string {
	if !h.Zoom {
		return ""
	}
	return ", zoomed"
}

func writeHighlightSummary(cmd *cobra.Command, tl panel.Timeline, track *fitactivity.Track, highlights []panel.Highlight, smoothing panel.Smoothing, theme panel.Theme, markersOnProfile bool) {
	if renderOpts.quiet || len(highlights) == 0 {
		return
	}
	out := cmd.ErrOrStderr()

	// The SAME thinned list RoutePanel itself draws the outline from, and
	// the SAME route.SpanIndices call its Prepare uses to resolve a
	// highlight's mark -- re-deriving the predicate here, even loosely,
	// would risk this line disagreeing with what the panel actually drew.
	// Fewer than two points means RoutePanel.Accepts already declined and
	// there is no route for any highlight to be reported against at all
	// -- an activity with no route at all leaves nothing to decide here.
	routePts := route.FromTrack(track, route.DefaultMaxPoints)
	canMarkRoute := len(routePts) >= 2

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
		lines[i] = fmt.Sprintf("%s %s -> %ss (%sx)%s%s",
			highlightSummaryName(h), panel.FormatClock(h.From), strconv.FormatFloat(video, 'f', 1, 64), formatMultiplier(rate),
			highlightZoomNote(h), highlightSmoothingNote(tl, h, smoothing))
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
		if h.HasBackground {
			fmt.Fprintf(out, "highlight %s background %s\n", highlightSummaryName(h), formatHexColor(h.Background))
			for _, warning := range backgroundContrastWarnings(theme, h.Background) {
				fmt.Fprintf(out, "highlight %s background %s\n", highlightSummaryName(h), warning)
			}
		}
		if h.Zoom && !canMarkRoute {
			// Asked for on an activity with no route panel at all. Said
			// plainly rather than ignored: the flag was typed, it will do
			// nothing, and the reason is a property of the activity rather
			// than a mistake in the value.
			fmt.Fprintf(out, "highlight %s asked to zoom the route, but this activity has no route to draw\n",
				highlightSummaryName(h))
		}
		if canMarkRoute {
			start := tl.Start()
			if _, _, ok := route.SpanIndices(routePts, start.Add(h.From), start.Add(h.To)); !ok {
				if h.Zoom {
					fmt.Fprintf(out, "highlight %s asked to zoom the route, but has no GPS fixes in its span; the map stays on the whole course\n",
						highlightSummaryName(h))
				}
				fmt.Fprintf(out, "highlight %s has no GPS fixes; it is not marked on the route\n", highlightSummaryName(h))
			}
		}
		if markersOnProfile && !hasProfileDistanceSpan(track, tl.Start(), h.From, h.To) {
			fmt.Fprintf(out, "highlight %s has no distance at its bounds; it is not marked on the elevation profile\n", highlightSummaryName(h))
		}
	}
}

// hasProfileDistance is true when offset -- an activity-time offset from
// start -- resolves to a placeable distance, by calling panel.TimeToDistance
// directly rather than re-deriving the same gap-aware lookup here. That is
// the SAME function ElevationPanel's own buildMarks calls to place a mark
// (internal/panel/elevation.go), so this summary line can never disagree
// with what the panel actually drew -- matching the precedent this file's
// own canMarkRoute check already sets by calling route.SpanIndices directly
// instead of asking RoutePanel. An earlier version of this function
// hand-copied TimeToDistance's own body instead of calling it, on the
// mistaken belief that canMarkRoute was precedent for that; canMarkRoute
// calls an exported function, it does not duplicate one.
//
// false, harmlessly, when track is nil -- a caller with nothing to report
// against, not a bug in the check.
func hasProfileDistance(track *fitactivity.Track, start time.Time, offset time.Duration) bool {
	_, ok := panel.TimeToDistance(track, start, offset)
	return ok
}

// hasProfileDistanceSpan is hasProfileDistance applied to BOTH ends of a
// span: a highlight needs distance at both From and To to be placeable on
// the elevation profile's axis (see elevation.go's own D.1 policy -- one
// known endpoint and one unknown is still unplaceable), so this is false the
// moment either one is.
func hasProfileDistanceSpan(track *fitactivity.Track, start time.Time, from, to time.Duration) bool {
	return hasProfileDistance(track, start, from) && hasProfileDistance(track, start, to)
}

// formatHexColor renders c the same way a user would have typed it in
// background=, so the summary's resolved-colour line reads back what was
// asked for rather than some other spelling of the same value.
func formatHexColor(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// chromeContrastFloor is the ABSOLUTE floor a highlight's own background=
// must clear against Dim and Absent -- deliberately NOT WCAG 2's 4.5:1,
// which governs body text, and neither role is read like a paragraph: Dim
// is chrome the eye should not land on, and Absent only has to be tellable
// from a live reading (see Theme's own doc comment on both). A ratio of
// 1.0 means the background has collided with that role's own colour and the
// role has become genuinely invisible against it -- the only failure that
// is unambiguous at this end of the scale.
//
// 1.5 is anchored to what this project already ships, not invented: the
// weakest pairing either shipped theme itself relies on is 1.679:1 (the
// light theme's own Background against its own Absent), so any floor at or
// above that would condemn this program's own palette on every render, the
// same "warns about itself" failure the first version of this check had.
// 1.5 sits just under that measured floor -- close enough to catch a
// background that has effectively become one of the text roles (both land
// at exactly 1.0), while accepting every palette this project ships and
// every ordinary colour, including an unremarkable dark navy, that a user
// might reasonably type.
//
// Two earlier attempts at this check are worth naming so neither is
// rediscovered: an absolute 4.5:1 against Dim and Absent is unsatisfiable
// (no colour clears all three roles, and both shipped themes fail it
// against their OWN background); a check relative to the theme's own
// background is satisfiable but rejects nearly every distinguishable colour
// under the dark theme specifically, because that theme's Background
// already sits close to the darkest luminance achievable and almost any
// lighter colour necessarily scores worse against the recessive roles than
// it does. Both warn on ordinary input, which is the noise problem this
// floor exists to avoid.
const chromeContrastFloor = 1.5

// backgroundContrastWarnings names the ways bg -- a highlight's own
// background= -- is less legible than it should be against theme, one
// sentence per problem found, or nil when it earns none. Checked at full
// weight only: the endpoint is where text sits longest over the wash, and
// sweeping every intermediate ramp frame would be a threshold on a
// threshold.
//
// Foreground and the chrome roles (Dim, Absent) are checked against
// DIFFERENT numbers, and that asymmetry is deliberate rather than an
// inconsistency to smooth back into one rule: Foreground is genuinely body
// text -- the readings this dashboard exists to show -- so it gets WCAG 2's
// own published 4.5:1. Dim and Absent are not body text; they are DESIGNED
// to be recessive, so they get chromeContrastFloor instead -- see its own
// doc comment for why 1.5 is the anchored number and why an absolute 4.5:1
// or a check relative to the theme's own background both fail here.
func backgroundContrastWarnings(theme panel.Theme, bg color.NRGBA) []string {
	var warnings []string
	if ratio := panel.ContrastRatio(bg, theme.Foreground); ratio < 4.5 {
		warnings = append(warnings, fmt.Sprintf(
			"contrast against foreground is %.1f:1, below the WCAG 2 threshold of 4.5:1", ratio))
	}
	for _, role := range []struct {
		name string
		col  color.Color
	}{
		{"dim", theme.Dim},
		{"absent", theme.Absent},
	} {
		if ratio := panel.ContrastRatio(bg, role.col); ratio < chromeContrastFloor {
			warnings = append(warnings, fmt.Sprintf(
				"contrast against %s is %.1f:1, below the floor of %v:1", role.name, ratio, chromeContrastFloor))
		}
	}
	return warnings
}

// highlightSmoothingNote appends this highlight's own auto-smoothing window
// to its line in the highlight table, or nothing when there is no per-
// highlight figure to add.
//
// Only --smoothing auto varies per highlight. An EXPLICIT --smoothing 30s
// stays global (resolveSmoothing never reinterprets it per segment), so it
// is already the one number writePanelSummary's "smoothing 30s" line
// reports and restating it five times over in this table would say nothing
// new. Auto is the case this table exists for: a highlight running its own
// rate gets its own window (Timeline.AutoSmoothingAt), which is exactly
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

// writeLabelSummary reports each configured label's own on-screen span, and
// the three warnings resolveLabels marks as "adjusted, and reported" rather
// than fatal: a span truncated because the next label's own began before it
// would otherwise have ended, the LAST label's own span clamped because its
// video= would otherwise have run past the render's final frame, and a span
// too short for its own fade ever to reach full opacity.
//
// The first two share resolveLabels' one Truncated field rather than a
// second one, and are told apart here by POSITION alone: only the last
// label in the (At-sorted) slice can ever be clamped to the render's own
// end, and the overlap loop never touches that last index -- see
// the comment beside that clamp in resolveLabels for the proof -- so a Truncated label at
// any other position was cut short by its neighbour, and a Truncated last
// label was cut short by the render's own end. Nothing here re-derives
// resolveLabels' arithmetic to reach that conclusion, only its ordering.
//
// Printed only when at least one --label was configured, the same
// discipline writeHighlightSummary follows, so an ordinary render without
// --label prints none of this and its summary stays identical to one from
// before this feature existed.
//
// transition is renderOpts.highlightTransition, not a separate
// --label-transition: the same flag governs a label's own fade (see its
// own help text), so there is exactly one number to check a label's video=
// against.
//
// The opacity warning's reasoning: LabelAt's ramp (via the shared
// rampWeight) takes the MIN of a label's entrance ramp and its exit ramp,
// which is correct -- a name should not be fully visible before it has
// finished appearing, nor after it has started disappearing -- but it means
// a span shorter than twice the transition never lets both ramps reach 1 at
// once. The result is arithmetically right and looks like a bug: a name
// sitting at a fraction of full opacity for the whole time it is on screen,
// with nothing on screen saying why. Reported here for the same reason
// resolveHighlights reports a highlight forced down to one frame.
//
// The check is against l.Video, not against whatever --label asked for:
// resolveLabels already narrows Video to what the label actually received
// by the time it reaches here, for EITHER truncation cause above, so this
// warning fires (or stays silent) on the real on-screen span rather than on
// a duration the label never got.
//
// track and tl are here for exactly one thing, mirroring
// writeHighlightSummary's own identically-named parameters: reporting a
// label whose instant has no distance, so ElevationPanel could not mark it
// on the profile either, once markersOnProfile says the profile is drawing
// these labels' ticks at all -- see hasProfileDistance and
// writeHighlightSummary's own doc comment for the full reasoning, which
// applies here unchanged. track may be nil and tl the zero Timeline in a
// test exercising the rest of this function with markersOnProfile false, in
// which case the check is never reached.
//
// overlapping is render.Renderer.OverlappingLabels(): the index into labels
// of every label ElevationPanel could not pull a full tick-width away from
// its neighbour even after D.4's bounded nudge (internal/panel/elevation.go).
// Reported the same way a mark with no resolvable distance is -- in words,
// once, per affected label -- rather than silently leaving two names to
// print on top of each other with nothing on screen explaining why. Passed
// in rather than recomputed here for the identical reason hasProfileDistance
// now calls panel.TimeToDistance instead of re-deriving it: the pixel
// geometry that decides "still touching" is sized from this render's own
// box and lives only on the Painter that drew it.
func writeLabelSummary(cmd *cobra.Command, tl panel.Timeline, track *fitactivity.Track, labels []panel.Label, transition time.Duration, markersOnProfile bool, overlapping []int) {
	if renderOpts.quiet || len(labels) == 0 {
		return
	}
	out := cmd.ErrOrStderr()

	lines := make([]string, len(labels))
	for i, l := range labels {
		lines[i] = fmt.Sprintf("%s %s -> %s", labelSummaryName(l), panel.FormatClock(l.At), l.Video.Round(time.Millisecond))
	}
	fmt.Fprintf(out, "labels: %s\n", strings.Join(lines, ", "))

	stillTouching := make(map[int]bool, len(overlapping))
	for _, i := range overlapping {
		stillTouching[i] = true
	}

	for i, l := range labels {
		switch {
		case l.Truncated && i == len(labels)-1:
			fmt.Fprintf(out, "label %s clipped to %s so it ends at the render's last frame\n",
				labelSummaryName(l), l.Video.Round(time.Millisecond))
		case l.Truncated:
			fmt.Fprintf(out, "label %s truncated to %s so it ends where the next label begins\n",
				labelSummaryName(l), l.Video.Round(time.Millisecond))
		}
		if transition > 0 && l.Video < 2*transition {
			fmt.Fprintf(out, "label %s is on screen for %s, shorter than twice --highlight-transition (%s); its name never reaches full opacity\n",
				labelSummaryName(l), l.Video.Round(time.Millisecond), transition)
		}
		if markersOnProfile && !hasProfileDistance(track, tl.Start(), l.At) {
			fmt.Fprintf(out, "label %s has no distance at its instant; it is not marked on the elevation profile\n", labelSummaryName(l))
		}
		if markersOnProfile && stillTouching[i] {
			fmt.Fprintf(out, "label %s is too close to a neighbouring label on the elevation profile; the two are not clearly separated\n", labelSummaryName(l))
		}
	}
}

// runFrames writes selected frames as PNGs instead of encoding.
//
// It runs the IDENTICAL render path -- only the sink differs -- which is what
// makes the fast visual loop a trustworthy proxy for the real render rather
// than a second implementation free to disagree with it.
//
// ctx is rctx, passed through for writeGaugeSummary -- see runVideo's own
// doc comment for why.
func runFrames(cmd *cobra.Command, r *render.Renderer, ctx *panel.Context, in renderInputs) error {
	indices, err := frameIndices(in.tl, r.LastFrameWithSample(), renderOpts.frameAt, renderOpts.frameAtVideo, in.highlights, in.labels)
	if err != nil {
		return err
	}
	dir := renderOpts.outputDir

	sink, err := encode.OpenPNGFrames(dir, indices)
	if err != nil {
		return err
	}
	defer sink.Close()

	// Stopped before anything else is written, always: the display owns the
	// bottom of the terminal until it is told otherwise, and a summary
	// printed underneath a live bar lands inside the region the next redraw
	// would erase. Stop is idempotent, so the deferred call covers the paths
	// that return early.
	d, bar := newProgress(cmd, r.Frames())
	defer d.Stop()
	if err := render.Run(cmd.Context(), r, sink, func(i, n int) { bar.Set(int64(i)) }); err != nil {
		d.Stop()
		return err
	}
	d.Stop()
	if err := sink.Close(); err != nil {
		return err
	}
	for _, p := range sink.Written() {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", p)
	}
	writeMergeSummary(cmd, in.track, in.sources)
	writePanelSummary(cmd, r, in.tl, in.layoutName, in.theme.Name, in.smoothing)
	writeClockSummary(cmd, ctx)
	writePauseSummary(cmd, ctx, in)
	writeGaugeSummary(cmd, r, ctx)
	writeGaugeSelectionSummary(cmd, r, ctx)
	writeElevationSummary(cmd, in.elevation, in.elevationSource)
	// The fast visual loop is where a transition actually gets looked at
	// (see --frame-at-video), so it gets the same base/highlight
	// decomposition and warnings the video path prints -- otherwise the one
	// command built for checking a highlight would say nothing about it.
	markersOnProfile := markersAbsorbedIntoProfile(r)
	writeHighlightSummary(cmd, in.tl, in.track, in.highlights, in.smoothing, in.theme, markersOnProfile)
	writeLabelSummary(cmd, in.tl, in.track, in.labels, renderOpts.highlightTransition, markersOnProfile, r.OverlappingLabels())
	return nil
}

// frameIndices picks which frames --frames writes: the landmarks, plus
// anything --frame-at or --frame-at-video named.
//
// The landmarks are the first, the quarters, the last, and -- when any
// highlight or label is configured -- each one's own first and last frame.
// Frame 0 and the last frame are there because panels that accumulate -- a
// progress bar, a covered route, a splits list -- are wrong at the
// boundaries far more often than in the middle, and frame 0 in particular
// hits every "nothing has happened yet" branch at once. A highlight's or a
// label's own boundaries are there for the same reason: that is exactly
// where a panel gets a transition wrong, and each one creates two new
// boundaries the quarters will not land near on a long render.
func frameIndices(tl panel.Timeline, lastFrame int, at, atVideo []time.Duration, highlights []panel.Highlight, labels []panel.Label) ([]int, error) {
	n := tl.Frames()
	// lastFrame is the last frame that DRAWS something, not n-1. The two
	// differ when the session's declared window outlasts the final record --
	// see render.Renderer.LastFrameWithSample, which is where that question
	// is answered once. Writing n-1 here handed the user an "end state"
	// landmark on which every gauge showed its placeholder, and the elevation
	// profile's fill washed absent across the whole axis: honest about that
	// instant, and useless as the frame you reach for to see how the render
	// finished.
	out := []int{0, n / 4, n / 2, (3 * n) / 4, lastFrame}
	for _, h := range highlights {
		out = append(out, highlightLandmarkFrames(tl, h)...)
	}
	for _, l := range labels {
		out = append(out, labelLandmarkFrames(l)...)
	}
	// Each --pauses skip seam is two landmarks: the frame the splice lands
	// on, where the route dot jumps and the elapsed clock steps forward, and
	// the middle of its notice, where the "PAUSED" card is at full
	// strength. The seam frame alone would not show the card at all -- the
	// ramp starts at zero there, exactly as a label's name does at its own
	// instant -- and the card is the half of this feature most worth
	// looking at before committing to a whole encode.
	for _, c := range tl.Cuts() {
		out = append(out, c.FirstFrame, (c.FirstFrame+c.LastFrame)/2)
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
		// Bounded by the activity's own elapsed extent, NOT by how much of
		// it the frames cover: --frame-at is an offset into the activity
		// (see the flag's own help text), and under --pauses skip the
		// frames cover less than the activity ran. Comparing against
		// ActivityDuration there would refuse an offset plainly inside the
		// recording -- see Timeline.ActivitySpan.
		if d > tl.ActivitySpan() {
			return nil, fmt.Errorf("render: --frame-at %v is past the end of the activity, which runs %s",
				d, panel.FormatClock(tl.ActivitySpan()))
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
	out := plannedOutputPath(activity, explicit, dir, ext)
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
