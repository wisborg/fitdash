package panel

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

func newTestCanvas(t *testing.T, w, h int) (*Canvas, *image.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatalf("NewCanvas: %v", err)
	}
	return c, img
}

// inkIn counts pixels in b that differ from the theme background. It is how
// every "did this actually draw" assertion here is phrased: a drawing call
// that returns no error and leaves the buffer untouched is the characteristic
// silent failure of this layer.
func inkIn(img *image.RGBA, b Box, bg color.Color) int {
	br, bgc, bb, _ := bg.RGBA()
	n := 0
	for y := int(b.Y); y < int(b.Y+b.H) && y < img.Bounds().Dy(); y++ {
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			if x < 0 || y < 0 {
				continue
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != br || g != bgc || bl != bb {
				n++
			}
		}
	}
	return n
}

// TestCanvas_FillCoversEveryPixel pins that Fill is what makes a declining
// panel's absence read as clean background rather than as whatever was left
// in the buffer from the previous frame.
func TestCanvas_FillCoversEveryPixel(t *testing.T) {
	c, img := newTestCanvas(t, 40, 30)
	// Poison the buffer first: a Fill that only painted part of the frame
	// would pass against a freshly allocated image, which is already zeroed.
	for i := range img.Pix {
		img.Pix[i] = 0xAB
	}
	c.Fill(c.Theme.Background)

	want := color.RGBAModel.Convert(c.Theme.Background).(color.RGBA)
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			if got := img.RGBAAt(x, y); got != want {
				t.Fatalf("pixel (%d,%d) is %v, want the background %v", x, y, got, want)
			}
		}
	}
}

// TestCanvas_RectFillsExactlyItsBox checks the ink lands inside the box and
// nowhere else -- the property that makes disjoint boxes actually prevent
// overlap, rather than merely describing an intention.
func TestCanvas_RectFillsExactlyItsBox(t *testing.T) {
	c, img := newTestCanvas(t, 100, 100)
	c.Fill(c.Theme.Background)

	box := Box{X: 20, Y: 30, W: 40, H: 25}
	c.Rect(box, c.Theme.Foreground)

	if got, want := inkIn(img, box, c.Theme.Background), 40*25; got != want {
		t.Errorf("filled %d pixels inside the box, want %d", got, want)
	}
	whole := Box{W: 100, H: 100}
	if got, want := inkIn(img, whole, c.Theme.Background), 40*25; got != want {
		t.Errorf("filled %d pixels in the frame, want %d -- ink escaped the box", got, want)
	}
}

// TestCanvas_TextDrawsInkAndHonoursItsAnchor is the test that a drawing call
// which returns nil actually drew.
//
// The anchor assertions matter more than they look: a panel positioning a
// readout against the right edge of its box passes ax=1 and never measures the
// string, because the string's width changes every frame as the digits do. If
// the anchor were ignored, every right-aligned readout would drift.
func TestCanvas_TextDrawsInkAndHonoursItsAnchor(t *testing.T) {
	c, img := newTestCanvas(t, 200, 60)
	c.Fill(c.Theme.Background)

	if err := c.Text("88:88", 100, 30, 0, 0.5, 24, c.Theme.Foreground); err != nil {
		t.Fatalf("Text: %v", err)
	}
	leftAnchored := inkIn(img, Box{W: 200, H: 60}, c.Theme.Background)
	if leftAnchored == 0 {
		t.Fatal("Text returned nil and drew nothing")
	}
	// Anchored left AT x=100, so the ink must sit to the right of it.
	if inkIn(img, Box{X: 0, Y: 0, W: 99, H: 60}, c.Theme.Background) != 0 {
		t.Error("left-anchored text put ink left of its anchor")
	}

	c.Fill(c.Theme.Background)
	if err := c.Text("88:88", 100, 30, 1, 0.5, 24, c.Theme.Foreground); err != nil {
		t.Fatalf("Text: %v", err)
	}
	if inkIn(img, Box{X: 101, Y: 0, W: 99, H: 60}, c.Theme.Background) != 0 {
		t.Error("right-anchored text put ink right of its anchor")
	}
	if inkIn(img, Box{X: 0, Y: 0, W: 100, H: 60}, c.Theme.Background) == 0 {
		t.Error("right-anchored text drew nothing left of its anchor")
	}

	// An empty string is a no-op rather than an error: a panel with nothing
	// to say this frame should not have to guard the call.
	c.Fill(c.Theme.Background)
	if err := c.Text("", 10, 10, 0, 0, 20, c.Theme.Foreground); err != nil {
		t.Errorf("empty Text returned %v", err)
	}
	if inkIn(img, Box{W: 200, H: 60}, c.Theme.Background) != 0 {
		t.Error("empty Text drew something")
	}
}

// TestCanvas_PolylineDrawsAndIgnoresDegenerateInput covers both the route
// panel's main call and the single-fix activity that is a real input.
func TestCanvas_PolylineDrawsAndIgnoresDegenerateInput(t *testing.T) {
	c, img := newTestCanvas(t, 100, 100)
	c.Fill(c.Theme.Background)

	c.Polyline([]float64{10, 90}, []float64{50, 50}, 3, c.Theme.Accent)
	if inkIn(img, Box{W: 100, H: 100}, c.Theme.Background) == 0 {
		t.Fatal("Polyline drew nothing")
	}

	c.Fill(c.Theme.Background)
	for _, bad := range []struct {
		name   string
		xs, ys []float64
		w      float64
	}{
		// This case is documentation rather than a discriminating assertion:
		// verified that relaxing Polyline's bound to `< 1` still passes,
		// because a single point becomes a MoveTo with no LineTo and strokes
		// as nothing anyway. The guard states the precondition; it is the
		// length-mismatch check below whose removal actually panics.
		{"a single point is not a line", []float64{5}, []float64{5}, 2},
		{"no points", nil, nil, 2},
		{"mismatched lengths", []float64{1, 2}, []float64{1}, 2},
		{"zero width", []float64{1, 2}, []float64{1, 2}, 0},
	} {
		c.Polyline(bad.xs, bad.ys, bad.w, c.Theme.Accent)
		if inkIn(img, Box{W: 100, H: 100}, c.Theme.Background) != 0 {
			t.Errorf("%s: Polyline drew something", bad.name)
			c.Fill(c.Theme.Background)
		}
	}
}

// TestCanvas_PolygonFillsAndIgnoresDegenerateInput mirrors
// TestCanvas_PolylineDrawsAndIgnoresDegenerateInput: a known triangle fills
// pixels inside it and none outside, and the same guards that protect
// Polyline protect Polygon.
func TestCanvas_PolygonFillsAndIgnoresDegenerateInput(t *testing.T) {
	c, img := newTestCanvas(t, 100, 100)
	c.Fill(c.Theme.Background)

	// A right triangle with legs on the frame's edges: (10,10), (90,10),
	// (10,90). Its centroid at (36.67, 36.67) must be filled; a point well
	// outside it, near the frame's opposite corner, must not be.
	c.Polygon([]float64{10, 90, 10}, []float64{10, 10, 90}, c.Theme.Accent)
	if inkIn(img, Box{W: 100, H: 100}, c.Theme.Background) == 0 {
		t.Fatal("Polygon drew nothing")
	}
	want := color.RGBAModel.Convert(c.Theme.Accent).(color.RGBA)
	if got := img.RGBAAt(30, 30); got != want {
		t.Errorf("pixel (30,30), inside the triangle, is %v, want the fill colour %v", got, want)
	}
	if inkIn(img, Box{X: 91, Y: 91, W: 9, H: 9}, c.Theme.Background) != 0 {
		t.Error("Polygon painted outside the triangle, near the frame's far corner")
	}

	c.Fill(c.Theme.Background)
	for _, bad := range []struct {
		name   string
		xs, ys []float64
	}{
		{"two points is not an area", []float64{10, 90}, []float64{10, 90}},
		{"one point", []float64{10}, []float64{10}},
		{"no points", nil, nil},
		{"mismatched lengths", []float64{1, 2, 3}, []float64{1, 2}},
	} {
		c.Polygon(bad.xs, bad.ys, c.Theme.Accent)
		if inkIn(img, Box{W: 100, H: 100}, c.Theme.Background) != 0 {
			t.Errorf("%s: Polygon drew something", bad.name)
			c.Fill(c.Theme.Background)
		}
	}
}

// TestContrastRatio_MatchesWCAGWorkedExamples pins the formula against values
// that can be checked independently of this codebase rather than against
// whatever the implementation currently returns.
//
// Pure black against pure white is WCAG 2's own edge case: luminance is
// exactly 0 and exactly 1, so the ratio is (1+0.05)/(0+0.05) = 21, the
// maximum the scale defines. Mid-grey (#808080) against white is a value
// commonly cited as the threshold example in WCAG discussions -- computed
// independently here (sRGB channel 128/255, linearized, weighted 0.2126/
// 0.7152/0.0722) it comes to (0.21586+0.05)/(0+0.05)... precisely
// 3.9494396480491156, which this test rounds to two decimals so a rounding
// difference in the last few bits of float64 arithmetic does not make an
// otherwise-correct implementation fail.
func TestContrastRatio_MatchesWCAGWorkedExamples(t *testing.T) {
	black := color.RGBA{R: 0, G: 0, B: 0, A: 0xFF}
	white := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	if got, want := ContrastRatio(black, white), 21.0; math.Abs(got-want) > 0.001 {
		t.Errorf("ContrastRatio(black, white) = %v, want %v", got, want)
	}
	// Order must not matter: the ratio is defined as (lighter+0.05)/(darker+0.05).
	if got, want := ContrastRatio(white, black), 21.0; math.Abs(got-want) > 0.001 {
		t.Errorf("ContrastRatio(white, black) = %v, want %v -- order should not matter", got, want)
	}

	gray := color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xFF}
	if got, want := ContrastRatio(gray, white), 3.9494396480491156; math.Abs(got-want) > 0.01 {
		t.Errorf("ContrastRatio(gray128, white) = %v, want ~%v", got, want)
	}

	if got := ContrastRatio(white, white); math.Abs(got-1) > 0.001 {
		t.Errorf("ContrastRatio of identical colours = %v, want 1", got)
	}
}

// TestThemesAreLegible pins the property the whole absent-data policy rests on
// visually, for EVERY shipped palette rather than only the default.
//
// A placeholder has to be tellable from a live reading and from the
// background, by a viewer who does not know in advance which is which. If
// Absent were merely Dim reused, a missing heart rate would look like an axis
// label; if it were near the background it would look like nothing at all,
// which is the unexplained hole the policy exists to prevent.
//
// Running it over Themes() rather than over one palette is what makes adding a
// theme safe. A light palette is not the dark one inverted -- inverting gives
// light grey chrome on white, which disappears -- so every role has to be
// picked again for its background, and this is what says whether it was done
// carefully.
//
// Foreground against Background is checked separately, against
// ContrastRatio and WCAG 2's own 4.5:1 threshold, rather than folded into the
// squared-distance loop below: it is the one pair with a published,
// defensible number, and pinning it against an invented floor -- as this
// test used to -- would say less than what is actually available.
func TestThemesAreLegible(t *testing.T) {
	dist := func(a, b color.Color) float64 {
		ar, ag, ab, _ := a.RGBA()
		br, bg, bb, _ := b.RGBA()
		dr, dg, db := float64(ar)-float64(br), float64(ag)-float64(bg), float64(ab)-float64(bb)
		return (dr*dr + dg*dg + db*db) / (65535 * 65535)
	}
	// A generous floor: these are palette roles, not a contrast standard.
	const minSeparation = 0.02

	// The one pair with a published, defensible number rather than a floor
	// invented here -- see ContrastRatio's own doc comment. Checked
	// separately from the squared-distance loop below, which pins every
	// OTHER role pair against a floor its own comment admits is not a
	// contrast standard.
	const minContrast = 4.5

	themes := Themes()
	if len(themes) < 2 {
		t.Fatal("Themes() offers fewer than two palettes; this test would only be checking the default")
	}
	for _, th := range themes {
		t.Run(th.Name, func(t *testing.T) {
			if th.Name == "" {
				t.Error("a theme with no name cannot be selected by --theme")
			}
			if r := ContrastRatio(th.Foreground, th.Background); r < minContrast {
				t.Errorf("foreground/background contrast is %.2f:1, below the WCAG 2 threshold of %v:1 for normal text",
					r, minContrast)
			}
			for _, c := range []struct {
				name string
				a, b color.Color
			}{
				{"absent vs foreground", th.Absent, th.Foreground},
				{"absent vs background", th.Absent, th.Background},
				{"absent vs dim", th.Absent, th.Dim},
				{"dim vs background", th.Dim, th.Background},
				{"accent vs background", th.Accent, th.Background},
				{"accent vs foreground", th.Accent, th.Foreground},
				// Highlight has to read against everything it can share a
				// frame with: the ribbon it sits on (Dim), the background
				// around it, and the playhead (Accent) that sweeps across
				// its own blocks -- conflating Highlight with Accent would
				// make a configured range indistinguishable from "the
				// render is here right now".
				{"highlight vs background", th.Highlight, th.Background},
				{"highlight vs dim", th.Highlight, th.Dim},
				{"highlight vs accent", th.Highlight, th.Accent},
			} {
				if d := dist(c.a, c.b); d < minSeparation {
					t.Errorf("%s: separation %.4f is below %v; the two would read as the same thing",
						c.name, d, minSeparation)
				}
			}
		})
	}
}

// TestSelectTheme_RefusesAnUnknownName pins that a typo costs a message rather
// than a whole render in a palette nobody asked for.
func TestSelectTheme_RefusesAnUnknownName(t *testing.T) {
	for _, th := range Themes() {
		got, err := SelectTheme(th.Name)
		if err != nil {
			t.Errorf("SelectTheme(%q): %v", th.Name, err)
			continue
		}
		if got.Name != th.Name {
			t.Errorf("SelectTheme(%q) returned %q", th.Name, got.Name)
		}
	}
	for _, bad := range []string{"", "Dark", "solarized", "none"} {
		if _, err := SelectTheme(bad); err == nil {
			t.Errorf("SelectTheme(%q) was accepted", bad)
		}
	}
	// The default must be one of the offered names, or --theme cannot spell it.
	if _, err := SelectTheme(DefaultTheme().Name); err != nil {
		t.Errorf("the default theme is not selectable by name: %v", err)
	}
}

// TestFaceCache_ReusesAFaceAcrossFrames pins the caching, which at 45,000
// frames is the difference between a render finishing and not.
func TestFaceCache_ReusesAFaceAcrossFrames(t *testing.T) {
	fc, err := NewFaceCache()
	if err != nil {
		t.Fatalf("NewFaceCache: %v", err)
	}
	a, err := fc.face(24)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fc.face(24)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("the same size produced two faces; the cache is not being hit")
	}

	// Sizes derived from a fractional box dimension are a different float64
	// on almost every call. Rounding to a whole pixel is what makes the cache
	// work at all -- keyed on the raw value it would cache nothing while
	// growing without bound.
	c, err := fc.face(24.4)
	if err != nil {
		t.Fatal(err)
	}
	if a != c {
		t.Error("24.4px produced a different face from 24px; sizes must round to a whole pixel")
	}
	if got := len(fc.faces); got != 1 {
		t.Errorf("cache holds %d faces after three near-identical requests, want 1", got)
	}

	d, err := fc.face(48)
	if err != nil {
		t.Fatal(err)
	}
	if a == d {
		t.Error("a genuinely different size reused the same face")
	}
}

// TestCanvas_MeasureTextGrowsWithSizeAndLength keeps the measurement honest
// enough to size against, since FitTextSize divides by it.
func TestCanvas_MeasureTextGrowsWithSizeAndLength(t *testing.T) {
	c, _ := newTestCanvas(t, 200, 100)

	small, _, err := c.MeasureText("00:00:00", 12)
	if err != nil {
		t.Fatal(err)
	}
	large, _, err := c.MeasureText("00:00:00", 48)
	if err != nil {
		t.Fatal(err)
	}
	if !(large > small) {
		t.Errorf("48px measured %g and 12px measured %g", large, small)
	}

	short, _, _ := c.MeasureText("0", 24)
	long, _, _ := c.MeasureText("00000000", 24)
	if !(long > short) {
		t.Errorf("eight characters measured %g and one measured %g", long, short)
	}
	// Monospace: eight characters are eight times one, within a pixel.
	if diff := long - 8*short; diff < -1 || diff > 1 {
		t.Errorf("eight characters measured %g, want ~%g -- the face should be monospace", long, 8*short)
	}
}

// TestCanvas_FitTextSizeActuallyFits is the guard for the case a rectangle Box
// creates: a readout comfortable at 1080p overflowing the same box in a
// portrait frame, where the width collapses.
func TestCanvas_FitTextSizeActuallyFits(t *testing.T) {
	c, _ := newTestCanvas(t, 400, 100)

	const s = "12:34:56"
	cases := []struct{ maxW, px float64 }{
		{300, 40}, {100, 40}, {40, 40}, {12, 40}, {1000, 40},
	}
	for _, tc := range cases {
		got, err := c.FitTextSize(s, tc.maxW, tc.px)
		if err != nil {
			t.Fatalf("FitTextSize: %v", err)
		}
		if got > tc.px {
			t.Errorf("FitTextSize(%v, %v) = %v, which is larger than the size asked for", tc.maxW, tc.px, got)
		}
		if got < 1 {
			t.Errorf("FitTextSize(%v, %v) = %v; it must never go below a pixel", tc.maxW, tc.px, got)
		}
		w, _, err := c.MeasureText(s, got)
		if err != nil {
			t.Fatal(err)
		}
		// The only case where overflow is allowed is the floor: a box too
		// small for one-pixel text is a layout problem, not a typography one.
		if w > tc.maxW && got > 1 {
			t.Errorf("FitTextSize(%v, %v) = %v, which still measures %g -- it does not fit", tc.maxW, tc.px, got, w)
		}
	}

	// Text that already fits must not be shrunk: a panel should not lose size
	// it was entitled to.
	if got, _ := c.FitTextSize(s, 1000, 40); got != 40 {
		t.Errorf("FitTextSize on text that already fits returned %v, want the requested 40", got)
	}
}

// TestNewCanvas_RejectsUnusableArguments keeps a Canvas from existing in a
// state where every later drawing call fails one at a time.
func TestNewCanvas_RejectsUnusableArguments(t *testing.T) {
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))

	if _, err := NewCanvas(nil, 20, DefaultTheme(), faces); err == nil {
		t.Error("accepted a nil image")
	}
	if _, err := NewCanvas(img, 20, DefaultTheme(), nil); err == nil {
		t.Error("accepted a nil font cache")
	}
	if _, err := NewCanvas(img, 0, DefaultTheme(), faces); err == nil {
		t.Error("accepted a zero base text size")
	}
}

// TestNewCanvas_ReadsItsDimensionsFromTheImage guards against a Canvas whose
// W and H disagree with the buffer it draws into, which would make every
// fractional size a panel derives wrong by the same ratio.
func TestNewCanvas_ReadsItsDimensionsFromTheImage(t *testing.T) {
	c, _ := newTestCanvas(t, 1280, 720)
	if c.W != 1280 || c.H != 720 {
		t.Errorf("Canvas is %dx%d, want 1280x720", c.W, c.H)
	}
}

// TestCanvas_ClippedToNothingDrawsNothing pins the direction this fails in.
//
// A clip box with no area produces an empty image.Rectangle, and an empty
// rectangle is indistinguishable from the zero value a Canvas starts with --
// so a guard written as "is the clip rectangle non-empty" reads a
// clipped-to-nothing region as "no clip at all" and paints the image across
// the WHOLE frame, which is the exact opposite of what was asked for. Nothing
// in this program passes a zero-area box today; the point is that if
// something ever does, the mistake must be an image that is missing rather
// than an image over everything else on screen.
func TestCanvas_ClippedToNothingDrawsNothing(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	c.Fill(c.Theme.Background)

	src := image.NewRGBA(image.Rect(0, 0, 10, 10))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.RGBA{R: 0, G: 200, B: 80, A: 255}}, image.Point{}, draw.Src)

	c.Clipped(Box{X: 40, Y: 40, W: 0, H: 0}, func() {
		c.Image(src, Box{X: 0, Y: 0, W: 100, H: 100}, 1)
	})

	if n := inkCount(img, Box{W: 100, H: 100}, c.Theme); n != 0 {
		t.Errorf("%d pixels drawn through a clip with no area; an empty clip must draw nothing, not everything", n)
	}
}

// TestCanvasImage_SourceAtTheDestinationSizeIsCopiedExactly pins the copy
// path Image takes when the source already matches the rectangle it is going
// into.
//
// It is not only a speed question. CatmullRom has negative lobes, so running
// an image through it at 1:1 is a real operation -- it softens edges and
// rings at hard ones -- and a cache that stores a resampled image would then
// differ from an uncached draw of the same thing by a little blur nobody
// would trace back to the cache. So the contract is exactness: a source
// already at size arrives pixel for pixel.
func TestCanvasImage_SourceAtTheDestinationSizeIsCopiedExactly(t *testing.T) {
	const w, h = 64, 48
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	c.Fill(color.RGBA{A: 255})

	// A hard checkerboard: the pattern a resampling filter cannot reproduce.
	// A gradient would survive the kernel closely enough to pass.
	src := image.NewRGBA(image.Rect(0, 0, 20, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 20; x++ {
			v := uint8(0)
			if (x+y)%2 == 0 {
				v = 255
			}
			src.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}

	c.Image(src, Box{X: 8, Y: 6, W: 20, H: 12}, 1)

	for y := 0; y < 12; y++ {
		for x := 0; x < 20; x++ {
			want := src.RGBAAt(x, y)
			got := img.RGBAAt(8+x, 6+y)
			if got != want {
				t.Fatalf("pixel (%d,%d) is %v, want %v: a source already at the destination's size was resampled rather than copied", x, y, got, want)
			}
		}
	}
}
