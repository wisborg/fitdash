package panel

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// hillTrack builds a track whose cumulative distance STARTS AT startD rather
// than at zero, with elevation varying over it.
//
// The non-zero origin is the whole point. An activity recorded from its own
// start line has a profile beginning at zero, and against that a panel that
// divides by the total distance alone behaves identically to one that spans
// the real axis -- which is exactly why the bug this panel is shaped around
// survived in the sibling project.
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

// TestElevationPanel_AxisBeginsAtTheProfilesOwnStart is the test this panel
// exists to pass.
//
// The x axis runs from the model's StartDistance to its TotalDistance. A panel
// that divided by the total alone would squeeze a profile beginning at 10.2 km
// into the right-hand fifth of its box -- and would look perfectly correct on
// every activity that happens to start at zero, which is most of them.
//
// The expectations are derived from the geometry, not observed: the first
// profile point belongs at the plot's left edge and the last at its right,
// whatever the axis happens to begin at.
func TestElevationPanel_AxisBeginsAtTheProfilesOwnStart(t *testing.T) {
	const startD, endD = 10200, 12400
	box := Box{X: 100, Y: 50, W: 800, H: 200}
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, 1920, 1080)

	if len(p.xs) < 2 {
		t.Fatal("no profile was built")
	}
	if p.axisStart < startD-1 || p.axisStart > startD+50 {
		t.Fatalf("axis starts at %v, want about %v", p.axisStart, startD)
	}

	if got, want := p.xs[0], p.plot.X; math.Abs(got-want) > 1 {
		t.Errorf("the profile begins at x=%v, want the plot's left edge %v -- "+
			"it is being scaled against 0 rather than against the axis's own start", got, want)
	}
	if got, want := p.xs[len(p.xs)-1], p.plot.X+p.plot.W; math.Abs(got-want) > 1 {
		t.Errorf("the profile ends at x=%v, want the plot's right edge %v", got, want)
	}

	// The specific symptom of the bug, asserted directly: dividing by the
	// total alone would put the start at 10200/12400 = 82% of the way across.
	//
	// The float64 conversions are load-bearing. startD and endD are untyped
	// INTEGER constants, so startD/endD is constant integer division and
	// evaluates to 0 -- which made "squeezed" the plot's left edge, so this
	// assertion compared the correct answer against itself and failed on
	// working code.
	squeezed := p.plot.X + p.plot.W*(float64(startD)/float64(endD))
	if math.Abs(p.xs[0]-squeezed) < 1 {
		t.Errorf("the profile begins at %v, which is where dividing by TotalDistance alone would put it", p.xs[0])
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

// TestElevationPanel_LabelsAndPlayheadShareOneAxis is the trap's other half.
//
// The labels and the playhead could each be placed by their own arithmetic and
// disagree, and nothing would report it -- the graph would simply be wrong by
// a constant. Both go through xForDistance, and this asserts the consequence:
// the playhead at the distance a label names lands exactly where that label
// is.
func TestElevationPanel_LabelsAndPlayheadShareOneAxis(t *testing.T) {
	const startD, endD = 10200, 12400
	box := Box{X: 100, Y: 50, W: 800, H: 200}
	p := elevationPainterFor(t, hillTrack(startD, endD, 400), box, 1920, 1080)

	// The start label is drawn at xForDistance(axisStart), left-anchored; the
	// end label at xForDistance(axisEnd), right-anchored. So a playhead at
	// those distances must land on the plot's edges.
	if got, want := p.xForDistance(p.axisStart), p.plot.X; math.Abs(got-want) > 0.001 {
		t.Errorf("a playhead at the start distance lands at %v, but the start label is at %v", got, want)
	}
	if got, want := p.xForDistance(p.axisEnd), p.plot.X+p.plot.W; math.Abs(got-want) > 0.001 {
		t.Errorf("a playhead at the end distance lands at %v, but the end label is at %v", got, want)
	}

	// And the midpoint, which neither label pins: half way along the axis is
	// half way across the plot.
	mid := p.axisStart + (p.axisEnd-p.axisStart)/2
	if got, want := p.xForDistance(mid), p.plot.X+p.plot.W/2; math.Abs(got-want) > 0.001 {
		t.Errorf("the axis midpoint lands at %v, want %v", got, want)
	}
}

// TestElevationPanel_PlayheadMovesAcrossThePlot checks the dynamic pass draws
// something that actually tracks the activity, rather than a fixed line.
func TestElevationPanel_PlayheadMovesAcrossThePlot(t *testing.T) {
	const startD, endD = 10200, 12400
	track := hillTrack(startD, endD, 400)
	box := Box{X: 0, Y: 0, W: 600, H: 200}

	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, track, box, 600, 200)

	// The mean x of the playhead's ink, which is where it is.
	meanX := func(d float64) float64 {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: d}})
		var sum, n float64
		for y := 0; y < 200; y++ {
			for x := 0; x < 600; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				br, bg, bb, _ := c.Theme.Background.RGBA()
				if r != br || g != bg || b != bb {
					sum += float64(x)
					n++
				}
			}
		}
		if n == 0 {
			t.Fatalf("no playhead drawn at distance %v", d)
		}
		return sum / n
	}

	early := meanX(startD + 100)
	late := meanX(endD - 100)
	if !(late > early) {
		t.Errorf("the playhead is at x=%v early and x=%v late; it is not tracking distance", early, late)
	}
	// And it should have crossed most of the plot, not shuffled a few pixels.
	if late-early < p.plot.W*0.7 {
		t.Errorf("the playhead moved %v across a %v plot; that is not the whole profile", late-early, p.plot.W)
	}
}

// TestElevationPanel_NoDistanceReadingDrawsNoPlayhead pins the refusal to
// assert a position nobody measured.
//
// The profile and its labels stay -- they are still true -- and only the mark
// saying "you are here" is withheld. This is not the silent-nothing the panel
// contract forbids: the panel has drawn.
func TestElevationPanel_NoDistanceReadingDrawsNoPlayhead(t *testing.T) {
	track := hillTrack(0, 5000, 300)
	box := Box{X: 0, Y: 0, W: 600, H: 200}

	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := elevationPainterFor(t, track, box, 600, 200)

	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{HasSample: false})
	if got := inkCount(img, box, c.Theme); got != 0 {
		t.Errorf("%d pixels drawn with no distance reading; a position was asserted that nobody measured", got)
	}

	// The static layer is unaffected: the profile is still true.
	c.Fill(c.Theme.Background)
	p.Static(c)
	if got := inkCount(img, box, c.Theme); got == 0 {
		t.Error("the profile vanished; only the playhead should be withheld")
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
		track := hillTrack(10200, 12400, 300) // labels are "10.2 km" and "12.4 km"
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
		startRight := p.xForDistance(p.axisStart) + wStart
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
			p := ElevationPanel{}.Prepare(ctx, box)

			cv.Fill(cv.Theme.Background)
			p.Static(cv)
			if inkCount(img, box, cv.Theme) == 0 {
				t.Error("Accepts said yes but Static drew nothing; the panel would hold an empty box")
			}
		})
	}
}
