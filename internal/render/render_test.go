package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
)

// --- fixtures ---------------------------------------------------------------

// buildContext decodes a synthetic activity and assembles a render context
// over it. Every test here runs on a generated file: a real recording is
// personal data, and a committed one would also be an unreviewable binary blob.
func buildContext(t *testing.T, opts fittest.Options, w, h int, fps float64) *panel.Context {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivity(timer, fps)
	if err != nil {
		t.Fatalf("NewTimelineForActivity: %v", err)
	}
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: w, Height: h, FontScale: 0.05, Fonts: mustFaces(t),
	}
}

func shortOptions() fittest.Options {
	o := fittest.DefaultOptions()
	o.Count = 60
	return o
}

// markerPanel draws a fixed block in its box (static) and a block whose height
// tracks the frame index (dynamic).
//
// Both halves are real: a panel with no static content would let the
// static/dynamic equality test pass without ever exercising what it is for.
type markerPanel struct {
	name   string
	accept bool
}

func (p markerPanel) Name() string { return p.name }

func (p markerPanel) Accepts(*panel.Context) bool { return p.accept }

func (p markerPanel) Prepare(ctx *panel.Context, box panel.Box) panel.Painter {
	return &markerPainter{box: box, frames: ctx.Timeline.Frames()}
}

type markerPainter struct {
	box    panel.Box
	frames int
}

func (m *markerPainter) Static(c *panel.Canvas) {
	// Chrome: a rule across the top of the box.
	c.Rect(panel.Box{X: m.box.X, Y: m.box.Y, W: m.box.W, H: 3}, c.Theme.Dim)
}

func (m *markerPainter) Dynamic(c *panel.Canvas, f panel.Frame) {
	frac := float64(f.Index+1) / float64(m.frames)

	// A growing bar, and a SWEEPING marker. The sweep is what makes a
	// forgotten static-base restore visible: a bar that only grows paints over
	// where it used to be, so frames still differ from one another and an
	// accumulating render looks fine. A marker that moves leaves a trail.
	h := m.box.H * 0.5 * frac
	c.Rect(panel.Box{X: m.box.X, Y: m.box.Y + m.box.H - h, W: m.box.W, H: h}, c.Theme.Accent)

	const marker = 6
	x := m.box.X + (m.box.W-marker)*frac
	c.Rect(panel.Box{X: x, Y: m.box.Y + m.box.H/2, W: marker, H: marker}, c.Theme.Foreground)

	if !f.HasSample {
		c.Rect(panel.Box{X: m.box.X, Y: m.box.Y + 10, W: 8, H: 8}, c.Theme.Absent)
	}
}

func oneMarkerLayout(panels ...panel.Panel) panel.Layout {
	children := make([]panel.Slot, len(panels))
	for i, p := range panels {
		children[i] = panel.Slot{Panel: p}
	}
	return panel.Layout{Name: "test", FontScale: 0.05, Root: panel.Slot{Dir: panel.Row, Children: children}}
}

// recordingSink keeps every frame it is given, so a render test needs no
// ffmpeg. This is the payoff of internal/encode knowing nothing about panels.
type recordingSink struct {
	frames []*image.RGBA
	closed int
}

func (s *recordingSink) WriteFrame(img *image.RGBA) error {
	cp := image.NewRGBA(img.Bounds())
	copy(cp.Pix, img.Pix)
	s.frames = append(s.frames, cp)
	return nil
}

func (s *recordingSink) Close() error { s.closed++; return nil }

// --- the test this design exists for ---------------------------------------

// TestRenderer_StaticPlusDynamicEqualsRenderExactly is the automated catch for
// the trap the whole architecture is shaped around.
//
// Render draws background, static and dynamic together. The frame loop instead
// rasterizes the static layer ONCE and composites the dynamic pass onto a copy
// of it. Those two must produce identical pixels. If a panel's static content
// ever comes to depend on something that differs between the paths -- state
// that is present when Render is called and absent when RenderStatic is --
// the images diverge and this fails with a pixel count, rather than the
// disagreement shipping silently in a video.
//
// This project's sibling has both paths and never compares them, which is how
// the axis-origin bug lived there long enough to earn a twenty-line warning.
func TestRenderer_StaticPlusDynamicEqualsRenderExactly(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 320, 180, 10)
	r, err := New(ctx, oneMarkerLayout(
		markerPanel{name: "a", accept: true},
		markerPanel{name: "b", accept: true},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	base := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	if err := r.RenderStatic(base); err != nil {
		t.Fatalf("RenderStatic: %v", err)
	}

	// Several indices, including the boundaries, because a divergence can
	// depend on the frame.
	for _, i := range []int{0, 1, r.Frames() / 2, r.Frames() - 2, r.Frames() - 1} {
		f := r.Frame(i)

		whole := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(whole, f); err != nil {
			t.Fatalf("Render(%d): %v", i, err)
		}

		split := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		copy(split.Pix, base.Pix)
		if err := r.RenderDynamic(split, f); err != nil {
			t.Fatalf("RenderDynamic(%d): %v", i, err)
		}

		if !bytes.Equal(whole.Pix, split.Pix) {
			t.Errorf("frame %d: the one-shot and static+dynamic paths differ in %d pixels",
				i, countDiff(whole, split))
		}
	}
}

func countDiff(a, b *image.RGBA) int {
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] {
			n++
		}
	}
	return n
}

// TestRenderer_StaticLayerIsNotEmpty keeps the equality test above honest.
//
// Two paths that both draw nothing are trivially identical. If the fixture
// panel's static content ever stopped landing, the comparison would keep
// passing while testing nothing at all.
func TestRenderer_StaticLayerIsNotEmpty(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 320, 180, 10)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	base := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	if err := r.RenderStatic(base); err != nil {
		t.Fatal(err)
	}
	bg := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	c, err := panel.NewCanvas(bg, 10, panel.DefaultTheme(), mustFaces(t))
	if err != nil {
		t.Fatal(err)
	}
	c.Fill(panel.DefaultTheme().Background)
	if bytes.Equal(base.Pix, bg.Pix) {
		t.Fatal("the static layer is indistinguishable from a plain background; the equality test proves nothing")
	}
}

func mustFaces(t *testing.T) *panel.FaceCache {
	t.Helper()
	f, err := panel.NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// --- frame state ------------------------------------------------------------

// TestRenderer_AbsentSampleIsTheZeroSample pins the invariant every panel's
// single presence check depends on.
//
// A panel writes `if f.Sample.HasHeartRate { ... } else { placeholder }` and is
// correct inside a dropout WITHOUT a second condition, but only because every
// presence flag on a zero Sample is false. If the loop ever held the last good
// sample instead -- the obvious change to stop a readout flickering -- that
// reasoning collapses and a frozen reading becomes indistinguishable from a
// live one.
func TestRenderer_AbsentSampleIsTheZeroSample(t *testing.T) {
	opts := shortOptions()
	opts.Count = 600
	// A pause writes no records, so the timeline crosses a stretch with no
	// samples at all -- the same shape as a GPS dropout, and a real one.
	opts.Pauses = []fittest.Pause{{Start: 100 * time.Second, End: 200 * time.Second}}

	ctx := buildContext(t, opts, 160, 120, 5)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var absent, present int
	for i := 0; i < r.Frames(); i++ {
		f := r.Frame(i)
		if f.HasSample {
			present++
			continue
		}
		absent++
		// DeepEqual rather than ==: Sample carries a developer-field map and
		// is not a comparable type. The map matters here as much as the
		// scalars -- a stale DevFields would leave a Stryd readout live
		// through a dropout while every standard field correctly went blank.
		if !reflect.DeepEqual(f.Sample, fitactivity.Sample{}) {
			t.Fatalf("frame %d has HasSample=false but a non-zero Sample; a stale reading is being held", i)
		}
	}
	if absent == 0 {
		t.Fatal("no frame fell in the gap; the fixture cannot exercise the invariant")
	}
	if present == 0 {
		t.Fatal("no frame found a sample; the fixture is broken")
	}
	t.Logf("%d frames absent, %d present", absent, present)
}

// TestRenderer_FrameCarriesBothClocksAndThePauseFlag checks the per-frame
// state is actually populated rather than left at its zero value -- a Frame
// built with only Index and At set would satisfy any test that only looked at
// timing.
func TestRenderer_FrameCarriesBothClocksAndThePauseFlag(t *testing.T) {
	opts := shortOptions()
	opts.Count = 600
	opts.Pauses = []fittest.Pause{{Start: 100 * time.Second, End: 200 * time.Second}}

	ctx := buildContext(t, opts, 160, 120, 5)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var sawPaused, sawRunning bool
	var maxElapsed, maxActive time.Duration
	for i := 0; i < r.Frames(); i++ {
		f := r.Frame(i)
		if f.Paused {
			sawPaused = true
		} else {
			sawRunning = true
		}
		if f.Elapsed > maxElapsed {
			maxElapsed = f.Elapsed
		}
		if f.Active > maxActive {
			maxActive = f.Active
		}
		if f.Active > f.Elapsed {
			t.Fatalf("frame %d: active %v exceeds elapsed %v", i, f.Active, f.Elapsed)
		}
	}
	if !sawPaused || !sawRunning {
		t.Fatalf("swept the render and saw paused=%v running=%v; both must occur for this to mean anything", sawPaused, sawRunning)
	}
	// The fixture pauses for 100 s, so active must end up about that much
	// short of elapsed. Derived from the fixture, not read off a run.
	if gap := maxElapsed - maxActive; gap < 95*time.Second || gap > 105*time.Second {
		t.Errorf("elapsed ends at %v and active at %v, a gap of %v; want about the fixture's 100s pause",
			maxElapsed, maxActive, gap)
	}
}

// --- placement and declining ------------------------------------------------

// TestNew_DecliningPanelIsPrunedAndAnnounced checks both halves of the
// absent-data policy at this layer: the layout closes up, AND the panel's
// absence is recorded so the render summary can name it.
//
// The second half matters as much as the first. A panel that vanishes with no
// explanation leaves "no unexplained holes" true in the pixels and false in
// the user's understanding of them.
func TestNew_DecliningPanelIsPrunedAndAnnounced(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	r, err := New(ctx, oneMarkerLayout(
		markerPanel{name: "keeps", accept: true},
		markerPanel{name: "declines", accept: false},
		markerPanel{name: "also-keeps", accept: true},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := len(r.Placed()); got != 2 {
		t.Fatalf("placed %d panels, want 2", got)
	}
	for _, p := range r.Placed() {
		if p.Panel.Name() == "declines" {
			t.Error("a declining panel was placed")
		}
		// Two survivors of three share a 300px row: 150 each.
		if p.Box.W < 149 || p.Box.W > 151 {
			t.Errorf("%s is %g wide, want ~150 -- the survivors must grow into the gap", p.Panel.Name(), p.Box.W)
		}
	}
	if got := r.Declined(); len(got) != 1 || got[0] != "declines" {
		t.Errorf("Declined() = %v, want [declines]", got)
	}
}

// TestNew_RejectsANilPainter turns a panel bug into a message naming the panel
// rather than a nil dereference in the middle of a render.
func TestNew_RejectsANilPainter(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 200, 100, 10)
	_, err := New(ctx, oneMarkerLayout(nilPainterPanel{}), panel.DefaultTheme())
	if err == nil {
		t.Fatal("New accepted a panel that prepared a nil painter")
	}
	if !contains(err.Error(), "nil-painter") {
		t.Errorf("the error should name the panel; got: %v", err)
	}
}

type nilPainterPanel struct{}

func (nilPainterPanel) Name() string                                    { return "nil-painter" }
func (nilPainterPanel) Accepts(*panel.Context) bool                     { return true }
func (nilPainterPanel) Prepare(*panel.Context, panel.Box) panel.Painter { return nil }

func contains(s, sub string) bool {
	return len(s) >= len(sub) && bytes.Contains([]byte(s), []byte(sub))
}

// --- the loop ---------------------------------------------------------------

// TestRun_WritesEveryFrameAndTheyDiffer is the end-to-end check on the loop,
// against an in-memory sink so it needs no ffmpeg.
//
// The frames must DIFFER from one another. A loop that rendered frame 0 and
// then wrote the same buffer repeatedly -- forgetting to restore the static
// base, or reusing a stale frame -- would produce the right count and a video
// of the right length showing a frozen dashboard, which is precisely the
// failure a frame count cannot see.
func TestRun_WritesEveryFrameAndTheyDiffer(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 160, 90, 10)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sink := &recordingSink{}
	var lastReported int
	if err := Run(context.Background(), r, sink, func(i, n int) { lastReported = i }); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := len(sink.frames), r.Frames(); got != want {
		t.Fatalf("wrote %d frames, want %d", got, want)
	}
	if lastReported != r.Frames() {
		t.Errorf("progress ended at %d of %d", lastReported, r.Frames())
	}
	if sink.closed != 0 {
		t.Error("Run closed the sink; closing it is the caller's, because Close is the encode's verdict")
	}

	distinct := 0
	for i := 1; i < len(sink.frames); i++ {
		if !bytes.Equal(sink.frames[i].Pix, sink.frames[i-1].Pix) {
			distinct++
		}
	}
	if distinct < len(sink.frames)/2 {
		t.Errorf("only %d of %d consecutive frames differ; the dashboard is not animating",
			distinct, len(sink.frames)-1)
	}

	// Every frame the loop wrote must equal the same frame drawn from
	// scratch. This is what catches an accumulating render -- one that forgets
	// to restore the static base before compositing, so each frame carries
	// every previous frame's dynamic content as a trail.
	//
	// Asserting only that consecutive frames DIFFER cannot see it: an
	// accumulating render differs frame to frame just as a correct one does.
	// Verified -- with only that check in place, dropping the restore from the
	// loop still passed.
	for _, i := range []int{0, 1, len(sink.frames) / 2, len(sink.frames) - 1} {
		want := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(want, r.Frame(i)); err != nil {
			t.Fatalf("Render(%d): %v", i, err)
		}
		if !bytes.Equal(sink.frames[i].Pix, want.Pix) {
			t.Errorf("frame %d from the loop differs from the same frame drawn on its own, in %d pixels",
				i, countDiff(sink.frames[i], want))
		}
	}
}

// TestRun_StopsOnCancellation checks a cancelled context ends the render
// promptly and says where it stopped, rather than running to completion.
func TestRun_StopsOnCancellation(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 160, 90, 30)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{}
	stopAt := 5
	err = Run(cctx, r, sink, func(i, n int) {
		if i == stopAt {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("Run completed despite cancellation")
	}
	if len(sink.frames) > stopAt+1 {
		t.Errorf("wrote %d frames after cancelling at %d", len(sink.frames), stopAt)
	}
	if !contains(err.Error(), fmt.Sprint(r.Frames())) {
		t.Errorf("the error should say how far it got of how many; got: %v", err)
	}
}

// TestRun_ReportsAWriteFailureWithTheFrameNumber keeps a sink failure
// actionable: which frame, of how many.
func TestRun_ReportsAWriteFailureWithTheFrameNumber(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 80, 60, 10)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := Run(context.Background(), r, &failingSink{after: 3}, nil); err == nil {
		t.Fatal("Run reported success despite a failing sink")
	} else if !contains(err.Error(), "frame 3") {
		t.Errorf("the error should name the failing frame; got: %v", err)
	}
}

type failingSink struct {
	after int
	n     int
}

func (s *failingSink) Close() error { return nil }

func (s *failingSink) WriteFrame(*image.RGBA) error {
	if s.n >= s.after {
		return fmt.Errorf("sink is full")
	}
	s.n++
	return nil
}
