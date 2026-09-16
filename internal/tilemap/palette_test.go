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
			pal, err := c.inks.localPalette()
			if err != nil {
				t.Fatalf("localPalette: %v", err)
			}
			got := luminance(pal.Road)
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
	first, err := darkInks().localPalette()
	if err != nil {
		t.Fatalf("localPalette: %v", err)
	}
	for i := range 20 {
		got, err := darkInks().localPalette()
		if err != nil {
			t.Fatalf("localPalette: %v", err)
		}
		if got != first {
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
			p, err := c.inks.localPalette()
			if err != nil {
				t.Fatalf("localPalette: %v", err)
			}
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

// TestAtChroma_TheSTOREDInkIsWhatStaysInTheBand is the invariant that lets
// the chroma cap be applied at all, measured at both stages because only one
// of them actually holds it.
//
// Every contrast guarantee in this file is a statement about LUMINANCE,
// computed before the chroma bound is applied, so a desaturation that moved
// an ink out of its band would leave a check that passed for a colour nobody
// draws.
//
// The reasoning used to be that this follows from the mix alone: it runs in
// linear light toward a grey of the ink's own luminance, relative luminance
// is a weighted sum of the linear channels, and any convex combination of two
// colours with the same luminance has that luminance exactly -- leaving only
// eight-bit rounding, worth about 0.0004 near black where the sRGB curve is
// shallowest and 0.004 near white where it is steepest.
//
// That is wrong, and the earlier version of this test could not see it was
// wrong: it measured the ink AFTER nearestStorable, which searches the
// neighbouring channel values for a better luminance and so repairs exactly
// the drift being claimed not to exist. Measured before that repair, the
// desaturation alone misses by up to 0.0055 at the light band -- above the
// 0.004 the comment attributed to rounding. The mix is exact in real
// arithmetic; what breaks it is that atLuminance and atChroma each quantise
// to eight bits, and the second one quantises a value the first already
// moved.
//
// So the property is about the composed pipeline, and it is the composed
// pipeline that produces the ink a palette stores and a contrast check
// judges. Both stages are asserted here: the loose bound on the bare
// desaturation records what it really does, and the tight bound on the stored
// ink is the one the guarantees rest on. Deleting the repair fails the
// second, which is the point of measuring them apart.
//
// The target luminances are the two bands' loud ends, where the cap bites
// hardest: 0.018512 for the dark inks and 0.704456 for the light ones.
func TestAtChroma_TheStoredInkIsWhatStaysInTheBand(t *testing.T) {
	for _, c := range []struct {
		name   string
		target float64
		// bare is what the desaturation alone manages, stored what survives
		// the search for a better neighbouring colour. Both have headroom
		// over the measured worst case; neither is the measurement itself,
		// which would make any change to the rounding a test failure.
		bare, stored float64
	}{
		{"dark band", 0.018512, 0.0008, 0.0006},
		{"light band", 0.704456, 0.008, 0.004},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, r := range mapRoles {
				bare := atChroma(atLuminance(r.anchor, c.target), maxRoleChroma)
				if got := math.Abs(luminance(bare) - c.target); got > c.bare {
					t.Errorf("%s: desaturating moved the ink %.6f from its band, further than the %.4f this stage is expected to drift",
						r.name, got, c.bare)
				}

				ink := nearestStorable(bare, c.target, maxRoleChroma)
				if got := chroma(ink); got > maxRoleChroma {
					t.Errorf("%s: chroma %.2f after capping at %.1f", r.name, got, maxRoleChroma)
				}
				if got := math.Abs(luminance(ink) - c.target); got > c.stored {
					t.Errorf("%s: the stored ink sits %.6f from luminance %.6f, outside the %.4f every contrast guarantee here assumes",
						r.name, got, c.target, c.stored)
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

// TestLocalPalette_RefusesInksThatLeaveNoBandRatherThanCollapsing is the
// failure a warning could not have covered.
//
// Both branches of band can put loud on the wrong side of quiet, and the
// consequence is not a hard-to-read map: every role is asked for a luminance
// outside [0,1], bisection has nothing to search, and all six mix fully to one
// colour. The picture is then a solid rectangle indistinguishable from a
// render that was never given a basemap -- while the summary still reports one
// as drawn, which is exactly the shape of silent failure the architecture's
// absent-data rule exists to prevent.
//
// It is one plausible theme edit away. Darkening a dark theme's Dim ink from
// #6E6E78 to #4A4A52 drives the band from 0.0061 down to -0.0092.
func TestLocalPalette_RefusesInksThatLeaveNoBandRatherThanCollapsing(t *testing.T) {
	for _, c := range []struct {
		name    string
		inks    MapInks
		binding string
	}{
		{
			name: "a dark theme whose dim ink sits too close to the background",
			inks: MapInks{
				Background: color.RGBA{R: 0x12, G: 0x12, B: 0x14, A: 0xff},
				Foreground: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
				Dim:        color.RGBA{R: 0x4a, G: 0x4a, B: 0x52, A: 0xff},
				Accent:     color.RGBA{R: 0xff, G: 0x5b, B: 0x3c, A: 0xff},
				Highlight:  color.RGBA{R: 0x9a, G: 0x7c, B: 0xf0, A: 0xff},
			},
			binding: "Dim",
		},
		{
			name: "a light theme whose brightest ink sits too close to the background",
			inks: MapInks{
				Background: color.RGBA{R: 0xf4, G: 0xf4, B: 0xf2, A: 0xff},
				Foreground: color.RGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff},
				Dim:        color.RGBA{R: 0x5e, G: 0x5e, B: 0x5e, A: 0xff},
				Accent:     color.RGBA{R: 0xb3, G: 0x24, B: 0x0f, A: 0xff},
				Highlight:  color.RGBA{R: 0xf0, G: 0xb0, B: 0x00, A: 0xff},
			},
			binding: "Highlight",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := c.inks.localPalette()
			if err == nil {
				// Say what would have shipped, so a future reader does not
				// have to rebuild the case to see why it matters.
				distinct := map[color.RGBA]bool{p.Land: true, p.Water: true, p.Green: true,
					p.Built: true, p.Road: true, p.Ink: true}
				t.Fatalf("inks with no usable band were accepted, giving %d distinct map colours", len(distinct))
			}
			if !strings.Contains(err.Error(), c.binding) {
				t.Errorf("the failure does not name %s, the ink to move:\n%v", c.binding, err)
			}
		})
	}
}

// TestMixToward_PreservesLuminanceBecauseTheMixRunsInLinearLight is the
// invariant the whole chroma cap rests on, tested where it can still be seen.
//
// atChroma is allowed to run AFTER atLuminance only because desaturating
// cannot move an ink off the luminance the band placed it at, and that is
// true for one reason: the mix runs in linear light toward a grey of the
// ink's own luminance, and relative luminance is a weighted sum of the linear
// channels, so every convex combination of two colours of equal luminance has
// that luminance exactly. Taken in sRGB instead -- the classic gamma-blend --
// the same mix dips darker on the way and quietly invalidates the placement.
//
// # Why this is not covered by the test above it
//
// TestAtChroma_DesaturatingLeavesTheInkWhereTheBandPutIt measures the colour
// AFTER nearestStorable, which searches the twenty-seven neighbours for the
// luminance it was asked for and so repairs the drift before it is measured.
// Its tolerances are the eight-bit ones, four to six times wider than what
// that repair actually delivers, and a gamma-blended mixToward passes it
// unchanged -- verified by mutation. So the invariant it names is asserted
// nowhere, and this is the assertion.
//
// # Where the bound comes from
//
// Nothing is pinned to what the code returns. The exact mix preserves
// luminance perfectly, so the only error is encodeSRGB rounding each channel
// to eight bits -- at most half a step each, which halfStepLuminance converts
// into luminance from sRGB's own transfer function at the channel values in
// question. Measured against that bound the linear mix stays under it for
// every anchor and every t, while the sRGB mix exceeds it by two to ten
// times.
func TestMixToward_PreservesLuminanceBecauseTheMixRunsInLinearLight(t *testing.T) {
	for _, r := range mapRoles {
		t.Run(r.name, func(t *testing.T) {
			y := luminance(r.anchor)
			bound := halfStepLuminance(r.anchor)
			for i := 1; i < 40; i++ {
				mix := float64(i) / 40
				got := luminance(mixToward(r.anchor, y, mix))
				if math.Abs(got-y) > bound {
					t.Errorf("mixToward(#%02x%02x%02x, its own luminance, %.3f) has luminance %.6f, want %.6f (+/- %.6f, half an eight-bit step); "+
						"a mix that moves luminance is a mix taken in the wrong space, and every band placement before it is void",
						r.anchor.R, r.anchor.G, r.anchor.B, mix, got, y, bound)
				}
			}
		})
	}
}

// halfStepLuminance is the most luminance one half-step of eight-bit rounding
// can be worth, for a colour at these channel values.
//
// Derived from sRGB's transfer function rather than from any measurement of
// this package: one step at channel v is linearSRGB(v+1)-linearSRGB(v), the
// three are weighted as WCAG weights them, and rounding costs at most half a
// step in each.
func halfStepLuminance(c color.RGBA) float64 {
	q := func(v uint8, w float64) float64 {
		if v == 0xff {
			v = 0xfe
		}
		return w * (linearSRGB(v+1) - linearSRGB(v))
	}
	return 0.5 * (q(c.R, 0.2126729) + q(c.G, 0.7151522) + q(c.B, 0.0721750))
}

// TestNearestStorable_TakesTheNeighbourNearestTheTargetAndNeverBreaksTheCap
// covers the last step of the derivation, which nothing else does: deleting
// its whole search and returning the colour handed to it passes every other
// test in this file, because their tolerances are the eight-bit ones this
// step exists to beat.
//
// It matters for the reason mapHeadroom exists. The band leaves the loudest
// ink about 6% of its width in clearance -- 0.0009 of luminance for the dark
// theme -- and the colour arriving here can sit further off than that when
// all three channels round the same way, measured at up to 0.0055 in the
// light band. A contrast guarantee decided by a rounding mode is a coin toss,
// so the rounding is chosen rather than accepted.
//
// Both halves are checked, and each is a different bug. The first case has an
// exactly reachable answer -- the target is the luminance of a colour one
// step away, so the right result has zero error and is derived from the
// fixture rather than from the code. The second gives the search a limit
// tight enough that the nearest neighbour in luminance is over it, and the
// answer must stay under the cap instead of buying accuracy with chroma.
func TestNearestStorable_TakesTheNeighbourNearestTheTargetAndNeverBreaksTheCap(t *testing.T) {
	t.Run("moves to the neighbour that hits the target exactly", func(t *testing.T) {
		// A dark, slightly warm ink of the kind the dark theme's band is made
		// of. The answer is want, by construction: target IS its luminance.
		want := color.RGBA{R: 0x29, G: 0x24, B: 0x1b, A: 0xff}
		start := color.RGBA{R: 0x28, G: 0x25, B: 0x1c, A: 0xff}
		target := luminance(want)
		if luminance(start) == target {
			t.Fatal("the fixture's two colours have the same luminance, so this could not tell the search from a no-op")
		}

		got := nearestStorable(start, target, maxRoleChroma)
		if math.Abs(luminance(got)-target) > math.Abs(luminance(start)-target) {
			t.Fatalf("nearestStorable returned #%02x%02x%02x, further from the target than the colour it was given", got.R, got.G, got.B)
		}
		if got != want {
			t.Errorf("nearestStorable = #%02x%02x%02x (luminance %.8f), want #%02x%02x%02x, which sits exactly on the target %.8f",
				got.R, got.G, got.B, luminance(got), want.R, want.G, want.B, target)
		}
	})

	t.Run("will not break the chroma cap to reach the target", func(t *testing.T) {
		// The limit is set to the starting colour's own chroma, so every
		// neighbour that is more colourful than it is forbidden -- and the
		// nearest neighbour in luminance is one of them.
		start := color.RGBA{R: 0x2e, G: 0x26, B: 0x18, A: 0xff}
		limit := chroma(start)
		// A target a long way off, so the search is pulled hard toward
		// whichever neighbour is brightest rather than staying put.
		target := luminance(color.RGBA{R: 0x2f, G: 0x27, B: 0x17, A: 0xff})

		got := nearestStorable(start, target, limit)
		if chroma(got) > limit {
			t.Errorf("nearestStorable returned #%02x%02x%02x at chroma %.4f, above the %.4f it was given: "+
				"it bought luminance accuracy with saturation, which is the one trade this step must not make",
				got.R, got.G, got.B, chroma(got), limit)
		}
	})
}

// highContrastDarkInks is a theme whose overlay inks are all bright on a
// black background -- a high-contrast dark theme, which is an ordinary thing
// for somebody to want.
//
// It exists to make band's SECOND constraint bind. The first, that every
// overlay ink clears 3:1 against every map ink, is generous here precisely
// because the inks are bright: it would let the map run up to a luminance of
// about 0.25. The second says no map ink may exceed 4.5:1 against the
// background, which on black is a luminance of 0.175, and that is what has to
// win. Neither of fitdash's own themes reaches this case.
func highContrastDarkInks() MapInks {
	return MapInks{
		Background: color.RGBA{A: 0xFF},
		Foreground: color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF},
		Dim:        color.RGBA{R: 0xE8, G: 0xE8, B: 0xEC, A: 0xFF},
		Accent:     color.RGBA{R: 0xFF, G: 0xE4, B: 0xD6, A: 0xFF},
		Highlight:  color.RGBA{R: 0xEC, G: 0xE4, B: 0xFF, A: 0xFF},
	}
}

// highContrastLightInks is the same case on the other polarity: near-black
// inks on white, where the map is pushed DOWN by the overlay constraint and
// has to be stopped before it gets loud enough to read as content.
func highContrastLightInks() MapInks {
	return MapInks{
		Background: color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF},
		Foreground: color.RGBA{A: 0xFF},
		Dim:        color.RGBA{R: 0x2A, G: 0x2A, B: 0x2E, A: 0xFF},
		Accent:     color.RGBA{R: 0x50, A: 0xFF},
		Highlight:  color.RGBA{G: 0x10, B: 0x40, A: 0xFF},
	}
}

// TestMapInks_TheMapStaysContextEvenWhenTheOverlayWouldAllowItToShout is the
// constraint that can otherwise be deleted without a single failure.
//
// band computes its far end from two rules and takes whichever is nearer the
// background: the overlay rule (no map ink within 3:1 of any ink drawn over
// it) and the background rule (no map ink above 4.5:1 against the background,
// because a map as prominent as body text is content rather than context).
// For both of fitdash's shipped themes the overlay rule binds, so deleting
// the background rule changes nothing either of them can see -- verified by
// mutation.
//
// highContrastDarkInks is the ordinary theme that binds the other way. Put
// every overlay ink far from the background and the overlay rule goes quiet,
// leaving this as the only thing stopping the map climbing until it is as
// loud as the route drawn over it.
//
// The expected figure is WCAG's, worked out here rather than read from the
// code: 4.5:1 above a black background is a luminance of 4.5*(0+0.05)-0.05 =
// 0.175, and the loudest role must land at or under it.
//
// The light polarity is included as the pair, but it proves less and the
// difference is worth recording. Its floor -- the same rule the other way up
// -- can only bind when every overlay ink is darker than about #1A1A1A on a
// near-white background, which is not a theme anybody would design, since
// Dim, Accent and Highlight would be indistinguishable from Foreground. So on
// that side the ceiling is enforced by the overlay rule in every reachable
// case, and this checks the PROPERTY holds rather than claiming to exercise
// the clamp.
func TestMapInks_TheMapStaysContextEvenWhenTheOverlayWouldAllowItToShout(t *testing.T) {
	for _, c := range []struct {
		name string
		inks MapInks
	}{
		{"high-contrast dark", highContrastDarkInks()},
		{"high-contrast light", highContrastLightInks()},
		{"fitdash dark", darkInks()},
		{"fitdash light", lightInks()},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, loud, _, ok, _ := c.inks.band()
			if !ok {
				t.Fatalf("these inks leave no band at all, so this says nothing about how loud the map may be")
			}
			// The ratio the map's loudest permitted luminance stands at
			// against its background, from WCAG's own definition rather than
			// from any helper in the package under test.
			bg := luminance(c.inks.Background)
			hi, lo := math.Max(bg, loud), math.Min(bg, loud)
			if ratio := (hi + 0.05) / (lo + 0.05); ratio > 4.5 {
				t.Errorf("the band reaches luminance %.6f, which is %.2f:1 against the background at %.6f -- above 4.5, so the map reads as content rather than as context",
					loud, ratio, bg)
			}
			if err := c.inks.CheckContrast(); err != nil {
				t.Errorf("the map derived for these inks cannot carry them:\n%v", err)
			}
		})
	}
}
