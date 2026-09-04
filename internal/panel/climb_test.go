package panel

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// climbPainterFor mirrors elevationPainterFor (elevation_test.go): it builds
// the same Context a real render would (see elevationContext) and returns
// the concrete painter so a test can inspect the fields Prepare resolved
// rather than only what Static/Dynamic put on screen.
func climbPainterFor(t *testing.T, track *fitactivity.Track, box Box, fw, fh int) *climbPainter {
	t.Helper()
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	ctx := elevationContext(track)
	ctx.Width, ctx.Height, ctx.FontScale, ctx.Fonts = fw, fh, 0.05, faces
	p, ok := ClimbPanel{}.Prepare(ctx, box).(*climbPainter)
	if !ok {
		t.Fatal("Prepare did not return a climb painter")
	}
	return p
}

// TestClimbPanel_AcceptsAgreesWithElevationPanel pins the property the whole
// round exists to guarantee: ClimbPanel and ElevationPanel must never
// disagree about whether ctx.Elevation is worth drawing from, because one
// panel drawing on a model the other declined is precisely the two-panels-
// disagree failure the shared elevationIsPlottable predicate exists to
// prevent (see elevation.go). This is deliberately an EQUALITY check, not
// "ClimbPanel accepts whenever ElevationPanel does" -- a one-directional
// implication would still let the two disagree in the other direction.
func TestClimbPanel_AcceptsAgreesWithElevationPanel(t *testing.T) {
	track := hillTrack(0, 5000, 300)
	present := func(has bool) int {
		if has {
			return len(track.Samples)
		}
		return 0
	}

	cases := []struct {
		name                string
		elevation, distance bool
	}{
		{"both carried", true, true},
		{"elevation carried, distance not", true, false},
		{"distance carried, elevation not", false, true},
		{"neither carried", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := inspect.Report{
				Samples: len(track.Samples),
				Metrics: []inspect.Metric{
					{Name: inspect.MetricElevation, Present: present(c.elevation)},
					{Name: inspect.MetricDistance, Present: present(c.distance)},
				},
			}
			ctx := &Context{Track: track, Report: rep, Elevation: BuildElevation(track, DefaultElevationTuning(track))}

			gotClimb := (ClimbPanel{}).Accepts(ctx)
			gotElevation := (ElevationPanel{}).Accepts(ctx)
			if gotClimb != gotElevation {
				t.Errorf("elevation=%v distance=%v: ClimbPanel.Accepts=%v, ElevationPanel.Accepts=%v -- "+
					"both must answer the identical elevationIsPlottable predicate", c.elevation, c.distance, gotClimb, gotElevation)
			}
		})
	}

	// A flat profile: both metrics fully carried, but there is no range to
	// draw a scale against (see TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile).
	// Both panels must decline together here too.
	t.Run("flat profile", func(t *testing.T) {
		base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		flat := &fitactivity.Track{Samples: []fitactivity.Sample{
			{Time: base, HasDistance: true, Distance: 0, HasElevation: true, Elevation: 50},
			{Time: base.Add(time.Second), HasDistance: true, Distance: 100, HasElevation: true, Elevation: 50},
			{Time: base.Add(2 * time.Second), HasDistance: true, Distance: 200, HasElevation: true, Elevation: 50},
		}}
		ctx := elevationContext(flat)
		if (ElevationPanel{}).Accepts(ctx) {
			t.Fatal("precondition failed: a flat profile must decline")
		}
		if (ClimbPanel{}).Accepts(ctx) {
			t.Error("ClimbPanel accepted a flat profile that ElevationPanel declines")
		}
	})

	// ctx.Elevation left nil, the way a Context nobody populated it on looks
	// (see Context.Elevation's own doc comment) -- both must decline rather
	// than one of them reaching for a model that was never built.
	t.Run("nil elevation model", func(t *testing.T) {
		ctx := &Context{Track: track, Report: inspect.Build(track)}
		if (ElevationPanel{}).Accepts(ctx) || (ClimbPanel{}).Accepts(ctx) {
			t.Error("expected both panels to decline with ctx.Elevation nil")
		}
	})
}

// TestClimbPanel_MaxTotalIsTheLargerOfGainAndLoss pins the shared-scale
// arithmetic Prepare resolves once: maxTotal must be max(totalGain,
// totalLoss), never either total normalised against itself -- see the type
// doc comment's "the shared scale" section for why per-track normalisation
// is the misreading this design exists to prevent.
func TestClimbPanel_MaxTotalIsTheLargerOfGainAndLoss(t *testing.T) {
	track := hillTrack(0, 5000, 400)
	box := Box{X: 0, Y: 0, W: 900, H: 120}
	p := climbPainterFor(t, track, box, 1920, 1080)

	if p.totalGain <= 0 || p.totalLoss <= 0 {
		t.Fatalf("precondition failed: this hill should have a real gain and a real loss, got gain=%v loss=%v",
			p.totalGain, p.totalLoss)
	}
	if want := math.Max(p.totalGain, p.totalLoss); p.maxTotal != want {
		t.Errorf("maxTotal = %v, want max(totalGain, totalLoss) = %v", p.maxTotal, want)
	}
}

// rightmostInkOnRow scans row y from the right, returning the rightmost x
// whose pixel differs from bg, or -1 if the row is empty. It is how this
// file finds a bar's own right edge without trusting the arithmetic that
// placed it -- the same reasoning
// TestElevationPanel_LabelsAreDrawnOnTheAxisTheyName gives for scanning
// pixels rather than re-deriving a position with the function under test.
func rightmostInkOnRow(img *image.RGBA, y int, bg color.Color) int {
	br, bgc, bb, _ := bg.RGBA()
	bounds := img.Bounds()
	for x := bounds.Dx() - 1; x >= 0; x-- {
		r, g, b, _ := img.At(x, y).RGBA()
		if r != br || g != bgc || b != bb {
			return x
		}
	}
	return -1
}

// TestClimbPanel_StaticGhostsAreProportionalToTheSharedScale renders only
// Static and checks each track's ghost reaches exactly the fraction of the
// track its own total is of maxTotal -- the "shorter track's ghost simply
// ends short of the box" property the type doc comment states as the whole
// point of the shared scale.
func TestClimbPanel_StaticGhostsAreProportionalToTheSharedScale(t *testing.T) {
	const w, h = 900, 120
	track := hillTrack(0, 5000, 400)
	box := Box{X: 0, Y: 0, W: w, H: h}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := climbPainterFor(t, track, box, w, h)

	c.Fill(c.Theme.Background)
	p.Static(c)

	gainRight := rightmostInkOnRow(img, int(p.gainY), c.Theme.Background)
	lossRight := rightmostInkOnRow(img, int(p.lossY), c.Theme.Background)
	if gainRight < 0 || lossRight < 0 {
		t.Fatal("no ghost ink was found on one of the two rows")
	}

	wantGainRight := p.trackX + p.trackW*(p.totalGain/p.maxTotal)
	wantLossRight := p.trackX + p.trackW*(p.totalLoss/p.maxTotal)
	const tol = 2.0
	if math.Abs(float64(gainRight)-wantGainRight) > tol {
		t.Errorf("gain ghost's rightmost ink at x=%d, want about %v", gainRight, wantGainRight)
	}
	if math.Abs(float64(lossRight)-wantLossRight) > tol {
		t.Errorf("loss ghost's rightmost ink at x=%d, want about %v", lossRight, wantLossRight)
	}

	// Whichever total is the larger reaches the track's own right edge --
	// that IS "the box" the shorter one falls short of.
	largerWant := math.Max(wantGainRight, wantLossRight)
	if want := p.trackX + p.trackW; math.Abs(largerWant-want) > tol {
		t.Errorf("the larger total's own ghost should reach the track's right edge %v, computed at %v", want, largerWant)
	}
}

// TestClimbPanel_DynamicFillsToTheCurrentCumulativeFigure checks the live
// fill -- drawn in Dynamic, over the ghost Static already drew -- reaches
// exactly the fraction of the shared scale the CURRENT distance's gain and
// loss represent, using the identical model query (AtDistance) the panel
// itself calls so this test cannot silently duplicate a wrong answer.
func TestClimbPanel_DynamicFillsToTheCurrentCumulativeFigure(t *testing.T) {
	const w, h = 900, 120
	track := hillTrack(0, 5000, 400)
	box := Box{X: 0, Y: 0, W: w, H: h}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := climbPainterFor(t, track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)

	const d = 2500.0
	_, gain, loss := p.model.AtDistance(d)
	if gain <= 0 || loss <= 0 {
		t.Fatalf("precondition failed: want a real gain and loss reached by d=%v, got gain=%v loss=%v", d, gain, loss)
	}
	p.Dynamic(c, Frame{Sample: fitactivity.Sample{HasDistance: true, Distance: d}})

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	check := func(row string, y float64, wantX float64) {
		got := img.RGBAAt(int(wantX)-2, int(y))
		if got != fg {
			t.Errorf("%s row: pixel just before the expected fill edge (x=%v) is %v, want the foreground fill colour %v",
				row, wantX-2, got, fg)
		}
	}
	check("gain", p.gainY, p.trackX+p.trackW*(gain/p.maxTotal))
	check("loss", p.lossY, p.trackX+p.trackW*(loss/p.maxTotal))
}

// TestClimbPanel_DropoutWashesBothTracksFullLength pins the ordinary
// absent-data case: no distance at all this frame. Both tracks must wash
// their FULL length (not a fraction, and not nothing), because a fill of
// zero width is pixel-identical to "no climbing yet" -- the confident lie
// this project refuses (see the type doc comment's "Absence" section).
func TestClimbPanel_DropoutWashesBothTracksFullLength(t *testing.T) {
	const w, h = 900, 120
	track := hillTrack(0, 5000, 400)
	box := Box{X: 0, Y: 0, W: w, H: h}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := climbPainterFor(t, track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)

	p.Dynamic(c, Frame{}) // the zero Frame: HasSample false, HasDistance false.

	// blendOver/closeRGBA (elevation_test.go) are the same rasterizer-aware
	// comparison ElevationPanel's own dropout test uses: the composited
	// pixel is neither Background nor Absent outright, and byte-exact
	// equality against a hand-computed blend is the wrong tool for a real
	// rasterizer's own rounding.
	checkFullWash("gain", p, img, c, p.gainY, t)
	checkFullWash("loss", p, img, c, p.lossY, t)
}

// checkFullWash asserts the pixel just inside the track's own right edge is
// the absent wash -- Fade(Theme.Absent, ...) composited over whatever Static
// already drew there. That underlying colour is Theme.Dim wherever this
// row's own ghost reaches this far (see the type doc comment: whichever
// total equals maxTotal has a ghost spanning the WHOLE track), and
// Theme.Background beyond it otherwise -- both are legitimate, so this
// accepts either rather than assuming one, which is what a translucent wash
// drawn over a Painter's own persistent chrome always has to allow for.
func checkFullWash(row string, p *climbPainter, img *image.RGBA, c *Canvas, y float64, t *testing.T) {
	t.Helper()
	x := int(p.trackX + p.trackW - 2) // just inside the track's own right edge
	got := img.RGBAAt(x, int(y))
	alpha := elevationAbsentFillAlpha(c.Theme)
	overBackground := blendOver(c.Theme.Background, c.Theme.Absent, alpha)
	overDim := blendOver(c.Theme.Dim, c.Theme.Absent, alpha)
	if !closeRGBA(got, overBackground, 2) && !closeRGBA(got, overDim, 2) {
		t.Errorf("%s row: pixel near the track's right edge is %v, want the absent wash over either the background %v "+
			"or the ghost %v -- an absent instant must wash the WHOLE track, not stop partway through it",
			row, got, overBackground, overDim)
	}
}

// TestClimbPanel_PreDataTrapTakesThePlaceholderBranchNotAConfidentZero is
// THE test the plan calls out as the most important case in this round.
//
// fitactivity.ElevationModel.AtDistance clamps to its own ends, so asked for
// a distance before the model's own StartDistance it answers gain 0, loss 0
// -- confidently, with no error. A track built on hillTrack(startD, endD, n)
// with startD > 0 has exactly that gap, and a frame whose Sample carries a
// KNOWN distance inside it (HasDistance true, but Distance < StartDistance)
// must still take the placeholder branch -- washing the whole track, the
// identical treatment the ordinary dropout gets -- rather than reading the
// clamp's confident zero and drawing an honest-looking but false "0 m" fill.
func TestClimbPanel_PreDataTrapTakesThePlaceholderBranchNotAConfidentZero(t *testing.T) {
	const w, h = 900, 120
	const startD, endD = 10200, 12400
	track := hillTrack(startD, endD, 400)
	box := Box{X: 0, Y: 0, W: w, H: h}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := climbPainterFor(t, track, box, w, h)
	if p.profileStart <= 0 {
		t.Fatalf("precondition failed: want a non-zero profileStart from this fixture, got %v", p.profileStart)
	}
	c.Fill(c.Theme.Background)
	p.Static(c)

	// A distance the render KNOWS -- HasDistance is true -- but short of the
	// model's own recorded start. AtDistance(halfway) would answer a
	// confident (0, 0) here; the panel must never ask it to.
	halfway := p.profileStart / 2
	p.Dynamic(c, Frame{Sample: fitactivity.Sample{HasDistance: true, Distance: halfway}})

	checkFullWash("gain", p, img, c, p.gainY, t)
	checkFullWash("loss", p, img, c, p.lossY, t)
}

// TestClimbPanel_PastTheRecordedEndIsNotTreatedAsAbsent pins the opposite
// edge, named explicitly in the type doc comment: a distance PAST the
// model's own TotalDistance is not a trap, because by then the activity's
// own final totals are the honest, fully-known answer. The fill must reach
// the FULL track (clamped, exactly as ElevationPanel's own fill does past
// its axis end), not wash as absent.
func TestClimbPanel_PastTheRecordedEndIsNotTreatedAsAbsent(t *testing.T) {
	const w, h = 900, 120
	track := hillTrack(0, 5000, 400)
	box := Box{X: 0, Y: 0, W: w, H: h}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := climbPainterFor(t, track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)

	p.Dynamic(c, Frame{Sample: fitactivity.Sample{HasDistance: true, Distance: p.axisEnd + 500}})

	// Past axisEnd the model clamps to the activity's own FINAL totals, so
	// the fill reaches exactly totalGain/maxTotal and totalLoss/maxTotal of
	// the track -- only the LARGER of the two (== maxTotal) reaches the
	// track's own right edge; checking both rows at that edge would wrongly
	// expect the shorter one to have filled past its own true total.
	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	check := func(row string, y, total float64) {
		x := int(p.trackX + p.trackW*(total/p.maxTotal) - 2)
		if got := img.RGBAAt(x, int(y)); got != fg {
			t.Errorf("%s row past the recorded end is %v at x=%d, want the live foreground fill %v -- "+
				"this is the activity's own known final total, not an absent instant", row, got, x, fg)
		}
	}
	check("gain", p.gainY, p.totalGain)
	check("loss", p.lossY, p.totalLoss)
}

// TestClimbPanel_ScalesWithFrameDimensionsNotAPixelConstant checks the
// contract's own "how does it scale" question the cheap way: the same
// track, prepared against boxes whose ratio mirrors 1080p, 4K and the
// portrait tree, must produce a track width and bar height that scale with
// the box rather than sitting at a size unrelated to it.
func TestClimbPanel_ScalesWithFrameDimensionsNotAPixelConstant(t *testing.T) {
	track := hillTrack(0, 5000, 300)
	sizes := []struct {
		name   string
		w, h   float64
		fw, fh int
	}{
		{"1080p-shaped", 1800, 100, 1920, 1080},
		{"4K-shaped", 3600, 200, 3840, 2160},
		{"portrait-shaped", 980, 120, 1080, 1920},
	}
	var prevW, prevBar float64
	for i, s := range sizes {
		box := Box{X: 0, Y: 0, W: s.w, H: s.h}
		p := climbPainterFor(t, track, box, s.fw, s.fh)
		if p.trackW <= 0 {
			t.Fatalf("%s: trackW <= 0", s.name)
		}
		if i > 0 && p.trackW == prevW {
			t.Errorf("%s: trackW is identical to the previous box's (%v) despite a different box width", s.name, prevW)
		}
		if i > 0 && p.barH == prevBar {
			t.Errorf("%s: barH is identical to the previous box's (%v) despite a different box height", s.name, prevBar)
		}
		prevW, prevBar = p.trackW, p.barH
	}
}
