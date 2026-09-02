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
// MarkerPanel's own doc comment states the reason: it never reads a
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

// markerTestContext is highlightTestContext's sibling for tests that need
// --label as well as, or instead of, --highlight. labels must already carry
// resolved FirstFrame/LastFrame, exactly as cmd/label.go's resolveLabels
// would hand them to a real Context -- this helper does not re-derive them,
// the same discipline highlightTestContext follows for Highlight.From/To
// already being activity-time offsets rather than something resolved here.
func markerTestContext(t *testing.T, w, h int, highlights []Highlight, labels []Label) (*Canvas, *image.RGBA, *Context) {
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
		Timeline: tl, Highlights: highlights, Labels: labels,
	}
	return c, img, ctx
}

// --- Accepts: "is there anything on the timeline to show at all" -----------

// TestMarkerPanel_DeclinesWithNeitherHighlightsNorLabelsConfigured pins the
// project's absent-data policy for this panel: no --highlight AND no --label
// given at all is a fact about the FLAGS, and the panel declines outright
// rather than drawing an empty strip. Accepts asks exactly one question --
// this covers every combination of "nil" and "empty, non-nil" across BOTH
// slices, so a regression that only zero-checked one of the two would still
// be caught.
func TestMarkerPanel_DeclinesWithNeitherHighlightsNorLabelsConfigured(t *testing.T) {
	cases := []struct {
		name string
		ctx  Context
	}{
		{"both nil", Context{}},
		{"both empty, non-nil", Context{Highlights: []Highlight{}, Labels: []Label{}}},
		{"highlights empty, labels nil", Context{Highlights: []Highlight{}}},
		{"highlights nil, labels empty", Context{Labels: []Label{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if (MarkerPanel{}).Accepts(&c.ctx) {
				t.Error("accepted a Context with neither a highlight nor a label configured")
			}
		})
	}
}

// TestMarkerPanel_AcceptsWhenHighlightsAreConfigured is one half of the
// "anything at all" question: once even one highlight exists, the panel has
// real, invariant content (the ribbon and its block) and must be placed.
func TestMarkerPanel_AcceptsWhenHighlightsAreConfigured(t *testing.T) {
	highlights := []Highlight{{Name: "Hill", From: time.Minute, To: 2 * time.Minute}}
	if !(MarkerPanel{}).Accepts(&Context{Highlights: highlights}) {
		t.Error("declined despite a configured highlight")
	}
}

// TestMarkerPanel_AcceptsWhenOnlyLabelsAreConfigured is the other half, and
// the one the rename exists for: a labels-only render (no --highlight given
// at all) must still be placed, because a label's tick is real, invariant
// content of its own -- not a highlight, but still something on the
// timeline to show.
func TestMarkerPanel_AcceptsWhenOnlyLabelsAreConfigured(t *testing.T) {
	labels := []Label{{Name: "Lighthouse", At: time.Minute, Video: 3 * time.Second, FirstFrame: 10, LastFrame: 19}}
	if !(MarkerPanel{}).Accepts(&Context{Labels: labels}) {
		t.Error("declined despite a configured label, with no highlight at all")
	}
}

// --- did it actually draw ---------------------------------------------------

// TestMarkerPanel_StaticDrawsTheRibbonAndDynamicDrawsThePlayhead is the
// "did it draw" test every panel needs: Draw returns nothing, so a guard
// that early-returned or a colour equal to the background would pass a test
// that only checks for a nil error.
func TestMarkerPanel_StaticDrawsTheRibbonAndDynamicDrawsThePlayhead(t *testing.T) {
	highlights := []Highlight{
		{Name: "Hill climb", From: 10 * time.Second, To: 20 * time.Second},
		{Name: "Sprint", From: 40 * time.Second, To: 41 * time.Second},
	}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 20, Y: 20, W: 360, H: 160}
	p := MarkerPanel{}.Prepare(ctx, box)

	c.Fill(c.Theme.Background)
	p.Static(c)
	static := inkCount(img, box, c.Theme)
	if static == 0 {
		t.Fatal("Static drew nothing; the ribbon and the blocks are its whole content")
	}
	if whole := inkCount(img, Box{W: 400, H: 200}, c.Theme); whole != static {
		t.Errorf("Static put %d pixels of ink outside its box", whole-static)
	}

	// Dynamic with no highlight active. This is NOT a hole, because the
	// ribbon and the blocks already drew in Static. The
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

// TestMarkerPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea pins the
// plan's most easily-gotten-wrong absent-data rule: a highlight with no name
// shows NOTHING in the name area, not ReadoutPlaceholder or any other "--".
// The block lighting up is itself the honest statement that a highlight is
// active.
func TestMarkerPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea(t *testing.T) {
	highlights := []Highlight{{Name: "", From: 10 * time.Second, To: 20 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box)
	pp := p.(*markerPainter)

	c.Fill(c.Theme.Background)
	p.Static(c)
	// Fully inside the highlight's body, at full weight, so if anything
	// were EVER going to be drawn in the name row for this highlight, this
	// is the frame that would show it.
	p.Dynamic(c, Frame{Index: 450, Interval: 0, IntervalWeight: 1})

	// 0.8x of namePx, not a doubled band: the re-derived stack (see
	// Prepare's own comment beside nameY) sits the highlight name row
	// close enough to the ribbon that a wider band would sample the
	// ribbon's own always-on ink -- the lit block, present regardless of
	// whether the highlight has a name -- and this test would then be
	// unable to tell "no name drawn" from "the ribbon is there, as
	// always".
	nameBand := Box{X: box.X, Y: pp.nameY - pp.namePx*0.4, W: box.W, H: pp.namePx * 0.8}
	if n := inkCount(img, nameBand, c.Theme); n != 0 {
		t.Errorf("an unnamed highlight put %d pixels of ink in the name row; want none, not a placeholder", n)
	}
	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("an unnamed, active highlight drew nothing at all; the ribbon and the lit block must still be ink")
	}
}

// TestMarkerPanel_NamedHighlightFadesInWithIntervalWeight checks the
// entrance ramp actually depends on Frame.IntervalWeight, and that at
// weight 0 nothing is visible yet -- it fades in rather than cutting in.
func TestMarkerPanel_NamedHighlightFadesInWithIntervalWeight(t *testing.T) {
	highlights := []Highlight{{Name: "Hill climb", From: 10 * time.Second, To: 20 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box)
	pp := p.(*markerPainter)
	// See TestMarkerPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea for
	// why this band is 0.8x of namePx rather than a wider one: the
	// highlight name row sits close enough to the ribbon in the re-derived
	// stack that a doubled band would sample the ribbon's own always-on
	// ink.
	nameBand := Box{X: box.X, Y: pp.nameY - pp.namePx*0.4, W: box.W, H: pp.namePx * 0.8}

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

// TestMarkerPanel_BlockBrightensMonotonicallyTowardHighlightColour is the
// pixel-level test the block itself never had: both
// TestMarkerPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea and
// TestMarkerPanel_NamedHighlightFadesInWithIntervalWeight sample only the
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
func TestMarkerPanel_BlockBrightensMonotonicallyTowardHighlightColour(t *testing.T) {
	highlights := []Highlight{{Name: "Hill", From: 10 * time.Second, To: 90 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box)
	pp := p.(*markerPainter)

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

// TestMarkerPanel_NameSizeUsesTheLongestNameRegardlessOfOrder pins the
// template rule: sized once, in Prepare, against the longest
// name across every highlight -- never per-highlight, which would resize
// the text as the render moved from one highlight to the next. Checking
// argv order rather than just "long present vs not" is what actually tells
// "longest" apart from "first" or "last".
func TestMarkerPanel_NameSizeUsesTheLongestNameRegardlessOfOrder(t *testing.T) {
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
		p := MarkerPanel{}.Prepare(ctx, box).(*markerPainter)
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

// TestMarkerPanel_BlocksSitInsideTheRibbonInFromOrder checks the blocks
// this panel derives from Context.Highlights: within the ribbon it was given,
// and in the same order as the (already-sorted) highlights they represent.
func TestMarkerPanel_BlocksSitInsideTheRibbonInFromOrder(t *testing.T) {
	highlights := []Highlight{
		{Name: "First", From: 10 * time.Second, To: 15 * time.Second},
		{Name: "Second", From: 60 * time.Second, To: 65 * time.Second},
	}
	_, _, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 10, Y: 10, W: 380, H: 180}
	p := MarkerPanel{}.Prepare(ctx, box).(*markerPainter)

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

// TestMarkerPanel_ATinyHighlightStillGetsAVisibleBlock covers the same
// class of failure Timeline's own zero-frame trap exists for: a highlight
// short enough that its exact proportional width would round away to
// nothing. A named highlight occupying no visible space in the strip that
// exists to show it is the same silent failure as a highlight that occupies
// no video at all.
func TestMarkerPanel_ATinyHighlightStillGetsAVisibleBlock(t *testing.T) {
	highlights := []Highlight{{Name: "Blip", From: 50 * time.Second, To: 50*time.Second + 20*time.Millisecond}}
	_, _, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box).(*markerPainter)

	want := minBlockFraction * p.ribbon.H
	if got := p.blocks[0].W; got < want-0.5 {
		t.Errorf("a near-zero-length highlight's block is %v wide, want at least %v (minBlockFraction of the ribbon height)", got, want)
	}
}

// --- the playhead moves ------------------------------------------------------

// TestMarkerPanel_PlayheadMovesAcrossFrames guards the failure a single
// "did it draw" snapshot cannot see: a panel that draws the same thing every
// frame. The static layer would still be right and the video would be the
// right length, but the strip would never show where the render actually is.
func TestMarkerPanel_PlayheadMovesAcrossFrames(t *testing.T) {
	highlights := []Highlight{{Name: "Hill", From: 10 * time.Second, To: 20 * time.Second}}
	c, img, ctx := highlightTestContext(t, 400, 200, highlights)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box)

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

// TestMarkerPanel_FitsEveryBoxShape is the resolution and aspect-ratio
// check every panel needs: a Box is a rectangle this panel must fit, not an
// anchor it can grow away from, so its ink must stay inside boxes of very
// different shapes, including the narrow one a portrait layout produces.
func TestMarkerPanel_FitsEveryBoxShape(t *testing.T) {
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

			p := MarkerPanel{}.Prepare(ctx, s.box)
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

// --- labels: ticks, their own name area, and no cutting --------------------

// TestMarkerPanel_TicksDrawWithNoHighlightsConfigured pins the labels-only
// render: with no --highlight at all the panel must
// accept (see TestMarkerPanel_AcceptsWhenOnlyLabelsAreConfigured) AND its
// ticks must actually read as ink -- not merely a bare ribbon. Static draws
// the tick unconditionally, so this is real content on every frame of such
// a render, never a box waiting for a highlight that will not come.
func TestMarkerPanel_TicksDrawWithNoHighlightsConfigured(t *testing.T) {
	labels := []Label{{Name: "Lighthouse", At: 20 * time.Second, Video: 3 * time.Second, FirstFrame: 600, LastFrame: 689}}
	c, img, ctx := markerTestContext(t, 400, 200, nil, labels)
	box := Box{X: 20, Y: 20, W: 360, H: 160}
	p := MarkerPanel{}.Prepare(ctx, box)
	pp := p.(*markerPainter)
	if len(pp.tickX) != 1 {
		t.Fatalf("got %d ticks, want 1", len(pp.tickX))
	}

	c.Fill(c.Theme.Background)
	p.Static(c)

	if n := inkCount(img, box, c.Theme); n == 0 {
		t.Fatal("Static drew nothing for a labels-only render; the ribbon and the tick are its whole content")
	}
	tickBand := Box{
		X: pp.tickX[0] - pp.tickW, Y: pp.ribbon.Y - pp.tickExtend,
		W: 2 * pp.tickW, H: pp.tickExtend,
	}
	if n := inkCount(img, tickBand, c.Theme); n == 0 {
		t.Error("no ink directly above the ribbon at the label's own tick position")
	}
	// The tick extends ABOVE the ribbon only -- a band of the same height
	// sitting BELOW the ribbon's own bottom edge (outside the ribbon
	// rectangle entirely, so this is not the ribbon's own ink) must show
	// nothing. With no highlights configured there are no blocks and
	// Static draws no playhead, so this band is bare background unless the
	// tick itself bled downward through or past the ribbon, which the
	// plan's shape distinction (unlike the playhead, which spans both
	// sides) forbids. Started a couple of pixels past the ribbon's exact
	// edge, not AT it, so the ribbon rectangle's own antialiasing fringe
	// is not mistaken for a leaking tick.
	belowRibbon := Box{X: pp.tickX[0] - pp.tickW, Y: pp.ribbon.Y + pp.ribbon.H + 2, W: 2 * pp.tickW, H: pp.tickExtend}
	if n := inkCount(img, belowRibbon, c.Theme); n != 0 {
		t.Errorf("tick ink found %v pixel-units below the ribbon; the tick must extend ABOVE the ribbon only", n)
	}
}

// TestMarkerPanel_LabelNameFadesWithLabelWeight is Frame.LabelWeight's own
// version of TestMarkerPanel_NamedHighlightFadesInWithIntervalWeight: the
// label's name must genuinely ramp on Frame.LabelWeight rather than cut in,
// so weight 0 shows nothing yet and weight 1 shows real ink.
func TestMarkerPanel_LabelNameFadesWithLabelWeight(t *testing.T) {
	labels := []Label{{Name: "Lighthouse", At: 20 * time.Second, Video: 3 * time.Second, FirstFrame: 600, LastFrame: 689}}
	c, img, ctx := markerTestContext(t, 400, 200, nil, labels)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box)
	pp := p.(*markerPainter)
	// Sized a little SHORTER than the label name row's own real footprint
	// (roughly one labelNamePx tall, centred on labelNameY -- see
	// FaceCache.Measure, whose reported glyph height is close to the
	// requested px) rather than a doubled band: the label row sits close
	// enough to the ribbon (see Prepare's own reasoning beside
	// labelNameY) that a doubled band would reach into the ribbon's own
	// always-on ink, and even the full labelNamePx band clips the
	// ribbon's antialiasing fringe. 0.8x keeps clear of the ribbon while
	// still comfortably covering real glyph ink -- the identical
	// reasoning TestMarkerPanel_NoNameHighlightDrawsNoPlaceholderInTheNameArea
	// now applies on the OTHER side of the ribbon, for the highlight name
	// row, now that it too sits close by.
	nameBand := Box{X: box.X, Y: pp.labelNameY - pp.labelNamePx*0.4, W: box.W, H: pp.labelNamePx * 0.8}

	render := func(weight float64) int {
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{Index: 645, Interval: NoHighlight, Label: 0, LabelWeight: weight})
		return inkCount(img, nameBand, c.Theme)
	}

	if got := render(0); got != 0 {
		t.Errorf("at label weight 0 the label name row already has %d pixels of ink; it should not have started fading in yet", got)
	}
	if got := render(1); got == 0 {
		t.Error("at label weight 1 the label name row has no ink at all")
	}
}

// TestMarkerPanel_LabelOverlappingAnActiveHighlightShowsBothNamesUnmoved is
// the reason the two get separate areas: a frame where
// a highlight is fully active AND a label is fully on screen at once must
// show BOTH names, at their own fixed Y (nameY for the highlight, a
// distinct labelNameY for the label) -- neither one contending for, or
// popping the other out of, a shared area.
func TestMarkerPanel_LabelOverlappingAnActiveHighlightShowsBothNamesUnmoved(t *testing.T) {
	highlights := []Highlight{{Name: "Hill climb", From: 10 * time.Second, To: 90 * time.Second}}
	labels := []Label{{Name: "Lighthouse", At: 20 * time.Second, Video: 3 * time.Second, FirstFrame: 600, LastFrame: 689}}
	c, img, ctx := markerTestContext(t, 400, 200, highlights, labels)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box)
	pp := p.(*markerPainter)

	if pp.nameY == pp.labelNameY {
		t.Fatal("the highlight name and the label name share one Y position; they must have their own areas")
	}
	highlightBand := Box{X: box.X, Y: pp.nameY - pp.namePx, W: box.W, H: pp.namePx * 2}
	labelBand := Box{X: box.X, Y: pp.labelNameY - pp.labelNamePx, W: box.W, H: pp.labelNamePx * 2}

	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{Index: 645, Interval: 0, IntervalWeight: 1, Label: 0, LabelWeight: 1})

	if n := inkCount(img, highlightBand, c.Theme); n == 0 {
		t.Error("the highlight's own name area has no ink while a label overlaps it; the highlight name must not be pushed out")
	}
	if n := inkCount(img, labelBand, c.Theme); n == 0 {
		t.Error("the label's own name area has no ink while it overlaps an active highlight")
	}
}

// TestMarkerPanel_LabelNameAnchorsOverItsOwnTick is F5's regression test: a
// label's name must be centred over its OWN tick, exactly the way a
// highlight's name is centred over the midpoint of its own block -- not
// centred in the panel, which is invisible with a single label configured
// and, with several, leaves every name with no visible relationship to any
// tick at all, defeating the point of drawing ticks.
func TestMarkerPanel_LabelNameAnchorsOverItsOwnTick(t *testing.T) {
	labels := []Label{
		{Name: "A", At: 5 * time.Second, Video: 2 * time.Second, FirstFrame: 150, LastFrame: 209},
		{Name: "B", At: 45 * time.Second, Video: 2 * time.Second, FirstFrame: 1350, LastFrame: 1409},
		{Name: "C", At: 85 * time.Second, Video: 2 * time.Second, FirstFrame: 2550, LastFrame: 2609},
	}
	_, _, ctx := markerTestContext(t, 1200, 200, nil, labels)
	box := Box{X: 0, Y: 0, W: 1200, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box).(*markerPainter)

	if len(p.labelAnchorX) != len(labels) {
		t.Fatalf("got %d label anchors, want %d", len(p.labelAnchorX), len(labels))
	}
	for i := range labels {
		// Short, equal-length names spread well clear of either edge of a
		// wide box: none of the three ticks is close enough for the edge
		// clamp to move the anchor off it, so anchor and tick must coincide
		// exactly.
		if got, want := p.labelAnchorX[i], p.tickX[i]; got != want {
			t.Errorf("label %d (%q) anchored at %.1f, want it over its own tick at %.1f",
				i, labels[i].Name, got, want)
		}
	}
	// The bug this pins collapsed every label's anchor onto the panel's
	// centreX regardless of where its tick fell, which this also would have
	// caught even without comparing to tickX above.
	if p.labelAnchorX[0] == p.labelAnchorX[1] || p.labelAnchorX[1] == p.labelAnchorX[2] {
		t.Errorf("label anchors are %v; three labels at different times must anchor at three different positions", p.labelAnchorX)
	}
}

// TestMarkerPanel_LabelNameAnchorClampsNearTheEdge is the label-name
// counterpart of the highlight name's own edge clamp: a label whose tick
// falls right at the start of the ribbon must still print its whole name
// inside the box, the same guarantee Prepare already gives a highlight's
// name near either end.
func TestMarkerPanel_LabelNameAnchorClampsNearTheEdge(t *testing.T) {
	labels := []Label{{Name: "A rather long label name", At: 0, Video: 2 * time.Second, FirstFrame: 0, LastFrame: 59}}
	_, _, ctx := markerTestContext(t, 400, 200, nil, labels)
	box := Box{X: 0, Y: 0, W: 400, H: 200}
	p := MarkerPanel{}.Prepare(ctx, box).(*markerPainter)

	if len(p.labelAnchorX) != 1 {
		t.Fatalf("got %d label anchors, want 1", len(p.labelAnchorX))
	}
	// The tick sits at the ribbon's own left edge; anchoring the name
	// exactly over it would print half the name to the left of the box.
	if p.labelAnchorX[0] <= p.tickX[0] {
		t.Errorf("label anchored at %.1f, tick at %.1f; a name this long at the very start of the ribbon "+
			"must be clamped inward, not left centred on (or before) its own tick", p.labelAnchorX[0], p.tickX[0])
	}
	if p.labelAnchorX[0] < box.X || p.labelAnchorX[0] > box.X+box.W {
		t.Errorf("label anchor %.1f falls outside the panel's own box [%.1f, %.1f]", p.labelAnchorX[0], box.X, box.X+box.W)
	}
}

// --- the playhead and the tick must clear their neighbouring name rows -----
//
// The re-derived stack (label name above the ribbon, highlight name below)
// INVERTS which row each clearance constraint checks, from what this section
// held before the label name moved: downward now has to clear the
// HIGHLIGHT name row (bigger text, closer to the ribbon than the label name
// used to sit -- see Prepare's own comment beside headExtendBelow), and
// upward now has to clear the LABEL name row, for both the playhead
// (headExtend) and, newly, the tick (tickExtend) -- nothing sat above the
// ribbon before this move, so tickExtend had never needed a clearance test
// at all.

// TestMarkerPanel_PlayheadClearsTheHighlightNameRowAcrossEveryBoxShape is the
// geometric half of the playhead/highlight-row regression coverage,
// re-pointed from the pre-move version of this test (which asserted the
// identical shape of claim against the label name row, back when the label
// name sat below the ribbon). Prepare deliberately makes the playhead
// ASYMMETRIC (headExtendBelow shorter than headExtend) so its downward tip
// stops clear of the highlight name row -- see Prepare's own long comment
// beside headExtendBelow, which spells out that a SYMMETRIC playhead already
// happened once and would draw an accent line through every highlight's
// name.
//
// No existing test asserted the MARGIN itself. TestMarkerPanel_NamedHighlightFadesInWithIntervalWeight
// and its neighbours all sample a band chosen to avoid the danger zone
// rather than measure the clearance next to it, so a regression back to a
// symmetric playhead would still pass every one of them -- this is written
// specifically to close that gap.
//
// The margin is computed independently of Prepare's own internal fractions:
// this reads back only the fields Prepare exports on the Painter (ribbon,
// headExtendBelow, nameY, namePx) plus an ACTUAL glyph-height measurement
// from the font cache, rather than re-deriving Prepare's own doc-comment
// figures, which would only prove the arithmetic agrees with itself rather
// than that real ink clears real ink.
//
// namePx is deliberately left at its default, UNSHRUNK size -- the one
// highlight configured here has a short name -- rather than the size a
// real long name would be fit to: Prepare's own comment notes that
// shrinking only moves the row's top edge DOWN, away from the playhead, so
// the unshrunk size is the TIGHTEST clearance this panel ever has to hold,
// and the one worth checking at every shape.
func TestMarkerPanel_PlayheadClearsTheHighlightNameRowAcrossEveryBoxShape(t *testing.T) {
	highlights := []Highlight{{Name: "Hill climb", From: 10 * time.Second, To: 20 * time.Second}}
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
			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			ctx := &Context{
				Width: s.frameW, Height: s.frameH, FontScale: 0.05, Fonts: faces,
				Timeline: tl, Highlights: highlights,
			}
			p := MarkerPanel{}.Prepare(ctx, s.box).(*markerPainter)
			if !p.ok {
				t.Fatal("precondition: Prepare bailed out; the fixture is wrong")
			}

			// "Ap" carries both an ascender and a descender, so the measured
			// height is the real worst-case glyph extent rather than a
			// string that happens to sit entirely above the baseline.
			_, glyphH, err := faces.Measure("Ap", p.namePx)
			if err != nil {
				t.Fatalf("Measure: %v", err)
			}
			playheadBottom := p.ribbon.Y + p.ribbon.H + p.headExtendBelow
			nameTop := p.nameY - glyphH/2

			if margin := nameTop - playheadBottom; margin <= 0 {
				t.Errorf("playhead's own bottom tip (y=%.2f) reaches at or past the highlight name row's own top (y=%.2f); "+
					"margin = %.2f, want a positive margin at every box shape", playheadBottom, nameTop, margin)
			}
		})
	}
}

// TestMarkerPanel_PlayheadDoesNotBleedIntoAnOnScreenHighlightName is the
// pixel counterpart of the geometric test above, re-pointed the same way:
// it proves the margin holds in drawn ink, not only in the coordinates
// Prepare computed to produce it.
//
// The playhead and the active highlight's own name anchor are placed at
// EXACTLY the same x: the highlight spans a window centred on the frame the
// playhead is drawn at, so the block's midpoint -- and the name anchored
// over it, unclamped because the window sits well clear of either edge of a
// 1840px-wide ribbon -- lands at the identical fraction of the ribbon the
// playhead does. That is the single worst frame for this collision: any
// bleed lands squarely inside the name's own glyph box rather than beside
// it.
//
// The glyph box is MEASURED, not guessed: FaceCache.Measure at the
// painter's own namePx, centred on the painter's own anchorX and nameY --
// the exact box Canvas.Text paints into with ax=ay=0.5.
//
// PROVED LOAD-BEARING for the original, pre-move version of this test:
// temporarily setting headExtendBelow to the same value as headExtend in
// Prepare (restoring a symmetric playhead) made it fail with a nonzero
// accent-pixel count inside the glyph box; reverted immediately after
// confirming it, with no production code left changed. The row it now
// checks is closer to the ribbon and carries bigger text than the one that
// experiment ran against, so the failure mode it guards is, if anything,
// easier to reintroduce here, not harder.
func TestMarkerPanel_PlayheadDoesNotBleedIntoAnOnScreenHighlightName(t *testing.T) {
	const frames = 3000 // 100s at 30fps, matching markerTestContext's own Timeline
	const mid = frames / 2
	highlights := []Highlight{{Name: "Hill climb", From: 45 * time.Second, To: 55 * time.Second}}
	c, img, ctx := markerTestContext(t, 1920, 1080, highlights, nil)
	box := Box{X: 40, Y: 900, W: 1840, H: 140}
	p := MarkerPanel{}.Prepare(ctx, box).(*markerPainter)
	if !p.ok {
		t.Fatal("precondition: Prepare bailed out")
	}
	if p.frames != frames {
		t.Fatalf("precondition: painter carries %d frames, want %d -- the fixture is wrong", p.frames, frames)
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{Index: mid, Interval: 0, IntervalWeight: 1})

	w, h, err := ctx.Fonts.Measure(highlights[0].Name, p.namePx)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	glyphBox := Box{X: p.anchorX[0] - w/2, Y: p.nameY - h/2, W: w, H: h}

	if got := countNearInBox(img, glyphBox, c.Theme.Accent, 12); got != 0 {
		t.Errorf("%d accent-coloured pixels inside the highlight's own glyph box; the playhead bled into the name it must clear", got)
	}
	// And the highlight's own name DID draw somewhere in there, or a margin
	// so generous the playhead could never reach it would trivially pass
	// this test by drawing nothing worth colliding with at all.
	if got := countNearInBox(img, glyphBox, c.Theme.Foreground, 40); got == 0 {
		t.Fatal("no foreground ink in the highlight's own glyph box either; the fixture drew no name to test the margin against")
	}
}

// TestMarkerPanel_PlayheadClearsTheLabelNameRowAcrossEveryBoxShape is the
// UPWARD mirror of TestMarkerPanel_PlayheadClearsTheHighlightNameRowAcrossEveryBoxShape,
// against the row the label name moved INTO: above the ribbon, nothing sat
// there before this move, so the playhead's upward reach (headExtend) never
// had anything to clear and never needed a test. It does now.
func TestMarkerPanel_PlayheadClearsTheLabelNameRowAcrossEveryBoxShape(t *testing.T) {
	labels := []Label{{Name: "Lighthouse", At: 15 * time.Second, Video: 3 * time.Second, FirstFrame: 450, LastFrame: 539}}
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
			tl, err := NewSegmentedTimeline(highlightEpoch, 100*time.Second, 30, 1, nil)
			if err != nil {
				t.Fatalf("NewSegmentedTimeline: %v", err)
			}
			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			ctx := &Context{
				Width: s.frameW, Height: s.frameH, FontScale: 0.05, Fonts: faces,
				Timeline: tl, Labels: labels,
			}
			p := MarkerPanel{}.Prepare(ctx, s.box).(*markerPainter)
			if !p.ok {
				t.Fatal("precondition: Prepare bailed out; the fixture is wrong")
			}

			_, glyphH, err := faces.Measure("Ap", p.labelNamePx)
			if err != nil {
				t.Fatalf("Measure: %v", err)
			}
			playheadTop := p.ribbon.Y - p.headExtend
			labelBottom := p.labelNameY + glyphH/2

			if margin := playheadTop - labelBottom; margin <= 0 {
				t.Errorf("playhead's own top tip (y=%.2f) reaches at or past the label name row's own bottom (y=%.2f); "+
					"margin = %.2f, want a positive margin at every box shape", playheadTop, labelBottom, margin)
			}
		})
	}
}

// TestMarkerPanel_TickClearsTheLabelNameRowAcrossEveryBoxShape is the
// mirror of the test above for the TICK rather than the playhead -- the
// constraint most likely to be skipped, because a tick reads as decoration
// next to the
// playhead's much more visible sweep, and it is easy to reason "the
// playhead already clears this row, so the shorter tick must too" without
// writing anything down. That reasoning happens to hold for the constants
// chosen in Prepare today, but it is an accident of those two numbers, not
// a property of the geometry -- nothing stops a future change from growing
// tickExtend on its own, past headExtend, without anyone thinking to check
// it against the label row it was never tested against before.
//
// PROVED LOAD-BEARING while writing this test: temporarily widening
// tickExtend in Prepare until it exceeded the measured margin below made
// this test fail with a non-positive margin; reverted immediately after
// confirming it, with no production code left changed.
func TestMarkerPanel_TickClearsTheLabelNameRowAcrossEveryBoxShape(t *testing.T) {
	labels := []Label{{Name: "Lighthouse", At: 15 * time.Second, Video: 3 * time.Second, FirstFrame: 450, LastFrame: 539}}
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
			tl, err := NewSegmentedTimeline(highlightEpoch, 100*time.Second, 30, 1, nil)
			if err != nil {
				t.Fatalf("NewSegmentedTimeline: %v", err)
			}
			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			ctx := &Context{
				Width: s.frameW, Height: s.frameH, FontScale: 0.05, Fonts: faces,
				Timeline: tl, Labels: labels,
			}
			p := MarkerPanel{}.Prepare(ctx, s.box).(*markerPainter)
			if !p.ok {
				t.Fatal("precondition: Prepare bailed out; the fixture is wrong")
			}

			_, glyphH, err := faces.Measure("Ap", p.labelNamePx)
			if err != nil {
				t.Fatalf("Measure: %v", err)
			}
			tickTop := p.ribbon.Y - p.tickExtend
			labelBottom := p.labelNameY + glyphH/2

			if margin := tickTop - labelBottom; margin <= 0 {
				t.Errorf("tick's own top tip (y=%.2f) reaches at or past the label name row's own bottom (y=%.2f); "+
					"margin = %.2f, want a positive margin at every box shape", tickTop, labelBottom, margin)
			}
		})
	}
}

// --- the pixel-identical regression -----------------------------------------

// oldLandscapeLayout and oldPortraitLayout are NOT a historical snapshot
// frozen at some past commit -- they are an independently-written statement
// of "the current LandscapeLayout/PortraitLayout minus the marker row",
// written by hand from the same source the real functions are, and they
// must be updated deliberately whenever those functions' shape changes.
// (An earlier version of this comment claimed they were a fixed snapshot
// that would never need to change; that claim did not survive the
// elevation/distance merge and was wrong to make in the first place --
// deriving these by pruning MarkerPanel out of the real layout at test time
// would make TestNoHighlightOrLabelRenderIsPixelIdenticalToBeforeThisFeature
// compare the real Resolve call with itself and assert nothing.)
//
// Keeping these as literals, rather than a derivation, is what makes that
// test non-trivial: it is a second, independent statement of the tree that
// the real functions are checked against, so a change that silently altered
// the marker row's own weight or a sibling's box would still be caught.
// See TestNoHighlightOrLabelRenderIsPixelIdenticalToBeforeThisFeature.
//
// This was the first change to exercise the "update deliberately" rule the
// comment above states: the elevation/distance row changed from a Row with
// 4:1/3:1 weights to an Alt slot with none, because the two panels no longer
// draw in the same frame (see layouts.go). At that point the marker row's
// own weight was untouched, and so was the band's.
//
// It is NOT untouched any more, and this is the SECOND deliberate move,
// dated to when the marker strip's own marks were folded onto the elevation
// profile's axis (see elevation.go's "the name rows" and buildMarks): once
// MarkerPanel's row is pruned in every case the profile is placed at all --
// not only when neither --highlight nor --label is configured, but also
// (by internal/render's own keep filter) when the profile is drawing the
// configured marks itself -- the row's own weight moved onto the band
// rather than falling upward to the panels above it. Landscape's Alt grew
// from 1 to 2; portrait's from 2 to 3 (portrait's own total is larger, so
// the same single vacated unit of weight is worth proportionally less
// there -- see PortraitLayout's own comment for the number).
//
// The consequence stated to the user, and accepted: an ORDINARY render --
// no highlight, no label -- deliberately gained elevation-profile height by
// this change. That is exactly what going red here is reporting, and it is
// the reason these two functions are updated rather than the assertion
// weakened. What this test still guards, unchanged, is the mechanism, not
// the specific weight: that MarkerPanel's row, whenever pruned, costs its
// siblings nothing beyond what the band's own weight already accounts for.
func oldLandscapeLayout() Layout {
	return Layout{
		Name:      "landscape",
		Margin:    0.03,
		FontScale: 0.05,
		Root: Slot{Dir: Col, Children: []Slot{
			{Dir: Row, Weight: 4, Children: []Slot{
				{Dir: Col, Weight: 3, Children: []Slot{
					{Panel: RoutePanel{}, Weight: 3, Pad: 0.01},
					{Panel: ElapsedPanel{}, Weight: 2, Pad: 0.01},
				}},
				{Dir: Col, Weight: 1, Children: []Slot{
					{Panel: HeartRate(), Pad: 0.01},
					{Panel: Pace(), Pad: 0.01},
					{Panel: Power(), Pad: 0.01},
					{Panel: Cadence(), Pad: 0.01},
				}},
			}},
			{Dir: Alt, Weight: 2, Children: []Slot{
				{Panel: ElevationPanel{}, Pad: 0.01},
				{Panel: Distance(), Pad: 0.01},
			}},
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
			{Panel: ElapsedPanel{}, Weight: 2, Pad: 0.01},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: HeartRate(), Pad: 0.01},
				{Panel: Pace(), Pad: 0.01},
			}},
			{Dir: Row, Weight: 2, Children: []Slot{
				{Panel: Power(), Pad: 0.01},
				{Panel: Cadence(), Pad: 0.01},
			}},
			{Dir: Alt, Weight: 3, Children: []Slot{
				{Panel: ElevationPanel{}, Pad: 0.01},
				{Panel: Distance(), Pad: 0.01},
			}},
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

// TestNoHighlightOrLabelRenderIsPixelIdenticalToBeforeThisFeature is the
// strongest claim available for this panel: adding MarkerPanel's row to both
// layouts must not move a single pixel of a render that configures neither
// --highlight nor --label. Extended from the highlight-only original to
// cover Labels too -- a regression that zero-checked only Highlights in
// Accepts, or that gave MarkerPanel an empty-but-non-nil Labels slice by
// mistake somewhere upstream, would have passed the old version of this
// test and failed this one.
//
// It works BECAUSE of where the row sits and how Resolve prunes: Accepts
// declines with neither configured, and Layout.Resolve removes a declined
// leaf from the tree BEFORE any box is divided -- so the surviving rows'
// weight fractions are computed exactly as if this leaf had never existed
// (see Layout.Resolve's own doc comment). This test pins that mechanism
// against a frozen copy of the layouts as they stood immediately before
// this feature, rather than trusting the argument on its own.
func TestNoHighlightOrLabelRenderIsPixelIdenticalToBeforeThisFeature(t *testing.T) {
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
				// The point of the test: no --highlight and no --label
				// were given.
				Highlights: nil,
				Labels:     nil,
			}
			for _, i := range []int{0, tl.Frames() / 2, tl.Frames() - 1} {
				got := renderFullFrame(t, ctx, c.newL, i)
				want := renderFullFrame(t, ctx, c.oldL, i)
				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("frame %d: rendering %s WITH MarkerPanel's row differs from rendering it without one; "+
						"a render with no --highlight and no --label must be pixel-identical to before this feature existed",
						i, c.name)
				}
			}
		})
	}
}
