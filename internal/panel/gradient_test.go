package panel

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// rampTrack builds a track with a CONSTANT grade -- elevation is exactly
// linear in distance, startElev + grade*(distance-startD) -- rather than
// hillTrack's sinusoid (elevation_test.go), so a test can compute the exact
// grade GradeAtDistance ought to report at an interior point without also
// modelling the library's own Gaussian smoothing: a linear function is a
// fixed point of any symmetric smoothing kernel away from its own ends, and
// AtDistance's linear interpolation between two points of a linear function
// reproduces it exactly, so grade == the constant this track was built with,
// everywhere but close to its own two ends.
func rampTrack(startD, endD float64, n int, grade float64) *fitactivity.Track {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, n)
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n-1)
		d := startD + f*(endD-startD)
		samples[i] = fitactivity.Sample{
			Time:         base.Add(time.Duration(i) * time.Second),
			HasDistance:  true,
			Distance:     d,
			HasElevation: true,
			Elevation:    100 + grade*(d-startD),
		}
	}
	return &fitactivity.Track{Samples: samples}
}

// gradientPainterFor mirrors climbPainterFor (climb_test.go): builds the
// same Context a real render would (see elevationContext) and returns the
// concrete painter so a test can inspect the fields Prepare resolved rather
// than only what Static/Dynamic put on screen.
func gradientPainterFor(t *testing.T, track *fitactivity.Track, box Box, fw, fh int) *gradientPainter {
	t.Helper()
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	ctx := elevationContext(track)
	ctx.Width, ctx.Height, ctx.FontScale, ctx.Fonts = fw, fh, 0.05, faces
	p, ok := GradientPanel{}.Prepare(ctx, box).(*gradientPainter)
	if !ok {
		t.Fatal("Prepare did not return a gradient painter")
	}
	return p
}

// TestGradientPanel_AcceptsAgreesWithElevationPanel mirrors
// TestClimbPanel_AcceptsAgreesWithElevationPanel (climb_test.go): both
// GradientPanel and ElevationPanel must never disagree about whether
// ctx.Elevation is worth drawing from -- one panel drawing on a model
// another declined is precisely the two-panels-disagree failure the shared
// elevationIsPlottable predicate exists to prevent (see elevation.go). An
// EQUALITY check, deliberately, not a one-directional implication.
func TestGradientPanel_AcceptsAgreesWithElevationPanel(t *testing.T) {
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

			gotGradient := (GradientPanel{}).Accepts(ctx)
			gotElevation := (ElevationPanel{}).Accepts(ctx)
			if gotGradient != gotElevation {
				t.Errorf("elevation=%v distance=%v: GradientPanel.Accepts=%v, ElevationPanel.Accepts=%v -- "+
					"both must answer the identical elevationIsPlottable predicate", c.elevation, c.distance, gotGradient, gotElevation)
			}
		})
	}

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
		if (GradientPanel{}).Accepts(ctx) {
			t.Error("GradientPanel accepted a flat profile that ElevationPanel declines")
		}
	})

	t.Run("nil elevation model", func(t *testing.T) {
		ctx := &Context{Track: track, Report: inspect.Build(track)}
		if (ElevationPanel{}).Accepts(ctx) || (GradientPanel{}).Accepts(ctx) {
			t.Error("expected both panels to decline with ctx.Elevation nil")
		}
	})
}

// TestGradientAngleFor_ProportionalBelowClampAndClampedNearTwentyPercent
// derives every expected value from gradientAngleFor's own stated formula --
// atan(grade)*gradientAngleAmplification, clamped to
// +/-gradientMaxAngleRadians -- rather than pinning whatever the code
// currently returns, per this project's own testing rule against recording
// current behaviour as if it were a spec.
//
// The clamp boundary is derived, not guessed: solving
// atan(x)*gradientAngleAmplification = gradientMaxAngleRadians for x gives
// tan(gradientMaxAngleRadians/gradientAngleAmplification), which with the
// shipped 4x amplification and 45 degree clamp is tan(pi/16) ~= 0.199 --
// close to 20% grade, exactly the real-world bound
// gradientAngleAmplification's own doc comment states as the reason 4 is
// the number.
func TestGradientAngleFor_ProportionalBelowClampAndClampedNearTwentyPercent(t *testing.T) {
	clampGrade := math.Tan(gradientMaxAngleRadians / gradientAngleAmplification)

	cases := []struct {
		name  string
		grade float64
	}{
		{"flat", 0},
		{"gentle climb, 5%", 0.05},
		{"climb, 10%", 0.10},
		{"descent, 10%", -0.10},
		{"steep climb beyond the real-world bound, 35%", 0.35},
		{"steep descent beyond the real-world bound, 35%", -0.35},
		{"exactly at the derived clamp boundary", clampGrade},
		{"just past the derived clamp boundary", clampGrade * 1.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := math.Atan(c.grade) * gradientAngleAmplification
			if want > gradientMaxAngleRadians {
				want = gradientMaxAngleRadians
			}
			if want < -gradientMaxAngleRadians {
				want = -gradientMaxAngleRadians
			}
			got := gradientAngleFor(c.grade)
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("gradientAngleFor(%v) = %v, want %v", c.grade, got, want)
			}
			if math.Abs(got) > gradientMaxAngleRadians+1e-9 {
				t.Errorf("gradientAngleFor(%v) = %v exceeds the clamp %v", c.grade, got, gradientMaxAngleRadians)
			}
		})
	}
}

// TestGradientAngleFor_SignMatchesGradeSign pins the property the whole
// visual design depends on: a climb (positive grade) must produce a positive
// angle (this panel's Dynamic then draws the RIGHT end higher, per its own
// comment on image y growing downward) and a descent a negative one, with
// flat producing exactly zero -- never a sign flip, which would draw a
// descent as a climb.
func TestGradientAngleFor_SignMatchesGradeSign(t *testing.T) {
	if got := gradientAngleFor(0); got != 0 {
		t.Errorf("gradientAngleFor(0) = %v, want exactly 0", got)
	}
	if got := gradientAngleFor(0.08); got <= 0 {
		t.Errorf("gradientAngleFor(0.08) = %v, want a positive angle for a climb", got)
	}
	if got := gradientAngleFor(-0.08); got >= 0 {
		t.Errorf("gradientAngleFor(-0.08) = %v, want a negative angle for a descent", got)
	}
}

// TestFormatGrade_MatchesVideofxFormatString pins formatGrade's exact
// spelling against videofx's own `%+.1f%%` for the identical figure (see
// gauges.go's inclineLine there) -- the type doc comment's "division of
// labour" section states this as the reason the two programs must never
// print a different string for the same instant.
func TestFormatGrade_MatchesVideofxFormatString(t *testing.T) {
	cases := []struct {
		grade float64
		want  string
	}{
		{0, "+0.0%"},
		{0.061, "+6.1%"},
		{-0.061, "-6.1%"},
		{0.199, "+19.9%"},
		{-0.005, "-0.5%"},
	}
	for _, c := range cases {
		if got := formatGrade(c.grade); got != c.want {
			t.Errorf("formatGrade(%v) = %q, want %q", c.grade, got, c.want)
		}
	}
}

// lineInkYAt returns the topmost and bottommost ink row within a 3px-wide
// strip centred on x, spanning [yMin,yMax] -- reusing minInkRow/maxInkRow
// (elevation_test.go) the way this package's other tests do rather than
// writing a third pixel scanner, on a column narrow enough that only the
// tilted line itself (never the caption or reading columns either side of
// it) can appear inside it.
func lineInkYAt(img *image.RGBA, x int, yMin, yMax float64, bg Theme) (minY, maxY int) {
	box := Box{X: float64(x - 1), Y: yMin, W: 3, H: yMax - yMin}
	return minInkRow(img, box, bg.Background), maxInkRow(img, box, bg.Background)
}

// tiltedLineY renders one frame of a rampTrack of the given constant grade
// and returns the ink's own y-centre at x, restricted to a window tight
// around wantWindowY -- narrow enough to exclude Static's own horizontal
// reference (drawn at centerY, a fixed distance away whenever grade is not
// tiny) so this helper reports the DYNAMIC tilted line's position alone,
// never an average of the two unrelated features.
func tiltedLineY(t *testing.T, img *image.RGBA, c *Canvas, p *gradientPainter, d, x, wantWindowY float64) float64 {
	t.Helper()
	const margin = 15.0
	minY, maxY := lineInkYAt(img, int(x), wantWindowY-margin, wantWindowY+margin, c.Theme)
	if minY < 0 {
		t.Fatalf("no ink found near x=%v within +/-%v of y=%v", x, margin, wantWindowY)
	}
	return float64(minY+maxY) / 2
}

// TestGradientPanel_DynamicTiltsProportionalToGrade renders Dynamic against
// two tracks of known, constant, opposite-signed grade (rampTrack) and
// checks the line's ink, sampled well off-centre (70% of the way to the
// right end, where the vertical offset is largest and easiest to tell apart
// from a level line), lands at the y gradientAngleFor's own formula
// predicts for EACH -- not merely that some ink was drawn somewhere, and not
// merely that the climb and the descent produced the same pixels.
func TestGradientPanel_DynamicTiltsProportionalToGrade(t *testing.T) {
	const w, h = 900, 200
	const startD, endD = 0, 5000
	const d = 2500.0 // well interior: clear of both the track's own ends and the +/-window
	const frac = 0.7

	render := func(grade float64) (img *image.RGBA, c *Canvas, p *gradientPainter, wantX, wantY float64) {
		track := rampTrack(startD, endD, 400, grade)
		box := Box{X: 0, Y: 0, W: w, H: h}
		img = image.NewRGBA(image.Rect(0, 0, w, h))
		faces, err := NewFaceCache()
		if err != nil {
			t.Fatal(err)
		}
		c, err = NewCanvas(img, 20, DefaultTheme(), faces)
		if err != nil {
			t.Fatal(err)
		}
		p = gradientPainterFor(t, track, box, w, h)
		if p.halfLen <= 0 {
			t.Fatal("precondition failed: want a real line half-length from this box")
		}
		gotGrade := p.model.GradeAtDistance(d, p.window)
		if math.Abs(gotGrade-grade) > 0.01 {
			t.Fatalf("precondition failed: GradeAtDistance(%v) = %v, want close to the track's own constant grade %v", d, gotGrade, grade)
		}
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: d}})

		angle := gradientAngleFor(gotGrade)
		wantX = p.centerX + frac*p.halfLen*math.Cos(angle)
		wantY = p.centerY - frac*p.halfLen*math.Sin(angle)
		return
	}

	tol := 0.0

	climbImg, climbC, climbP, climbX, climbWantY := render(0.12)
	tol = climbP.lineWidth + 3
	climbGotY := tiltedLineY(t, climbImg, climbC, climbP, d, climbX, climbWantY)
	if math.Abs(climbGotY-climbWantY) > tol {
		t.Errorf("climb: line ink at x=%v centred at y=%v, want within %v of %v", climbX, climbGotY, tol, climbWantY)
	}

	descImg, descC, descP, descX, descWantY := render(-0.12)
	descGotY := tiltedLineY(t, descImg, descC, descP, d, descX, descWantY)
	if math.Abs(descGotY-descWantY) > tol {
		t.Errorf("descent: line ink at x=%v centred at y=%v, want within %v of %v", descX, descGotY, tol, descWantY)
	}

	// The climb's own sample point sits ABOVE centre (smaller y) and the
	// descent's sits BELOW it (larger y) -- checked directly, so a sign
	// error in gradientAngleFor or in Dynamic's y arithmetic fails this
	// even if each half of the check above passed against a self-consistent
	// but flipped expectation.
	if !(climbGotY < climbP.centerY && descGotY > descP.centerY) {
		t.Errorf("climb ink y=%v and descent ink y=%v should straddle centreY=%v in opposite directions",
			climbGotY, descGotY, climbP.centerY)
	}
}

// TestGradientPanel_DropoutDrawsNoLine pins the ordinary absent-data case:
// no distance at all this frame. No line may be drawn -- a line parked level
// would be a confident, drawable "0% grade" this panel does not have (see
// the type doc comment's "Absence" section) -- so the only ink left along
// the line's own span must be Static's dim horizontal reference, never
// Theme.Foreground.
func TestGradientPanel_DropoutDrawsNoLine(t *testing.T) {
	const w, h = 900, 200
	track := rampTrack(0, 5000, 400, 0.12)
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
	p := gradientPainterFor(t, track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{}) // the zero Frame: HasSample false, HasDistance false.

	assertNoForegroundOnLine(t, img, p, c.Theme)
}

// TestGradientPanel_PreDataTrapDrawsNoLineNotAConfidentZero is THE case the
// plan calls out as the most important in this round.
// fitactivity.ElevationModel.GradeAtDistance clamps to its own ends, so
// asked for a distance before the model's own StartDistance it answers a
// confident 0.0 grade -- no error. A track built with a non-zero start (per
// rampTrack's own startD argument) has exactly that gap, and a frame whose
// Sample carries a KNOWN distance inside it (HasDistance true, but Distance
// < StartDistance) must draw no line at all, exactly like the ordinary
// dropout above, rather than reading the clamp's confident 0.0% and drawing
// an honest-looking but false level line.
func TestGradientPanel_PreDataTrapDrawsNoLineNotAConfidentZero(t *testing.T) {
	const w, h = 900, 200
	const startD, endD = 10200, 12400
	track := rampTrack(startD, endD, 400, 0.12)
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
	p := gradientPainterFor(t, track, box, w, h)
	if p.profileStart <= 0 {
		t.Fatalf("precondition failed: want a non-zero profileStart from this fixture, got %v", p.profileStart)
	}
	c.Fill(c.Theme.Background)
	p.Static(c)

	halfway := p.profileStart / 2
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: halfway}})

	assertNoForegroundOnLine(t, img, p, c.Theme)
}

// colorFromRGBA adapts image.Image.At's 16-bit-per-channel RGBA() output
// (as color.RGBA.RGBA() also returns) into an 8-bit color.RGBA closeRGBA can
// compare, the same >>8 truncation blendOver already relies on elsewhere in
// this package's tests.
func colorFromRGBA(r, g, b uint32) (c struct{ R, G, B, A uint8 }) {
	return struct{ R, G, B, A uint8 }{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xFF}
}

// foregroundOnLine scans the whole line span for a pixel close to
// Theme.Foreground -- the colour ONLY the live tilted line is ever drawn in
// (Static's own reference uses a faded Theme.Dim), so its presence or
// absence there is exactly the fact both the absent-data tests and the
// past-end test below need: whether Dynamic drew a live line at all.
func foregroundOnLine(img *image.RGBA, p *gradientPainter, theme Theme) bool {
	fr, fg, fb, _ := theme.Foreground.RGBA()
	want := colorFromRGBA(fr, fg, fb)
	x0, x1 := int(p.centerX-p.halfLen), int(p.centerX+p.halfLen)
	y0, y1 := int(p.centerY-p.halfLen-2), int(p.centerY+p.halfLen+2)
	for y := y0; y <= y1; y++ {
		if y < 0 || y >= img.Bounds().Dy() {
			continue
		}
		for x := x0; x <= x1; x++ {
			if x < 0 || x >= img.Bounds().Dx() {
				continue
			}
			r, g, b, _ := img.At(x, y).RGBA()
			if closeRGBA(colorFromRGBA(r, g, b), want, 8) {
				return true
			}
		}
	}
	return false
}

// assertNoForegroundOnLine fails if foregroundOnLine finds a live-line pixel
// -- which is what would show up if Dynamic drew a line (tilted or, worse,
// parked level) where the absent-data policy requires none.
func assertNoForegroundOnLine(t *testing.T, img *image.RGBA, p *gradientPainter, theme Theme) {
	t.Helper()
	if foregroundOnLine(img, p, theme) {
		t.Fatal("Theme.Foreground ink found on the line's own span -- a line was drawn where the absent-data policy requires none")
	}
}

// TestGradientPanel_PastRecordedEndDrawsLiveNotPlaceholder pins the opposite
// edge, named explicitly in the type doc comment: a distance PAST the
// model's own TotalDistance is not a trap -- the terrain's own final
// measured grade is the honest answer once the activity has finished -- so
// the line must still be drawn (tilted, not parked level, and not a
// placeholder), exactly ClimbPanel's identical choice for its own past-end
// case.
func TestGradientPanel_PastRecordedEndDrawsLiveNotPlaceholder(t *testing.T) {
	const w, h = 900, 200
	track := rampTrack(0, 5000, 400, 0.12)
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
	p := gradientPainterFor(t, track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasDistance: true, Distance: p.axisEnd + 500}})

	if !foregroundOnLine(img, p, c.Theme) {
		t.Error("no Theme.Foreground ink found past the model's own recorded end -- want the live line still drawn, not a placeholder")
	}
}

// TestGradientPanel_ScalesWithFrameDimensionsNotAPixelConstant mirrors
// TestClimbPanel_ScalesWithFrameDimensionsNotAPixelConstant (climb_test.go):
// the same track, prepared against boxes whose ratio mirrors 1080p, 4K and
// the portrait tree, must produce a line half-length and stroke width that
// scale with the box rather than sitting at a size unrelated to it.
func TestGradientPanel_ScalesWithFrameDimensionsNotAPixelConstant(t *testing.T) {
	track := rampTrack(0, 5000, 300, 0.08)
	sizes := []struct {
		name   string
		w, h   float64
		fw, fh int
	}{
		{"1080p-shaped", 900, 100, 1920, 1080},
		{"4K-shaped", 1800, 200, 3840, 2160},
		{"portrait-shaped", 490, 120, 1080, 1920},
	}
	var prevHalf, prevWidth float64
	for i, s := range sizes {
		box := Box{X: 0, Y: 0, W: s.w, H: s.h}
		p := gradientPainterFor(t, track, box, s.fw, s.fh)
		if p.halfLen <= 0 {
			t.Fatalf("%s: halfLen <= 0", s.name)
		}
		if i > 0 && p.halfLen == prevHalf {
			t.Errorf("%s: halfLen is identical to the previous box's (%v) despite a different box", s.name, prevHalf)
		}
		if i > 0 && p.lineWidth == prevWidth {
			t.Errorf("%s: lineWidth is identical to the previous box's (%v) despite a different box", s.name, prevWidth)
		}
		prevHalf, prevWidth = p.halfLen, p.lineWidth
	}
}
