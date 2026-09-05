package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
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
	return contextFor(t, opts, w, h, fps)
}

// benchContext is buildContext at the shipping frame rate, for benchmarks.
func benchContext(b testing.TB, opts fittest.Options, w, h int) *panel.Context {
	return contextFor(b, opts, w, h, 30)
}

func contextFor(t testing.TB, opts fittest.Options, w, h int, fps float64) *panel.Context {
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := panel.NewTimelineForActivity(timer, fps, 1)
	if err != nil {
		t.Fatalf("NewTimelineForActivity: %v", err)
	}
	// Elevation is built here, once, the same way cmd/render.go now builds it
	// for a real render -- with no --elevation-smoothing/-gain/-loss typed, the
	// "auto" tuning panel.DefaultElevationTuning derives from the track's own
	// totals. ElevationPanel's Accepts and Prepare read this field rather than
	// building their own copy (see internal/panel/elevation.go), so a fixture
	// that left it nil would make every test below see an activity with no
	// elevation model regardless of what the fixture's own track carries.
	elevTuning := panel.DefaultElevationTuning(track)
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: w, Height: h, FontScale: 0.05, Fonts: mustFaces(t),
		Elevation: panel.BuildElevation(track, elevTuning),
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

func (s *recordingSink) WriteFrame(_ int, img *image.RGBA) error {
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

// stripElevationEdges clears HasElevation on ctx.Track.Samples[0:first] and
// ctx.Track.Samples[len-last:], leaving distance untouched, and rebuilds
// ctx.Report and ctx.Elevation to match -- the same rebuild withoutElevation
// (below) performs for "no elevation at all", narrowed to the two edges so
// the model gains a real PRE-DATA region at the start (elevation.go's own
// term: distance recorded, elevation not, because BuildElevationModel keeps
// only samples carrying both) and a real stretch PAST the model's own axis
// end at the finish, rather than losing the model outright.
func stripElevationEdges(ctx *panel.Context, first, last int) {
	for i := 0; i < first; i++ {
		ctx.Track.Samples[i].HasElevation = false
	}
	for i := len(ctx.Track.Samples) - last; i < len(ctx.Track.Samples); i++ {
		ctx.Track.Samples[i].HasElevation = false
	}
	ctx.Report = inspect.Build(ctx.Track)
	ctx.Elevation = panel.BuildElevation(ctx.Track, panel.DefaultElevationTuning(ctx.Track))
}

// firstFrameIndex returns the index of the first frame (0..r.Frames()-1)
// carrying a sample for which pred is true, and false when none does --
// callers treat that as a broken test fixture (see its own precondition
// checks), never as licence to guess an index instead.
func firstFrameIndex(r *Renderer, pred func(fitactivity.Sample) bool) (int, bool) {
	for i := 0; i < r.Frames(); i++ {
		f := r.Frame(i)
		if f.HasSample && pred(f.Sample) {
			return i, true
		}
	}
	return 0, false
}

// TestRenderer_StaticPlusDynamicEqualsRenderExactly_RealLayout closes a gap
// the test above leaves open: that test's own panels are markerPainter, a
// test double resolved against a synthetic one-row Layout, so ElevationPanel,
// ClimbPanel and GradientPanel -- the three panels whose own doc comments
// (see elevation.go's Dynamic, climb.go's Dynamic, gradient.go's "no easing"
// section) forbid caching a value across frames PRECISELY because this
// equality test is supposed to catch it -- were never actually resolved by
// either test in this file. A per-frame cache smuggled into any of the three
// would have sailed through both untouched.
//
// This resolves a REAL layout (panel.LandscapeLayout) over a REAL Context
// carrying a real elevation model, so all three panels sit in the tree this
// asserts against, and picks frame indices that visit every branch their own
// absence policy has to answer honestly rather than from a cache: the very
// first frame, one still inside the pre-data region, the midpoint, one past
// the elevation model's own axis end (the "known, honestly" case past
// TotalDistance -- see elevation.go's Dynamic doc comment), and the very
// last frame. docs/architecture.md's "equality test" section names this case
// explicitly; keep the two in sync if either changes.
func TestRenderer_StaticPlusDynamicEqualsRenderExactly_RealLayout(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 640, 360, 10)
	// 3 of 60 samples at each edge: enough width, in this synthetic
	// constant-pace track, for the pre-data and past-axis-end regions to
	// span several frames rather than a single one this test could not
	// reliably land a frame inside of.
	const stripFirst, stripLast = 3, 3
	stripElevationEdges(ctx, stripFirst, stripLast)
	if ctx.Elevation == nil || ctx.Elevation.Empty() {
		t.Fatal("precondition: the fixture must still carry an elevation model after stripping only its edges")
	}
	profileStart, axisEnd := ctx.Elevation.StartDistance(), ctx.Elevation.TotalDistance()
	if profileStart <= 0 {
		t.Fatalf("precondition: profileStart = %v, want > 0 (a real pre-data region)", profileStart)
	}

	r, err := New(ctx, panel.LandscapeLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	preDataIdx, ok := firstFrameIndex(r, func(s fitactivity.Sample) bool {
		return s.HasDistance && s.Distance > 0 && s.Distance < profileStart
	})
	if !ok {
		t.Fatal("precondition: no frame found inside the pre-data region")
	}
	pastEndIdx, ok := firstFrameIndex(r, func(s fitactivity.Sample) bool {
		return s.HasDistance && s.Distance > axisEnd
	})
	if !ok {
		t.Fatal("precondition: no frame found past the elevation model's own axis end")
	}

	base := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	if err := r.RenderStatic(base); err != nil {
		t.Fatalf("RenderStatic: %v", err)
	}

	for _, i := range []int{0, preDataIdx, r.Frames() / 2, pastEndIdx, r.Frames() - 1} {
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
			t.Errorf("frame %d: the one-shot and static+dynamic paths differ in %d pixels under the real landscape layout",
				i, countDiff(whole, split))
		}
	}
}

// --- --gauges' default must draw nothing new --------------------------------

// oldLandscapeLayout is a frozen, hand-copied snapshot of
// panel.LandscapeLayout's gauge block exactly as it stood before --gauges
// existed -- a plain Col of the four ordinary readouts, Weight 1, with no Alt
// and no balance candidate -- with every OTHER slot copied verbatim from the
// current tree (internal/panel/layouts.go) so this differs from it in
// exactly the one place --gauges touched. See
// TestNew_DefaultGaugesRendersIdenticallyToBeforeTheBalanceAltExisted, the
// only test that uses it.
func oldLandscapeLayout() panel.Layout {
	return panel.Layout{
		Name:      "landscape",
		Margin:    0.03,
		FontScale: 0.05,
		Root: panel.Slot{Dir: panel.Col, Children: []panel.Slot{
			{Dir: panel.Row, Weight: 4, Children: []panel.Slot{
				{Dir: panel.Col, Weight: 3, Children: []panel.Slot{
					{Panel: panel.RoutePanel{}, Weight: 3, Pad: 0.01},
					{Dir: panel.Row, Weight: 2, Children: []panel.Slot{
						{Panel: panel.ElapsedPanel{}, Weight: 2, Pad: 0.01},
						{Panel: panel.Distance(), Weight: 1, Pad: 0.01},
					}},
				}},
				{Dir: panel.Col, Weight: 1, Children: []panel.Slot{
					{Panel: panel.HeartRate(), Pad: 0.01},
					{Panel: panel.Pace(), Pad: 0.01},
					{Panel: panel.Power(), Pad: 0.01},
					{Panel: panel.Cadence(), Pad: 0.01},
				}},
			}},
			{Dir: panel.Row, Weight: 1, Children: []panel.Slot{
				{Panel: panel.ClimbPanel{}, Weight: 1, Pad: 0.01},
				{Panel: panel.GradientPanel{}, Weight: 1, Pad: 0.01},
			}},
			{Dir: panel.Alt, Weight: 2, Children: []panel.Slot{
				{Panel: panel.ElevationPanel{}, Pad: 0.01},
				{Panel: panel.Distance(), Pad: 0.01},
			}},
			{Panel: panel.MarkerPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}

// oldPortraitLayout is oldLandscapeLayout's own counterpart for
// panel.PortraitLayout: the two gauge rows (heart rate/pace, power/cadence)
// as separate children of the root Col, at Weight 2 each, exactly as they
// stood before this tree's own wrap into one Alt candidate (layouts.go, "the
// one structural change"). Every other slot is copied verbatim from the
// current tree.
func oldPortraitLayout() panel.Layout {
	return panel.Layout{
		Name:      "portrait",
		Margin:    0.03,
		FontScale: 0.05,
		Root: panel.Slot{Dir: panel.Col, Children: []panel.Slot{
			{Panel: panel.RoutePanel{}, Weight: 4, Pad: 0.01},
			{Dir: panel.Row, Weight: 2, Children: []panel.Slot{
				{Panel: panel.ElapsedPanel{}, Weight: 2, Pad: 0.01},
				{Panel: panel.Distance(), Weight: 1, Pad: 0.01},
			}},
			{Dir: panel.Row, Weight: 2, Children: []panel.Slot{
				{Panel: panel.HeartRate(), Pad: 0.01},
				{Panel: panel.Pace(), Pad: 0.01},
			}},
			{Dir: panel.Row, Weight: 2, Children: []panel.Slot{
				{Panel: panel.Power(), Pad: 0.01},
				{Panel: panel.Cadence(), Pad: 0.01},
			}},
			{Dir: panel.Row, Weight: 2, Children: []panel.Slot{
				{Panel: panel.ClimbPanel{}, Weight: 1, Pad: 0.01},
				{Panel: panel.GradientPanel{}, Weight: 1, Pad: 0.01},
			}},
			{Dir: panel.Alt, Weight: 3, Children: []panel.Slot{
				{Panel: panel.ElevationPanel{}, Pad: 0.01},
				{Panel: panel.Distance(), Pad: 0.01},
			}},
			{Panel: panel.MarkerPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}

// TestNew_DefaultGaugesRendersIdenticallyToBeforeTheBalanceAltExisted is the
// pixel-identity half of proving --gauges' own wiring
// (internal/panel/gauges.go) changed nothing about a default render.
// oldLandscapeLayout and oldPortraitLayout, above, are frozen snapshots of
// the two real trees' gauge block exactly as it stood before this flag
// existed, compared byte for byte against the CURRENT
// panel.LandscapeLayout/PortraitLayout under Context.Gauges left at its zero
// value (GaugesMetrics' own behaviour).
//
// This is what actually PROVES PortraitLayout's own wrap (layouts.go, "the
// one structural change") is weight-neutral, rather than merely asserting it
// in a comment: a wrap that silently changed either row's own share of the
// frame would move real pixels here. The generic tiling checks in
// layout_test.go only prove the placed boxes cover the frame with no gap and
// no overlap -- a real property, but one a DIFFERENT-but-still-valid split
// would also satisfy, which is exactly why this test compares against the
// old tree instead of only asserting the new one tiles.
func TestNew_DefaultGaugesRendersIdenticallyToBeforeTheBalanceAltExisted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		old, cur panel.Layout
		w, h     int
	}{
		{"landscape", oldLandscapeLayout(), panel.LandscapeLayout(), 640, 360},
		{"portrait", oldPortraitLayout(), panel.PortraitLayout(), 360, 640},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildContext(t, shortOptions(), tc.w, tc.h, 10)
			// ctx.Gauges left at its zero value on purpose: GaugesMetrics' own
			// behaviour, so an unset Context -- and every caller from before
			// --gauges existed -- must render exactly as it always has.

			oldR, err := New(ctx, tc.old, panel.DefaultTheme())
			if err != nil {
				t.Fatalf("New(old): %v", err)
			}
			curR, err := New(ctx, tc.cur, panel.DefaultTheme())
			if err != nil {
				t.Fatalf("New(current): %v", err)
			}

			oldBase := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
			if err := oldR.RenderStatic(oldBase); err != nil {
				t.Fatalf("RenderStatic(old): %v", err)
			}
			curBase := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
			if err := curR.RenderStatic(curBase); err != nil {
				t.Fatalf("RenderStatic(current): %v", err)
			}
			if !bytes.Equal(oldBase.Pix, curBase.Pix) {
				t.Errorf("static base differs in %d pixels between the pre-\"--gauges\" gauge block and the "+
					"current --gauges Alt under its default", countDiff(oldBase, curBase))
			}

			for _, i := range []int{0, oldR.Frames() / 2, oldR.Frames() - 1} {
				oldImg := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
				if err := oldR.Render(oldImg, oldR.Frame(i)); err != nil {
					t.Fatalf("Render(old, %d): %v", i, err)
				}
				curImg := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
				if err := curR.Render(curImg, curR.Frame(i)); err != nil {
					t.Fatalf("Render(current, %d): %v", i, err)
				}
				if !bytes.Equal(oldImg.Pix, curImg.Pix) {
					t.Errorf("frame %d differs in %d pixels between the pre-\"--gauges\" gauge block and the "+
						"current --gauges Alt under its default", i, countDiff(oldImg, curImg))
				}
			}
		})
	}
}

// buildHighlightContext is buildContext with one highlight configured and
// --highlight-style wash selected, so a test built on it exercises the one
// style that renders TWO static bases and blends them per frame -- the only
// thing this feature added to internal/render that the plain equality test
// above cannot see, since it never configures a highlight at all.
func buildHighlightContext(t *testing.T, w, h int, fps float64) (*panel.Context, panel.Timeline) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, shortOptions()); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	highlights := []panel.Highlight{{Name: "test highlight", From: 5 * time.Second, To: 15 * time.Second, RateFactor: 3}}
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, fps, 1, highlights)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	ctx := &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: w, Height: h, FontScale: 0.05, Fonts: mustFaces(t),
		Highlights: highlights, HighlightStyle: panel.HighlightStyleWash, HighlightTransition: 200 * time.Millisecond,
	}
	return ctx, tl
}

// highlightMarkerLayout is oneMarkerLayout with a real margin, so the
// highlighted equality test below also exercises the border overlay --
// drawn inside that margin, see drawHighlightBorder -- at the same time as
// the wash blend, rather than only one of the two effects this feature added
// here. A zero margin (oneMarkerLayout's own) makes the border a no-op, which
// would leave it untested by this file.
func highlightMarkerLayout(panels ...panel.Panel) panel.Layout {
	l := oneMarkerLayout(panels...)
	l.Margin = 0.05
	return l
}

// TestRenderer_StaticPlusDynamicEqualsRenderExactly_HighlightWash extends the
// equality test above to a highlighted frame under --highlight-style wash --
// the one style that can break the invariant, and the reason it ships last
// among this feature's steps (see docs/architecture.md).
//
// The split path here is NOT the naive "RenderStatic once, then
// RenderDynamic" the plain test above uses: Run's own fast loop blends TWO
// precomputed static bases by Frame.IntervalWeight before compositing the
// dynamic pass (see blendBases and renderStaticWash), so that is what this
// test replicates by hand and compares against Render's single-call path --
// which resolves the same blend itself, in renderBase, on every call. If the
// two ever disagreed, this is what would catch it: a pixel count, not a
// video that quietly differs depending on which path rendered it.
func TestRenderer_StaticPlusDynamicEqualsRenderExactly_HighlightWash(t *testing.T) {
	ctx, tl := buildHighlightContext(t, 320, 180, 10)
	r, err := New(ctx, highlightMarkerLayout(
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
	wash := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	if err := r.renderStaticWash(wash); err != nil {
		t.Fatalf("renderStaticWash: %v", err)
	}

	// Frames spanning the highlight's own entrance and exit transitions, plus
	// its middle, since IntervalWeight -- and therefore the wash blend and
	// the border's own alpha -- varies across exactly those. A frame outside
	// the highlight cannot exercise either.
	first := tl.IndexAt(5 * time.Second)
	last := tl.IndexAt(15*time.Second - time.Nanosecond)
	if last <= first {
		t.Fatalf("precondition: the fixture highlight occupies %d frames, want more than one to see a transition", last-first+1)
	}
	mid := (first + last) / 2
	for _, i := range []int{first, first + 1, mid, last - 1, last} {
		f := r.Frame(i)
		if f.Interval == panel.NoHighlight {
			t.Fatalf("frame %d fell outside the fixture's own highlight; the test fixture is wrong", i)
		}

		whole := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(whole, f); err != nil {
			t.Fatalf("Render(%d): %v", i, err)
		}

		split := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		blendBases(split, base, wash, f.IntervalWeight)
		if err := r.RenderDynamic(split, f); err != nil {
			t.Fatalf("RenderDynamic(%d): %v", i, err)
		}

		if !bytes.Equal(whole.Pix, split.Pix) {
			t.Errorf("frame %d (weight %.3f): the one-shot and static+dynamic paths differ in %d pixels under --highlight-style wash",
				i, f.IntervalWeight, countDiff(whole, split))
		}
	}
}

// buildTwoColourHighlightContext is buildHighlightContext with a SECOND
// highlight, each carrying its own distinct background=, so a test built on
// it can exercise Run's colour-keyed table of static wash bases (see
// washBaseFor) with more than one entry. The single-highlight fixture above
// can never do this: with only one highlight, the table can only ever hold
// one entry, so it cannot distinguish a table indexed correctly from one
// indexed by something else entirely (the highlight's own slice index, say,
// or "whichever colour was built last") that happens to work when there is
// only one possible answer.
func buildTwoColourHighlightContext(t *testing.T, w, h int, fps float64) (*panel.Context, panel.Timeline, []panel.Highlight) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, shortOptions()); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	highlights := []panel.Highlight{
		{
			Name: "first", From: 5 * time.Second, To: 15 * time.Second, RateFactor: 3,
			Background: color.NRGBA{R: 0x1B, G: 0x2A, B: 0x4A, A: 0xFF}, HasBackground: true,
		},
		{
			Name: "second", From: 20 * time.Second, To: 30 * time.Second, RateFactor: 3,
			Background: color.NRGBA{R: 0xC8, G: 0x40, B: 0x20, A: 0xFF}, HasBackground: true,
		},
	}
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, fps, 1, highlights)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	ctx := &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: w, Height: h, FontScale: 0.05, Fonts: mustFaces(t),
		Highlights: highlights, HighlightStyle: panel.HighlightStyleWash, HighlightTransition: 200 * time.Millisecond,
	}
	return ctx, tl, highlights
}

// TestRun_TwoDifferentlyColouredHighlightsMatchRenderAtAFrameInsideEach is
// the gate the two-colour case needs, per docs/architecture.md and this
// feature's own build plan: a table indexed by the wrong thing fails HERE
// and only here.
//
// It runs the real fast loop (Run, which builds and reads r.washBases, the
// colour-keyed table -- see washBaseFor) end to end, then compares a frame
// deep inside EACH highlight against Render, the independent simple path,
// which never reads that table at all and recomputes its own base from
// washColorFor on every call (see renderBase). If the table were keyed by
// the highlight's own index instead of its resolved colour, or collapsed
// two distinct colours into one entry, at least one of the two frames below
// would blend toward the wrong colour in Run's output while Render kept
// resolving its own correctly, and the two would diverge in exactly the
// pixels that colour touches.
//
// Verified load-bearing by hand while writing this test: temporarily keying
// r.washBases in Run on the highlight's slice index rather than
// r.washColorFor(i) left every OTHER test in this file green (none of them
// configures two distinct colours) and made exactly this test fail, on the
// second highlight, with a nonzero pixel count.
func TestRun_TwoDifferentlyColouredHighlightsMatchRenderAtAFrameInsideEach(t *testing.T) {
	ctx, tl, highlights := buildTwoColourHighlightContext(t, 160, 90, 10)
	r, err := New(ctx, highlightMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if highlights[0].Background == highlights[1].Background {
		t.Fatal("precondition: the fixture's two highlights must use different colours, or this test cannot tell a correct table from a broken one")
	}

	sink := &recordingSink{}
	if err := Run(context.Background(), r, sink, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for n, h := range highlights {
		mid := (h.From + h.To) / 2
		i := tl.IndexAt(mid)
		f := r.Frame(i)
		if f.Interval != n {
			t.Fatalf("frame %d (t=%v) sits in interval %d, want highlight %d (%q); the fixture is wrong", i, mid, f.Interval, n, h.Name)
		}
		if f.IntervalWeight < 1 {
			t.Fatalf("frame %d has IntervalWeight %v, want 1 at this highlight's own middle; the fixture's transition is too wide for this fixture's rate", i, f.IntervalWeight)
		}

		want := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(want, f); err != nil {
			t.Fatalf("Render(%d): %v", i, err)
		}
		if !bytes.Equal(sink.frames[i].Pix, want.Pix) {
			t.Errorf("frame %d (highlight %q, colour %v): Run's fast loop differs from Render in %d pixels -- the wash-base table is indexed by the wrong thing",
				i, h.Name, h.Background, countDiff(sink.frames[i], want))
		}
	}
}

// buildSameColourHighlightContext is buildTwoColourHighlightContext with
// both highlights resolving to the exact SAME colour -- the fixture the
// two-different-colour test above cannot produce. That test proves the
// table is not indexed by the highlight's own slice index when the two
// colours DIFFER; it says nothing about what happens when they agree. A
// table indexed by highlight rather than by colour would still pass every
// pixel comparison in this file, including the one above, because each
// entry -- even a duplicate -- holds the right pixels regardless of how
// many entries exist. Only counting, or identity-checking, the table's own
// entries can catch that mistake, which is what the test below does.
func buildSameColourHighlightContext(t *testing.T, w, h int, fps float64) (*panel.Context, panel.Timeline, []panel.Highlight) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, shortOptions()); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	same := color.NRGBA{R: 0x1B, G: 0x2A, B: 0x4A, A: 0xFF}
	highlights := []panel.Highlight{
		{
			Name: "first", From: 5 * time.Second, To: 15 * time.Second, RateFactor: 3,
			Background: same, HasBackground: true,
		},
		{
			Name: "second", From: 20 * time.Second, To: 30 * time.Second, RateFactor: 3,
			Background: same, HasBackground: true,
		},
	}
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, fps, 1, highlights)
	if err != nil {
		t.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	ctx := &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: w, Height: h, FontScale: 0.05, Fonts: mustFaces(t),
		Highlights: highlights, HighlightStyle: panel.HighlightStyleWash, HighlightTransition: 200 * time.Millisecond,
	}
	return ctx, tl, highlights
}

// TestRun_TwoHighlightsWithTheSameColourShareOneWashBase pins the memory
// argument docs/architecture.md and this feature's own build plan make for
// keying the wash-base table by colour rather than by highlight: "k
// distinct wash colours cost k full-frame RGBA buffers", so two highlights
// that type the SAME background= must cost ONE buffer, not two. See
// buildSameColourHighlightContext's own doc comment for why no pixel
// comparison, including TestRun_TwoDifferentlyColouredHighlightsMatchRenderAtAFrameInsideEach
// above, can tell a table indexed by highlight from one indexed by colour
// in this case -- only inspecting the table itself can.
//
// A declining panel is included deliberately: washColorFor/washBaseFor are
// resolved in New from ctx.Highlights alone, before any panel's Prepare
// runs at all, so the wash table's own size must not depend on which
// panels were placed or declined -- a render with the marker strip alone,
// and nothing else accepting, must still tint its whole background under
// every highlight the user configured.
func TestRun_TwoHighlightsWithTheSameColourShareOneWashBase(t *testing.T) {
	ctx, tl, highlights := buildSameColourHighlightContext(t, 160, 90, 10)
	if highlights[0].Background != highlights[1].Background {
		t.Fatal("precondition: the fixture's two highlights must share one colour, or this test cannot tell a correct table from one indexed by highlight")
	}
	layout := highlightMarkerLayout(
		markerPanel{name: "a", accept: true},
		markerPanel{name: "declines", accept: false},
	)
	r, err := New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.Declined(); len(got) != 1 || got[0] != "declines" {
		t.Fatalf("Declined() = %v, want [declines]", got)
	}

	sink := &recordingSink{}
	if err := Run(context.Background(), r, sink, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(r.washBases); got != 1 {
		t.Fatalf("Run built %d wash bases for two highlights sharing one colour, want 1 -- "+
			"a table indexed by highlight rather than colour would build one entry per highlight regardless of whether their colours agree", got)
	}
	if r.washBaseFor(0) != r.washBaseFor(1) {
		t.Error("the two highlights' own wash bases are different buffers; two highlights naming the same colour must share ONE, not build two")
	}

	// Belt and suspenders: correctness at the pixel level too, exactly like
	// the two-different-colour test above, so this test cannot pass merely
	// because it stopped checking pixels.
	for n, h := range highlights {
		mid := (h.From + h.To) / 2
		i := tl.IndexAt(mid)
		f := r.Frame(i)
		if f.Interval != n {
			t.Fatalf("frame %d sits in interval %d, want highlight %d (%q); the fixture is wrong", i, f.Interval, n, h.Name)
		}
		want := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(want, f); err != nil {
			t.Fatalf("Render(%d): %v", i, err)
		}
		if !bytes.Equal(sink.frames[i].Pix, want.Pix) {
			t.Errorf("frame %d (highlight %q): Run's fast loop differs from Render in %d pixels",
				i, h.Name, countDiff(sink.frames[i], want))
		}
	}
}

// TestRenderer_HighlightBorderRampsAndStaysInsideTheMargin pins step 6's own
// contract for the accent border: it draws nothing outside a highlight, it
// ramps rather than cutting (a mid-transition frame is neither absent nor at
// full strength), it draws nothing at all under --highlight-style none, and
// every pixel it touches sits within the layout's own margin -- the region
// Layout.Resolve guarantees carries no panel's Box, which is what makes this
// overlay collision-proof by construction rather than by convention.
func TestRenderer_HighlightBorderRampsAndStaysInsideTheMargin(t *testing.T) {
	ctx, tl := buildHighlightContext(t, 200, 120, 10)
	ctx.HighlightStyle = panel.HighlightStyleBorder
	layout := highlightMarkerLayout(markerPanel{name: "a", accept: true})
	r, err := New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.marginPx <= 0 {
		t.Fatal("precondition: the test layout must carry a margin for the border to draw inside of")
	}

	// The point on the frame this test samples: the middle of the top
	// border's own stroke, computed from the same constants
	// drawHighlightBorder itself uses rather than a pixel guessed from the
	// test -- a hardcoded offset would silently stop meaning "inside the
	// border" the moment either constant changed.
	inset := r.marginPx * highlightBorderInsetFraction
	thick := r.marginPx * highlightBorderThicknessFraction
	x, y := ctx.Width/2, int(inset+thick/2)

	sample := func(f panel.Frame) color.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(img, f); err != nil {
			t.Fatalf("Render: %v", err)
		}
		return img.RGBAAt(x, y)
	}
	bg := color.RGBAModel.Convert(panel.DefaultTheme().Background).(color.RGBA)
	highlightRGBA := color.RGBAModel.Convert(panel.DefaultTheme().Highlight).(color.RGBA)
	sqDist := func(a, b color.RGBA) float64 {
		dr, dg, db := float64(a.R)-float64(b.R), float64(a.G)-float64(b.G), float64(a.B)-float64(b.B)
		return dr*dr + dg*dg + db*db
	}

	// Outside the highlight -- the fixture's own starts at 5s -- the border
	// draws nothing: the sampled pixel is bare background.
	outside := r.Frame(0)
	if outside.Interval != panel.NoHighlight {
		t.Fatal("precondition: frame 0 must sit outside the fixture's own highlight, which starts at 5s")
	}
	if got := sample(outside); got != bg {
		t.Errorf("outside the highlight the border's own pixel is %v, want the background %v", got, bg)
	}

	// One frame into the highlight -- inside its 200ms entrance transition at
	// this fixture's 10fps -- IntervalWeight is a mid-ramp value, neither 0
	// nor 1, and the sampled pixel must already have moved off the
	// background without being a hard cut to the full highlight colour.
	entrance := r.Frame(tl.IndexAt(5*time.Second) + 1)
	if entrance.IntervalWeight <= 0 || entrance.IntervalWeight >= 1 {
		t.Fatalf("precondition: the entrance frame's weight is %v, want a mid-ramp value or this frame proves nothing about ramping", entrance.IntervalWeight)
	}
	entranceColor := sample(entrance)
	if entranceColor == bg {
		t.Error("the border drew nothing at the entrance frame, despite a positive IntervalWeight -- it is cutting rather than ramping")
	}

	// Further into the highlight, past the transition, IntervalWeight reaches
	// 1 and the sampled pixel must sit CLOSER to the theme's own Highlight
	// colour than the entrance frame's partial ramp did -- proving the ramp
	// actually progresses rather than reaching full strength immediately.
	full := r.Frame(tl.IndexAt(5*time.Second) + int(0.5*tl.FPS()))
	if full.IntervalWeight <= entrance.IntervalWeight {
		t.Fatalf("precondition: frame %d's weight %v should exceed the entrance frame's %v", full.Index, full.IntervalWeight, entrance.IntervalWeight)
	}
	fullColor := sample(full)
	if sqDist(fullColor, highlightRGBA) >= sqDist(entranceColor, highlightRGBA) {
		t.Errorf("the border did not ramp toward full strength: entrance pixel %v, later pixel %v, theme highlight %v",
			entranceColor, fullColor, highlightRGBA)
	}

	// --highlight-style none re-paces the highlighted stretch without
	// drawing the border at all -- the highlight strip panel is what still
	// marks it.
	noneCtx, noneTl := buildHighlightContext(t, 200, 120, 10)
	noneCtx.HighlightStyle = panel.HighlightStyleNone
	noneR, err := New(noneCtx, highlightMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	noneFrame := noneR.Frame(noneTl.IndexAt(5*time.Second) + int(0.5*noneTl.FPS()))
	if noneFrame.IntervalWeight <= 0 {
		t.Fatal("precondition: this frame must sit inside the highlight for --highlight-style none to be a real test of anything")
	}
	noneImg := image.NewRGBA(image.Rect(0, 0, noneCtx.Width, noneCtx.Height))
	if err := noneR.Render(noneImg, noneFrame); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := noneImg.RGBAAt(x, y); got != bg {
		t.Errorf("--highlight-style none drew something at the border's own pixel: %v, want the background %v", got, bg)
	}
}

// TestRenderer_NoHighlightsBorderStyleIsByteIdenticalToNoneStyle is the
// differential test the pixel-identity claim in internal/panel never
// actually exercises: TestNoHighlightRenderIsPixelIdenticalToBeforeThisFeature
// (internal/panel/highlight_panel_test.go) reimplements the frame loop by
// hand -- renderFullFrame there never calls drawHighlightBorder, because that
// function lives in this package and the loop it reimplements does not
// reach it. So a border overlay that painted something even with NO
// highlights configured at all (a bug in the r.marginPx<=0 or
// f.Interval==NoHighlight guards in drawHighlightBorder) could ship with
// every existing test green.
//
// Run through the real Renderer instead: with Context.Highlights nil (no
// --highlight given), --highlight-style border and --highlight-style none
// must produce byte-identical renders across several frames, since neither
// style has anything to mark.
func TestRenderer_NoHighlightsBorderStyleIsByteIdenticalToNoneStyle(t *testing.T) {
	base := buildContext(t, shortOptions(), 200, 120, 10)
	base.Highlights = nil // the whole point: nothing was configured at all

	borderCtx := *base
	borderCtx.HighlightStyle = panel.HighlightStyleBorder
	noneCtx := *base
	noneCtx.HighlightStyle = panel.HighlightStyleNone

	layout := highlightMarkerLayout(markerPanel{name: "a", accept: true})

	borderR, err := New(&borderCtx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New (border): %v", err)
	}
	noneR, err := New(&noneCtx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New (none): %v", err)
	}
	if borderR.marginPx <= 0 {
		t.Fatal("precondition: the test layout must carry a margin, or the two styles could never possibly differ and this test would prove nothing")
	}

	for _, i := range []int{0, borderR.Frames() / 2, borderR.Frames() - 1} {
		imgB := image.NewRGBA(image.Rect(0, 0, borderCtx.Width, borderCtx.Height))
		if err := borderR.Render(imgB, borderR.Frame(i)); err != nil {
			t.Fatalf("Render (border, frame %d): %v", i, err)
		}
		imgN := image.NewRGBA(image.Rect(0, 0, noneCtx.Width, noneCtx.Height))
		if err := noneR.Render(imgN, noneR.Frame(i)); err != nil {
			t.Fatalf("Render (none, frame %d): %v", i, err)
		}
		if !bytes.Equal(imgB.Pix, imgN.Pix) {
			t.Errorf("frame %d: --highlight-style border differs from --highlight-style none with NO highlights configured, in %d pixels; "+
				"with nothing to mark the two styles must be identical", i, countDiff(imgB, imgN))
		}
	}
}

// TestRenderer_HighlightBorderPixelsLieStrictlyWithinTheMarginBand is the
// margin-containment check with an INDEPENDENT sample point: unlike
// TestRenderer_HighlightBorderRampsAndStaysInsideTheMargin, this test never
// reads highlightBorderInsetFraction or highlightBorderThicknessFraction --
// the two constants drawHighlightBorder itself uses to place its stroke --
// so a mistake in either constant's own arithmetic cannot cancel out against
// the same mistake reappearing in the test that is supposed to catch it.
//
// Instead it isolates exactly which pixels the border touched (by diffing a
// render against the identical one under --highlight-style none, which
// draws no border at all) and checks each one's plain distance to the
// nearest frame edge against r.marginPx alone -- the one number
// Layout.Resolve itself insets by, and the fact this feature's own fix round
// exists to make structural (see Layout.MarginPx and render.New's own doc
// comment on marginPx).
func TestRenderer_HighlightBorderPixelsLieStrictlyWithinTheMarginBand(t *testing.T) {
	ctx, tl := buildHighlightContext(t, 240, 140, 10)
	ctx.HighlightStyle = panel.HighlightStyleBorder
	layout := highlightMarkerLayout(markerPanel{name: "a", accept: true})
	r, err := New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.marginPx <= 0 {
		t.Fatal("precondition: the test layout must carry a margin for the border to draw inside of")
	}

	noneCtx, noneTl := buildHighlightContext(t, 240, 140, 10)
	noneCtx.HighlightStyle = panel.HighlightStyleNone
	noneR, err := New(noneCtx, layout, panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New (none): %v", err)
	}

	i := tl.IndexAt(5*time.Second) + int(0.5*tl.FPS())
	f := r.Frame(i)
	if f.IntervalWeight <= 0 {
		t.Fatal("precondition: this frame must sit inside the fixture's own highlight")
	}
	ni := noneTl.IndexAt(5*time.Second) + int(0.5*noneTl.FPS())

	withBorder := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	if err := r.Render(withBorder, f); err != nil {
		t.Fatalf("Render: %v", err)
	}
	withoutBorder := image.NewRGBA(image.Rect(0, 0, noneCtx.Width, noneCtx.Height))
	if err := noneR.Render(withoutBorder, noneR.Frame(ni)); err != nil {
		t.Fatalf("Render (none): %v", err)
	}

	w, h := ctx.Width, ctx.Height
	margin := r.marginPx
	touched := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if withBorder.RGBAAt(x, y) == withoutBorder.RGBAAt(x, y) {
				continue
			}
			touched++
			distToEdge := math.Min(math.Min(float64(x), float64(w-1-x)), math.Min(float64(y), float64(h-1-y)))
			if distToEdge >= margin {
				t.Fatalf("the border touched pixel (%d,%d), %.2fpx from the nearest edge, which is outside the layout's own %.2fpx margin",
					x, y, distToEdge, margin)
			}
		}
	}
	if touched == 0 {
		t.Fatal("the border and none renders are pixel-identical; nothing isolates the border for this test to check")
	}
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

func mustFaces(t testing.TB) *panel.FaceCache {
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

// TestNew_BottomBandDistanceOmitsElevationWithoutConsultingAccepts is the
// render-layer half of --bottom-band=distance: panel/layout_test.go already
// proves Resolve composes correctly when "elevation" is rejected by name,
// but that test cannot see WHY a panel was rejected, only that it was. This
// checks the reason: with Context.BottomBand set to BottomBandDistance, a
// panel named exactly like ElevationPanel -- one that would ACCEPT, standing
// in for an activity that genuinely carries elevation -- is removed anyway,
// and is reported through Omitted(), never through Declined().
//
// That split matters on its own terms, not just as bookkeeping: Declined()
// existing for this panel would tell cmd's writePanelSummary (and, through
// it, a user) that the activity carries no elevation, which this test's own
// fixture proves false by construction (accept: true). Omitted() is the only
// list that can truthfully carry this name.
func TestNew_BottomBandDistanceOmitsElevationWithoutConsultingAccepts(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	ctx.BottomBand = panel.BottomBandDistance

	r, err := New(ctx, oneMarkerLayout(
		markerPanel{name: elevationPanelName, accept: true},
		markerPanel{name: "keeps", accept: true},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, p := range r.Placed() {
		if p.Panel.Name() == elevationPanelName {
			t.Error("--bottom-band distance did not remove the elevation panel from the placements")
		}
	}
	if got := r.Declined(); len(got) != 0 {
		t.Errorf("Declined() = %v, want none -- a panel omitted by the flag is not a decline", got)
	}
	if got := r.Omitted(); len(got) != 1 || got[0] != elevationPanelName {
		t.Errorf("Omitted() = %v, want [%s]", got, elevationPanelName)
	}
}

// TestNew_BottomBandProfileLeavesElevationToItsOwnAccepts pins the default:
// with Context.BottomBand at its zero value (BottomBandProfile's own
// behaviour), a panel named like ElevationPanel is placed or declined
// exactly as its own Accepts says, with nothing appearing in Omitted() --
// the flag must not touch a render that never asked for --bottom-band
// distance at all.
func TestNew_BottomBandProfileLeavesElevationToItsOwnAccepts(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	// ctx.BottomBand left at its zero value on purpose: an unset Context
	// must behave exactly as it did before this flag existed.

	r, err := New(ctx, oneMarkerLayout(
		markerPanel{name: elevationPanelName, accept: true},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := len(r.Placed()); got != 1 {
		t.Fatalf("placed %d panels, want 1 -- the zero-value Context must not omit elevation", got)
	}
	if got := r.Omitted(); len(got) != 0 {
		t.Errorf("Omitted() = %v, want none", got)
	}
}

// withoutElevation strips every sample's elevation from ctx.Track in place
// and rebuilds ctx.Report AND ctx.Elevation to match, standing in for an
// activity that never carried elevation at all (a treadmill session, an
// indoor ride) -- the "profile absent" half of the two cases below. Mutating
// a fittest-built track after decode, rather than reaching for a second
// fixture generator, keeps this test on the same fixture the "profile
// present" case uses, differing only in the one field this feature cares
// about.
//
// Rebuilding ctx.Elevation here is not optional now that ElevationPanel reads
// it rather than building its own copy: contextFor already built one from
// the ORIGINAL track, before this mutation ran, and a Context carrying that
// stale, still-populated model would leave Accepts seeing an activity that
// carries elevation regardless of what this function just stripped from
// ctx.Track.
func withoutElevation(ctx *panel.Context) {
	for i := range ctx.Track.Samples {
		ctx.Track.Samples[i].HasElevation = false
	}
	ctx.Report = inspect.Build(ctx.Track)
	ctx.Elevation = panel.BuildElevation(ctx.Track, panel.DefaultElevationTuning(ctx.Track))
}

// TestNew_MarkerPanelUnaffectedWhenTheProfileCannotTakeTheBand is the first
// of the two tests docs/architecture.md and PLAN5.md's section E both name:
// "the standalone strip stays exactly as it is today" has to be a checkable
// claim, not a hope, and this is the check. It runs render.New against the
// REAL ElevationPanel and REAL MarkerPanel -- not the markerPanel test
// stand-in used above, because the absorption guard in New's keep filter
// calls (panel.ElevationPanel{}).Accepts(ctx) BY NAME, and a stand-in named
// "elevation" would never exercise that call at all.
//
// Table-driven over the two ways the profile can fail to take the band --
// an activity that never carried elevation, and one that did but was
// removed by --bottom-band distance -- because both must behave IDENTICALLY
// from MarkerPanel's own point of view: in either case profileTakesTheBand
// is false, the absorption branch in keep never fires, and MarkerPanel is
// placed or declined by its own ordinary Accepts exactly as before this
// feature existed.
func TestNew_MarkerPanelUnaffectedWhenTheProfileCannotTakeTheBand(t *testing.T) {
	cases := []struct {
		name       string
		noElev     bool
		bottomBand string
	}{
		{name: "activity carries no elevation", noElev: true},
		{name: "--bottom-band distance", bottomBand: panel.BottomBandDistance},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := buildContext(t, shortOptions(), 300, 100, 10)
			ctx.BottomBand = c.bottomBand
			ctx.Highlights = []panel.Highlight{{Name: "h", From: 5 * time.Second, To: 15 * time.Second}}
			if c.noElev {
				withoutElevation(ctx)
			}

			r, err := New(ctx, oneMarkerLayout(panel.ElevationPanel{}, panel.MarkerPanel{}), panel.DefaultTheme())
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if got := r.Absorbed(); len(got) != 0 {
				t.Errorf("Absorbed() = %v, want none -- the profile cannot take the band in this case", got)
			}
			placed := r.Placed()
			if len(placed) != 1 || placed[0].Panel.Name() != markerPanelName {
				names := make([]string, len(placed))
				for i, p := range placed {
					names[i] = p.Panel.Name()
				}
				t.Fatalf("Placed() = %v, want [%s] alone -- the marker panel must be placed exactly as its own Accepts says", names, markerPanelName)
			}
			// The sole survivor of a two-child row takes the whole row --
			// panel/layout_test.go already proves the arithmetic generally;
			// this pins that MarkerPanel specifically still gets it, which is
			// the "same boxes as today" half of the claim.
			if got, want := placed[0].Box.W, 300.0; math.Abs(got-want) > 0.001 {
				t.Errorf("markers box width = %v, want %v -- the sole survivor of the row must take it all", got, want)
			}
		})
	}
}

// TestNew_MarkerPanelAbsorbedWhenTheProfileTakesTheBand is the second of the
// two named tests: with the profile both accepting and permitted to take the
// band (BottomBand at its zero value) and at least one highlight configured,
// the marker panel is reported through Absorbed(), never through Declined(),
// and does not appear among the placed panels at all -- it has something to
// show, and it IS on screen, just inside ElevationPanel's own box rather
// than its own.
func TestNew_MarkerPanelAbsorbedWhenTheProfileTakesTheBand(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	// ctx.BottomBand left at its zero value (BottomBandProfile's own
	// behaviour) on purpose: the profile must be free to take the band.
	ctx.Highlights = []panel.Highlight{{Name: "h", From: 5 * time.Second, To: 15 * time.Second}}

	r, err := New(ctx, oneMarkerLayout(panel.ElevationPanel{}, panel.MarkerPanel{}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := r.Absorbed(); len(got) != 1 || got[0] != markerPanelName {
		t.Errorf("Absorbed() = %v, want [%s]", got, markerPanelName)
	}
	if got := r.Declined(); len(got) != 0 {
		t.Errorf("Declined() = %v, want none -- an absorbed panel is not a decline", got)
	}
	for _, p := range r.Placed() {
		if p.Panel.Name() == markerPanelName {
			t.Error("the marker panel was placed in its own box despite being absorbed into the profile")
		}
	}
}

// --- --gauges: the balance display is a flag decision, not a data one ------

// withStanceBalance sets a genuine, uniform StanceTimeBalance on every
// sample of ctx.Track and rebuilds ctx.Report to match -- standing in for an
// activity whose device recorded Garmin Running Dynamics, the one balance
// metric fitactivity resolves natively (fitactivity.Sample.StanceTimeBalance).
// 55, not 50: BalancePanel's own "zero is not a balance" rule (balance.go)
// refuses a recorded 0, and an exactly-even reading would too easily read as
// "the field is absent" to someone skimming this fixture; 55 is unambiguously
// a real, off-centre reading.
func withStanceBalance(ctx *panel.Context) {
	for i := range ctx.Track.Samples {
		ctx.Track.Samples[i].HasStanceTimeBalance = true
		ctx.Track.Samples[i].StanceTimeBalance = 55
	}
	ctx.Report = inspect.Build(ctx.Track)
}

// TestNew_GaugesDefaultOmitsBalancePanelsWithoutConsultingAccepts pins
// --gauges' default (the zero value of Context.Gauges, GaugesMetrics' own
// behaviour): a balance panel is removed from the placements WITHOUT its own
// Accepts ever being asked -- the identical shape
// TestNew_BottomBandDistanceOmitsElevationWithoutConsultingAccepts pins for
// --bottom-band distance, above. panel.IsBalancePanel is the type assertion
// New's keep filter actually checks, so this exercises the production
// tripwire, not a stand-in for it.
//
// withStanceBalance makes ContactBalance().Accepts(ctx) genuinely TRUE, so
// this proves the rejection is unconditional -- not a coincidence of an
// activity that would have declined on its own regardless.
func TestNew_GaugesDefaultOmitsBalancePanelsWithoutConsultingAccepts(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	withStanceBalance(ctx)
	// ctx.Gauges left at its zero value on purpose -- see Context.Gauges'
	// own doc comment for why that must behave exactly like GaugesMetrics.

	if !(panel.ContactBalance()).Accepts(ctx) {
		t.Fatal("precondition failed: the fixture's own ContactBalance().Accepts must be true")
	}

	r, err := New(ctx, oneMarkerLayout(
		panel.ContactBalance(),
		markerPanel{name: "keeps", accept: true},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, p := range r.Placed() {
		if p.Panel.Name() == "contact-balance" {
			t.Error("a balance panel was placed under the default gauge selection")
		}
	}
	if got := r.Declined(); len(got) != 0 {
		t.Errorf("Declined() = %v, want none -- a panel removed by --gauges is not a decline, the identical "+
			"distinction --bottom-band distance's own Omitted() draws for ElevationPanel", got)
	}
}

// TestNew_GaugesBalanceLeavesBalancePanelsToTheirOwnAccepts pins the other
// side: with Context.Gauges set to GaugesBalance, New's keep filter does NOT
// intervene, and a balance panel is placed or declined exactly as its own
// Accepts says -- the same "flag off means no change at all" guarantee
// TestNew_BottomBandProfileLeavesElevationToItsOwnAccepts pins for
// --bottom-band.
func TestNew_GaugesBalanceLeavesBalancePanelsToTheirOwnAccepts(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	withStanceBalance(ctx)
	ctx.Gauges = panel.GaugesBalance

	r, err := New(ctx, oneMarkerLayout(panel.ContactBalance()), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := len(r.Placed()); got != 1 {
		t.Fatalf("placed %d panels, want 1 -- GaugesBalance must not omit a balance panel that its own Accepts keeps", got)
	}
}

// --- QA round: the OverlappingLabels report, plumbed through New -----------

// overlapReportingPanel is a stand-in Painter that implements
// panel.LabelOverlapReporter directly, standing in for ElevationPanel's own
// implementation without needing a real distance axis geometry to
// reproduce a collision: New's own new code just has to type-assert for the
// interface and aggregate what it returns, and that is what this proves,
// independently of whether any real Painter's geometry actually collides
// (internal/panel/elevation_test.go proves that half, on the real Painter).
type overlapReportingPanel struct {
	name        string
	overlapping []int
}

func (p overlapReportingPanel) Name() string                { return p.name }
func (p overlapReportingPanel) Accepts(*panel.Context) bool { return true }
func (p overlapReportingPanel) Prepare(*panel.Context, panel.Box) panel.Painter {
	return &overlapReportingPainter{overlapping: p.overlapping}
}

type overlapReportingPainter struct {
	overlapping []int
}

func (p *overlapReportingPainter) Static(*panel.Canvas)               {}
func (p *overlapReportingPainter) Dynamic(*panel.Canvas, panel.Frame) {}
func (p *overlapReportingPainter) OverlappingLabels() []int           { return p.overlapping }

// TestNew_OverlappingLabelsReportsWhatAPlacedPainterReturns pins the
// mechanism itself: a placed Painter implementing panel.LabelOverlapReporter
// has its OverlappingLabels() result surfaced through Renderer.OverlappingLabels()
// unchanged. Before this wiring existed in New, a Painter could report
// exactly this and the render summary would never see it -- two labels
// visibly too close together on the elevation profile with nothing on
// screen or in the log explaining why.
func TestNew_OverlappingLabelsReportsWhatAPlacedPainterReturns(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	r, err := New(ctx, oneMarkerLayout(
		overlapReportingPanel{name: "reporter", overlapping: []int{2, 5}},
	), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := r.OverlappingLabels()
	if len(got) != 2 || got[0] != 2 || got[1] != 5 {
		t.Errorf("OverlappingLabels() = %v, want [2 5]", got)
	}
}

// TestNew_OverlappingLabelsEmptyWhenNoPlacedPainterReportsThem is the
// negative case: an ordinary placed Painter -- markerPainter, used
// throughout this file, implements neither Static nor Dynamic in terms of
// labels at all -- must not make OverlappingLabels() panic on the type
// assertion or invent a report where none exists.
func TestNew_OverlappingLabelsEmptyWhenNoPlacedPainterReportsThem(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 300, 100, 10)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "silent", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.OverlappingLabels(); len(got) != 0 {
		t.Errorf("OverlappingLabels() = %v, want none -- no placed painter implements LabelOverlapReporter", got)
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

func (s *failingSink) WriteFrame(int, *image.RGBA) error {
	if s.n >= s.after {
		return fmt.Errorf("sink is full")
	}
	s.n++
	return nil
}

// selectiveSink is a recording sink that only wants some frames.
type selectiveSink struct {
	recordingSink
	want    map[int]bool
	asked   []int
	written []int
}

func (s *selectiveSink) Wants(i int) bool {
	s.asked = append(s.asked, i)
	return s.want[i]
}

func (s *selectiveSink) WriteFrame(i int, img *image.RGBA) error {
	s.written = append(s.written, i)
	return s.recordingSink.WriteFrame(i, img)
}

// TestRun_SkipsDrawingFramesTheSinkDoesNotWant pins the optimisation that makes
// --frames a preview rather than a full render.
//
// Before it, writing five PNGs from a 25-minute activity drew and discarded
// 46,600 frames -- 36 seconds to produce five images, while the README called
// it the fast visual loop. Nothing failed; it was simply doing all the work and
// throwing it away.
//
// The assertion is that WriteFrame is called ONLY for wanted frames, which is
// the observable consequence of not drawing the rest. Asserting on elapsed time
// would be flaky; asserting on the call pattern is exact.
func TestRun_SkipsDrawingFramesTheSinkDoesNotWant(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 120, 80, 10)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}

	want := map[int]bool{0: true, 5: true, r.Frames() - 1: true}
	sink := &selectiveSink{want: want}
	if err := Run(context.Background(), r, sink, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(sink.asked) != r.Frames() {
		t.Errorf("the sink was asked about %d frames, want all %d -- it cannot otherwise tell how long the render was",
			len(sink.asked), r.Frames())
	}
	if got := len(sink.written); got != len(want) {
		t.Fatalf("WriteFrame was called %d times, want %d -- unwanted frames are still being drawn and written",
			got, len(want))
	}
	for _, i := range sink.written {
		if !want[i] {
			t.Errorf("frame %d was written but not wanted", i)
		}
	}
	// And the frames that WERE written must be the real ones, identical to a
	// standalone render. Skipping must change what is drawn for nobody.
	for n, i := range sink.written {
		expect := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
		if err := r.Render(expect, r.Frame(i)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(sink.frames[n].Pix, expect.Pix) {
			t.Errorf("frame %d differs from the same frame rendered on its own", i)
		}
	}
}

// TestRun_WithoutSelectorWritesEveryFrame is the other half: a sink that does
// not implement Selector -- the video encoder -- must still receive everything.
// Skipping a frame there would shorten the output.
func TestRun_WithoutSelectorWritesEveryFrame(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 120, 80, 10)
	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	if _, isSelector := any(sink).(interface{ Wants(int) bool }); isSelector {
		t.Fatal("the plain recording sink must not implement Selector, or this proves nothing")
	}
	if err := Run(context.Background(), r, sink, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.frames); got != r.Frames() {
		t.Errorf("wrote %d frames, want all %d", got, r.Frames())
	}
}

// TestNew_RequiresTrackAndTimer covers the two Context fields dereferenced on
// every frame. A nil one panicked inside the loop rather than failing in the
// constructor beside the two checks that were already there.
func TestNew_RequiresTrackAndTimer(t *testing.T) {
	full := buildContext(t, shortOptions(), 80, 60, 10)
	layout := oneMarkerLayout(markerPanel{name: "a", accept: true})

	noTrack := *full
	noTrack.Track = nil
	if _, err := New(&noTrack, layout, panel.DefaultTheme()); err == nil {
		t.Error("New accepted a context with no track; it would panic in the frame loop")
	}

	noTimer := *full
	noTimer.Timer = nil
	if _, err := New(&noTimer, layout, panel.DefaultTheme()); err == nil {
		t.Error("New accepted a context with no timer model")
	}
}

// TestRenderer_LastFrameWithSampleSkipsATrailingDeadZone pins the landmark
// --frames reaches for when it wants the end state.
//
// A session's declared window can outlast its final record: a watch that
// stops recording a moment before the session closes leaves real elapsed
// time with nothing in it. Those frames are honest -- Track.AtWithGap
// interpolates between readings and refuses to extrapolate past the last
// one, so every gauge shows its placeholder -- but a landmark that lands
// there shows a frame on which nothing reads, which is the opposite of
// what an "end state" frame is for.
//
// The fixture makes the dead zone many frames wide on purpose. A one- or
// two-frame tail would pass against an implementation that simply returned
// the final index, since the assertion would land within rounding of it.
func TestRenderer_LastFrameWithSampleSkipsATrailingDeadZone(t *testing.T) {
	ctx := buildContext(t, shortOptions(), 320, 180, 30)
	// Ask for a window a full two seconds longer than the samples cover.
	// At 30 fps that is 60 frames past the last reading, none of which can
	// carry a sample at any gap tolerance, because there is nothing beyond
	// the last one to interpolate towards.
	samples := ctx.Track.Samples
	last := samples[len(samples)-1].Time
	tl, err := panel.NewTimeline(ctx.Timeline.Start(), last.Add(2*time.Second).Sub(ctx.Timeline.Start()), 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Timeline = tl

	r, err := New(ctx, oneMarkerLayout(markerPanel{name: "a", accept: true}), panel.DefaultTheme())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	n := r.Frames()
	got := r.LastFrameWithSample()
	if got == n-1 {
		t.Fatalf("LastFrameWithSample() = %d, the final frame, but the final frames are past the last sample", got)
	}
	if !r.Frame(got).HasSample {
		t.Errorf("frame %d was named as the last one with a sample, but it has none", got)
	}
	for i := got + 1; i < n; i++ {
		if r.Frame(i).HasSample {
			t.Fatalf("frame %d is after the one named last-with-a-sample (%d) and yet has one", i, got)
		}
	}
}
