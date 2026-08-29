package panel

import (
	"bytes"
	"image"
	"image/color"
	"path/filepath"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/inspect"
)

// highlightEpoch is the fixed instant every controlled-Timeline test below
// builds from. Its value is arbitrary; what matters is that every test uses
// the same one, so a mistake in one test's arithmetic cannot be masked by a
// coincidence in another's.
var highlightEpoch = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// highlightTestContext builds a Context over a 100-second, 30fps timeline
// carrying the given highlights -- no Track and no Timer, because
// HighlightPanel's own doc comment states the reason: it never reads a
// Sample, so nothing here needs one. Tests that DO need a real activity (the
// pixel-identical regression, below) build their own Context.
func highlightTestContext(t *testing.T, w, h int, highlights []Highlight) (*Canvas, *image.RGBA, *Context) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	unit := float64(w)
	if h < w {
		unit = float64(h)
	}
	c, err := NewCanvas(img, unit*0.05, DefaultTheme(), faces)
	if err != nil {
		t.Fatalf("NewCanvas: %v", err)
	}
	tl, err := NewSegmentedTimeline(highlightEpoch, 100*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatalf("NewSegmentedTimeline: %v", err)
	}
	ctx := &Context{
		Width: w, Height: h, FontScale: 0.05, Fonts: faces,
		Timeline: tl, Highlights: highlights,
	}
	return c, img, ctx
}

// --- Accepts: the "no --highlight at all" policy ---------------------------

// TestHighlightPanel_DeclinesWithNoHighlightsConfigured pins the policy from
// the plan's absent-data table: no --highlight given at all is a fact about
// the FLAGS, and the panel declines outright rather than drawing an empty
// strip.
func TestHighlightPanel_DeclinesWithNoHighlightsConfigured(t *testing.T) {
	if (HighlightPanel{}).Accepts(&Context{}) {
		t.Error("accepted a Context with a nil Highlights slice")
	}
	if (HighlightPanel{}).Accepts(&Context{Highlights: []Highlight{}}) {
		t.Error("accepted a Context with an empty (non-nil) Highlights slice")
	}
}

// TestHighlightPanel_AcceptsWhenHighlightsAreConfigured is the other half:
// once even one highlight exists, the panel has real, invariant content
// (the ribbon and its block) and must be placed.
func TestHighlightPanel_AcceptsWhenHighlightsAreConfigured(t *testing.T) {
	highlights := []Highlight{{Name: "Hill", From: time.Minute, To: 2 * time.Minute}}
	if !(HighlightPanel{}).Accepts(&Context{Highlights: highlights}) {
		t.Error("declined despite a configured highlight")
	}
}

// --- did it actually draw ---------------------------------------------------

// TestHighlightPanel_StaticDrawsTheRibbonAndDynamicDrawsThePlayhead is the
// "did it draw" test every panel needs: Draw returns nothing, so a guard
// that early-returned or a colour equal to the background would pass a test
// that only checks for a nil error.
func TestHighlightPanel_StaticDrawsTheRibbonAndDynamicDrawsThePlayhead(t *testing.T) {
	highlights := []Highlight{
		{Name: "Hill climb", From: 10 * time.Second, To: 20 * time.Second},
		{Name: "Sprint", From: 40 * time.Second, To: 41 * time.Second},
	}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 20, Y: 20, W: 360, H: 160}
	p := HighlightPanel{}.Prepare(ctx, box)

	c.Fill(c.Theme.Background)
	p.Static(c)
	static := inkCount(img, box, c.Theme)
	if static == 0 {
		t.Fatal("Static drew nothing; the ribbon and the blocks are its whole content")
	}
	if whole := inkCount(img, Box{W: 400, H: 200}, c.Theme); whole != static {
		t.Errorf("Static put %d pixels of ink outside its box", whole-static)
	}

	// Dynamic with no highlight active: per the plan's table this is NOT a
	// hole, because the ribbon and the blocks already drew in Static. The
	// only thing Dynamic itself adds here is the playhead, and it must
	// still be real ink.
	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{Index: 0, Interval: NoHighlight})
	dyn := inkCount(img, box, c.Theme)
	if dyn == 0 {
		t.Fatal("Dynamic drew nothing even with no highlight active; the playhead alone should still be ink")
	}
	if whole := inkCount(img, Box{W: 400, H: 200}, c.Theme); whole != dyn {
		t.Errorf("Dynamic put %d pixels of ink outside its box", whole-dyn)
	}
}

// --- the "no name" policy ---------------------------------------------------

// TestHighlightPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea pins the
// plan's most easily-gotten-wrong absent-data rule: a highlight with no name
// shows NOTHING in the name area, not ReadoutPlaceholder or any other "--".
// The block lighting up is itself the honest statement that a highlight is
// active.
func TestHighlightPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea(t *testing.T) {
	highlights := []Highlight{{Name: "", From: 10 * time.Second, To: 20 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := HighlightPanel{}.Prepare(ctx, box)
	pp := p.(*highlightPainter)

	c.Fill(c.Theme.Background)
	p.Static(c)
	// Fully inside the highlight's body, at full weight, so if anything
	// were EVER going to be drawn in the name row for this highlight, this
	// is the frame that would show it.
	p.Dynamic(c, Frame{Index: 450, Interval: 0, IntervalWeight: 1})

	nameBand := Box{X: box.X, Y: pp.nameY - pp.namePx, W: box.W, H: pp.namePx * 2}
	if n := inkCount(img, nameBand, c.Theme); n != 0 {
		t.Errorf("an unnamed highlight put %d pixels of ink in the name row; want none, not a placeholder", n)
	}
	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("an unnamed, active highlight drew nothing at all; the ribbon and the lit block must still be ink")
	}
}

// TestHighlightPanel_NamedHighlightFadesInWithIntervalWeight checks the
// entrance ramp actually depends on Frame.IntervalWeight, and that at
// weight 0 nothing is visible yet -- the "fades in" half of the plan's
// description, as distinct from a hard cut.
func TestHighlightPanel_NamedHighlightFadesInWithIntervalWeight(t *testing.T) {
	highlights := []Highlight{{Name: "Hill climb", From: 10 * time.Second, To: 20 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := HighlightPanel{}.Prepare(ctx, box)
	pp := p.(*highlightPainter)
	nameBand := Box{X: box.X, Y: pp.nameY - pp.namePx, W: box.W, H: pp.namePx * 2}

	render := func(weight float64) int {
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{Index: 450, Interval: 0, IntervalWeight: weight})
		return inkCount(img, nameBand, c.Theme)
	}

	if got := render(0); got != 0 {
		t.Errorf("at weight 0 the name row already has %d pixels of ink; it should not have started fading in yet", got)
	}
	if got := render(1); got == 0 {
		t.Error("at weight 1 (fully in the highlight's body) the name row has no ink at all")
	}
}

// TestHighlightPanel_BlockBrightensMonotonicallyTowardHighlightColour is the
// pixel-level test the block itself never had: both
// TestHighlightPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea and
// TestHighlightPanel_NamedHighlightFadesInWithIntervalWeight sample only the
// NAME band, so a regression that drew the block at highlightRestAlpha
// forever -- Dynamic brightening the name but never the block it sits over
// -- would pass every test in this file. Sampling the block's own pixel and
// checking it moves monotonically toward the theme's Highlight colour as
// IntervalWeight rises from 0 to 1 is what that regression would fail.
//
// The monotonic claim is exact, not just plausible: Static and Dynamic both
// draw the block as Fade(Theme.Highlight, alpha) over an opaque background,
// so the sampled pixel is a straight alpha blend between Theme.Background
// and Theme.Highlight, and alpha itself
// (highlightRestAlpha + (1-highlightRestAlpha)*weight) is strictly
// increasing in weight -- so the pixel's squared distance to Theme.Highlight
// must strictly decrease at every step, and at weight 1 (alpha exactly 1,
// fully opaque) it must land on Theme.Highlight exactly, with no background
// showing through at all.
func TestHighlightPanel_BlockBrightensMonotonicallyTowardHighlightColour(t *testing.T) {
	highlights := []Highlight{{Name: "Hill", From: 10 * time.Second, To: 90 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := HighlightPanel{}.Prepare(ctx, box)
	pp := p.(*highlightPainter)

	block := pp.blocks[0]
	x, y := int(block.X+block.W/2), int(block.Y+block.H/2)

	highlightRGBA := color.RGBAModel.Convert(c.Theme.Highlight).(color.RGBA)
	bgRGBA := color.RGBAModel.Convert(c.Theme.Background).(color.RGBA)
	sqDist := func(a color.RGBA) float64 {
		dr := float64(a.R) - float64(highlightRGBA.R)
		dg := float64(a.G) - float64(highlightRGBA.G)
		db := float64(a.B) - float64(highlightRGBA.B)
		return dr*dr + dg*dg + db*db
	}

	sample := func(weight float64) color.RGBA {
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{Index: 1200, Interval: 0, IntervalWeight: weight})
		return img.RGBAAt(x, y)
	}

	if got := sample(0); got == bgRGBA {
		t.Fatal("the block's own pixel is bare background even at rest; Static never drew it")
	}

	prevDist := sqDist(sample(0)) + 1 // guaranteed larger than the first real sample
	for _, w := range []float64{0, 0.25, 0.5, 0.75, 1} {
		got := sample(w)
		d := sqDist(got)
		if d >= prevDist {
			t.Errorf("weight %v: block pixel %v (dist^2 %v to the highlight colour) is not closer than the previous weight's %v",
				w, got, d, prevDist)
		}
		prevDist = d
	}
	if got := sample(1); got != highlightRGBA {
		t.Errorf("at weight 1 (fully opaque) the block pixel is %v, want exactly the theme's own Highlight colour %v", got, highlightRGBA)
	}
}

// --- sizing: the longest name, not the active one ---------------------------

// TestHighlightPanel_NameSizeUsesTheLongestNameRegardlessOfOrder pins the
// template rule from the plan: sized once, in Prepare, against the longest
// name across every highlight -- never per-highlight, which would resize
// the text as the render moved from one highlight to the next. Checking
// argv order rather than just "long present vs not" is what actually tells
// "longest" apart from "first" or "last".
func TestHighlightPanel_NameSizeUsesTheLongestNameRegardlessOfOrder(t *testing.T) {
	const short = "A"
	const long = "A very long highlight name indeed"
	box := Box{X: 0, Y: 0, W: 300, H: 150}

	sizeFor := func(names ...string) float64 {
		var hs []Highlight
		from := time.Duration(0)
		for _, n := range names {
			hs = append(hs, Highlight{Name: n, From: from, To: from + time.Second})
			from += 2 * time.Second
		}
		_, _, ctx := highlightTestContext(t, 300, 150, hs)
		p := HighlightPanel{}.Prepare(ctx, box).(*highlightPainter)
		return p.namePx
	}

	shortOnly := sizeFor(short)
	longFirst := sizeFor(long, short)
	shortFirst := sizeFor(short, long)

	if longFirst != shortFirst {
		t.Errorf("namePx depends on argv order (long-first %v, short-first %v); it must be the same longest-name size either way",
			longFirst, shortFirst)
	}
	if longFirst >= shortOnly {
		t.Errorf("namePx = %v with the long name present, want it below the short-only size %v", longFirst, shortOnly)
	}
}

// --- geometry ----------------------------------------------------------------

// TestFrameFraction_MatchesIndexAtOverFrames pins the arithmetic that ties a
// highlight's static block to the per-frame playhead: both must place
// themselves on the SAME axis (frame index / Frames()) or the two would
// visibly disagree about where "now" is relative to a highlight.
func TestFrameFraction_MatchesIndexAtOverFrames(t *testing.T) {
	tl, err := NewTimeline(highlightEpoch, 100*time.Second, 30, 1)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	for _, offset := range []time.Duration{0, 10 * time.Second, 50 * time.Second, 99 * time.Second} {
		want := float64(tl.IndexAt(offset)) / float64(tl.Frames())
		if got := frameFraction(tl, offset); got != want {
			t.Errorf("frameFraction(%v) = %v, want %v (IndexAt(offset)/Frames())", offset, got, want)
		}
	}
}

// TestHighlightPanel_BlocksSitInsideTheRibbonInFromOrder checks the blocks
// this panel derives from Context.Highlights: within the ribbon it was given,
// and in the same order as the (already-sorted) highlights they represent.
func TestHighlightPanel_BlocksSitInsideTheRibbonInFromOrder(t *testing.T) {
	highlights := []Highlight{
		{Name: "First", From: 10 * time.Second, To: 15 * time.Second},
		{Name: "Second", From: 60 * time.Second, To: 65 * time.Second},
	}
	_, _, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 10, Y: 10, W: 380, H: 180}
	p := HighlightPanel{}.Prepare(ctx, box).(*highlightPainter)

	if len(p.blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(p.blocks))
	}
	for i, b := range p.blocks {
		if b.X < p.ribbon.X-0.5 || b.X+b.W > p.ribbon.X+p.ribbon.W+0.5 {
			t.Errorf("block %d (%+v) extends outside the ribbon %+v", i, b, p.ribbon)
		}
	}
	if p.blocks[0].X >= p.blocks[1].X {
		t.Errorf("block 0 (from %v) does not sit before block 1 (from %v) along the ribbon",
			highlights[0].From, highlights[1].From)
	}
}

// TestHighlightPanel_ATinyHighlightStillGetsAVisibleBlock covers the same
// class of failure Timeline's own zero-frame trap exists for: a highlight
// short enough that its exact proportional width would round away to
// nothing. A named highlight occupying no visible space in the strip that
// exists to show it is the same silent failure as a highlight that occupies
// no video at all.
func TestHighlightPanel_ATinyHighlightStillGetsAVisibleBlock(t *testing.T) {
	highlights := []Highlight{{Name: "Blip", From: 50 * time.Second, To: 50*time.Second + 20*time.Millisecond}}
	_, _, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := HighlightPanel{}.Prepare(ctx, box).(*highlightPainter)

	want := minBlockFraction * p.ribbon.H
	if got := p.blocks[0].W; got < want-0.5 {
		t.Errorf("a near-zero-length highlight's block is %v wide, want at least %v (minBlockFraction of the ribbon height)", got, want)
	}
}

// --- the playhead moves ------------------------------------------------------

// TestHighlightPanel_PlayheadMovesAcrossFrames guards the failure a single
// "did it draw" snapshot cannot see: a panel that draws the same thing every
// frame. The static layer would still be right and the video would be the
// right length, but the strip would never show where the render actually is.
func TestHighlightPanel_PlayheadMovesAcrossFrames(t *testing.T) {
	highlights := []Highlight{{Name: "Hill", From: 10 * time.Second, To: 20 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := HighlightPanel{}.Prepare(ctx, box)

	render := func(i int) []byte {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{Index: i, Interval: NoHighlight})
		out := make([]byte, len(img.Pix))
		copy(out, img.Pix)
		return out
	}

	a := render(0)
	b := render(ctx.Timeline.Frames() / 2)
	same := render(0)
	if bytes.Equal(a, b) {
		t.Error("the playhead at frame 0 and at the midpoint render identically")
	}
	if !bytes.Equal(a, same) {
		t.Error("the same frame index rendered differently twice; the panel is not deterministic")
	}
}

// --- fitting every box shape -------------------------------------------------

// TestHighlightPanel_FitsEveryBoxShape is the resolution and aspect-ratio
// check every panel needs: a Box is a rectangle this panel must fit, not an
// anchor it can grow away from, so its ink must stay inside boxes of very
// different shapes, including the narrow one a portrait layout produces.
func TestHighlightPanel_FitsEveryBoxShape(t *testing.T) {
	highlights := []Highlight{
		{Name: "Hill climb", From: 10 * time.Second, To: 20 * time.Second},
		{Name: "", From: 60 * time.Second, To: 61 * time.Second},
	}
	shapes := []struct {
		name           string
		frameW, frameH int
		box            Box
	}{
		{"1080p, a wide strip", 1920, 1080, Box{X: 40, Y: 900, W: 1840, H: 140}},
		{"4K, the same layout", 3840, 2160, Box{X: 80, Y: 1800, W: 3680, H: 280}},
		{"portrait, a short strip", 1080, 1920, Box{X: 30, Y: 1750, W: 1020, H: 140}},
		{"a very narrow column", 1080, 1920, Box{X: 30, Y: 1750, W: 220, H: 140}},
		{"a small box", 640, 360, Box{X: 10, Y: 300, W: 600, H: 50}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			tl, err := NewSegmentedTimeline(highlightEpoch, 100*time.Second, 30, 1, highlights)
			if err != nil {
				t.Fatalf("NewSegmentedTimeline: %v", err)
			}
			img := image.NewRGBA(image.Rect(0, 0, s.frameW, s.frameH))
			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			unit := float64(s.frameW)
			if s.frameH < s.frameW {
				unit = float64(s.frameH)
			}
			c, err := NewCanvas(img, unit*0.05, DefaultTheme(), faces)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &Context{
				Width: s.frameW, Height: s.frameH, FontScale: 0.05, Fonts: faces,
				Timeline: tl, Highlights: highlights,
			}

			p := HighlightPanel{}.Prepare(ctx, s.box)
			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{Index: tl.Frames() / 3, Interval: 0, IntervalWeight: 0.6})

			inBox := inkCount(img, s.box, c.Theme)
			if inBox == 0 {
				t.Fatal("nothing drawn")
			}
			whole := inkCount(img, Box{W: float64(s.frameW), H: float64(s.frameH)}, c.Theme)
			if whole != inBox {
				t.Errorf("%d pixels of ink escaped the box", whole-inBox)
			}
		})
	}
}

// --- the pixel-identical regression -----------------------------------------

// oldLandscapeLayout and oldPortraitLayout are a FROZEN copy of
// LandscapeLayout and PortraitLayout exactly as they stood before
// HighlightPanel's row was added -- not a helper that could drift with a
// future edit, a fixed historical snapshot this test diffs the real
// functions against. See TestNoHighlightRenderIsPixelIdenticalToBeforeThisFeature.
func oldLandscapeLayout() Layout {
	return Layout{
		Name:      "landscape",
		Margin:    0.03,
		FontScale: 0.05,
		Root: Slot{Dir: Col, Children: []Slot{
			{Dir: Row, Weight: 4, Children: []Slot{
				{Dir: Col, Weight: 3, Children: []Slot{
					{Panel: RoutePanel{}, Weight: 3, Pad: 0.01},
					{Dir: Row, Weight: 2, Children: []Slot{
						{Panel: ElapsedPanel{}, Weight: 3, Pad: 0.01},
						{Panel: Distance(), Weight: 2, Pad: 0.01},
					}},
				}},
				{Dir: Col, Weight: 1, Children: []Slot{
					{Panel: HeartRate(), Pad: 0.01},
					{Panel: Pace(), Pad: 0.01},
					{Panel: Power(), Pad: 0.01},
					{Panel: Cadence(), Pad: 0.01},
				}},
			}},
			{Panel: ElevationPanel{}, Weight: 1, Pad: 0.01},
		}},
	}
}

func oldPortraitLayout() Layout {
	return Layout{
		Name:      "portrait",
		Margin:    0.03,
		FontScale: 0.05,
		Root: Slot{Dir: Col, Children: []Slot{
			{Panel: RoutePanel{}, Weight: 4, Pad: 0.01},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: ElapsedPanel{}, Weight: 3, Pad: 0.01},
				{Panel: Distance(), Weight: 2, Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: HeartRate(), Pad: 0.01},
				{Panel: Pace(), Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: Power(), Pad: 0.01},
				{Panel: Cadence(), Pad: 0.01},
			}},
			{Panel: ElevationPanel{}, Weight: 2, Pad: 0.01},
		}},
	}
}

// renderFullFrame resolves layout over ctx and draws one complete frame --
// static, then dynamic -- exactly as internal/render.Renderer does.
//
// Reimplemented locally rather than imported: internal/panel cannot import
// internal/render without a cycle (render already imports panel), and this
// feature's own scope keeps this panel out of internal/render entirely (see
// CLAUDE.md). Duplicating a dozen lines of loop-glue here is the honest
// price of that boundary, not an oversight.
func renderFullFrame(t *testing.T, ctx *Context, layout Layout, i int) *image.RGBA {
	t.Helper()
	keep := func(p Panel) bool { return p.Accepts(ctx) }
	placed, err := layout.Resolve(ctx.Width, ctx.Height, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	theme := DefaultTheme()
	c, err := NewCanvas(img, ctx.BasePx(), theme, ctx.Fonts)
	if err != nil {
		t.Fatalf("NewCanvas: %v", err)
	}
	c.Fill(theme.Background)

	painters := make([]Painter, len(placed))
	for k, pl := range placed {
		painters[k] = pl.Panel.Prepare(ctx, pl.Box)
		painters[k].Static(c)
	}

	at := ctx.Timeline.At(i)
	interval, weight := ctx.Timeline.IntervalAt(i, ctx.HighlightTransition)
	f := Frame{
		Index:          i,
		At:             at,
		Elapsed:        ctx.Timer.Elapsed(at),
		Active:         ctx.Timer.Active(at),
		Paused:         ctx.Timer.Paused(at),
		HasTimerEvents: ctx.Timer.HasTimerEvents(),
		Interval:       interval,
		IntervalWeight: weight,
	}
	if s, ok := ctx.Track.AtWithGap(at, fitactivity.DefaultMaxGap); ok {
		f.Sample, f.HasSample = s, true
	}
	for _, pt := range painters {
		pt.Dynamic(c, f)
	}
	return img
}

// TestNoHighlightRenderIsPixelIdenticalToBeforeThisFeature is the strongest
// claim the plan asks for: adding HighlightPanel's row to both layouts must
// not move a single pixel of a render that configures no --highlight.
//
// It works BECAUSE of where the row sits and how Resolve prunes: Accepts
// declines with no highlights configured, and Layout.Resolve removes a
// declined leaf from the tree BEFORE any box is divided -- so the surviving
// rows' weight fractions are computed exactly as if this leaf had never
// existed (see Layout.Resolve's own doc comment). This test pins that
// mechanism against a frozen copy of the layouts as they stood immediately
// before this feature, rather than trusting the argument on its own.
func TestNoHighlightRenderIsPixelIdenticalToBeforeThisFeature(t *testing.T) {
	opts := fittest.DefaultOptions()
	opts.Count = 300
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)
	tl, err := NewTimelineForActivity(timer, 30, 60)
	if err != nil {
		t.Fatalf("NewTimelineForActivity: %v", err)
	}
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		w, h int
		newL Layout
		oldL Layout
	}{
		{"landscape", 960, 540, LandscapeLayout(), oldLandscapeLayout()},
		{"portrait", 540, 960, PortraitLayout(), oldPortraitLayout()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := &Context{
				Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
				Width: c.w, Height: c.h, FontScale: 0.05, Fonts: faces,
				// The point of the test: no --highlight was given.
				Highlights: nil,
			}
			for _, i := range []int{0, tl.Frames() / 2, tl.Frames() - 1} {
				got := renderFullFrame(t, ctx, c.newL, i)
				want := renderFullFrame(t, ctx, c.oldL, i)
				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("frame %d: rendering %s WITH HighlightPanel's row differs from rendering it without one; "+
						"a render with no --highlight must be pixel-identical to before this feature existed",
						i, c.name)
				}
			}
		})
	}
}
