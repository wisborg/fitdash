package cmd

import (
	"fmt"
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
}

var renderOpts renderOptions

// bindRenderFlags attaches the render flags to the root command.
func bindRenderFlags(c *cobra.Command) {
	f := c.Flags()
	f.StringVar(&renderOpts.outputDir, "output-dir", ".", "directory for the rendered video; the name is derived from the activity")
	f.StringVarP(&renderOpts.output, "output", "o", "", "write to this exact path instead of deriving one")
	f.StringVar(&renderOpts.size, "size", "1920x1080", "output resolution; width and height must both be even")
	f.Float64Var(&renderOpts.fps, "fps", 30, "frames per second")
	f.IntVar(&renderOpts.crf, "crf", 20, "H.264 quality; lower is better (18-28 is the useful range)")
	f.BoolVar(&renderOpts.frames, "frames", false, "write landmark frames as PNG instead of encoding a video")
	f.DurationSliceVar(&renderOpts.frameAt, "frame-at", nil, "with --frames, also write the frame at this offset into the activity (repeatable, e.g. 12m30s)")
}

// runRender is the root command: fitdash ACTIVITY.fit.
func runRender(cmd *cobra.Command, args []string) error {
	w, h, err := parseSize(renderOpts.size)
	if err != nil {
		return err
	}

	activity := args[0]
	track, err := fitactivity.Decode(activity)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	timer := fitactivity.BuildTimerModel(track)
	timeline, err := panel.NewTimelineForActivity(timer, renderOpts.fps)
	if err != nil {
		return err
	}
	fonts, err := panel.NewFaceCache()
	if err != nil {
		return err
	}

	layout := panel.SelectLayout(w, h)
	rctx := &panel.Context{
		Track:     track,
		Report:    inspect.Build(track),
		Timer:     timer,
		Timeline:  timeline,
		Width:     w,
		Height:    h,
		FontScale: layout.FontScale,
		Fonts:     fonts,
	}
	r, err := render.New(rctx, layout, panel.DefaultTheme())
	if err != nil {
		return err
	}

	if renderOpts.frames {
		return runFrames(cmd, r, timeline, activity)
	}
	return runVideo(cmd, r, timeline, activity, w, h)
}

// runVideo encodes the whole render to a video file.
func runVideo(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, activity string, w, h int) error {
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

	if err := render.Run(cmd.Context(), r, sink, nil); err != nil {
		return err
	}
	if err := sink.Close(); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out)
	fmt.Fprintf(cmd.ErrOrStderr(), "%d frames, %s at %s fps, %dx%d\n",
		tl.Frames(), panel.FormatClock(tl.Duration()),
		strconv.FormatFloat(tl.FPS(), 'f', -1, 64), w, h)
	return nil
}

// runFrames writes selected frames as PNGs instead of encoding.
//
// It runs the IDENTICAL render path -- only the sink differs -- which is what
// makes the fast visual loop a trustworthy proxy for the real render rather
// than a second implementation free to disagree with it.
func runFrames(cmd *cobra.Command, r *render.Renderer, tl panel.Timeline, activity string) error {
	dir := renderOpts.outputDir
	if renderOpts.output != "" {
		dir = renderOpts.output
	}
	indices := frameIndices(tl, renderOpts.frameAt)

	sink, err := encode.OpenPNGFrames(dir, indices)
	if err != nil {
		return err
	}
	defer sink.Close()

	if err := render.Run(cmd.Context(), r, sink, nil); err != nil {
		return err
	}
	if err := sink.Close(); err != nil {
		return err
	}
	for _, p := range sink.Written() {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", p)
	}
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
func frameIndices(tl panel.Timeline, at []time.Duration) []int {
	n := tl.Frames()
	out := []int{0, n / 4, n / 2, (3 * n) / 4, n - 1}
	for _, d := range at {
		out = append(out, tl.IndexAt(d))
	}
	return out
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
