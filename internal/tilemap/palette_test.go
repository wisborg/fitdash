package tilemap

import (
	"image/color"
	"math"
	"strings"
	"testing"
)

// lightInks is the other polarity: a near-white background with a near-black
// foreground. Spelled out here for the same reason darkInks is -- this
// package must not import the one that imports it -- and kept in step with
// fitdash's own themes by cmd's own test over panel.Themes().
func lightInks() MapInks {
	return MapInks{
		Background: color.RGBA{R: 0xF5, G: 0xF4, B: 0xF1, A: 0xFF},
		Foreground: color.RGBA{R: 0x16, G: 0x16, B: 0x1A, A: 0xFF},
		Dim:        color.RGBA{R: 0x7A, G: 0x7A, B: 0x84, A: 0xFF},
		Accent:     color.RGBA{R: 0xC4, G: 0x37, B: 0x14, A: 0xFF},
		Highlight:  color.RGBA{R: 0x5B, G: 0x3D, B: 0xE0, A: 0xFF},
	}
}

// TestMapInks_BothPolaritiesDeriveAMapThatCanCarryThem is the deliverable of
// the derivation rather than a smoke test. The whole argument for drawing a
// basemap locally is that the separation between the map and the route drawn
// over it is guaranteed by construction instead of corrected afterwards with
// a wash -- and a guarantee nothing enforces is a hope. If this fails,
// --basemap-dim's default for the local backend is wrong too.
func TestMapInks_BothPolaritiesDeriveAMapThatCanCarryThem(t *testing.T) {
	for _, c := range []struct {
		name string
		inks MapInks
	}{
		{"dark background", darkInks()},
		{"light background", lightInks()},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.inks.CheckContrast(); err != nil {
				t.Errorf("the derived map cannot carry these inks:\n%v", err)
			}
		})
	}
}

// TestMapInks_TheLoudestInkSitsJustInsideTheBandTheOverlayLeavesFree pins the
// algebra rather than the colours, and the expected numbers are derived here
// rather than read off what the code produced.
//
// Every overlay ink must clear WCAG's 3:1 against every map ink, so an
// overlay ink at luminance Lo forbids the map everything above
// (Lo+0.05)/3-0.05. On a dark background the map lives below all of them, so
// the DIMMEST overlay ink sets the ceiling. For the dark inks that is Dim
// #6E6E78: each channel 0x6E/255 = 0.4314 and 0x78/255 = 0.4706, linearized
// and weighted by WCAG's 0.2126/0.7152/0.0722 gives a luminance of 0.158229,
// so the ceiling is (0.158229+0.05)/3-0.05 = 0.019410. The background sits at
// 0.004448 and the loudest role is placed at mapHeadroom of the way from one
// to the other: 0.004448 + 0.94*(0.019410-0.004448) = 0.018512.
//
// The light case runs the other way: the map lives ABOVE the overlay inks, so
// the BRIGHTEST one -- Dim #7A7A84 at 0.197226 -- sets a floor of
// 3*(0.197226+0.05)-0.05 = 0.691678, and the loudest role lands at 0.904647 +
// 0.94*(0.691678-0.904647) = 0.704456.
//
// The tolerances are what eight-bit channels cost, and they differ by a
// factor of six because the sRGB transfer curve does: the bisection settles
// far finer than 1/255, but rounding the result to a storable colour moves
// its luminance by half a channel step -- which is worth about 0.0004 down
// near black and about 0.004 up near white, where the curve is at its
// steepest.
func TestMapInks_TheLoudestInkSitsJustInsideTheBandTheOverlayLeavesFree(t *testing.T) {
	for _, c := range []struct {
		name      string
		inks      MapInks
		want      float64
		tolerance float64
	}{
		{"dark background", darkInks(), 0.018512, 0.0006},
		{"light background", lightInks(), 0.704456, 0.004},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := luminance(c.inks.localPalette().Road)
			if math.Abs(got-c.want) > c.tolerance {
				t.Errorf("the loudest map ink has luminance %.6f, want %.6f (+/- %.4f)", got, c.want, c.tolerance)
			}
		})
	}
}

// TestMapInks_ADimInkTooCloseToTheBackgroundIsReportedByName is the failure
// this check exists to catch, and the wording matters as much as the
// detection: a consumer whose quiet ink sits almost on its own background
// leaves the map no room at all between the two, and the useful thing to say
// is which ink caused it rather than that some pair somewhere failed.
//
// #14141A against a #0E0E10 background is a 1.15:1 pair, which is a route
// outline nobody could see even on plain background -- so no map exists that
// could carry it, and the derivation must say so rather than produce
// something that looks fine until it is rendered.
func TestMapInks_ADimInkTooCloseToTheBackgroundIsReportedByName(t *testing.T) {
	inks := darkInks()
	inks.Dim = color.RGBA{R: 0x14, G: 0x14, B: 0x1A, A: 0xFF}

	err := inks.CheckContrast()
	if err == nil {
		t.Fatal("a dim ink all but invisible against its own background was accepted")
	}
	if !strings.Contains(err.Error(), "Dim") {
		t.Errorf("the failure does not name the ink that caused it: %v", err)
	}
}

// TestMapInks_TheSameInksDeriveTheSamePaletteEveryTime pins reproducibility
// at the point it would be easiest to lose. The same activity and the same
// options have to produce the same frames, and a palette assembled by
// ranging over a map would be assembled in a different order every run --
// which decides nothing on its own and decides the pixels the moment two
// roles are written in the wrong places.
func TestMapInks_TheSameInksDeriveTheSamePaletteEveryTime(t *testing.T) {
	first := darkInks().localPalette()
	for i := range 20 {
		if got := darkInks().localPalette(); got != first {
			t.Fatalf("derivation %d produced %+v, want %+v", i, got, first)
		}
	}
}

// TestMapInks_EveryDerivedInkIsBoundedInChromaSoTheMapReadsAsContext is the
// constraint osmbase's own check cannot express, and the reason it exists is
// worth more than the assertion: CheckContrast bounds a contrast RATIO, which
// is a function of luminance alone and blind to hue, and a role SEPARATION,
// which is a floor that more chroma only helps. Nothing in it looks at how
// colourful an ink is, so a vivid ink at low lightness passes every one of
// its constraints while shouting -- which is exactly what the first real
// render of this derivation did, in bright green and orange-brown.
//
// The bound is stated as 15 here rather than read from maxRoleChroma so that
// moving the constant has to be a deliberate edit in two places. It is where
// osmbase's own palettes sit: their map roles measure C*ab 4.0, 5.6, 5.9,
// 10.6, 17.4 and 20.2 in the dark palette, four of the six below 11.
//
// Not vacuous: without the cap the same roles derive at 10.2, 20.7, 26.2,
// 19.8, 39.5 and 2.2, so five of the twelve values checked here would fail.
func TestMapInks_EveryDerivedInkIsBoundedInChromaSoTheMapReadsAsContext(t *testing.T) {
	const bound = 15.0
	for _, c := range []struct {
		name string
		inks MapInks
	}{
		{"dark background", darkInks()},
		{"light background", lightInks()},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := c.inks.localPalette()
			for _, ink := range []struct {
				role string
				c    color.RGBA
			}{
				{"Land", p.Land}, {"Green", p.Green}, {"Water", p.Water},
				{"Built", p.Built}, {"Ink", p.Ink}, {"Road", p.Road},
			} {
				if got := chroma(ink.c); got > bound {
					t.Errorf("%s is #%02x%02x%02x at chroma %.1f, above %.1f: it draws as a diagram rather than as a map",
						ink.role, ink.c.R, ink.c.G, ink.c.B, got, bound)
				}
			}
		})
	}
}

// TestAtChroma_DesaturatingLeavesTheInkWhereTheBandPutIt is the invariant
// that lets the cap be applied at all. Every contrast guarantee in this file
// is a statement about LUMINANCE, computed before the chroma bound is
// applied, so a desaturation that moved an ink even slightly out of the band
// would be a check that passed for a colour nobody draws.
//
// It holds because the mix runs in linear light toward a grey of the ink's
// own luminance, and relative luminance is a weighted sum of the linear
// channels: any convex combination of two colours with the same luminance has
// that luminance exactly. The only error left is the eight-bit rounding at
// the end, which is worth about half a channel step -- 0.0004 in luminance
// near black, where the sRGB curve is shallowest, and about 0.004 near white,
// where it is steepest. Those are the tolerances below and they are the same
// two figures the band test above derives.
//
// The target luminances are the two bands' loud ends, which is where the cap
// bites hardest: 0.018512 for the dark inks and 0.704456 for the light ones.
func TestAtChroma_DesaturatingLeavesTheInkWhereTheBandPutIt(t *testing.T) {
	for _, c := range []struct {
		name      string
		target    float64
		tolerance float64
	}{
		{"dark band", 0.018512, 0.0006},
		{"light band", 0.704456, 0.004},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, r := range mapRoles {
				ink := nearestStorable(
					atChroma(atLuminance(r.anchor, c.target), maxRoleChroma), c.target, maxRoleChroma)
				if got := chroma(ink); got > maxRoleChroma {
					t.Errorf("%s: chroma %.2f after capping at %.1f", r.name, got, maxRoleChroma)
				}
				if got := luminance(ink); math.Abs(got-c.target) > c.tolerance {
					t.Errorf("%s: the derived ink sits at luminance %.6f, want %.6f (+/- %.4f)",
						r.name, got, c.target, c.tolerance)
				}
			}
		})
	}
}

// TestChroma_IsZeroOnTheNeutralAxisAndMatchesAKnownColour checks the metric
// itself, because every judgement above is only as good as this number.
//
// A grey has a = b = 0 by definition, whatever its lightness, so chroma must
// be exactly zero for all of them -- a white point that did not match sRGB's
// own D65 would leave a residue here and quietly spend part of the cap on it.
//
// The non-neutral case is pure sRGB red, whose CIELAB coordinates are a
// published reference value: L* 53.24, a* 80.09, b* 67.20, giving a chroma of
// hypot(80.09, 67.20) = 104.55. Derived from the standard's own numbers
// rather than from what this code returns.
func TestChroma_IsZeroOnTheNeutralAxisAndMatchesAKnownColour(t *testing.T) {
	for _, v := range []uint8{0x00, 0x22, 0x7f, 0xc4, 0xff} {
		grey := color.RGBA{R: v, G: v, B: v, A: 0xff}
		// Not exactly zero: the three channels are summed with different
		// coefficients and then divided by those coefficients' own totals, so
		// the last bit of the float does not always cancel. The residue is
		// around 1e-14. A white point that did not match the matrix would
		// show up here as 1e-3 to 3e-3, three hundred million times larger,
		// so this tolerance still tells the two apart.
		if got := chroma(grey); got > 1e-9 {
			t.Errorf("chroma(#%02x%02x%02x) = %v, want ~0 for a neutral grey", v, v, v, got)
		}
	}
	if got := chroma(color.RGBA{R: 0xff, A: 0xff}); math.Abs(got-104.55) > 0.05 {
		t.Errorf("chroma(sRGB red) = %.2f, want 104.55", got)
	}
}
