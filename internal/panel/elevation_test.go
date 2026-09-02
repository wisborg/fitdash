package panel

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// hillTrack builds a track whose cumulative distance STARTS AT startD rather
// than at zero, with elevation varying over it.
//
// startD == 0 is the ordinary whole-activity case: BuildElevationModel's own
// StartDistance lands at zero because the first sample already carries both
// distance and elevation. startD > 0 stands in for BuildElevationModel's
// filter skipping the opening samples that have distance but no valid
// altitude yet (a barometer settling) -- see elevation.go's own doc comment
// for why the AXIS still starts at zero regardless, and why the resulting
// gap is the "pre-data region" several tests below exercise. On a real
// recording that gap is a few metres; the fixtures below use gaps orders of
// magnitude larger purely so the assertions land many pixels apart rather
// than sub-pixel -- the mechanism does not care why the gap exists, only
// that it does.
func hillTrack(startD, endD float64, n int) *fitactivity.Track {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, n)
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n-1)
		samples[i] = fitactivity.Sample{
			Time:         base.Add(time.Duration(i) * time.Second),
			HasDistance:  true,
			Distance:     startD + f*(endD-startD),
			HasElevation: true,
			Elevation:    50 + 40*math.Sin(f*2*math.Pi),
		}
	}
	return &fitactivity.Track{Samples: samples}
}

func elevationPainterFor(t *testing.T, track *fitactivity.Track, box Box, fw, fh int) *elevationPainter {
	t.Helper()
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{
		Track: track, Report: inspect.Build(track),
		Width: fw, Height: fh, FontScale: 0.05, Fonts: faces,
	}
	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	return p
}

// TestElevationPanel_AxisSpansZeroToTotalNotTheProfilesOwnStart is the test
// this panel exists to pass, for the CURRENT design: the AXIS spans
// 0..axisEnd (the activity's own start and end), and the recorded TRACE is a
// proper subset of it, beginning at profileStart rather than at the axis's
// own left edge whenever the model's StartDistance is non-zero.
//
// An earlier version of this panel spanned the axis itself
// StartDistance..TotalDistance, which is the right span for a caller that
// renders CLIPS (see elevation.go's own doc comment) but wrong here, since
// fitdash always renders a whole activity. This test pins the replacement:
// the fraction of the axis before profileStart is real, honestly-labelled
// distance with nothing plotted on it yet, not squeezed out of the picture.
func TestElevationPanel_AxisSpansZeroToTotalNotTheProfilesOwnStart(t *testing.T) {
	const startD, endD = 10200, 12400
	box := Box{X: 100, Y: 50, W: 800, H: 200}
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, 1920, 1080)

	if len(p.xs) < 2 {
		t.Fatal("no profile was built")
	}
	if p.profileStart < startD-1 || p.profileStart > startD+50 {
		t.Fatalf("profileStart = %v, want about %v", p.profileStart, startD)
	}
	if p.axisEnd < endD-1 || p.axisEnd > endD+50 {
		t.Fatalf("axisEnd = %v, want about %v", p.axisEnd, endD)
	}

	// The axis's own ends: distance zero is the plot's left edge, axisEnd is
	// its right edge, regardless of where the recorded trace begins.
	if got, want := p.xForDistance(0), p.plot.X; math.Abs(got-want) > 0.001 {
		t.Errorf("xForDistance(0) = %v, want the plot's left edge %v", got, want)
	}
	if got, want := p.xForDistance(p.axisEnd), p.plot.X+p.plot.W; math.Abs(got-want) > 0.001 {
		t.Errorf("xForDistance(axisEnd) = %v, want the plot's right edge %v", got, want)
	}

	// The trace's own first point sits well INSIDE the plot, at the fraction
	// of the FULL axis its own distance represents -- not at the plot's left
	// edge, which is what an axis still spanning profileStart..axisEnd (the
	// earlier design) would have produced.
	//
	// The float64 conversions are load-bearing. startD and endD are untyped
	// INTEGER constants, so startD/endD is constant integer division and
	// evaluates to 0.
	wantX0 := p.plot.X + p.plot.W*(float64(startD)/float64(endD))
	if math.Abs(p.xs[0]-wantX0) > 1 {
		t.Errorf("the trace begins at x=%v, want %v (the plot's own fraction startD/axisEnd)", p.xs[0], wantX0)
	}
	if math.Abs(p.xs[0]-p.plot.X) < p.plot.W*0.1 {
		t.Errorf("the trace begins at x=%v, suspiciously close to the plot's left edge %v -- "+
			"the axis may still be spanning profileStart..axisEnd rather than 0..axisEnd", p.xs[0], p.plot.X)
	}
	if got, want := p.xs[len(p.xs)-1], p.plot.X+p.plot.W; math.Abs(got-want) > 1 {
		t.Errorf("the trace ends at x=%v, want the plot's right edge %v", got, want)
	}
}

// TestElevationPanel_LabelsAreDrawnOnTheAxisTheyName finds the labels in the
// rendered pixels rather than trusting the arithmetic that should have placed
// them.
//
// The distinction matters and was measured: an earlier version of this file
// checked label positions by calling xForDistance itself, which is the very
// function Static is supposed to use -- so a Static that placed its end label
// at 90% of the plot width by its own arithmetic passed every assertion. The
// labels and the trace would have disagreed on screen with nothing reporting
// it.
//
// Scanning the label row is the only check that can see where a label
// actually is. The band below the plot holds only the two distance labels: the
// elevation labels sit in the gutter, left of the plot's left edge.
func TestElevationPanel_LabelsAreDrawnOnTheAxisTheyName(t *testing.T) {
	const startD, endD = 10200, 12400
	box := Box{X: 0, Y: 0, W: 900, H: 260}

	img := image.NewRGBA(image.Rect(0, 0, 900, 260))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, 900, 260)

	c.Fill(c.Theme.Background)
	p.Static(c)

	// The row beneath the plot, from the plot's left edge rightward.
	top := int(p.plot.Y + p.plot.H)
	minX, maxX := 1<<30, -1
	br, bg, bb, _ := c.Theme.Background.RGBA()
	for y := top + 1; y < 260; y++ {
		for x := int(p.plot.X); x < 900; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r != br || g != bg || b != bb {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	if maxX < 0 {
		t.Fatal("no distance labels were drawn")
	}

	// The start label is left-anchored at the axis's start, which is the
	// plot's left edge; the end label is right-anchored at the axis's end,
	// which is its right edge. Text has side bearings, so a few pixels of
	// slack -- but not the 10% of the plot a wrong origin would cost.
	tol := p.plot.W * 0.02
	if math.Abs(float64(minX)-p.plot.X) > tol {
		t.Errorf("the leftmost label ink is at x=%d, want the plot's left edge %v", minX, p.plot.X)
	}
	if want := p.plot.X + p.plot.W; math.Abs(float64(maxX)-want) > tol {
		t.Errorf("the rightmost label ink is at x=%d, want the plot's right edge %v", maxX, want)
	}
}

// TestElevationPanel_StartLabelNamesTheAxisOrigin pins that the start label
// names what the axis actually starts at -- zero -- regardless of where the
// model's own StartDistance (profileStart) happens to land. Naming
// profileStart instead, as an earlier version did, would repeat on this axis
// exactly the label/axis disagreement TestElevationPanel_LowLabelMovesUpWithTheFloor
// exists to catch on the other one.
func TestElevationPanel_StartLabelNamesTheAxisOrigin(t *testing.T) {
	for _, tc := range []struct {
		name         string
		startD, endD float64
	}{
		{"an activity recorded from its own start line", 0, 5000},
		{"an activity whose profile begins well after zero", 10200, 12400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := Box{X: 0, Y: 0, W: 900, H: 260}
			p := elevationPainterFor(t, hillTrack(tc.startD, tc.endD, 300), box, 900, 260)
			if want := formatDistance(0); p.startLabel != want {
				t.Errorf("startLabel = %q, want %q (the axis's own origin) regardless of profileStart=%v",
					p.startLabel, want, p.profileStart)
			}
		})
	}
}

// TestElevationPanel_YForElevationSpansTheFloorNotTheMinimum pins the axis
// span itself, independent of any drawing: yForElevation(maxElev) is the
// plot's top edge regardless of the floor, but yForElevation(minElev) is now
// strictly ABOVE the plot's bottom edge -- the floor sits below it, and the
// minimum is no longer the bottom of the axis.
func TestElevationPanel_YForElevationSpansTheFloorNotTheMinimum(t *testing.T) {
	const startD, endD = 0, 5000
	box := Box{X: 0, Y: 0, W: 600, H: 200}
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, 600, 200)

	if got, want := p.yForElevation(p.maxElev), p.plot.Y; math.Abs(got-want) > 0.001 {
		t.Errorf("yForElevation(maxElev) = %v, want the plot's top edge %v", got, want)
	}
	bottom := p.plot.Y + p.plot.H
	if got := p.yForElevation(p.minElev); !(got < bottom-0.5) {
		t.Errorf("yForElevation(minElev) = %v, want strictly above the plot's bottom edge %v -- "+
			"a floor below the minimum should lift it off the axis's own bottom", got, bottom)
	}
}

// TestElevationPanel_LowLabelMovesUpWithTheFloor is the regression this
// change could introduce, and the test most worth writing first: the low
// label's ink must be found where yForElevation places it, NOT at the plot's
// hard-coded bottom edge where an earlier version of this panel drew it.
//
// The expected position is derived from elevationFloorHeadroom directly,
// NOT by calling yForElevation from the test -- the same discipline
// TestElevationPanel_LabelsAreDrawnOnTheAxisTheyName's own comment gives for
// why calling the function under test to compute its own expectation proves
// nothing: a Static that placed the label by different, equally wrong
// arithmetic would pass a test that asks the same function twice.
func TestElevationPanel_LowLabelMovesUpWithTheFloor(t *testing.T) {
	const startD, endD = 0, 5000
	box := Box{X: 0, Y: 0, W: 900, H: 260}
	// fw, fh is a FRAME the box sits inside, deliberately larger than the box
	// on both axes rather than equal to it (as this test used before the axis
	// chrome moved off unit -- see elevAxisLabelFraction's own doc comment):
	// the label size now comes from ctx.BasePx(), which is derived from fw/fh
	// alone, and a frame sized to exactly match this one panel's own box is
	// not a shape any real layout ever produces -- every real placement gives
	// this panel a box smaller than the frame it sits in. 1280x720 keeps that
	// true here too, and keeps the low label's glyphs large enough (previously
	// they'd only be 5.85px at a 900x260 "frame", too small to compare
	// centroids to +-1px precision) for this test's own centroid comparison to
	// resolve the shift it exists to catch.
	fw, fh := 1280, 720

	img := image.NewRGBA(image.Rect(0, 0, fw, fh))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, fw, fh)

	c.Fill(c.Theme.Background)
	p.Static(c)

	// The gutter's lower half: left of the plot's own left edge and below
	// its vertical midpoint, which isolates the low label's ink from the
	// high label's (drawn near p.plot.Y, well above this region) without
	// needing to know either one's exact extent up front.
	//
	// The width stops a few pixels short of the plot's own left edge, NOT at
	// it: Canvas.Polyline's stroke caps bleed a pixel or two past a line's
	// nominal endpoint (measured directly: a 2.6px-wide horizontal line
	// starting at x=48 painted ink as far left as x=46), and the baseline
	// rule Static draws at yForElevation(floorElev) starts exactly at
	// p.plot.X. A gutter now narrow enough that the low label sits close to
	// the baseline's own row -- which the smaller axis chrome this test
	// exists to check for makes more likely than it was at the old, wider
	// gutter -- would otherwise let that bleed into this measurement and
	// shift the centroid away from the label's own ink, for a reason that
	// has nothing to do with where the label itself was drawn.
	lowerGutter := Box{X: 0, Y: p.plot.Y + p.plot.H*0.5, W: math.Max(0, p.plot.X-4), H: float64(fh) - (p.plot.Y + p.plot.H*0.5)}
	gotCy, n := centroidY(img, lowerGutter, c.Theme.Background)
	if n == 0 {
		t.Fatal("no ink found in the lower gutter; the low label was not drawn")
	}

	// Two independent reference renders of the SAME string, at two
	// candidate y's derived by hand rather than by calling yForElevation:
	// oldY is the plot's hard-coded bottom edge, where an earlier version of
	// this panel drew the low label; wantY is elevationFloorHeadroom's own
	// arithmetic for where the minimum now falls once the floor sits below
	// it. Comparing Static's actual ink against these two renders -- rather
	// than against a bare y coordinate -- sidesteps having to know how
	// DrawStringAnchored's ay=0.5 centres a monospace face's ascent/descent
	// box relative to glyphs with no descenders: whatever that offset is, it
	// is identical for both renders and for Static's own, so only the
	// SHIFT between old and new need be checked, not an absolute position.
	oldY := p.plot.Y + p.plot.H
	frac := elevationFloorHeadroom / (1 + elevationFloorHeadroom)
	wantY := p.plot.Y + p.plot.H*(1-frac)

	renderAt := func(y float64) float64 {
		ref := image.NewRGBA(image.Rect(0, 0, fw, fh))
		rc, err := NewCanvas(ref, 20, DefaultTheme(), faces)
		if err != nil {
			t.Fatal(err)
		}
		rc.Fill(rc.Theme.Background)
		if err := rc.Text(p.lowLabel, p.plot.X-p.labelPx*0.3, y, 1, 0.5, p.labelPx, rc.Theme.Dim); err != nil {
			t.Fatal(err)
		}
		cy, n := centroidY(ref, lowerGutter, rc.Theme.Background)
		if n == 0 {
			t.Fatalf("reference render at y=%v drew nothing", y)
		}
		return cy
	}
	cyAtOld := renderAt(oldY)
	cyAtWant := renderAt(wantY)

	// Static's actual ink must match the wantY reference closely (both are
	// the identical glyphs at the identical anchor) and be clearly
	// separated from the oldY reference by the headroom shift.
	if math.Abs(gotCy-cyAtWant) > 1 {
		t.Errorf("Static's low label centres at y=%v, but drawing it directly at yForElevation(minElev)'s "+
			"hand-derived position (wantY=%v) centres at y=%v -- Static is not placing it there", gotCy, wantY, cyAtWant)
	}
	if math.Abs(cyAtOld-cyAtWant) < 1 {
		t.Fatal("test fixture problem: the old and new candidate y's render to the same centroid, " +
			"so this test cannot distinguish them -- widen the box or shrink the font")
	}
	if math.Abs(gotCy-cyAtOld) < math.Abs(cyAtOld-cyAtWant)*0.5 {
		t.Errorf("Static's low label (centroid %v) is still close to the OLD, hard-coded position's "+
			"centroid %v rather than the new one's %v", gotCy, cyAtOld, cyAtWant)
	}
}

// centroidY returns the mean y of every pixel in b that differs from bg, and
// how many such pixels there were. Used to find where a piece of text is
// centred without needing to know its exact glyph extents up front -- robust
// to a font tall enough that a small positional shift still leaves some ink
// overlapping the old position.
func centroidY(img *image.RGBA, b Box, bg color.Color) (y float64, n int) {
	br, bgc, bb, _ := bg.RGBA()
	var sum float64
	for py := int(b.Y); py < int(b.Y+b.H) && py < img.Bounds().Dy(); py++ {
		for px := int(b.X); px < int(b.X+b.W) && px < img.Bounds().Dx(); px++ {
			if px < 0 || py < 0 {
				continue
			}
			r, g, bl, _ := img.At(px, py).RGBA()
			if r != br || g != bgc || bl != bb {
				sum += float64(py)
				n++
			}
		}
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// TestElevationPanel_ConstantElevationOffsetProducesIdenticalGeometry is the
// high-altitude render neither real recording in this project can produce
// (fittest cannot omit metrics, and publishing a real high-altitude file is
// exactly what CLAUDE.md forbids), asserted directly instead: a relative
// floor should make the y AXIS's geometry depend only on the elevation
// RANGE, never on the absolute values, so at the same fractional height
// within its own range, a track offset by a constant maps to the same y as
// the original.
//
// The comparison is through yForElevation at matched fractions, not through
// the stored xs/ys arrays: those also encode the LEFT GUTTER's width, which
// is sized from the label strings' own measured width (elevation.go's
// Prepare) and is legitimately wider for "2090 m" than for "90 m" -- two
// more monospace characters -- so the two painters' plot.X and plot.W differ
// for a reason that has nothing to do with the floor. plot.Y and plot.H,
// which the y axis actually uses, do not depend on label width and are
// checked directly.
func TestElevationPanel_ConstantElevationOffsetProducesIdenticalGeometry(t *testing.T) {
	const startD, endD = 0, 5000
	box := Box{X: 100, Y: 50, W: 800, H: 200}

	base := hillTrack(startD, endD, 400)
	offset := hillTrack(startD, endD, 400)
	for i := range offset.Samples {
		offset.Samples[i].Elevation += 2000
	}

	p1 := elevationPainterFor(t, base, box, 1920, 1080)
	p2 := elevationPainterFor(t, offset, box, 1920, 1080)

	if p1.plot.Y != p2.plot.Y || p1.plot.H != p2.plot.H {
		t.Fatalf("the plot's vertical extent differs between the two tracks: {Y:%v H:%v} vs {Y:%v H:%v} -- "+
			"only the horizontal gutter should move with label width", p1.plot.Y, p1.plot.H, p2.plot.Y, p2.plot.H)
	}

	for _, k := range []float64{0, 0.25, 0.5, 0.75, 1.0} {
		e1 := p1.minElev + k*(p1.maxElev-p1.minElev)
		e2 := p2.minElev + k*(p2.maxElev-p2.minElev) // == e1 + 2000, since offset shifted both bounds equally
		y1, y2 := p1.yForElevation(e1), p2.yForElevation(e2)
		if math.Abs(y1-y2) > 0.01 {
			t.Errorf("at fraction %v of the range, y = %v for the base track but %v for the same shape "+
				"offset by 2000 m in elevation; a relative floor should make the y axis depend only on the range",
				k, y1, y2)
		}
	}

	if p1.lowLabel == p2.lowLabel {
		t.Fatal("the two tracks' low labels are identical; the offset fixture is not actually offset")
	}
}

// TestElevationPanel_NegativeMinimumStaysInsideThePlot is the coastal case:
// a track whose minimum elevation is negative must still place its
// whole trace inside the plot box, because the floor is relative to the
// track's OWN minimum rather than to a literal zero that part of a
// below-sea-level recording could fall beneath.
func TestElevationPanel_NegativeMinimumStaysInsideThePlot(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	n := 200
	samples := make([]fitactivity.Sample, n)
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n-1)
		samples[i] = fitactivity.Sample{
			Time:         base.Add(time.Duration(i) * time.Second),
			HasDistance:  true,
			Distance:     f * 5000,
			HasElevation: true,
			Elevation:    -3 + 6*math.Sin(f*2*math.Pi),
		}
	}
	track := &fitactivity.Track{Samples: samples}
	box := Box{X: 0, Y: 0, W: 600, H: 200}
	p := elevationPainterFor(t, track, box, 600, 200)

	if p.minElev >= 0 {
		t.Fatalf("test fixture precondition failed: minElev = %v, want negative", p.minElev)
	}
	for i, y := range p.ys {
		if y < p.plot.Y-0.01 || y > p.plot.Y+p.plot.H+0.01 {
			t.Errorf("ys[%d] = %v is outside the plot [%v, %v]", i, y, p.plot.Y, p.plot.Y+p.plot.H)
		}
	}
}

// TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile pins the ruling new
// since the fill became the panel's only distance indicator: a flat or
// zero-distance-span profile would take the band and show distance nowhere,
// so Accepts declines it and hands the band to the distance readout.
func TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	flat := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasDistance: true, Distance: 0, HasElevation: true, Elevation: 50},
		{Time: base.Add(time.Second), HasDistance: true, Distance: 100, HasElevation: true, Elevation: 50},
		{Time: base.Add(2 * time.Second), HasDistance: true, Distance: 200, HasElevation: true, Elevation: 50},
	}}
	if (ElevationPanel{}).Accepts(&Context{Track: flat, Report: inspect.Build(flat)}) {
		t.Error("accepted a flat profile; with the fill as the sole distance indicator it would show distance nowhere")
	}

	zeroSpan := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasDistance: true, Distance: 500, HasElevation: true, Elevation: 10},
		{Time: base.Add(time.Second), HasDistance: true, Distance: 500, HasElevation: true, Elevation: 40},
		{Time: base.Add(2 * time.Second), HasDistance: true, Distance: 500, HasElevation: true, Elevation: 25},
	}}
	if (ElevationPanel{}).Accepts(&Context{Track: zeroSpan, Report: inspect.Build(zeroSpan)}) {
		t.Error("accepted a zero-distance-span profile; there is no axis to place a fill on")
	}

	ok := hillTrack(0, 1000, 50)
	if !(ElevationPanel{}).Accepts(&Context{Track: ok, Report: inspect.Build(ok)}) {
		t.Error("declined an ordinary hilly profile, which has both a real elevation range and a real distance span")
	}
}

// TestElevationPanel_LabelsFillAndDotShareOneAxis is the trap's other half.
//
// The labels, the fill and the dot could each be placed by their own
// arithmetic and disagree, and nothing would report it -- the graph would
// simply be wrong by a constant. All three go through xForDistance, and this
// asserts the consequence: a playhead at the distance a label names lands
// exactly where that label is, and the fill's rightmost filled column at
// that same distance lands there too.
func TestElevationPanel_LabelsFillAndDotShareOneAxis(t *testing.T) {
	const startD, endD = 10200, 12400
	box := Box{X: 100, Y: 50, W: 800, H: 200}
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, 1920, 1080)

	// The start label is drawn at xForDistance(0), left-anchored; the end
	// label at xForDistance(axisEnd), right-anchored. So a playhead at those
	// distances must land on the plot's edges.
	if got, want := p.xForDistance(0), p.plot.X; math.Abs(got-want) > 0.001 {
		t.Errorf("a playhead at distance zero lands at %v, but the start label is at %v", got, want)
	}
	if got, want := p.xForDistance(p.axisEnd), p.plot.X+p.plot.W; math.Abs(got-want) > 0.001 {
		t.Errorf("a playhead at the end distance lands at %v, but the end label is at %v", got, want)
	}

	// And a distance neither label pins, part way along the RECORDED trace
	// (profileStart..axisEnd, not the axis's own 0..axisEnd -- the fill and
	// the dot only have a curve to sit on from profileStart onward; see
	// Dynamic's "pre-data region").
	mid := p.profileStart + (p.axisEnd-p.profileStart)/2
	if got, want := p.xForDistance(mid), p.plot.X+p.plot.W*(mid/p.axisEnd); math.Abs(got-want) > 0.001 {
		t.Errorf("xForDistance(mid) = %v, want %v", got, want)
	}

	// The fill's own rightmost filled column, found in the rendered pixels
	// rather than trusted from the arithmetic that should have placed it --
	// the same discipline TestElevationPanel_LabelsAreDrawnOnTheAxisTheyName
	// applies to the distance labels, extended to the fill that replaced the
	// distance readout.
	fw, fh := 1920, 1080
	img := image.NewRGBA(image.Rect(0, 0, fw, fh))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: mid}})

	rightmost := rightmostInk(img, box, c.Theme.Background)
	if rightmost < 0 {
		t.Fatal("the fill drew nothing at the recorded trace's midpoint")
	}
	want := p.xForDistance(mid)
	if math.Abs(float64(rightmost)-want) > p.plot.W*0.02 {
		t.Errorf("the fill's rightmost column is at x=%d, want %v -- "+
			"the fill has drifted from the axis the labels use", rightmost, want)
	}
}

// rightmostInk is the largest x within b whose column contains a pixel that
// differs from bg, or -1 if none does. It is how the fill's own leading edge
// is found in rendered pixels, mirroring how
// TestElevationPanel_LabelsAreDrawnOnTheAxisTheyName finds a label's ink
// rather than trusting the arithmetic that should have placed it.
func rightmostInk(img *image.RGBA, b Box, bg color.Color) int {
	br, bg2, bb, _ := bg.RGBA()
	maxX := -1
	for y := int(b.Y); y < int(b.Y+b.H) && y < img.Bounds().Dy(); y++ {
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != br || g != bg2 || bl != bb {
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	return maxX
}

// TestElevationPanel_FillExtentIsNonDecreasingWithDistance checks the dynamic
// pass draws something that actually tracks the activity, rather than a
// fixed area: the fill's rightmost ink column advances as distance does, and
// crosses most of the plot from early in the activity to late in it.
//
// profileStart == 0 deliberately: this test is about the ordinary,
// full-width trace, not the pre-data region (which has its own dedicated
// tests below). A track starting well after zero would have most of the
// plot's width given over to the pre-data wash instead of the trace, and
// "crosses most of the plot" would then be the wrong bar to check against.
func TestElevationPanel_FillExtentIsNonDecreasingWithDistance(t *testing.T) {
	const startD, endD = 0, 5000
	track := hillTrack(startD, endD, 400)
	box := Box{X: 0, Y: 0, W: 600, H: 200}

	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, track, box, 600, 200)

	rightEdge := func(d float64) int {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: d}})
		x := rightmostInk(img, box, c.Theme.Background)
		if x < 0 {
			t.Fatalf("no fill drawn at distance %v", d)
		}
		return x
	}

	// A sweep across the axis: the right edge must never retreat.
	var prev int = -1
	for _, d := range []float64{startD + 50, startD + 600, startD + 1200, startD + 1800, endD - 50} {
		x := rightEdge(d)
		if prev >= 0 && x < prev {
			t.Errorf("the fill's right edge retreated from x=%d to x=%d as distance increased to %v", prev, x, d)
		}
		prev = x
	}

	early := rightEdge(startD + 100)
	late := rightEdge(endD - 100)
	if !(late > early) {
		t.Errorf("the fill is at x=%v early and x=%v late; it is not tracking distance", early, late)
	}
	// And it should have crossed most of the plot, not shuffled a few pixels.
	if float64(late-early) < p.plot.W*0.7 {
		t.Errorf("the fill moved %v across a %v plot; that is not the whole profile", late-early, p.plot.W)
	}
}

// blendOver is the same source-over compositing gg's Fill performs when col
// carries a Fade-scaled alpha over an opaque bg: the formula this project's
// tests use to identify WHICH colour a faded fill was drawn in, since the
// composited pixel is neither bg nor col outright.
func blendOver(bg, col color.Color, alpha float64) color.RGBA {
	br, bgc, bb, _ := bg.RGBA()
	cr, cgc, cb, _ := col.RGBA()
	mix := func(b, c uint32) uint8 {
		return uint8((float64(c>>8)*alpha + float64(b>>8)*(1-alpha)))
	}
	return color.RGBA{R: mix(br, cr), G: mix(bgc, cgc), B: mix(bb, cb), A: 0xFF}
}

// closeRGBA reports whether a and b agree within tol per channel -- the
// rasterizer's own rounding, not this project's arithmetic, accounts for the
// slack.
func closeRGBA(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) int {
		v := int(x) - int(y)
		if v < 0 {
			v = -v
		}
		return v
	}
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol
}

// findRGBA returns the first point in b whose pixel is within tol of want, or
// ok=false if none is.
func findRGBA(img *image.RGBA, b Box, want color.RGBA, tol int) (x, y int, ok bool) {
	for y := int(b.Y); y < int(b.Y+b.H) && y < img.Bounds().Dy(); y++ {
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			if closeRGBA(img.RGBAAt(x, y), want, tol) {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

// TestElevationAbsentFillAlpha_ClearsTheContrastFloor pins the fix for the
// finding that mattered: the absent wash's OWN composited pixel -- what a
// viewer actually sees once Fade has scaled Theme.Absent's alpha and it is
// blended over Theme.Background -- must clear this project's own
// chromeContrastFloor-style standard, not merely be a distinct colour from
// Theme.Foreground's.
//
// Run over Themes() rather than pinned to DarkTheme, the way
// TestThemesAreLegible already is for the identical reason: this is a
// property every shipped palette must have, and a check against one palette
// would not have caught the light theme's own failure, which was worse than
// the dark theme's.
func TestElevationAbsentFillAlpha_ClearsTheContrastFloor(t *testing.T) {
	for _, th := range Themes() {
		t.Run(th.Name, func(t *testing.T) {
			alpha := elevationAbsentFillAlpha(th)
			if alpha <= 0 || alpha > 1 {
				t.Fatalf("elevationAbsentFillAlpha(%s) = %v, want a value in (0, 1]", th.Name, alpha)
			}
			composited := blendOver(th.Background, th.Absent, alpha)
			if ratio := ContrastRatio(composited, th.Background); ratio < elevationAbsentFillContrastFloor {
				t.Errorf("theme %s: absent wash at alpha=%.4f composites to a contrast of %.3f:1 against "+
					"Background, below elevationAbsentFillContrastFloor (%v:1)",
					th.Name, alpha, ratio, elevationAbsentFillContrastFloor)
			}
		})
	}
}

// TestElevationAbsentFillAlpha_NeedsMoreOpacityThanTheCoveredFill pins WHY
// the two fills cannot share elevationFillAlpha (see that constant's own doc
// comment): Theme.Absent is deliberately close to Theme.Background in every
// shipped theme, so clearing the identical contrast floor the covered fill
// clears easily at 0.35 needs noticeably more opacity for the absent wash.
// A future change that let this collapse back to elevationFillAlpha (or
// below it) would silently reopen the exact failure this change fixes.
func TestElevationAbsentFillAlpha_NeedsMoreOpacityThanTheCoveredFill(t *testing.T) {
	for _, th := range Themes() {
		if got := elevationAbsentFillAlpha(th); got <= elevationFillAlpha {
			t.Errorf("theme %s: elevationAbsentFillAlpha = %v, want strictly greater than "+
				"elevationFillAlpha (%v) -- Absent needs more opacity to clear the SAME floor "+
				"Foreground clears easily, because it sits closer to Background by design",
				th.Name, got, elevationFillAlpha)
		}
	}
}

// TestMinAlphaForContrast pins the bisection itself, independent of any
// theme: the returned alpha must actually clear the requested contrast once
// composited, and asking for more contrast than fg can ever reach against bg
// (even undimmed, alpha=1) must return 1 rather than looping forever or
// returning a value that understates what was achieved.
func TestMinAlphaForContrast(t *testing.T) {
	black := color.NRGBA{0, 0, 0, 255}
	white := color.NRGBA{255, 255, 255, 255}

	got := minAlphaForContrast(black, white, 4.5)
	if got <= 0 || got > 1 {
		t.Fatalf("minAlphaForContrast = %v, want a value in (0, 1]", got)
	}
	if ratio := ContrastRatio(fadeOverBackground(black, got, white), white); ratio < 4.5 {
		t.Errorf("alpha=%v composites to contrast %.3f:1, below the requested 4.5:1", got, ratio)
	}
	// Requesting the maximum possible ratio (21:1, pure black on pure white)
	// should resolve close to full opacity -- NOT exactly 1, because
	// fadeOverBackground's own 8-bit rounding means an alpha a fraction below
	// 1 already rounds white's tiny remaining contribution down to zero and
	// composites to pure black, so the bisection correctly finds that
	// slightly smaller alpha already meets the target once quantized. 2
	// quantization steps (2/255) is a generous allowance for that effect,
	// not a loosened requirement.
	if got := minAlphaForContrast(black, white, 21); got < 1-2.0/255 {
		t.Errorf("minAlphaForContrast for the maximum achievable ratio = %v, want within 2/255 of 1", got)
	}
	// A target no colour between fg and bg could ever reach (bg IS fg) must
	// return 1 -- the honest "as far as this can go" rather than 0, which
	// would silently claim the target was met at no opacity at all.
	if got := minAlphaForContrast(black, black, 4.5); got < 0.999 {
		t.Errorf("minAlphaForContrast for an unreachable target = %v, want 1 (the best available, "+
			"not a value pretending the target was met)", got)
	}
}

// TestElevationPanel_LowLabelClearsTheBaselineWithMargin pins the finding
// that elevationFloorHeadroom's own headroom, at 0.22, left the low label's
// ink TANGENT to the baseline rule -- zero rows of daylight at some
// resolutions -- rather than clear of it with anything to spare. It checks
// actual rendered ink on both sides of the gap, not a formula, for the same
// reason TestElevationPanel_LowLabelMovesUpWithTheFloor does: a Static that
// placed either one by different, equally wrong arithmetic would pass a test
// that asked the same functions the code under test uses.
//
// The shapes are the same representative boxes
// TestElevationPanel_StaysInsideItsBox already uses for "the sizes this
// panel is actually asked to fit" -- not a real layout's own numbers, and
// not derived from any recording.
func TestElevationPanel_LowLabelClearsTheBaselineWithMargin(t *testing.T) {
	shapes := []struct {
		name   string
		fw, fh int
		box    Box
	}{
		{"a wide bottom strip", 1280, 720, Box{X: 20, Y: 560, W: 1240, H: 140}},
		{"4K wide strip", 3840, 2160, Box{X: 60, Y: 1700, W: 3720, H: 420}},
		{"a portrait strip", 1080, 1920, Box{X: 30, Y: 1500, W: 1020, H: 360}},
	}
	// One full row is more than the zero this change fixes, and comfortably
	// less than what every shape above actually measures -- a floor for the
	// assertion, not a value being pinned from a run.
	const minClearance = 1.0

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			// This track's minimum sits at fraction 0.75 along the axis
			// (Elevation follows -sin, minimised at 3/4 of the way through),
			// well clear of the x bands both scans below use, so a curve
			// dipping toward its own minimum is never mistaken for either
			// the label or the baseline rule.
			track := hillTrack(0, 5000, 300)
			img := image.NewRGBA(image.Rect(0, 0, s.fw, s.fh))
			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewCanvas(img, 20, DefaultTheme(), faces)
			if err != nil {
				t.Fatal(err)
			}
			p := elevationPainterFor(t, track, s.box, s.fw, s.fh)

			c.Fill(c.Theme.Background)
			p.Static(c)

			// The low label's own ink, isolated to the gutter (left of the
			// plot) so the baseline rule -- drawn across plot.X..plot.X+W --
			// cannot be mistaken for it. The gutter stops 3px short of
			// plot.X rather than running right up to it: the baseline
			// stroke's own line cap bleeds a pixel or two past its nominal
			// endpoint, which a first version of this scan caught and
			// mistook for the label. The label itself is anchored well clear
			// of this margin (p.plot.X-p.labelPx*0.3, growing further left),
			// so the 3px it gives up here is never part of its own ink.
			lowerGutter := Box{
				X: 0, Y: p.plot.Y + p.plot.H*0.5,
				W: p.plot.X - 3, H: float64(s.fh) - (p.plot.Y + p.plot.H*0.5),
			}
			labelBottom := maxInkRow(img, lowerGutter, c.Theme.Background)
			if labelBottom < 0 {
				t.Fatal("no low-label ink found in the gutter")
			}

			// The baseline rule's own ink, found by scanning a thin band
			// around the plot's geometric bottom edge (plot.Y+plot.H -- box
			// arithmetic, not a call to yForElevation, the same hand-derived
			// value TestElevationPanel_LowLabelMovesUpWithTheFloor's own oldY
			// uses) rather than trusting the trace is not there: the band is
			// deliberately thinner than any headroom this project would ever
			// ship, so the profile's own curve -- which by construction never
			// comes closer to the floor than the headroom allows -- cannot be
			// mistaken for the rule the way scanning the WHOLE plot height
			// would risk.
			baselineY := p.plot.Y + p.plot.H
			baselineBand := Box{
				X: p.plot.X + p.plot.W*0.05, Y: baselineY - 3,
				W: p.plot.W * 0.05, H: 6,
			}
			baselineTop := minInkRow(img, baselineBand, c.Theme.Background)
			if baselineTop < 0 {
				t.Fatal("no baseline ink found near the plot's own bottom edge")
			}

			clearance := float64(baselineTop - labelBottom)
			t.Logf("clearance=%.1f px at %s (label bottom row %d, baseline top row %d)", clearance, s.name, labelBottom, baselineTop)
			if clearance < minClearance {
				t.Errorf("low label's ink clears the baseline rule by only %.1f px at %s "+
					"(label bottom row %d, baseline top row %d); elevationFloorHeadroom's own "+
					"doc comment requires a margin here, not merely a non-negative gap",
					clearance, s.name, labelBottom, baselineTop)
			}
		})
	}
}

// maxInkRow returns the largest y within b whose row contains at least one
// pixel differing from bg, or -1 if none does -- scanning from the box's own
// bottom edge upward, so it finds the LOWEST ink in the box.
func maxInkRow(img *image.RGBA, b Box, bg color.Color) int {
	br, bgc, bb, _ := bg.RGBA()
	for y := int(b.Y+b.H) - 1; y >= int(b.Y); y-- {
		if y < 0 || y >= img.Bounds().Dy() {
			continue
		}
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			if x < 0 {
				continue
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != br || g != bgc || bl != bb {
				return y
			}
		}
	}
	return -1
}

// minInkRow is maxInkRow's mirror: the smallest y within b whose row
// contains ink, found scanning from the box's own top edge downward, or -1
// if none does.
func minInkRow(img *image.RGBA, b Box, bg color.Color) int {
	br, bgc, bb, _ := bg.RGBA()
	for y := int(b.Y); y < int(b.Y+b.H); y++ {
		if y < 0 || y >= img.Bounds().Dy() {
			continue
		}
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			if x < 0 {
				continue
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != br || g != bgc || bl != bb {
				return y
			}
		}
	}
	return -1
}

// TestElevationPanel_DropoutWashesTheWholeAxisInAbsent pins the deliberate
// choice for a distance dropout: not nothing (pixel-identical to a fill of
// zero, i.e. "back at the start line"), and not the previous frame's fill
// (Dynamic must not mutate the Painter, so there IS no previous frame to
// hold) -- but the whole axis washed in Fade(Theme.Absent, ...), a
// positively-marked "unknown", in the fill's own vocabulary.
func TestElevationPanel_DropoutWashesTheWholeAxisInAbsent(t *testing.T) {
	track := hillTrack(0, 5000, 300)
	box := Box{X: 0, Y: 0, W: 600, H: 200}

	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, track, box, 600, 200)

	// The static layer is unaffected: the profile, baseline and labels are
	// still true and stay drawn regardless of this instant's data.
	c.Fill(c.Theme.Background)
	p.Static(c)
	if got := inkCount(img, box, c.Theme); got == 0 {
		t.Fatal("the profile vanished; the static layer must not depend on this frame's data")
	}
	staticOnly := make([]byte, len(img.Pix))
	copy(staticOnly, img.Pix)

	p.Dynamic(c, Frame{HasSample: false})

	// Absent-tinted ink is found near the axis's far edge -- and, because
	// the fill's shape follows the terrain (it fills UNDER the curve, not a
	// full-height rectangle), it is looked for one row above the baseline
	// rather than at plot mid-height: that row is inside the filled area at
	// every x on the axis, whatever the terrain does, since the floor sits
	// strictly below the profile's own minimum everywhere. This is NOT
	// pixel-identical to what the static layer alone drew, which is the
	// assertion a "drew SOMETHING" check that does not look at colour would
	// have skipped.
	wantAbsent := blendOver(c.Theme.Background, c.Theme.Absent, elevationAbsentFillAlpha(c.Theme))
	nearBaseline := Box{X: p.plot.X + p.plot.W*0.9, Y: p.plot.Y + p.plot.H - 3, W: p.plot.W * 0.08, H: 2}
	if _, _, ok := findRGBA(img, nearBaseline, wantAbsent, 3); !ok {
		t.Error("no Absent-tinted fill found near the axis's far edge; the dropout should wash the WHOLE axis")
	}

	// No Accent pixels: a dropout draws no dot, because there is no position
	// to mark one on.
	wantAccent := color.RGBAModel.Convert(c.Theme.Accent).(color.RGBA)
	if _, _, ok := findRGBA(img, box, wantAccent, 0); ok {
		t.Error("an Accent-coloured dot was drawn on a dropout frame; there is no position to mark")
	}

	// And distinct from the d == profileStart frame (this track's profile
	// begins at zero, so that is also the axis's own start): drawing nothing
	// here would have been pixel-identical to a fill of zero, the confident
	// lie in pixels this project exists to refuse.
	copy(img.Pix, staticOnly)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: p.profileStart}})
	atStart := make([]byte, len(img.Pix))
	copy(atStart, img.Pix)

	copy(img.Pix, staticOnly)
	p.Dynamic(c, Frame{HasSample: false})
	dropout := img.Pix

	identical := true
	for i := range dropout {
		if dropout[i] != atStart[i] {
			identical = false
			break
		}
	}
	if identical {
		t.Error("a dropout frame is pixel-identical to the d == profileStart frame; " +
			"it reads as \"back at the start line\" rather than as \"unknown\"")
	}
}

// boxDiffers reports whether any pixel within b differs between img's
// current contents and the reference bytes ref, which must be the same
// stride and bounds as img.Pix.
func boxDiffers(img *image.RGBA, ref []byte, b Box) bool {
	for y := int(b.Y); y < int(b.Y+b.H) && y < img.Bounds().Dy(); y++ {
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			if x < 0 || y < 0 {
				continue
			}
			i := img.PixOffset(x, y)
			for k := 0; k < 4; k++ {
				if img.Pix[i+k] != ref[i+k] {
					return true
				}
			}
		}
	}
	return false
}

// TestElevationPanel_PreDataRegionWashesInAbsentAndGrowsWithDistance pins the
// policy decided for the pre-data region -- the stretch of the axis before
// the recorded trace's own first point, where distance is real and known but
// no elevation was ever recorded (see Dynamic's own doc comment for why):
// the fill washes it in Fade(Theme.Absent, ...) rather than inventing a
// shape, and its washed extent grows with distance, so the activity's
// opening stretch still reads as moving rather than as stalled.
//
// The gap here (10200 m of a 12400 m axis) is exaggerated on purpose: on
// an ordinary recording the gap between distance zero and
// the profile's first valid altitude reading is a few metres against
// several kilometres of axis, sub-pixel at any rendered size, so a test built
// to that scale could not tell a correctly-growing wash from a
// slightly-wrong one. The mechanism does not care why the gap exists, only
// that it does; see hillTrack's own doc comment.
func TestElevationPanel_PreDataRegionWashesInAbsentAndGrowsWithDistance(t *testing.T) {
	const startD, endD = 10200, 12400
	track := hillTrack(startD, endD, 400)
	box := Box{X: 0, Y: 0, W: 600, H: 200}

	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, track, box, 600, 200)

	gap := p.xs[0] - p.plot.X
	if gap < 40 {
		t.Fatalf("test fixture problem: the pre-data region is only %v px wide, too narrow to assert on", gap)
	}

	wantAbsent := blendOver(c.Theme.Background, c.Theme.Absent, elevationAbsentFillAlpha(c.Theme))
	nearLeftEdge := Box{X: p.plot.X, Y: p.plot.Y, W: gap * 0.1, H: p.plot.H}
	nearTraceStart := Box{X: p.xs[0] - gap*0.1, Y: p.plot.Y, W: gap * 0.1, H: p.plot.H}
	wantAccent := color.RGBAModel.Convert(c.Theme.Accent).(color.RGBA)

	// A quarter of the way through the gap: the wash has reached near the
	// axis's own left edge, but nowhere near the trace's own start, and
	// there is no dot -- there is no curve yet to put one on.
	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: startD * 0.25}})
	if _, _, ok := findRGBA(img, nearLeftEdge, wantAbsent, 3); !ok {
		t.Error("no Absent-tinted wash found near the axis's own left edge a quarter through the pre-data region")
	}
	if _, _, ok := findRGBA(img, nearTraceStart, wantAbsent, 3); ok {
		t.Error("the wash already reached the trace's own start a quarter through the pre-data region")
	}
	if _, _, ok := findRGBA(img, box, wantAccent, 0); ok {
		t.Error("a dot was drawn while the playhead is still in the pre-data region")
	}

	// Nearly all the way through the gap: the wash now reaches near the
	// trace's own start too.
	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: startD * 0.97}})
	if _, _, ok := findRGBA(img, nearTraceStart, wantAbsent, 3); !ok {
		t.Error("the wash has not grown to reach near the trace's own start late in the pre-data region")
	}
}

// TestElevationPanel_PreDataKnownDistanceDiffersFromDropout pins that "a
// known distance short of the recorded trace" and "distance unknown right
// now" are different facts and must not collapse to the same pixels, even
// though both wash in the SAME Absent tint for the same reason (there is no
// elevation to show either way): a dropout washes the WHOLE axis -- the
// pre-data region AND the recorded trace -- while a known pre-data distance
// washes only the fraction of the pre-data region covered so far and leaves
// the recorded trace's own portion of the plot untouched.
func TestElevationPanel_PreDataKnownDistanceDiffersFromDropout(t *testing.T) {
	const startD, endD = 10200, 12400
	track := hillTrack(startD, endD, 300)
	box := Box{X: 0, Y: 0, W: 600, H: 200}

	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, track, box, 600, 200)

	if p.xs[0]-p.plot.X < 20 {
		t.Fatalf("test fixture problem: the pre-data region is only %v px wide, too narrow to assert on", p.xs[0]-p.plot.X)
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	staticOnly := make([]byte, len(img.Pix))
	copy(staticOnly, img.Pix)

	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: startD - 500}})

	wantAccent := color.RGBAModel.Convert(c.Theme.Accent).(color.RGBA)
	if _, _, ok := findRGBA(img, box, wantAccent, 0); ok {
		t.Error("an Accent-coloured dot was drawn before the recorded trace's own start")
	}

	// The recorded trace's own portion of the plot is untouched: this
	// distance has not reached anything the model has a shape for.
	traceRegion := Box{X: p.xs[0] + 2, Y: p.plot.Y, W: p.plot.X + p.plot.W - p.xs[0] - 2, H: p.plot.H}
	if boxDiffers(img, staticOnly, traceRegion) {
		t.Error("the fill drew something in the recorded trace's own region before reaching it")
	}

	knownPreData := make([]byte, len(img.Pix))
	copy(knownPreData, img.Pix)

	copy(img.Pix, staticOnly)
	p.Dynamic(c, Frame{HasSample: false})
	differs := false
	for i := range img.Pix {
		if img.Pix[i] != knownPreData[i] {
			differs = true
			break
		}
	}
	if !differs {
		t.Error("the dropout frame is pixel-identical to the known-pre-data frame; both would read as the same fact")
	}
}

// TestElevationPanel_AcceptsNeedsDistanceToo pins that distance is the axis
// rather than an optional extra. A treadmill session with barometric
// elevation and no distance has readings with nothing to plot them against.
func TestElevationPanel_AcceptsNeedsDistanceToo(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	elevOnly := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasElevation: true, Elevation: 10},
		{Time: base.Add(time.Second), HasElevation: true, Elevation: 12},
	}}
	distOnly := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasDistance: true, Distance: 0},
		{Time: base.Add(time.Second), HasDistance: true, Distance: 3},
	}}

	// Track as well as Report: Accepts builds the elevation model to check it
	// is not empty, which a report alone cannot tell it. See its doc comment.
	if (ElevationPanel{}).Accepts(&Context{Track: elevOnly, Report: inspect.Build(elevOnly)}) {
		t.Error("accepted elevation with no distance; there is no axis to plot against")
	}
	if (ElevationPanel{}).Accepts(&Context{Track: distOnly, Report: inspect.Build(distOnly)}) {
		t.Error("accepted distance with no elevation")
	}
	both := hillTrack(0, 1000, 50)
	if !(ElevationPanel{}).Accepts(&Context{Track: both, Report: inspect.Build(both)}) {
		t.Error("declined an activity carrying both")
	}
	// And a context with no track at all declines rather than panicking in
	// BuildElevationModel, which ranges over the samples without a nil check.
	if (ElevationPanel{}).Accepts(&Context{Report: inspect.Build(both)}) {
		t.Error("accepted a context with no track")
	}
}

// TestElevationPanel_StaysInsideItsBox is the containment check, including the
// narrow box that made the two distance labels collide into one smear.
func TestElevationPanel_StaysInsideItsBox(t *testing.T) {
	shapes := []struct {
		name   string
		fw, fh int
		box    Box
	}{
		{"a wide bottom strip", 1280, 720, Box{X: 20, Y: 560, W: 1240, H: 140}},
		{"4K wide strip", 3840, 2160, Box{X: 60, Y: 1700, W: 3720, H: 420}},
		{"the narrow column that once collided", 1280, 720, Box{X: 1000, Y: 440, W: 250, H: 240}},
		{"a portrait strip", 1080, 1920, Box{X: 30, Y: 1500, W: 1020, H: 360}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			track := hillTrack(0, 5000, 300)
			img := image.NewRGBA(image.Rect(0, 0, s.fw, s.fh))
			faces, _ := NewFaceCache()
			c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
			p := elevationPainterFor(t, track, s.box, s.fw, s.fh)

			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: 2500}})

			in := inkCount(img, s.box, c.Theme)
			if in == 0 {
				t.Fatal("nothing drawn")
			}
			if whole := inkCount(img, Box{W: float64(s.fw), H: float64(s.fh)}, c.Theme); whole != in {
				t.Errorf("%d pixels escaped the box", whole-in)
			}
		})
	}
}

// TestElevationPanel_DistanceLabelsDoNotCollide pins the fix for the smear.
//
// The two labels sit at opposite ends of the plot and grow toward each other,
// so on a narrow panel they overlapped and printed as one unreadable run of
// glyphs -- "5.0 km" over "5.00 km". Neither the containment test nor any ink
// assertion could see it: both labels were inside the box.
func TestElevationPanel_DistanceLabelsDoNotCollide(t *testing.T) {
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	for _, box := range []Box{
		{X: 0, Y: 0, W: 1240, H: 140},
		{X: 0, Y: 0, W: 400, H: 160},
		{X: 0, Y: 0, W: 250, H: 240},
		{X: 0, Y: 0, W: 160, H: 200},
	} {
		track := hillTrack(10200, 12400, 300) // labels are "0 m" and "12.4 km"
		p := elevationPainterFor(t, track, box, 1280, 720)
		if p.plot.W <= 0 {
			continue
		}

		wStart, _, err := faces.Measure(p.startLabel, p.distPx)
		if err != nil {
			t.Fatal(err)
		}
		wEnd, _, err := faces.Measure(p.endLabel, p.distPx)
		if err != nil {
			t.Fatal(err)
		}
		// Left-anchored at the plot's left, right-anchored at its right.
		startRight := p.xForDistance(0) + wStart
		endLeft := p.xForDistance(p.axisEnd) - wEnd
		if startRight > endLeft {
			t.Errorf("in a %gpx box the labels %q and %q overlap by %g pixels",
				box.W, p.startLabel, p.endLabel, startRight-endLeft)
		}
	}
}

// TestElevationPanel_DeclinesWhenTheModelWouldBeEmpty covers the gap between
// "the report says both metrics are present" and "the model can be built".
//
// BuildElevationModel keeps only samples carrying elevation AND distance. A
// file whose two metrics land on disjoint samples satisfies the report's
// questions independently while producing an empty model -- and the panel was
// then placed, given the full-width strip at the bottom of the landscape
// layout, and drew nothing. An unexplained band of background, absent from the
// declined summary because it had not declined.
func TestElevationPanel_DeclinesWhenTheModelWouldBeEmpty(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	// Elevation on the even samples, distance on the odd ones: never both.
	samples := make([]fitactivity.Sample, 200)
	for i := range samples {
		s := fitactivity.Sample{Time: base.Add(time.Duration(i) * time.Second)}
		if i%2 == 0 {
			s.HasElevation, s.Elevation = true, 50+float64(i)*0.1
		} else {
			s.HasDistance, s.Distance = true, float64(i)*3
		}
		samples[i] = s
	}
	track := &fitactivity.Track{Samples: samples}
	rep := inspect.Build(track)

	// The precondition: independently, the report says both are there.
	if !rep.Carries(inspect.MetricElevation) || !rep.Carries(inspect.MetricDistance) {
		t.Fatal("precondition: the report should report both metrics present")
	}

	ctx := &Context{Track: track, Report: rep}
	if (ElevationPanel{}).Accepts(ctx) {
		t.Error("the panel accepted an activity whose model cannot be built; " +
			"it would take a box and draw nothing in it")
	}
}

// TestElevationPanel_AcceptsAgreesWithWhatPrepareCanDraw is the general form:
// wherever Accepts says yes, Prepare must produce a painter that actually
// draws. Otherwise the panel occupies space silently.
//
// This is strictly stronger than it was before the fill existed: Accepts now
// also requires a real elevation range and a real distance span (see
// TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile), so the painter it
// hands back must be non-flat -- carrying a real profile to fill under, not
// just two labels.
func TestElevationPanel_AcceptsAgreesWithWhatPrepareCanDraw(t *testing.T) {
	cases := []struct {
		name  string
		track *fitactivity.Track
	}{
		{"an ordinary hilly route", hillTrack(0, 5000, 300)},
		{"a route starting part way in", hillTrack(10200, 12400, 300)},
		{"a very short one", hillTrack(0, 200, 5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := &Context{Track: c.track, Report: inspect.Build(c.track)}
			if !(ElevationPanel{}).Accepts(ctx) {
				t.Skip("declined, which is a decision this test does not second-guess")
			}

			faces, err := NewFaceCache()
			if err != nil {
				t.Fatal(err)
			}
			ctx.Fonts, ctx.Width, ctx.Height, ctx.FontScale = faces, 1280, 720, 0.05
			box := Box{X: 20, Y: 560, W: 1240, H: 140}

			img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
			cv, _ := NewCanvas(img, 20, DefaultTheme(), faces)
			painter := ElevationPanel{}.Prepare(ctx, box)

			cv.Fill(cv.Theme.Background)
			painter.Static(cv)
			if inkCount(img, box, cv.Theme) == 0 {
				t.Error("Accepts said yes but Static drew nothing; the panel would hold an empty box")
			}

			p, ok := painter.(*elevationPainter)
			if !ok {
				t.Fatal("Prepare did not return an elevation painter")
			}
			if p.flat || len(p.xs) < 2 {
				t.Error("Accepts said yes but the painter is flat; with the fill as the sole distance " +
					"indicator a flat painter shows distance nowhere -- it would hold the box silently")
			}

			// And the fill itself draws for an ordinary in-range distance,
			// which is the case the readout it replaced is no longer there
			// to cover for. The midpoint of the RECORDED trace, not of the
			// axis: a case starting well after zero must still land inside
			// profileStart..axisEnd, where there is a curve to draw.
			cv.Fill(cv.Theme.Background)
			mid := p.profileStart + (p.axisEnd-p.profileStart)/2
			p.Dynamic(cv, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: mid}})
			if inkCount(img, box, cv.Theme) == 0 {
				t.Error("Accepts said yes but Dynamic drew nothing for a mid-activity distance")
			}
		})
	}
}

// TestElevationPanel_AcceptsImpliesDistanceReadoutAccepts pins the property
// that makes the bottom strip's Alt ordering -- [ElevationPanel{}, Distance()]
// in layouts.go -- safe: putting the richer candidate first can never lose a
// display, because wherever it accepts, the poorer candidate would have
// accepted too. An Alt slot never asks a later candidate once an earlier one
// survives (see layout.go), so if this relation ever failed in the other
// direction -- elevation accepting somewhere distance declines -- ordering
// elevation first would silently make distance unreachable on that activity,
// with nothing in the render or the decline summary saying so.
//
// This USED to explain why the merged strip's absent-data table had one
// half-present row rather than two, back when the two panels shared a Row
// and could in principle have drawn side by side; that framing stopped
// applying once the fill under the profile became the strip's only distance
// indicator and the two moved to an Alt slot that never shows them together.
// The underlying fact this test checks -- Accepts requires
// Carries(MetricDistance) alongside Carries(MetricElevation), making
// "elevation present, distance absent" UNREACHABLE -- did not change, only
// what it is needed for.
//
// The table drives a hand-built Report's coverage of the two metrics
// independently, against the SAME underlying track (real elevation and
// distance samples throughout), so that only the report's word on each
// metric changes between cases -- never whether ElevationPanel's own model
// could be built. That isolates the relation this test exists to check from
// TestElevationPanel_DeclinesWhenTheModelWouldBeEmpty above, which checks a
// different failure mode entirely.
//
// The flat-profile case below is a second, independent way the ordering
// could go wrong -- not a coverage gap but a model that carries both metrics
// and still has nothing worth drawing (see
// TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile) -- and it pins the
// consequence that matters for the Alt slot: the readout, not an empty band,
// is what a viewer actually sees.
func TestElevationPanel_AcceptsImpliesDistanceReadoutAccepts(t *testing.T) {
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
			ctx := &Context{Track: track, Report: rep}

			elevationAccepts := (ElevationPanel{}).Accepts(ctx)
			distanceAccepts := Distance().Accepts(ctx)

			if elevationAccepts && !distanceAccepts {
				t.Errorf("elevation panel accepted with elevation=%v distance=%v, but the distance readout declined; "+
					"putting elevation first in the strip's Alt slot assumes an accepting elevation panel always has a "+
					"drawable distance readout for a later candidate to fall back to, if it were ever needed",
					c.elevation, c.distance)
			}
		})
	}

	// A flat profile: both metrics are fully carried (the report and the
	// model agree, unlike the cases above), but ElevationPanel.Accepts still
	// declines (see TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile)
	// because there is no range to draw a fill against. This is the case the
	// Alt slot's ordering exists to fall through cleanly: distance must still
	// draw, and it must be the readout that does it, not an empty band.
	t.Run("flat profile declines, distance readout accepts", func(t *testing.T) {
		base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		flat := &fitactivity.Track{Samples: []fitactivity.Sample{
			{Time: base, HasDistance: true, Distance: 0, HasElevation: true, Elevation: 50},
			{Time: base.Add(time.Second), HasDistance: true, Distance: 100, HasElevation: true, Elevation: 50},
			{Time: base.Add(2 * time.Second), HasDistance: true, Distance: 200, HasElevation: true, Elevation: 50},
		}}
		ctx := &Context{Track: flat, Report: inspect.Build(flat)}

		if (ElevationPanel{}).Accepts(ctx) {
			t.Fatal("precondition failed: a flat profile must decline (see TestElevationPanel_AcceptsDeclinesFlatOrZeroSpanProfile)")
		}
		if !Distance().Accepts(ctx) {
			t.Error("the distance readout declined on a flat profile that still carries real distance samples; " +
				"the Alt slot has nothing left to fall through to and distance would appear nowhere")
		}
	})
}

// gappedDistanceTrack builds a track with a REAL recording gap: two samples
// a second apart, then nothing for gapWidth, then two more a second apart.
// fitactivity.DefaultMaxGap is 3s, so a gapWidth well past that is what lets
// this fixture tell a correct TimeToDistance apart from a broken one --
// see the function's own doc comment on why a query landing IN this gap,
// closer to neither side than DefaultMaxGap, must come back not-ok rather
// than an interpolated or extrapolated distance.
func gappedDistanceTrack(gapWidth time.Duration) (*fitactivity.Track, time.Time) {
	base := time.Date(2021, 6, 1, 8, 0, 0, 0, time.UTC)
	lo0, lo1 := base, base.Add(time.Second)
	hi0, hi1 := lo1.Add(gapWidth), lo1.Add(gapWidth+time.Second)
	track := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: lo0, HasDistance: true, Distance: 0},
		{Time: lo1, HasDistance: true, Distance: 3},
		{Time: hi0, HasDistance: true, Distance: 500},
		{Time: hi1, HasDistance: true, Distance: 503},
	}}
	return track, base
}

// TestTimeToDistance_RefusesToResolveInsideARealGap pins D.1's policy at the
// one function that implements it: a query point sitting deeper into a
// recording gap than fitactivity.DefaultMaxGap from EITHER bracketing
// sample must come back not-ok, not an interpolated straight line across
// the gap and not a snap to whichever side happens to be nearer. A fixture
// with no gap at all -- every existing elevation test up to this one --
// cannot exercise this path, because AtWithGap only ever takes it once the
// bracketing pair are more than DefaultMaxGap apart.
func TestTimeToDistance_RefusesToResolveInsideARealGap(t *testing.T) {
	const gapWidth = 100 * time.Second
	track, start := gappedDistanceTrack(gapWidth)

	// Comfortably past DefaultMaxGap (3s) from both lo1 (at 1s) and hi0 (at
	// 101s): the midpoint of the gap, 51s in.
	if _, ok := TimeToDistance(track, start, 51*time.Second); ok {
		t.Error("TimeToDistance resolved a distance from deep inside a real gap; " +
			"it must refuse rather than interpolate or extrapolate across one")
	}

	// Within DefaultMaxGap of the sample just before the gap (lo1 at 1s):
	// AtWithGap snaps to that sample rather than refusing.
	d, ok := TimeToDistance(track, start, 3*time.Second)
	if !ok {
		t.Fatal("TimeToDistance refused a query within DefaultMaxGap of a real sample; want a snap to it")
	}
	if d != 3 {
		t.Errorf("TimeToDistance snapped to distance %v, want 3 (lo1's own distance)", d)
	}

	// Within DefaultMaxGap of the sample just after the gap (hi0 at 101s).
	d, ok = TimeToDistance(track, start, 99*time.Second)
	if !ok {
		t.Fatal("TimeToDistance refused a query within DefaultMaxGap of a real sample; want a snap to it")
	}
	if d != 500 {
		t.Errorf("TimeToDistance snapped to distance %v, want 500 (hi0's own distance)", d)
	}
}

// TestTimeToDistance_OrdinaryOffsetInterpolates is the non-gap control for
// the test above: an offset strictly between two samples that ARE within
// DefaultMaxGap of each other resolves to the linear interpolation between
// them, exactly what AtWithGap already promises.
func TestTimeToDistance_OrdinaryOffsetInterpolates(t *testing.T) {
	track, start := gappedDistanceTrack(2 * time.Second)
	// lo1 (1s, distance 3) and hi0 (3s, distance 500) are 2s apart, inside
	// DefaultMaxGap. The midpoint in time, 2s, interpolates to (3+500)/2.
	d, ok := TimeToDistance(track, start, 2*time.Second)
	if !ok {
		t.Fatal("TimeToDistance refused an offset between two samples within DefaultMaxGap of each other")
	}
	if want := (3.0 + 500.0) / 2; d != want {
		t.Errorf("TimeToDistance = %v, want %v", d, want)
	}
}

// TestTimeToDistance_NilTrackIsUnplaceable is the same "no track at all"
// guard TimeToDistance opens with, pinned so a future refactor cannot drop
// it and panic on a nil Track instead.
func TestTimeToDistance_NilTrackIsUnplaceable(t *testing.T) {
	if _, ok := TimeToDistance(nil, time.Now(), 0); ok {
		t.Error("TimeToDistance resolved a distance with no Track at all")
	}
}

// TestTimeToDistance_DropoutAtTheQueriedInstantIsUnplaceable covers the
// second case TimeToDistance's own doc comment says it deliberately does
// NOT distinguish from a gap AtWithGap itself refuses: AtWithGap succeeds
// (the query sits inside DefaultMaxGap of real samples on both sides) but
// the sample it hands back does not carry distance at all -- a GPS dropout
// landing exactly on the query without the surrounding gap being wide
// enough to trip AtWithGap's own refusal.
func TestTimeToDistance_DropoutAtTheQueriedInstantIsUnplaceable(t *testing.T) {
	base := time.Date(2021, 6, 1, 8, 0, 0, 0, time.UTC)
	track := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasDistance: true, Distance: 0},
		{Time: base.Add(time.Second), HasDistance: false},
		{Time: base.Add(2 * time.Second), HasDistance: true, Distance: 20},
	}}
	if _, ok := TimeToDistance(track, base, time.Second); ok {
		t.Error("TimeToDistance resolved a distance from a sample with HasDistance false")
	}
}

// --- The marks on the profile itself ---------------------------------------

// pausedPaceTrack builds a track over highlightEpoch (the same wall clock
// highlight_panel_test.go's own controlled-Timeline tests use, so a
// Highlight.From/To given in that file's units lands on the same instants
// here) that moves at a steady pace, STOPS for a while, then moves again --
// the coffee-stop shape section 0 of the plan this change was built to
// describes: distance is flat while time keeps advancing, which is exactly
// what makes the difference between a time-indexed mark and a
// distance-indexed one visible rather than coincidental.
//
// Elevation varies smoothly throughout so BuildElevationModel never sees a
// flat profile (which would make Accepts decline and Prepare bail early on
// p.flat) -- the pause is in DISTANCE only, not in elevation, on purpose.
func pausedPaceTrack() *fitactivity.Track {
	samples := make([]fitactivity.Sample, 0, 101)
	d := 0.0
	for i := 0; i <= 100; i++ {
		switch {
		case i <= 50:
			d = float64(i) * 10 // 0..500 m over the first 50 s
		case i <= 70:
			// stopped: distance does not advance for 20 s
		default:
			d = 500 + float64(i-70)*10 // resumes, 500..800 m over the last 30 s
		}
		samples = append(samples, fitactivity.Sample{
			Time:         highlightEpoch.Add(time.Duration(i) * time.Second),
			HasDistance:  true,
			Distance:     d,
			HasElevation: true,
			Elevation:    50 + 20*math.Sin(float64(i)/12),
		})
	}
	return &fitactivity.Track{Samples: samples}
}

// elevMarkContext builds a Context over pausedPaceTrack (or another track
// carrying the same highlightEpoch-based timeline) with the given highlights
// and labels already resolved the way cmd/highlight.go and cmd/label.go
// would hand them to a real render.
func elevMarkContext(t *testing.T, track *fitactivity.Track, highlights []Highlight, labels []Label) *Context {
	t.Helper()
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	tl, err := NewSegmentedTimeline(highlightEpoch, 100*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatal(err)
	}
	return &Context{
		Track: track, Report: inspect.Build(track),
		Width: 1200, Height: 300, FontScale: 0.05, Fonts: faces,
		Timeline: tl, Highlights: highlights, Labels: labels,
	}
}

// TestElevationPanel_MarkPositionComesFromDistanceNotFromTimeFraction pins
// section 0's ruling: once a mark is drawn inside this panel's own box, its
// x MUST come from xForDistance, never from the highlight's fraction of the
// render's TIME axis -- the same axis MarkerPanel's ribbon uses. This
// highlight sits in the ORDINARY (non-paused) part of pausedPaceTrack, where
// the two axes disagree measurably: 10s..20s is 10%..20% of the 100s
// timeline, but 100m..200m is 12.5%..25% of the activity's own 800m total,
// because the 20s stop later in the activity does not shrink the TIME axis
// but does shrink -- relatively -- the DISTANCE one.
func TestElevationPanel_MarkPositionComesFromDistanceNotFromTimeFraction(t *testing.T) {
	track := pausedPaceTrack()
	highlights := []Highlight{{Name: "Rep", From: 10 * time.Second, To: 20 * time.Second}}
	ctx := elevMarkContext(t, track, highlights, nil)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 1 || !p.marks[0].ok {
		t.Fatalf("mark not placed: %+v", p.marks)
	}

	wantX0, wantX1 := p.xForDistance(100), p.xForDistance(200)
	if math.Abs(p.marks[0].x0-wantX0) > 0.5 || math.Abs(p.marks[0].x1-wantX1) > 0.5 {
		t.Errorf("mark = [%v, %v], want [%v, %v] (xForDistance(100), xForDistance(200))",
			p.marks[0].x0, p.marks[0].x1, wantX0, wantX1)
	}

	// The contrast that matters: what a TIME-fraction placement (the
	// ribbon's own frameFraction rule) would have put here instead, on the
	// same 100s/30fps timeline this Context resolved. If the two ever
	// coincided this test would prove nothing, so it also asserts they
	// differ on this fixture.
	timeX0 := p.plot.X + frameFraction(ctx.Timeline, 10*time.Second)*p.plot.W
	timeX1 := p.plot.X + frameFraction(ctx.Timeline, 20*time.Second)*p.plot.W
	if math.Abs(p.marks[0].x0-timeX0) < 0.5 && math.Abs(p.marks[0].x1-timeX1) < 0.5 {
		t.Fatal("precondition failed: the distance-indexed mark and a time-indexed placement coincide on this " +
			"fixture, so this test cannot tell the two mappings apart")
	}
}

// TestElevationPanel_MarkOverAPauseCollapsesToTheAxisMinimum pins the cost
// section 0 names explicitly and asks the user to accept: a highlight whose
// whole span sits inside a stop -- distance does not advance from From to
// To -- resolves to two IDENTICAL distances, so its mark collapses toward
// zero width and is rescued only by widenToMinimum, exactly as a
// one-frame-long highlight collapses on the ribbon's own time axis. The
// widened width is the axis's own floor, not a claim that the stop covered
// that much ground.
func TestElevationPanel_MarkOverAPauseCollapsesToTheAxisMinimum(t *testing.T) {
	track := pausedPaceTrack()
	// 55s..65s sits entirely inside the 50s..70s stop, where distance holds
	// at 500m throughout.
	highlights := []Highlight{{Name: "Stop", From: 55 * time.Second, To: 65 * time.Second}}
	ctx := elevMarkContext(t, track, highlights, nil)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 1 || !p.marks[0].ok {
		t.Fatalf("mark not placed: %+v", p.marks)
	}

	got := p.marks[0].x1 - p.marks[0].x0
	minMarkW := math.Max(2, p.markH*minBlockFraction)
	if math.Abs(got-minMarkW) > 0.5 {
		t.Errorf("mark width = %v, want the axis's own floor %v (widenToMinimum)", got, minMarkW)
	}
	if got <= 0 {
		t.Error("a collapsed mark drew with zero or negative width; widenToMinimum should have rescued it")
	}
}

// TestElevationPanel_MarkIsUnplaceableWhenAnEndpointHasNoDistance pins D.1:
// a mark needs BOTH endpoints resolved, and one known, one unplaceable is
// still unplaceable -- not interpolated, not drawn from the one end that IS
// known. The fixture is gappedDistanceTrack's own real gap, wide enough that
// From's own instant, deep inside it, cannot be resolved by TimeToDistance.
func TestElevationPanel_MarkIsUnplaceableWhenAnEndpointHasNoDistance(t *testing.T) {
	track, start := gappedDistanceTrack(100 * time.Second)
	// Give the track elevation too, so BuildElevationModel has something
	// to build a non-flat profile from; the gap in DISTANCE is what this
	// test is about, not a gap in elevation.
	for i := range track.Samples {
		track.Samples[i].HasElevation = true
		track.Samples[i].Elevation = 40 + float64(i)*5
	}
	tl, err := NewSegmentedTimeline(start, 4*time.Second, 30, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	// 51s (deep inside the 100s gap, unresolvable) .. 101.5s (within
	// DefaultMaxGap of hi0 at 101s, resolvable) -- one known endpoint, one
	// not.
	highlights := []Highlight{{Name: "Unplaceable", From: 51 * time.Second, To: 101500 * time.Millisecond}}
	ctx := &Context{
		Track: track, Report: inspect.Build(track),
		Width: 1200, Height: 300, FontScale: 0.05, Fonts: faces,
		Timeline: tl, Highlights: highlights,
	}
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 1 {
		t.Fatalf("len(p.marks) = %d, want 1", len(p.marks))
	}
	if p.marks[0].ok {
		t.Error("a mark with one endpoint inside an unresolvable gap was placed; D.1 says one known, one not is still unplaceable")
	}
}

// TestElevationPanel_MarkInThePreDataRegionIsStillPlaced pins D.3: a
// highlight whose span sits before the elevation model's own StartDistance
// -- distance is known there, elevation never was -- still gets a mark. The
// mark answers "where on the course", which is known throughout; only the
// TERRAIN is missing there, and that is the fill's own concern (see
// preDataFillTo), not this mark's.
func TestElevationPanel_MarkInThePreDataRegionIsStillPlaced(t *testing.T) {
	base := highlightEpoch
	samples := make([]fitactivity.Sample, 0, 61)
	for i := 0; i <= 60; i++ {
		s := fitactivity.Sample{
			Time: base.Add(time.Duration(i) * time.Second), HasDistance: true, Distance: float64(i) * 10,
		}
		// Elevation only starts being recorded 30s in -- everything before
		// that is the pre-data region BuildElevationModel's own filter
		// produces (see elevation.go's type doc comment).
		if i >= 30 {
			s.HasElevation = true
			s.Elevation = 50 + float64(i-30)
		}
		samples = append(samples, s)
	}
	track := &fitactivity.Track{Samples: samples}

	highlights := []Highlight{{Name: "Early", From: 5 * time.Second, To: 15 * time.Second}}
	ctx := elevMarkContext(t, track, highlights, nil)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if p.profileStart <= 0 {
		t.Fatalf("precondition failed: profileStart = %v, want > 0 (a real pre-data region)", p.profileStart)
	}
	if len(p.marks) != 1 || !p.marks[0].ok {
		t.Fatalf("mark inside the pre-data region was not placed: %+v", p.marks)
	}
}

// TestElevationPanel_MarksStillDrawDuringADistanceDropout pins D.5: a mark
// is a property of the render's TIMELINE, not of what the CURRENT frame's
// sensor recorded, so it must keep drawing -- brightened by weight when
// active -- straight over the absent wash on a distance dropout, the
// identical policy MarkerPanel's own doc comment states for the ribbon.
func TestElevationPanel_MarksStillDrawDuringADistanceDropout(t *testing.T) {
	track := pausedPaceTrack()
	highlights := []Highlight{{Name: "Rep", From: 10 * time.Second, To: 20 * time.Second}}
	ctx := elevMarkContext(t, track, highlights, nil)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	img := image.NewRGBA(image.Rect(0, 0, 1200, 300))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 1 || !p.marks[0].ok {
		t.Fatalf("mark not placed: %+v", p.marks)
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	// A dropout frame, with the highlight fully active (weight 1).
	p.Dynamic(c, Frame{HasSample: false, Interval: 0, IntervalWeight: 1})

	markBox := Box{X: p.marks[0].x0, Y: p.yForElevation(p.floorElev) - p.markH, W: p.marks[0].x1 - p.marks[0].x0, H: p.markH}
	wantLit := color.RGBAModel.Convert(Fade(c.Theme.Highlight, restAlpha(1))).(color.RGBA)
	if _, _, found := findRGBA(img, markBox, wantLit, 3); !found {
		t.Error("the active mark did not draw over the dropout's absent wash; a mark is a property of the " +
			"timeline, not of this frame's sensor reading, and must stay lit through a dropout")
	}
}

// --- The name rows above the plot -------------------------------------------

// TestElevationPanel_NoMarksReservesNoRowsAndLeavesThePlotUnchanged pins the
// load-bearing half of step 4: with NEITHER --highlight nor --label
// configured, Prepare must reserve nothing above the plot, so the plot
// resolves to exactly the rectangle Prepare's formula produced before this
// feature existed. That is what keeps a render configuring neither
// pixel-identical to one from before -- the frozen claim
// TestElevationPanel_StaysInsideItsBox and this project's own
// pixel-identity test at the render level both depend on.
//
// wantY/wantH are derived from the SAME two quantities Prepare's own
// pre-existing formula uses -- p.labelPx (unit*0.16) and p.labelPx*1.8 (the
// distance label row already reserved below the plot) -- not a number
// recorded from a run, so a future retuning of either moves both the code
// and this expectation together rather than silently drifting apart.
func TestElevationPanel_NoMarksReservesNoRowsAndLeavesThePlotUnchanged(t *testing.T) {
	track := hillTrack(0, 5000, 100)
	box := Box{X: 10, Y: 20, W: 1200, H: 300}
	p := elevationPainterFor(t, track, box, 1200, 300)

	wantY := box.Y + p.labelPx*0.9
	wantH := box.H - p.labelPx*0.9 - p.labelPx*1.8
	if math.Abs(p.plot.Y-wantY) > 1e-9 {
		t.Errorf("plot.Y = %v, want %v -- neither highlight nor label was configured, so no row should be reserved", p.plot.Y, wantY)
	}
	if math.Abs(p.plot.H-wantH) > 1e-9 {
		t.Errorf("plot.H = %v, want %v -- neither highlight nor label was configured, so no row should be reserved", p.plot.H, wantH)
	}
	if p.namePx != 0 || p.labelNamePx != 0 {
		t.Errorf("namePx = %v, labelNamePx = %v, want both 0: a row's own font size is only ever set when that row is reserved", p.namePx, p.labelNamePx)
	}
}

// TestElevationPanel_NameRowsAreReservedOnlyForWhatIsConfigured pins the
// other half: ONE row -- highlight or label -- when only one is configured,
// and both, stacked, when both are. Each row's own height is derived from
// the same unit-scaled constants Prepare uses (elevNamePx/elevLabelNamePx
// times elevNameRowPadding), so this asserts the reservation's SIZE rather
// than merely that the plot got a little smaller.
func TestElevationPanel_NameRowsAreReservedOnlyForWhatIsConfigured(t *testing.T) {
	track := pausedPaceTrack()
	box := Box{X: 0, Y: 0, W: 1200, H: 300}
	highlight := Highlight{Name: "Climb", From: 10 * time.Second, To: 20 * time.Second}
	label := Label{Name: "Aid Station", At: 15 * time.Second}

	none := elevationPainterFor(t, track, box, 1200, 300)

	prep := func(highlights []Highlight, labels []Label) *elevationPainter {
		ctx := elevMarkContext(t, track, highlights, labels)
		p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
		if !ok {
			t.Fatal("Prepare did not return an elevation painter")
		}
		return p
	}
	highlightOnly := prep([]Highlight{highlight}, nil)
	labelOnly := prep(nil, []Label{label})
	both := prep([]Highlight{highlight}, []Label{label})

	unit := math.Min(box.W, box.H)
	wantHighlightRowH := unit * elevNamePx * elevNameRowPadding
	wantLabelRowH := unit * elevLabelNamePx * elevNameRowPadding

	if got, want := none.plot.H-highlightOnly.plot.H, wantHighlightRowH; math.Abs(got-want) > 1e-6 {
		t.Errorf("highlight-only plot.H shrank by %v, want %v (one highlight row)", got, want)
	}
	if got, want := none.plot.H-labelOnly.plot.H, wantLabelRowH; math.Abs(got-want) > 1e-6 {
		t.Errorf("label-only plot.H shrank by %v, want %v (one label row)", got, want)
	}
	if got, want := none.plot.H-both.plot.H, wantHighlightRowH+wantLabelRowH; math.Abs(got-want) > 1e-6 {
		t.Errorf("both-configured plot.H shrank by %v, want %v (both rows stacked)", got, want)
	}

	// Ordering: the label row sits ABOVE the highlight row -- a smaller Y --
	// matching MarkerPanel's own top-to-bottom read (label above the
	// ribbon, highlight below it).
	if both.labelNameY >= both.nameY {
		t.Errorf("labelNameY = %v, nameY = %v: the label row should sit above the highlight row", both.labelNameY, both.nameY)
	}
}

// TestElevationPanel_ActiveHighlightAndLabelNamesDrawInTheirOwnRowsWithoutDisplacingEachOther
// pins the rejected-alternative claim from the plan directly: a highlight
// and a label active on the SAME frame each draw their own name in their
// own reserved row, and neither's presence removes or moves the other's --
// the alternative this project rejected once already, on the ribbon, for
// making a name visibly vanish and reappear as another mark passes over it.
func TestElevationPanel_ActiveHighlightAndLabelNamesDrawInTheirOwnRowsWithoutDisplacingEachOther(t *testing.T) {
	track := pausedPaceTrack()
	highlights := []Highlight{{Name: "Climb", From: 10 * time.Second, To: 20 * time.Second}}
	labels := []Label{{Name: "Aid Station", At: 15 * time.Second}}
	ctx := elevMarkContext(t, track, highlights, labels)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	img := image.NewRGBA(image.Rect(0, 0, 1200, 300))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if p.namePx == 0 || p.labelNamePx == 0 {
		t.Fatalf("precondition failed: both rows should be sized (namePx=%v labelNamePx=%v)", p.namePx, p.labelNamePx)
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	// Both the highlight and the label are active on this same frame.
	p.Dynamic(c, Frame{
		HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: 150},
		Interval: 0, IntervalWeight: 1,
		Label: 0, LabelWeight: 1,
	})

	rowH := math.Max(p.namePx, p.labelNamePx) * 1.4
	highlightRow := Box{X: p.plot.X, Y: p.nameY - rowH/2, W: p.plot.W, H: rowH}
	labelRow := Box{X: p.plot.X, Y: p.labelNameY - rowH/2, W: p.plot.W, H: rowH}

	if rightmostInk(img, highlightRow, c.Theme.Background) < 0 {
		t.Error("the active highlight's name did not draw in its own row")
	}
	if rightmostInk(img, labelRow, c.Theme.Background) < 0 {
		t.Error("the active label's name did not draw in its own row")
	}
}

// TestElevationPanel_CoincidentTicksAreNudgedApartBoundedByTwoTickWidths pins
// D.4: two labels landing on the same distance -- here, both inside
// pausedPaceTrack's own 50s..70s stop, where distance holds at 500m
// throughout -- must not draw as one indistinguishable tick with one name
// printed over the other now that a name draws for each. buildMarks nudges
// them apart using the identical separation sweep the highlight blocks
// above already use, bounded to 2*tickW away from each tick's own true
// (pre-nudge) position -- the bound this test pins, not merely that they
// moved at all.
func TestElevationPanel_CoincidentTicksAreNudgedApartBoundedByTwoTickWidths(t *testing.T) {
	track := pausedPaceTrack()
	labels := []Label{
		{Name: "Water", At: 55 * time.Second},
		{Name: "Turn", At: 65 * time.Second},
	}
	ctx := elevMarkContext(t, track, nil, labels)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.ticks) != 2 || !p.ticks[0].ok || !p.ticks[1].ok {
		t.Fatalf("ticks not placed: %+v", p.ticks)
	}

	rawX := p.xForDistance(500) // both labels sit at the stop's own held distance
	for i, tk := range p.ticks {
		if d := math.Abs(tk.x - rawX); d > 2*p.tickW+0.5 {
			t.Errorf("ticks[%d].x = %v, more than 2*tickW (%v) from its own true position %v -- "+
				"the nudge must stay bounded", i, tk.x, 2*p.tickW, rawX)
		}
	}
	if p.ticks[0].x == p.ticks[1].x {
		t.Error("two coincident ticks were not nudged apart at all; their names would print one over the other")
	}
}

// --- QA round: the index remapping in buildMarks ---------------------------

// TestElevationPanel_MiddleUnresolvableHighlightIsSkippedWithoutShiftingItsNeighboursIndices
// pins the sharpest risk in buildMarks: it builds boxes/order over only the
// PLACEABLE highlights, then writes back through order[j] to p.marks[i]. A
// version that instead assumed the placeable subset's own position equalled
// the original index (writing p.marks[j] rather than p.marks[order[j]]), or
// that ran separateSpans over a placeholder box for the skipped middle
// highlight instead of leaving it out, would misplace or mislabel First
// and/or Third here without erroring -- exactly the "plausible number, no
// crash" failure mode this project is built around, applied to indices
// instead of a Sample field.
//
// First and Third are constructed to straddle the pause in pausedPaceTrack
// (50s..70s, where distance holds at 500m) from either side, so they land
// on ADJACENT, touching distances (490..500 and 500..510) and the
// separation sweep has real work to do on exactly the two boxes that
// survive -- if the remap were wrong, that work would land on the wrong
// pair, or not run on this pair at all.
func TestElevationPanel_MiddleUnresolvableHighlightIsSkippedWithoutShiftingItsNeighboursIndices(t *testing.T) {
	track := pausedPaceTrack()
	// Force a genuine distance dropout at the two samples Middle's own
	// bounds land on exactly (60s, 61s) -- both timestamps are real
	// samples one second apart, well inside DefaultMaxGap of their own
	// neighbours, so AtWithGap resolves each query to that exact sample
	// (D.1's second case: HasDistance false at the queried instant itself)
	// rather than refusing for being deep in a wide gap.
	track.Samples[60].HasDistance = false
	track.Samples[61].HasDistance = false

	highlights := []Highlight{
		{Name: "First", From: 49 * time.Second, To: 50 * time.Second},  // 490m -> 500m
		{Name: "Middle", From: 60 * time.Second, To: 61 * time.Second}, // both bounds undefined
		{Name: "Third", From: 70 * time.Second, To: 71 * time.Second},  // 500m -> 510m
	}
	ctx := elevMarkContext(t, track, highlights, nil)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 3 {
		t.Fatalf("len(p.marks) = %d, want 3", len(p.marks))
	}
	if !p.marks[0].ok {
		t.Error("marks[0] (First) should be placeable; both its bounds carry distance")
	}
	if p.marks[1].ok {
		t.Error("marks[1] (Middle) should be unplaceable; both its bounds land on a distance dropout")
	}
	if !p.marks[2].ok {
		t.Error("marks[2] (Third) should be placeable; both its bounds carry distance")
	}
	if p.marks[1] != (elevMark{}) {
		t.Errorf("marks[1] (Middle) = %+v, want the zero value -- an unplaceable mark must not carry a leftover position", p.marks[1])
	}

	d0From, ok := TimeToDistance(track, highlightEpoch, 49*time.Second)
	if !ok {
		t.Fatal("precondition: First.From should resolve")
	}
	d0To, ok := TimeToDistance(track, highlightEpoch, 50*time.Second)
	if !ok {
		t.Fatal("precondition: First.To should resolve")
	}
	d2From, ok := TimeToDistance(track, highlightEpoch, 70*time.Second)
	if !ok {
		t.Fatal("precondition: Third.From should resolve")
	}
	d2To, ok := TimeToDistance(track, highlightEpoch, 71*time.Second)
	if !ok {
		t.Fatal("precondition: Third.To should resolve")
	}
	if d0From != 490 || d0To != 500 || d2From != 500 || d2To != 510 {
		t.Fatalf("precondition failed: distances = %v/%v and %v/%v, want 490/500 and 500/510", d0From, d0To, d2From, d2To)
	}

	// The expected positions, derived independently: widenToMinimum and
	// separateSpans -- the SAME shared primitives buildMarks calls -- run
	// here over ONLY First and Third's two boxes, exactly as buildMarks
	// should once Middle is left out.
	minMarkW := math.Max(2, p.markH*minBlockFraction)
	x0a, x1a := widenToMinimum(p.xForDistance(d0From), p.xForDistance(d0To), minMarkW)
	x0b, x1b := widenToMinimum(p.xForDistance(d2From), p.xForDistance(d2To), minMarkW)
	want := []Box{{X: x0a, W: x1a - x0a}, {X: x0b, W: x1b - x0b}}
	if want[0].X+want[0].W < want[1].X-0.01 {
		t.Fatalf("precondition failed: First and Third do not overlap even before separation (%v..%v vs %v..%v); "+
			"this test needs them to collide for the sweep to have real work to do",
			want[0].X, want[0].X+want[0].W, want[1].X, want[1].X+want[1].W)
	}
	separateSpans(want, p.markH*blockGapFraction)

	if math.Abs(p.marks[0].x0-want[0].X) > 0.01 || math.Abs(p.marks[0].x1-(want[0].X+want[0].W)) > 0.01 {
		t.Errorf("marks[0] (First) = [%v, %v], want [%v, %v]", p.marks[0].x0, p.marks[0].x1, want[0].X, want[0].X+want[0].W)
	}
	if math.Abs(p.marks[2].x0-want[1].X) > 0.01 || math.Abs(p.marks[2].x1-(want[1].X+want[1].W)) > 0.01 {
		t.Errorf("marks[2] (Third) = [%v, %v], want [%v, %v]", p.marks[2].x0, p.marks[2].x1, want[1].X, want[1].X+want[1].W)
	}
	if p.names[0] != "First" || p.names[2] != "Third" {
		t.Errorf("names = %v, want [First Middle Third]", p.names)
	}
}

// TestElevationPanel_MiddleUnresolvableLabelIsSkippedWithoutShiftingItsNeighboursIndices
// is the identical shape and the identical claim, applied to buildMarks'
// OTHER remap -- tickOrder/ticks for a --label's tick rather than
// order/marks for a --highlight's block. Water and Turn land on the
// IDENTICAL held distance (the pause's own 500m), which is D.4's own
// collision case, so this also proves the remap did not confuse which two
// ticks are adjacent once the middle one dropped out of the placeable
// subset.
func TestElevationPanel_MiddleUnresolvableLabelIsSkippedWithoutShiftingItsNeighboursIndices(t *testing.T) {
	track := pausedPaceTrack()
	track.Samples[60].HasDistance = false

	labels := []Label{
		{Name: "Water", At: 50 * time.Second}, // 500m, the pause's own start
		{Name: "Aid", At: 60 * time.Second},   // dropout: unresolvable
		{Name: "Turn", At: 70 * time.Second},  // 500m, the pause's own end
	}
	ctx := elevMarkContext(t, track, nil, labels)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.ticks) != 3 {
		t.Fatalf("len(p.ticks) = %d, want 3", len(p.ticks))
	}
	if !p.ticks[0].ok {
		t.Error("ticks[0] (Water) should be placeable")
	}
	if p.ticks[1].ok {
		t.Error("ticks[1] (Aid) should be unplaceable; its own instant is a distance dropout")
	}
	if !p.ticks[2].ok {
		t.Error("ticks[2] (Turn) should be placeable")
	}
	if p.ticks[1] != (elevTick{}) {
		t.Errorf("ticks[1] (Aid) = %+v, want the zero value", p.ticks[1])
	}

	rawX := p.xForDistance(500)
	if math.Abs(p.ticks[0].x-rawX) > 2*p.tickW+0.5 || math.Abs(p.ticks[2].x-rawX) > 2*p.tickW+0.5 {
		t.Errorf("ticks = [%v, _, %v], both should stay within 2*tickW (%v) of their own true position %v",
			p.ticks[0].x, p.ticks[2].x, 2*p.tickW, rawX)
	}
	if p.ticks[0].x == p.ticks[2].x {
		t.Error("Water and Turn were not nudged apart at all despite landing on the identical held distance")
	}

	if p.labelNames[0] != "Water" || p.labelNames[2] != "Turn" {
		t.Fatalf("labelNames = %v, want [Water Aid Turn]", p.labelNames)
	}
	if math.Abs(p.labelAnchorX[0]-p.ticks[0].x) > 1 {
		t.Errorf("labelAnchorX[0] = %v, want close to ticks[0].x = %v", p.labelAnchorX[0], p.ticks[0].x)
	}
	if math.Abs(p.labelAnchorX[2]-p.ticks[2].x) > 1 {
		t.Errorf("labelAnchorX[2] = %v, want close to ticks[2].x = %v", p.labelAnchorX[2], p.ticks[2].x)
	}
}

// --- QA round: rest-state marks must draw at all ----------------------------

// TestElevationPanel_RestStateMarkDrawsDimmerThanTheActiveOne is this
// project's own sharpest failure mode, applied to the profile's new mark
// strip: nothing before this pinned that a highlight NOT currently active
// still draws its block. A Dynamic that only ever redrew the active
// highlight -- forgetting Static's own rest-state pass, or a stray early
// return before it -- would leave every OTHER configured highlight's block
// invisible with no error and an otherwise plausible frame: the "panel
// that draws nothing" failure this project is built around.
func TestElevationPanel_RestStateMarkDrawsDimmerThanTheActiveOne(t *testing.T) {
	track := pausedPaceTrack()
	highlights := []Highlight{
		{Name: "Active", From: 5 * time.Second, To: 15 * time.Second},
		{Name: "Resting", From: 80 * time.Second, To: 90 * time.Second},
	}
	ctx := elevMarkContext(t, track, highlights, nil)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	img := image.NewRGBA(image.Rect(0, 0, 1200, 300))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 2 || !p.marks[0].ok || !p.marks[1].ok {
		t.Fatalf("marks not placed: %+v", p.marks)
	}

	d, ok := TimeToDistance(track, highlightEpoch, 10*time.Second)
	if !ok {
		t.Fatal("precondition: distance at 10s should resolve")
	}

	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{
		HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: d},
		Interval: 0, IntervalWeight: 1, Label: NoLabel,
	})

	baseline := p.yForElevation(p.floorElev)
	blockPixel := func(m elevMark) color.Color {
		x := int((m.x0 + m.x1) / 2)
		y := int(baseline - p.markH/2)
		return img.At(x, y)
	}
	activeC := blockPixel(p.marks[0])
	restC := blockPixel(p.marks[1])
	bg := c.Theme.Background

	distFromBG := func(col color.Color) float64 {
		r1, g1, b1, _ := col.RGBA()
		r2, g2, b2, _ := bg.RGBA()
		return math.Abs(float64(r1)-float64(r2)) + math.Abs(float64(g1)-float64(g2)) + math.Abs(float64(b1)-float64(b2))
	}

	restDist := distFromBG(restC)
	activeDist := distFromBG(activeC)
	if restDist == 0 {
		t.Fatal("the resting highlight's block did not draw at all -- Static must draw every configured " +
			"highlight's block, not only the currently active one")
	}
	if activeDist <= restDist {
		t.Errorf("active block's colour distance from background = %v, resting block's = %v; "+
			"the active block should read brighter than a resting one", activeDist, restDist)
	}
}

// --- QA round: an unplaceable mark, proven undrawn in pixels ---------------

// TestElevationPanel_UnplaceableMarkDrawsNothingEvenWhenClaimedActive is the
// inverse of the test above and of TestElevationPanel_MarksStillDrawDuringADistanceDropout:
// a highlight whose mark could not be placed at all (D.1) must draw
// NOTHING -- not merely "p.marks[0].ok is false" inspected on the struct,
// but no ink anywhere in the rendered frame -- even when the render loop
// claims it is the CURRENTLY ACTIVE interval, which is exactly the
// condition drawActiveMarks' own `m.ok` guard exists to refuse. Comparing
// a frame that claims it active against one that does not claim any
// interval active at all is what proves the guard is load-bearing rather
// than a coincidence of both branches happening to draw nothing anyway.
func TestElevationPanel_UnplaceableMarkDrawsNothingEvenWhenClaimedActive(t *testing.T) {
	track, start := gappedDistanceTrack(100 * time.Second)
	for i := range track.Samples {
		track.Samples[i].HasElevation = true
		track.Samples[i].Elevation = 40 + float64(i)*5
	}
	tl, err := NewSegmentedTimeline(start, 4*time.Second, 30, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	highlights := []Highlight{{Name: "Unplaceable", From: 51 * time.Second, To: 101500 * time.Millisecond}}
	ctx := &Context{
		Track: track, Report: inspect.Build(track),
		Width: 1200, Height: 300, FontScale: 0.05, Fonts: faces,
		Timeline: tl, Highlights: highlights,
	}
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.marks) != 1 || p.marks[0].ok {
		t.Fatalf("precondition failed: mark should be unplaceable, got %+v", p.marks)
	}

	renderFrame := func(f Frame) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 1200, 300))
		c, err := NewCanvas(img, 20, DefaultTheme(), faces)
		if err != nil {
			t.Fatal(err)
		}
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, f)
		return img
	}

	notClaimed := renderFrame(Frame{HasSample: false, Interval: NoHighlight, IntervalWeight: 0, Label: NoLabel})
	claimedActive := renderFrame(Frame{HasSample: false, Interval: 0, IntervalWeight: 1, Label: NoLabel})

	if !bytes.Equal(notClaimed.Pix, claimedActive.Pix) {
		t.Error("claiming the unplaceable highlight as the active interval changed the rendered pixels; " +
			"a mark with no resolvable position must draw nothing, active or not")
	}
}

// --- QA round: the size hierarchy ------------------------------------------

// TestElevationPanel_AxisChromeDependsOnlyOnTheFrameNotTheBox pins
// elevAxisLabelFraction's own ruling directly: labelPx comes from
// ctx.BasePx() -- the OUTPUT FRAME's own smaller dimension times the
// layout's FontScale -- and must not move when this panel is handed a
// differently-shaped box at the identical frame size. Before that fraction
// moved off unit (this panel's own box height, what every other measure on
// this Painter is still sized from), the same box-height change this test
// makes would have changed labelPx too -- exactly the inversion the doc
// comment on elevAxisLabelFraction records as having happened once already.
func TestElevationPanel_AxisChromeDependsOnlyOnTheFrameNotTheBox(t *testing.T) {
	const fw, fh = 1920, 1080
	track := hillTrack(0, 5000, 200)
	tall := Box{X: 0, Y: 0, W: 1200, H: 500}
	short := Box{X: 0, Y: 0, W: 1200, H: 120}

	pTall := elevationPainterFor(t, track, tall, fw, fh)
	pShort := elevationPainterFor(t, track, short, fw, fh)

	want := 0.05 * math.Min(float64(fw), float64(fh)) * elevAxisLabelFraction
	if math.Abs(pTall.labelPx-want) > 1e-9 {
		t.Errorf("labelPx = %v for the %vpx box, want %v (FontScale * min(frame) * elevAxisLabelFraction)", pTall.labelPx, tall.H, want)
	}
	if pTall.labelPx != pShort.labelPx {
		t.Errorf("labelPx = %v for a %vpx-tall box but %v for a %vpx-tall box at the SAME %vx%v frame; "+
			"axis chrome must depend only on ctx.BasePx(), never on the box it was given",
			pTall.labelPx, tall.H, pShort.labelPx, short.H, fw, fh)
	}
}

// TestElevationPanel_MarkNameRowsAreLargerThanTheAxisChrome pins the OTHER
// half of the hierarchy elevNamePx/elevLabelNamePx's own doc comment
// states as intent: a mark's own name must read larger than the axis chrome
// (elevAxisLabelFraction) it sits beside, and the highlight's name a step
// larger than the label's beneath it -- checked against the REAL boxes
// LandscapeLayout and PortraitLayout resolve the elevation panel into at
// three shipped resolutions, not an arbitrary box, since the two fractions
// are of DIFFERENT bases (unit here, ctx.BasePx() for the chrome) and the
// doc comment is explicit that retuning one without the other can silently
// invert this exact ordering.
func TestElevationPanel_MarkNameRowsAreLargerThanTheAxisChrome(t *testing.T) {
	cases := []struct {
		name   string
		layout Layout
		fw, fh int
	}{
		{"1080p landscape", LandscapeLayout(), 1920, 1080},
		{"4K landscape", LandscapeLayout(), 3840, 2160},
		{"portrait", PortraitLayout(), 1080, 1920},
	}

	track := pausedPaceTrack()
	highlights := []Highlight{{Name: "Climb", From: 10 * time.Second, To: 20 * time.Second}}
	labels := []Label{{Name: "Aid Station", At: 15 * time.Second}}
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	tl, err := NewSegmentedTimeline(highlightEpoch, 100*time.Second, 30, 1, highlights)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Resolved the way a REAL render with marks configured resolves
			// it: MarkerPanel is absorbed, because the profile is taking the
			// band and carrying the marks itself (see internal/render.New's
			// keep filter). Passing a nil keep instead would place the strip
			// AND reserve the profile's name rows in one frame -- a
			// combination the keep filter makes unreachable, since the rows
			// are reserved only when marks are configured and the strip is
			// dropped in exactly that case. Measuring it would test a band
			// narrower than any render produces, and would fail on a
			// hierarchy the shipped layout does not actually invert.
			placed, err := c.layout.Resolve(c.fw, c.fh, func(pnl Panel) bool {
				return pnl.Name() != (MarkerPanel{}).Name()
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			var box Box
			found := false
			for _, pl := range placed {
				if pl.Panel.Name() == (ElevationPanel{}).Name() {
					box, found = pl.Box, true
				}
			}
			if !found {
				t.Fatalf("elevation panel was not placed in %s", c.name)
			}

			ctx := &Context{
				Track: track, Report: inspect.Build(track),
				Width: c.fw, Height: c.fh, FontScale: c.layout.FontScale, Fonts: faces,
				Timeline: tl, Highlights: highlights, Labels: labels,
			}
			p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
			if !ok {
				t.Fatal("Prepare did not return an elevation painter")
			}

			if p.namePx <= p.labelPx {
				t.Errorf("namePx = %v, labelPx (axis chrome) = %v; a highlight's own name should read larger than the axis chrome beside it", p.namePx, p.labelPx)
			}
			if p.labelNamePx <= p.labelPx {
				t.Errorf("labelNamePx = %v, labelPx (axis chrome) = %v; a label's own name should read larger than the axis chrome beside it", p.labelNamePx, p.labelPx)
			}
			if p.namePx <= p.labelNamePx {
				t.Errorf("namePx = %v, labelNamePx = %v; the highlight name should read a step larger than the label name beneath it", p.namePx, p.labelNamePx)
			}
		})
	}
}

// --- QA round: the overlap report -------------------------------------------

// TestElevationPanel_ThreeLabelsAtTheIdenticalDistanceUnderSeparateAndAreReported
// is finding 3 from the previous review, now implemented: the plan's own
// verification put three labels "at nearly the same distance" about 1.27px
// apart against a 2.4px target, because the single left-to-right sweep
// under-separates an INTERIOR tick even when no individual nudge anywhere
// hits its own 2*tickW clamp. This fixture reproduces the mechanism at its
// most extreme (three ticks at the IDENTICAL held distance, inside
// pausedPaceTrack's own pause) rather than merely "nearly" identical, so
// the shortfall is not a fluke of this fixture's own numbers: with all
// three original positions equal, the sweep's own arithmetic (see
// separateSpans and marks.go) leaves each adjacent pair exactly tickW/2
// apart at their CENTRES, half of the tickW gap it is asked to enforce at
// their edges, and every centre stays well inside its own clamp bound.
func TestElevationPanel_ThreeLabelsAtTheIdenticalDistanceUnderSeparateAndAreReported(t *testing.T) {
	track := pausedPaceTrack()
	labels := []Label{
		{Name: "Water", At: 55 * time.Second},
		{Name: "Aid", At: 58 * time.Second},
		{Name: "Turn", At: 61 * time.Second},
	}
	ctx := elevMarkContext(t, track, nil, labels)
	box := Box{X: 0, Y: 0, W: 1200, H: 300}

	p, ok := ElevationPanel{}.Prepare(ctx, box).(*elevationPainter)
	if !ok {
		t.Fatal("Prepare did not return an elevation painter")
	}
	if len(p.ticks) != 3 || !p.ticks[0].ok || !p.ticks[1].ok || !p.ticks[2].ok {
		t.Fatalf("ticks not placed: %+v", p.ticks)
	}

	rawX := p.xForDistance(500) // all three fall inside the pause, at the identical held distance
	for i, tk := range p.ticks {
		if math.Abs(tk.x-rawX) >= 2*p.tickW {
			t.Fatalf("ticks[%d].x = %v is at (or past) its own 2*tickW clamp bound around %v; "+
				"this fixture is supposed to under-separate WITHOUT any individual nudge saturating its clamp",
				i, tk.x, rawX)
		}
	}

	gap01 := math.Abs(p.ticks[1].x - p.ticks[0].x)
	gap12 := math.Abs(p.ticks[2].x - p.ticks[1].x)
	if gap01 <= 0 || gap12 <= 0 {
		t.Fatalf("adjacent ticks collapsed to identical positions (gaps %v, %v); the sweep should still separate them some", gap01, gap12)
	}
	if gap01 >= p.tickW && gap12 >= p.tickW {
		t.Fatalf("both adjacent gaps (%v, %v) reached the full tickW (%v) target; "+
			"this fixture is supposed to reproduce the sweep UNDER-separating an interior tick", gap01, gap12, p.tickW)
	}

	overlapping := p.OverlappingLabels()
	if len(overlapping) == 0 {
		t.Fatal("OverlappingLabels() reported nothing, but the ticks above are still closer than tickW apart")
	}
	seen := map[int]bool{}
	for _, i := range overlapping {
		seen[i] = true
	}
	if !seen[1] {
		t.Errorf("OverlappingLabels() = %v, want the middle label (index 1) included -- every "+
			"adjacent pair's under-separation touches it", overlapping)
	}
}
