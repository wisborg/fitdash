package panel

import (
	"bytes"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
)

// balanceEpoch is an arbitrary, synthetic reference instant -- not a real
// activity's start time (see CLAUDE.md: no real dates in a committed
// fixture). balanceSampleTime offsets from it so every fixture track below
// carries real, distinct, evenly spaced Times, the same discipline
// gaugeEpoch/gaugeSampleTime (gauge_test.go) use for the identical reason.
var balanceEpoch = time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)

func balanceSampleTime(i int) time.Time {
	return balanceEpoch.Add(time.Duration(i) * time.Second)
}

// --- value accessors -----------------------------------------------------

// TestBalanceValueAccessors_RefuseRecordedZeroAndAbsence pins the "zero is
// not a balance" rule (BalancePanel's own doc comment) for all four
// constructors: a recorded 0 -- present by fitactivity's own flag, or by
// DevFields key membership -- is refused exactly like an absent reading, and
// a genuine nonzero reading is reported verbatim.
func TestBalanceValueAccessors_RefuseRecordedZeroAndAbsence(t *testing.T) {
	cases := []struct {
		name   string
		panel  BalancePanel
		sample fitactivity.Sample
		wantOK bool
		wantV  float64
	}{
		{"contact present nonzero", ContactBalance(),
			fitactivity.Sample{HasStanceTimeBalance: true, StanceTimeBalance: 54}, true, 54},
		{"contact present zero refused", ContactBalance(),
			fitactivity.Sample{HasStanceTimeBalance: true, StanceTimeBalance: 0}, false, 0},
		{"contact flag false", ContactBalance(),
			fitactivity.Sample{HasStanceTimeBalance: false, StanceTimeBalance: 54}, false, 0},
		{"contact absent sample", ContactBalance(), fitactivity.Sample{}, false, 0},

		{"impact present nonzero", ImpactBalance(),
			fitactivity.Sample{DevFields: map[string]float64{fitactivity.StrydImpactLoadingRateBalanceField: 47}}, true, 47},
		{"impact present zero refused", ImpactBalance(),
			fitactivity.Sample{DevFields: map[string]float64{fitactivity.StrydImpactLoadingRateBalanceField: 0}}, false, 0},
		{"impact key absent", ImpactBalance(),
			fitactivity.Sample{DevFields: map[string]float64{}}, false, 0},
		{"impact nil map", ImpactBalance(), fitactivity.Sample{}, false, 0},

		{"stiffness present nonzero", StiffnessBalance(),
			fitactivity.Sample{DevFields: map[string]float64{fitactivity.StrydLegSpringStiffnessBalanceField: 61}}, true, 61},
		{"stiffness present zero refused", StiffnessBalance(),
			fitactivity.Sample{DevFields: map[string]float64{fitactivity.StrydLegSpringStiffnessBalanceField: 0}}, false, 0},

		{"oscillation present nonzero", OscillationBalance(),
			fitactivity.Sample{DevFields: map[string]float64{fitactivity.StrydVerticalOscillationBalanceField: 39}}, true, 39},
		{"oscillation present zero refused", OscillationBalance(),
			fitactivity.Sample{DevFields: map[string]float64{fitactivity.StrydVerticalOscillationBalanceField: 0}}, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := c.panel.value(c.sample)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && v != c.wantV {
				t.Errorf("v = %v, want %v", v, c.wantV)
			}
		})
	}
}

// --- fixed-scale arithmetic ------------------------------------------------

// TestBalanceDeviationMagnitudeFraction derives every expectation from the
// documented arithmetic: deviation = v-50, magnitude = |deviation|,
// fraction = deviation/balanceHalfRange(5), unclamped.
func TestBalanceDeviationMagnitudeFraction(t *testing.T) {
	cases := []struct {
		v                          float64
		wantDev, wantMag, wantFrac float64
	}{
		{50, 0, 0, 0},
		{53, 3, 3, 0.6},
		{47, -3, 3, -0.6},
		{55, 5, 5, 1},     // exactly on-scale at the ceiling
		{45, -5, 5, -1},   // exactly on-scale at the floor
		{58, 8, 8, 1.6},   // off-scale high
		{44, -6, 6, -1.2}, // off-scale low
		{100, 50, 50, 10},
	}
	for _, c := range cases {
		if got := balanceDeviation(c.v); !gaugeAlmostEqual(got, c.wantDev) {
			t.Errorf("balanceDeviation(%v) = %v, want %v", c.v, got, c.wantDev)
		}
		if got := balanceMagnitude(c.v); !gaugeAlmostEqual(got, c.wantMag) {
			t.Errorf("balanceMagnitude(%v) = %v, want %v", c.v, got, c.wantMag)
		}
		if got := balanceFraction(balanceDeviation(c.v)); !gaugeAlmostEqual(got, c.wantFrac) {
			t.Errorf("balanceFraction(balanceDeviation(%v)) = %v, want %v", c.v, got, c.wantFrac)
		}
	}
}

// TestBalanceReadingText_EvenThreshold pins where "EVEN" replaces a printed
// number: exactly where the magnitude ROUNDS to 0.0 at one decimal, not
// merely where it is exactly zero.
func TestBalanceReadingText_EvenThreshold(t *testing.T) {
	cases := []struct {
		magnitude float64
		want      string
	}{
		{0, "EVEN"},
		{0.04, "EVEN"},  // rounds to 0.0
		{0.049, "EVEN"}, // still rounds to 0.0
		{0.05, "0.1"},   // rounds away from zero to 0.1, no longer even
		{3.2, "3.2"},
		{12.0, "12.0"},
		{50, "50.0"},
	}
	for _, c := range cases {
		if got := balanceReadingText(c.magnitude); got != c.want {
			t.Errorf("balanceReadingText(%v) = %q, want %q", c.magnitude, got, c.want)
		}
	}
}

// --- Accepts / carries -----------------------------------------------------

// balanceTrackOf builds one sample per second from balanceEpoch, one dev
// field key with the given values (nil marks "no key at all" for that
// sample) -- used for the three Stryd-backed panels' own Accepts and series
// tests.
func balanceTrackOf(key string, values []float64) *fitactivity.Track {
	samples := make([]fitactivity.Sample, len(values))
	for i, v := range values {
		samples[i] = fitactivity.Sample{Time: balanceSampleTime(i)}
		if key != "" {
			samples[i].DevFields = map[string]float64{key: v}
		}
	}
	return &fitactivity.Track{Samples: samples}
}

// TestBalancePanel_AcceptsWalksItsOwnAccessorNotTheReport pins the reason
// this panel needs its own carries rule (BalancePanel.Accepts' own doc
// comment): an activity whose every recorded balance is a refused 0 must
// decline, even though the DevFields key exists at every sample -- which is
// all a coverage-report-based rule could ever see.
func TestBalancePanel_AcceptsWalksItsOwnAccessorNotTheReport(t *testing.T) {
	allZero := balanceTrackOf(fitactivity.StrydImpactLoadingRateBalanceField, []float64{0, 0, 0, 0, 0})
	if (ImpactBalance()).Accepts(&Context{Track: allZero}) {
		t.Error("Accepts = true for a track whose every reading is a refused zero, want false")
	}

	oneGenuine := balanceTrackOf(fitactivity.StrydImpactLoadingRateBalanceField, []float64{0, 0, 47, 0, 0})
	if !(ImpactBalance()).Accepts(&Context{Track: oneGenuine}) {
		t.Error("Accepts = false for a track carrying one genuine reading, want true")
	}

	if (ImpactBalance()).Accepts(&Context{Track: nil}) {
		t.Error("Accepts = true for a nil Track, want false")
	}
	if (ImpactBalance()).Accepts(nil) {
		t.Error("Accepts = true for a nil Context, want false")
	}

	empty := &fitactivity.Track{}
	if (ImpactBalance()).Accepts(&Context{Track: empty}) {
		t.Error("Accepts = true for an empty Track, want false")
	}
}

// --- the series: built from the track, never from f.Sample -----------------

// TestBalanceSeries_ExcludesRefusedZerosFromTheWindowAverage is the finding
// that makes this panel unusual (see BalancePanel's own doc comment, "Do not
// read f.Sample for the value"): buildGaugeSeries, driven by this panel's own
// zero-refusing accessor, must average only the genuine readings inside a
// window, never the refused zeros sharing it.
//
// Five 1Hz samples: 0, 0, 0, 54, 56 (StanceTimeBalance). A 10s window
// centred on the third sample (index 2) reaches every one of the five (half
// = 5s). If the zeros counted, the average of all five raw values would be
// (0+0+0+54+56)/5 = 22 -- nowhere near either genuine reading. Excluding
// them, the average of the two genuine values is (54+56)/2 = 55.
func TestBalanceSeries_ExcludesRefusedZerosFromTheWindowAverage(t *testing.T) {
	raw := []float64{0, 0, 0, 54, 56}
	samples := make([]fitactivity.Sample, len(raw))
	for i, v := range raw {
		samples[i] = fitactivity.Sample{Time: balanceSampleTime(i), HasStanceTimeBalance: true, StanceTimeBalance: v}
	}
	track := &fitactivity.Track{Samples: samples}

	series := buildGaugeSeries(track, 10*time.Second, contactBalanceValue)
	got, ok := series.At(balanceSampleTime(2))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := 55.0; !gaugeAlmostEqual(got, want) {
		t.Errorf("series.At(sample 2) = %v, want %v (the average of the two genuine readings, zeros excluded)", got, want)
	}
}

// TestBalanceSeries_GapWiderThanDefaultMaxGapRefuses pins the placeholder-not-
// a-straight-line policy (gaugeSeries.At, gauge.go) as it applies to THIS
// panel's own window resolution: two genuine readings ten seconds apart, a
// window (balanceSeriesWindow, multiplier 1, floored at minSmoothingWindow =
// 1s for an unset Context.Smoothing) far narrower than the gap, so each
// sample's own window catches only itself and the two series points sit
// exactly 10s apart -- wider than fitactivity.DefaultMaxGap(3s). A query at
// the midpoint must refuse rather than interpolate a straight line across a
// gap this wide.
func TestBalanceSeries_GapWiderThanDefaultMaxGapRefuses(t *testing.T) {
	track := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: balanceSampleTime(0), HasStanceTimeBalance: true, StanceTimeBalance: 54},
		{Time: balanceSampleTime(10), HasStanceTimeBalance: true, StanceTimeBalance: 46},
	}}
	ctx := &Context{Track: track}
	window := balanceSeriesWindow(ctx)
	if window >= 10*time.Second {
		t.Fatalf("precondition failed: window %v is not narrower than the 10s gap between samples", window)
	}
	series := buildGaugeSeries(track, window, contactBalanceValue)
	mid := balanceSampleTime(5)
	if _, ok := series.At(mid); ok {
		t.Error("series.At(midpoint) = ok, want a refusal across a gap wider than fitactivity.DefaultMaxGap")
	}
}

// --- drawing states ----------------------------------------------------

// balancePainterFor builds the Context a real render would (Track, Fonts,
// frame dimensions) and returns the concrete painter, mirroring
// climbPainterFor (climb_test.go) and heartRateGaugePainter (gauge_test.go)
// for this panel's own type.
func balancePainterFor(t *testing.T, panel BalancePanel, track *fitactivity.Track, box Box, fw, fh int) (*Canvas, *image.RGBA, *balancePainter) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, int(box.W), int(box.H)))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, float64(fh)*0.05, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Track: track, Width: fw, Height: fh, FontScale: 0.05, Fonts: faces}
	p, ok := panel.Prepare(ctx, box).(*balancePainter)
	if !ok {
		t.Fatal("Prepare did not return a *balancePainter")
	}
	if !p.hasBar {
		t.Fatal("precondition failed: want a resolved bar")
	}
	return c, img, p
}

// balanceContactTrackConstant builds n 1Hz samples all carrying the same
// genuine StanceTimeBalance v -- enough for buildGaugeSeries' own smoothing
// to reproduce v exactly at every interior point (a constant series smooths
// to itself), which is what lets these drawing tests hand-pick v and know
// exactly what the marker/fill will show.
func balanceContactTrackConstant(v float64, n int) *fitactivity.Track {
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{Time: balanceSampleTime(i), HasStanceTimeBalance: true, StanceTimeBalance: v}
	}
	return &fitactivity.Track{Samples: samples}
}

// TestBalancePanel_PresentInRangeFillsProportionallyFromCentre checks the
// ordinary case on both sides of 50: the fill reaches exactly the fraction
// balanceFraction implies, to the RIGHT of centre for a reading above 50 and
// to the LEFT for one below it, and never fills past the centre in the
// wrong direction.
//
// This checks exact pixels against Theme.Foreground rather than scanning
// for "any ink", the way climb_test.go's own TestClimbPanel_DynamicFillsTo...
// does -- and for the identical reason a scan would be WRONG here: Static's
// own dim ghost (drawTrack) already occupies the bar's FULL span, so "the
// first non-background pixel" finds the span's own far end regardless of
// where the live fill actually stops. Only a colour-specific check --
// Theme.Foreground exactly at and past the expected edge -- can tell "the
// fill reached here" apart from "the dim ghost was always here".
func TestBalancePanel_PresentInRangeFillsProportionallyFromCentre(t *testing.T) {
	const w, h = 700, 160
	box := Box{X: 0, Y: 0, W: w, H: h}
	fg := color.RGBAModel.Convert(DefaultTheme().Foreground).(color.RGBA)

	for _, tc := range []struct {
		name string
		v    float64
	}{
		{"above centre", 53}, // deviation +3, fraction 0.6
		{"below centre", 47}, // deviation -3, fraction -0.6
	} {
		t.Run(tc.name, func(t *testing.T) {
			track := balanceContactTrackConstant(tc.v, 21)
			c, img, p := balancePainterFor(t, ContactBalance(), track, box, w, h)
			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{At: balanceSampleTime(10)})

			frac := balanceFraction(balanceDeviation(tc.v))
			half := p.spanW / 2
			center := p.spanX + half
			edge := center + frac*half
			y := int(p.trackY)
			dir := 1.0
			if frac < 0 {
				dir = -1.0
			}

			// Just inside the fill's own edge, toward the centre: the live
			// foreground fill colour.
			insideX := int(edge - dir*3)
			if got := img.RGBAAt(insideX, y); got != fg {
				t.Errorf("%s: pixel just inside the fill's own edge (x=%d) is %v, want the foreground fill colour %v",
					tc.name, insideX, got, fg)
			}
			// Just past the fill's own edge, away from the centre: NOT the
			// foreground fill -- an on-scale reading must not fill further
			// than its own fraction implies.
			outsideX := int(edge + dir*3)
			if got := img.RGBAAt(outsideX, y); got == fg {
				t.Errorf("%s: pixel just past the fill's own edge (x=%d) is the foreground fill colour, want the dim ghost -- "+
					"the fill overran its own fraction", tc.name, outsideX)
			}
			// The mirror point on the OPPOSITE side of centre: no fill at
			// all, since this bar fills toward the reading's own side only.
			mirrorX := int(2*center - edge)
			if got := img.RGBAAt(mirrorX, y); got == fg {
				t.Errorf("%s: pixel mirrored to the opposite side of centre (x=%d) is the foreground fill colour -- "+
					"the bar must fill toward one side only", tc.name, mirrorX)
			}
		})
	}
}

// TestBalancePanel_OffScaleClipsTheFillAndDrawsTheChevron pins the second
// drawing state: a reading past +-balanceHalfRange must fill only to the
// span's own end (never past it, since there is nothing past it to fill
// toward) and must draw the reused off-scale chevron there.
func TestBalancePanel_OffScaleClipsTheFillAndDrawsTheChevron(t *testing.T) {
	const w, h = 700, 160
	box := Box{X: 0, Y: 0, W: w, H: h}
	const v = 58.0 // deviation +8, magnitude 8, fraction 1.6 -- off-scale high.
	track := balanceContactTrackConstant(v, 21)
	c, img, p := balancePainterFor(t, ContactBalance(), track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: balanceSampleTime(10)})

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	y := int(p.trackY)

	// The clipped fill's own edge is the span's own end -- a point just
	// inside it must be the live foreground colour, proving the FILL (not
	// merely the dim ghost, which reaches the identical x) actually drew
	// there.
	edgeInsideX := int(p.spanX + p.spanW - 3)
	if got := img.RGBAAt(edgeInsideX, y); got != fg {
		t.Errorf("pixel just inside the span's own end (x=%d) is %v, want the live foreground fill colour %v -- "+
			"an off-scale reading must still fill all the way to the span's own end", edgeInsideX, got, fg)
	}

	// The chevron's own triangle tip sits at (spanX+spanW+capW, trackY); a
	// pixel there must differ from the plain background.
	tipX := int(p.spanX + p.spanW + p.capW - 1)
	got := img.RGBAAt(tipX, y)
	bg := color.RGBAModel.Convert(c.Theme.Background).(color.RGBA)
	if got == bg {
		t.Error("no ink found near the chevron's own tip -- an off-scale reading must draw the overflow chevron")
	}

	// And the fill must NOT overrun past the chevron's own reach: a point
	// just before the reading column's own text is neither the ghost nor
	// the fill's foreground colour spilled across the gap. This is what
	// catches a clip that only clamps the LOWER bound (or none at all): an
	// unclamped fraction of 1.6 would compute a fill more than half again as
	// wide as the span itself, running the live foreground colour straight
	// through the gap and into the reading column.
	pastChevronX := int(p.readingX) - 5
	if pastChevronX > tipX {
		if got := img.RGBAAt(pastChevronX, y); got == fg {
			t.Errorf("pixel just before the reading column (x=%d) is the live foreground fill colour -- "+
				"the fill overran past the chevron's own reach instead of clipping at the span's own end", pastChevronX)
		}
	}
}

// TestBalancePanel_TrueUnclippedMagnitudeIsWhatGetsPrinted is the arithmetic
// half of the off-scale case, extracted from drawing per this project's own
// testing discipline: the number an off-scale reading prints is
// balanceMagnitude(v), the TRUE deviation, never the fraction clamped to
// [-1,1] that the fill itself is limited to.
func TestBalancePanel_TrueUnclippedMagnitudeIsWhatGetsPrinted(t *testing.T) {
	const v = 58.0 // magnitude 8, clipped fill fraction only reaches 1.0 (5 points)
	magnitude := balanceMagnitude(v)
	if magnitude != 8 {
		t.Fatalf("precondition failed: magnitude = %v, want 8", magnitude)
	}
	if got := balanceReadingText(magnitude); got != "8.0" {
		t.Errorf("balanceReadingText(%v) = %q, want %q (the true, unclipped magnitude)", magnitude, got, "8.0")
	}
}

// TestBalancePanel_DynamicPrintsTheTrueMagnitudeNotTheClampedOne is the
// end-to-end half of the check above: it exercises Dynamic itself, not just
// the arithmetic helpers, so a Dynamic that printed
// balanceReadingText(min(magnitude, balanceHalfRange)) -- which would still
// pass every other test in this file, since the fill and the chevron look
// identical either way -- is still caught.
//
// Two readings, 58 and 200, are BOTH off-scale high and therefore clip to
// the identical fill fraction (1.0) and draw the identical chevron -- so if
// Dynamic printed the clamped magnitude, both would print the SAME text
// ("5.0", balanceHalfRange). Their true magnitudes (8 and 150) differ, so
// the reading column's own pixels must differ between the two renders.
func TestBalancePanel_DynamicPrintsTheTrueMagnitudeNotTheClampedOne(t *testing.T) {
	const w, h = 700, 160
	box := Box{X: 0, Y: 0, W: w, H: h}

	render := func(v float64) []byte {
		track := balanceContactTrackConstant(v, 21)
		c, img, p := balancePainterFor(t, ContactBalance(), track, box, w, h)
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{At: balanceSampleTime(10)})
		readingBox := Box{X: p.readingX, Y: box.Y, W: box.W - p.readingX, H: box.H}
		return snapshotBox(img, readingBox)
	}

	a := render(58)  // magnitude 8
	b := render(200) // magnitude 150 -- both clip to fraction 1.0 alike

	if bytes.Equal(a, b) {
		t.Error("the reading column drew identically for two off-scale readings with different TRUE magnitudes -- " +
			"Dynamic must print the true, unclipped magnitude, not one clamped to balanceHalfRange")
	}
}

// TestBalancePanel_AbsentInstantDrawsNoFillAndWashesTheWholeTrack is the
// absent-per-frame check: no fill at all -- not a zero-length fill, which
// would sit exactly on the centre tick and read as perfectly even -- plus
// the whole track washed so the panel stays visually distinct from one that
// failed to draw.
func TestBalancePanel_AbsentInstantDrawsNoFillAndWashesTheWholeTrack(t *testing.T) {
	const w, h = 700, 160
	box := Box{X: 0, Y: 0, W: w, H: h}
	track := balanceContactTrackConstant(53, 21)
	c, img, p := balancePainterFor(t, ContactBalance(), track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)

	// An instant well outside the track's own recorded span: no series point
	// can possibly answer, so this must take the placeholder branch.
	absentAt := balanceSampleTime(-100)
	p.Dynamic(c, Frame{At: absentAt})

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	// Neither half of the span (left of centre, right of centre) may show the
	// live foreground fill colour.
	for _, x := range []int{int(p.spanX + 4), int(p.spanX+p.spanW/2) - 4, int(p.spanX+p.spanW/2) + 4, int(p.spanX + p.spanW - 4)} {
		got := img.RGBAAt(x, int(p.trackY))
		if got == fg {
			t.Errorf("pixel at x=%d on the track row is the live foreground colour %v on an absent instant -- no fill may be drawn at all", x, fg)
		}
	}

	// The wash must still visibly change the track from its own Static
	// ghost -- checked at a point well inside the span, away from the centre
	// tick's own overhang.
	x := int(p.spanX + 4)
	y := int(p.trackY)
	c.Fill(c.Theme.Background)
	p.Static(c)
	ghost := img.RGBAAt(x, y)
	p.Dynamic(c, Frame{At: absentAt})
	wash := img.RGBAAt(x, y)
	if ratio := ContrastRatio(wash, ghost); ratio < gaugeAbsentWashContrastFloor {
		t.Errorf("wash %v over ghost %v measured %.3f:1, below the %v:1 floor -- an absent instant must wash the whole track",
			wash, ghost, ratio, gaugeAbsentWashContrastFloor)
	}
}

// TestBalancePanel_ScalesWithFrameDimensionsNotAPixelConstant is
// ClimbPanel's own identical scaling check (climb_test.go), restated for
// this panel: the same track, prepared against boxes shaped like 1080p, 4K
// and the portrait tree, must produce a span width and bar thickness that
// scale with the box rather than sitting at a size unrelated to it.
func TestBalancePanel_ScalesWithFrameDimensionsNotAPixelConstant(t *testing.T) {
	track := balanceContactTrackConstant(53, 21)
	sizes := []struct {
		name   string
		w, h   float64
		fw, fh int
	}{
		{"1080p-shaped", 480, 160, 1920, 1080},
		{"4K-shaped", 960, 320, 3840, 2160},
		{"portrait-shaped", 300, 260, 1080, 1920},
	}
	var prevSpan, prevTrackH float64
	for i, s := range sizes {
		box := Box{X: 0, Y: 0, W: s.w, H: s.h}
		_, _, p := balancePainterFor(t, ContactBalance(), track, box, s.fw, s.fh)
		if p.spanW <= 0 {
			t.Fatalf("%s: spanW <= 0", s.name)
		}
		if i > 0 && p.spanW == prevSpan {
			t.Errorf("%s: spanW is identical to the previous box's (%v) despite a different box width", s.name, prevSpan)
		}
		if i > 0 && p.trackH == prevTrackH {
			t.Errorf("%s: trackH is identical to the previous box's (%v) despite a different box height", s.name, prevTrackH)
		}
		prevSpan, prevTrackH = p.spanW, p.trackH
	}
}

// TestBalancePanel_DynamicReadsItsOwnSeriesNeverFSample is the flagship test
// for this panel's own unusual finding (BalancePanel's own doc comment, "Do
// not read f.Sample for the value"): Frame.Sample carries a REFUSED zero at
// an instant where this panel's own series -- built in Prepare, straight
// from the track -- has a genuine, off-scale reading. A Dynamic that (bug)
// read f.Sample directly would see StanceTimeBalance=0, HasStanceTimeBalance
// true, refuse it via contactBalanceValue, and take the ABSENT branch --
// drawing no fill and washing the track. The correct Dynamic ignores
// f.Sample entirely and reads p.series.At(f.At), which is present at 60 (a
// constant series smooths to itself) -- so it must draw a live foreground
// fill and the off-scale chevron instead.
func TestBalancePanel_DynamicReadsItsOwnSeriesNeverFSample(t *testing.T) {
	const w, h = 700, 160
	box := Box{X: 0, Y: 0, W: w, H: h}
	const seriesV = 60.0 // deviation +10, off-scale high
	track := balanceContactTrackConstant(seriesV, 21)
	c, img, p := balancePainterFor(t, ContactBalance(), track, box, w, h)
	c.Fill(c.Theme.Background)
	p.Static(c)

	at := balanceSampleTime(10)
	// A Frame whose own Sample disagrees with the track: a refused zero,
	// which -- if Dynamic mistakenly read it -- would take the absent branch.
	p.Dynamic(c, Frame{At: at, HasSample: true, Sample: fitactivity.Sample{HasStanceTimeBalance: true, StanceTimeBalance: 0}})

	fg := color.RGBAModel.Convert(c.Theme.Foreground).(color.RGBA)
	// A point just inside the span's own right edge must be the LIVE fill
	// colour: reachable only if Dynamic used the series (present, off-scale,
	// clipped fill reaching the end), never if it used f.Sample (absent,
	// wash only, no foreground fill anywhere on the row).
	x := int(p.spanX + p.spanW - 4)
	got := img.RGBAAt(x, int(p.trackY))
	if got != fg {
		t.Errorf("pixel near the span's own end is %v, want the live foreground fill %v -- "+
			"Dynamic must read p.series.At(f.At), never f.Sample directly", got, fg)
	}
}

// --- Prepare's own defensive fallback --------------------------------------

// TestBalancePanel_PrepareNilContextOrTrackDrawsNothing mirrors
// ClimbPanel's own defensive-guard test: Accepts should always prevent this,
// but a defensive Prepare must not panic, and must leave hasBar false so
// Static/Dynamic draw no bar geometry at all.
func TestBalancePanel_PrepareNilContextOrTrackDrawsNothing(t *testing.T) {
	box := Box{X: 0, Y: 0, W: 400, H: 120}
	for _, ctx := range []*Context{nil, {Track: nil}} {
		p, ok := ContactBalance().Prepare(ctx, box).(*balancePainter)
		if !ok {
			t.Fatal("Prepare did not return a *balancePainter")
		}
		if p.hasBar {
			t.Error("hasBar = true for a nil Context or Track, want false")
		}
		img := image.NewRGBA(image.Rect(0, 0, int(box.W), int(box.H)))
		faces, err := NewFaceCache()
		if err != nil {
			t.Fatal(err)
		}
		c, err := NewCanvas(img, 20, DefaultTheme(), faces)
		if err != nil {
			t.Fatal(err)
		}
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{})
	}
}
