package tilemap

import (
	"fmt"
	"image/color"
	"math"

	osm "github.com/wisborg/osmbase/render"
)

// MapInks are the colours a caller draws ON TOP of a locally rendered map,
// plus the background it draws them against.
//
// It is the whole of what this package needs from a consumer's palette, and
// it is expressed as roles rather than as features so that nothing about
// fitdash leaks in here: Foreground is "the line the viewer follows", not
// "the covered part of the route". The map's own colours are then derived
// from these, which is the entire argument for drawing a basemap locally --
// the separation between the map and what is drawn over it can be
// GUARANTEED, by construction and by a check, rather than corrected
// afterwards with the wash --basemap-dim applies to third-party imagery.
type MapInks struct {
	// Background is what the frame is painted with, and what the map's own
	// unknown ground is painted with too. See localPalette.
	Background color.Color
	// Foreground is the brightest thing drawn over the map.
	Foreground color.Color
	// Dim is the foreground's quieter form, and it is almost always the
	// binding constraint: a dim ink is mid-luminance by construction, so it
	// is the overlay colour with the least room between itself and the map
	// underneath it.
	Dim color.Color
	// Accent is the single most important mark, drawn small.
	Accent color.Color
	// Highlight marks a stretch of the foreground as special.
	Highlight color.Color

	// Linework drops the landuse and building fills from the derived map,
	// leaving the lines -- roads, rail and boundaries -- with water as the
	// only filled feature.
	//
	// It selects which roles are drawn and not what they are drawn in. The
	// inks are still derived from the four colours above and still checked
	// against them, so this cannot produce a map the overlay disappears into;
	// it can only produce one with less on it.
	Linework bool
}

// lineworkOmits are the roles a linework map leaves out.
//
// Land, landcover and the built-up surface are the fills. Buildings go with
// them, since the buildings layer draws in the same role -- which is right:
// at the zooms an activity is rendered at, building footprints are the
// densest fill on the map and the least informative about a route.
//
// Water is NOT here. It is the strongest orientation cue after the roads, it
// is rarely dense enough to be noisy, and a linework map that drops it turns
// a coastal or riverside route into an unplaceable squiggle. Ink is not here
// either: it carries the railways as well as the boundaries, and a railway is
// linework by any reading of the word.
var lineworkOmits = osm.Roles(osm.RoleLand, osm.RoleGreen, osm.RoleBuilt)

// mapRole is one ink of the derived map: the hue it is drawn in, and how
// loud it is allowed to be relative to the rest of the map.
//
// A slice with a setter rather than a map, because a render has to be
// reproducible and map iteration order is the classic way it stops being --
// here it would decide nothing by itself, but a palette assembled in a
// different order every run is one hash away from being a difference in the
// pixels.
type mapRole struct {
	name string

	// anchor is the role's hue at full strength. It is never drawn: what is
	// drawn is the anchor mixed toward black or toward white until it lands
	// on the luminance the band allows, and then desaturated until its chroma
	// is within maxRoleChroma. The anchor's job is therefore to name a HUE and
	// to carry enough chroma to still have some after the first of those two
	// steps -- not to set how colourful the result is, which the cap decides.
	// See localPalette.
	anchor color.RGBA

	// prominence is how far from the background this role sits, 0 at the
	// background and 1 at the far end of the permitted band. It is a
	// direction-free number on purpose: a road is the loudest ink on a dark
	// map by being the lightest and on a light map by being the darkest, and
	// one prominence expresses both.
	prominence float64

	set func(*osm.Palette, color.RGBA)
}

// mapRoles are the map's inks, quietest first.
//
// The hues are the conventional ones -- blue water, green parks, warm ground
// and built-up land, purple administrative boundaries -- because a basemap
// that has to be learned is a basemap nobody reads. The anchors are
// deliberately more saturated than any map would be drawn in: mixing toward
// black or white to reach the permitted luminance washes chroma out, and on
// the light side there is very little chroma to be had at all, so what the
// anchor starts with is what the role has left to be told apart by.
//
// What the anchors are NOT is a statement of how colourful the map ends up.
// That is maxRoleChroma's job, applied after the luminance placement, and the
// division matters: an anchor is free to be vivid precisely because something
// downstream bounds the result.
var mapRoles = []mapRole{
	{"land", color.RGBA{R: 0xc8, G: 0xa0, B: 0x3c, A: 0xff}, 0.45, func(p *osm.Palette, c color.RGBA) { p.Land = c }},
	{"green", color.RGBA{R: 0x2f, G: 0xa8, B: 0x2a, A: 0xff}, 0.60, func(p *osm.Palette, c color.RGBA) { p.Green = c }},
	{"water", color.RGBA{R: 0x1f, G: 0x6a, B: 0xe8, A: 0xff}, 0.70, func(p *osm.Palette, c color.RGBA) { p.Water = c }},
	{"built", color.RGBA{R: 0xe0, G: 0x73, B: 0x26, A: 0xff}, 0.80, func(p *osm.Palette, c color.RGBA) { p.Built = c }},
	{"ink", color.RGBA{R: 0x8a, G: 0x3c, B: 0xe0, A: 0xff}, 0.90, func(p *osm.Palette, c color.RGBA) { p.Ink = c }},
	{"road", color.RGBA{R: 0xb4, G: 0xb8, B: 0xc4, A: 0xff}, 1.00, func(p *osm.Palette, c color.RGBA) { p.Road = c }},
}

// maxRoleChroma is how colourful a derived map ink is allowed to be, in
// CIELAB C*ab.
//
// # Why a bound on chroma exists at all
//
// Nothing else in this file constrains it, and that is a hole rather than an
// omission: osmbase's CheckContrast bounds luminance separation (a ratio,
// which is a function of luminance alone and cannot see hue) and role
// separation (a perceptual distance, which is a FLOOR -- more chroma only
// helps it). A saturated ink at low lightness therefore satisfies every
// constraint in the check while shouting, and the first render of a real
// activity through this derivation came out in bright green and orange-brown
// where it should have been quiet context. The check passed. The picture was
// wrong.
//
// # Where the number comes from
//
// Measured off osmbase's own two built-in palettes, which are the reference
// for what reads as a map rather than as a diagram. Their six map roles are
// at C*ab 4.0, 5.6, 5.9, 10.6, 17.4 and 20.2 (dark) and 4.7, 13.0, 17.6,
// 21.6, 21.7 and 28.4 (light). Fifteen sits at the top of the dark palette's
// cluster -- four of its six roles are below 11 -- and it is a cap rather
// than a target, so a role whose hue has less chroma than this available at
// its assigned luminance simply keeps what it has.
//
// It binds hardest exactly where the problem was. Before it, the inks derived
// from fitdash's dark theme measured 10.2 (land), 20.7 (green), 26.2 (water),
// 19.8 (built) and 39.5 (boundary ink) -- up to three and a half times
// osmbase's own figure for the same role, in a band four times darker, where
// the same chroma reads louder still.
//
// # Why it is not tuned per role
//
// A per-role cap would be six numbers nobody could justify against each
// other, and the roles are already separated by hue and by prominence. One
// ceiling is a rule; six are a palette, and a palette is what this file
// exists to avoid hand-writing.
const maxRoleChroma = 15.0

// chromaHeadroom is how far under maxRoleChroma the desaturation aims, so
// that the eight-bit rounding afterwards has room to correct the luminance
// instead of the cap and the rounding fighting over the last step. Its
// counterpart for the luminance band is mapHeadroom, and the reasoning is the
// same one: a margin of a few per cent is far less than the quantity is worth
// arguing about, and is the difference between a guarantee and a coin toss.
const chromaHeadroom = 0.94

// mapHeadroom keeps the loudest ink off the threshold it is allowed up to.
//
// The band below is computed from WCAG ratios and the ink is then rounded to
// eight bits per channel, so a role placed exactly at the limit lands a
// hundredth either side of it depending on which way that rounding went --
// which is a contrast check that passes or fails on a rounding mode. Six per
// cent of the band is far less than the difference is worth arguing about
// and is the difference between a guarantee and a coin toss.
const mapHeadroom = 0.94

// noDataDark and noDataLight are the hatch drawn where no tile covers the
// view.
//
// The one colour here that is meant to be NOTICED, so it is not derived from
// the band at all: everything else is held below a ceiling against the
// background and this is held above a floor. It is also the only ink exempt
// from the role-separation rule, because the hatch is not a map role -- a gap
// that looked like parkland would be the exact lie it exists to prevent.
var (
	noDataDark  = color.RGBA{R: 0xb4, G: 0x56, B: 0x4a, A: 0xff}
	noDataLight = color.RGBA{R: 0x8f, G: 0x2f, B: 0x20, A: 0xff}
)

// overlay is what the consumer draws, in the terms osmbase's check speaks in.
func (i MapInks) overlay() osm.Overlay {
	return osm.Overlay{
		Foreground: rgba(i.Foreground),
		Accent:     rgba(i.Accent),
		Highlight:  rgba(i.Highlight),
		Dim:        rgba(i.Dim),
	}
}

// localPalette derives the map's own colours from the inks that will be drawn
// over it.
//
// # Why the map cannot simply use osmbase's built-in palettes
//
// They are tuned against osmbase's own reference overlays, and fitdash's
// themes are not those. Measured: fitdash's dark theme fails osmbase's check
// against DarkPalette on four pairs (its Dim ink reads 2.12:1 against the
// map's boundary ink, against a 3.0 minimum) and the light theme fails on
// four more. Nor does washing a built-in toward the theme's background fix
// it: that buys overlay separation and spends role separation, and there is
// no weight at which both hold.
//
// # What is derived
//
// The BAND, which is where the algebra actually lives. Every overlay ink has
// to clear 3:1 against every map ink, and a ratio is a function of luminance
// alone, so each overlay ink at luminance Lo forbids the map the interval
// between (Lo+0.05)/3-0.05 and 3*(Lo+0.05)-0.05. On a dark background the map
// takes the space below all of them, which the DIMMEST overlay ink caps; on a
// light background it takes the space above, which the BRIGHTEST one floors.
// The far end is then pulled in again by the other constraint, that no map
// ink may exceed 4.5:1 against the background, because a map as prominent as
// body text is content rather than context.
//
// The band that leaves is narrow -- for fitdash's dark theme it is a
// luminance of 0.004 to 0.019, which is a lightness of L* 4 to 15 -- and that
// is why the roles carry hues rather than greys. Inside a band that thin,
// lightness can separate two colours and cannot separate seven.
//
// # Why the background is the consumer's own
//
// osmbase's light palette paints its background the colour of water, because
// in this tile schema the sea is what shows where no land polygon was drawn.
// This paints it the colour of the FRAME instead, so that the map fades into
// the dashboard at its edges and where it knows nothing -- which is also
// exactly what a failed fetch leaves, so the two degrade to the same picture
// rather than to two different ones. The cost is that open sea reads as
// background rather than as water, which is the honest reading of a tile
// schema in which open sea is the absence of land.
func (i MapInks) localPalette() (osm.Palette, error) {
	quiet, loud, light, ok, binding := i.band()
	if !ok {
		return osm.Palette{}, fmt.Errorf(
			"these theme colours leave no room for a map between the background and the inks drawn over it: "+
				"the background sits at luminance %.4f and %s, the binding ink, allows the map no further "+
				"than %.4f, which is the wrong side of it. Moving %s further from the background is the fix; "+
				"it is the overlay colour with the least room between itself and the map beneath it",
			quiet, binding, loud, binding)
	}
	p := osm.Palette{Background: rgba(i.Background), NoData: noDataDark}
	if i.Linework {
		// Declared rather than achieved by colour. osmbase refuses a palette
		// whose roles have collapsed into the background, because that is
		// what a derivation gone wrong looks like -- and a hidden role and a
		// collapsed one are the same colour. Saying which roles are left out
		// is what separates them; see osmbase's Palette.Omitted.
		p.Omitted = lineworkOmits
	}
	if light {
		// The map lies BELOW the overlay inks, and so does the hatch. Taken
		// from the branch band() actually used rather than re-derived from
		// quiet > loud, which is also true of a collapsed dark band.
		p.NoData = noDataLight
	}
	p.Label = i.labelInk(quiet, light)
	for _, r := range mapRoles {
		// Three steps, in this order. Luminance first, because that is what
		// every contrast guarantee in this file is written in; then chroma,
		// which mixes in linear light toward a grey of the same luminance and
		// so cannot undo the placement, where running the two the other way
		// round would; then the rounding, chosen against the same target
		// rather than accepted, since both earlier steps store their result
		// in eight bits per channel and at the dark end the achievable
		// luminances are sparse enough for that to cost more than the band's
		// headroom. NoData is deliberately not in this loop -- see
		// noDataDark.
		target := quiet + r.prominence*(loud-quiet)
		ink := atChroma(atLuminance(r.anchor, target), maxRoleChroma)
		r.set(&p, nearestStorable(ink, target, maxRoleChroma))
	}
	return p, nil
}

// labelRatio is how far a map label stands from the background, as a contrast
// ratio.
//
// osmbase's MinLabelRatio is 4.5, the floor below which small text stops
// being readable. This aims above it rather than at it, for two reasons that
// pull the same way. The ratio is computed against the background the map is
// PAINTED on, while a label is often read against a road or a water body that
// is already a little brighter, so the effective contrast at the glyph is
// lower than the number says. And the derivation lands on eight-bit channels,
// so a target sitting exactly on a threshold can round to the wrong side of
// it.
//
// It is not pushed higher than this. A label brighter than the route drawn
// over it would turn the map's names into the loudest thing in the frame,
// which is the failure the whole contrast apparatus exists to prevent -- just
// with text instead of a landuse fill.
const labelRatio = 6.0

// labelInk is the colour map labels are drawn in.
//
// Derived apart from the six roles in mapRoles, and that separation is the
// point rather than an inconvenience. Those are placed inside a band whose
// loud end is capped by the dimmest overlay ink, because they are areas and
// lines that must stay behind what is drawn over them. A label is text: it
// has to be read, it is held to a FLOOR rather than a ceiling, and that floor
// sits above the band's ceiling. Deriving it through the band would place it
// among the fills and make it unreadable by construction.
//
// The hue comes from the theme's Dim ink, which is the dashboard's own colour
// for "present, but not what the eye should land on" -- the same thing a
// place name is. Only the luminance is replaced.
func (i MapInks) labelInk(quiet float64, light bool) color.RGBA {
	// Solved from the WCAG ratio rather than searched for: with the
	// background's luminance known, the luminance that sits at a given ratio
	// from it is one rearrangement away. The polarity comes from the caller
	// rather than being re-derived here, for the same reason band returns it
	// -- see band's own comment on what a collapsed dark band does to a test
	// for "is this a light theme".
	target := labelRatio*(quiet+0.05) - 0.05
	if light {
		target = (quiet+0.05)/labelRatio - 0.05
	}
	target = math.Min(math.Max(target, 0), 1)
	ink := atChroma(atLuminance(rgba(i.Dim), target), maxRoleChroma)
	return nearestStorable(ink, target, maxRoleChroma)
}

// band is the luminance interval the map's inks may occupy, from the end
// nearest the background to the end furthest from it.
//
// The second value may be BELOW the first -- that is the light case, where
// the map is darker than its background -- and every caller treats the pair
// as an interpolation rather than as an ordered range for that reason.
// The second return says whether the band is usable at all, and it is not a
// formality.
//
// Both branches can put loud on the wrong side of quiet. Making a dark theme's
// Dim ink a little dimmer -- #6E6E78 to #4A4A52, one plausible edit -- drives
// (dimmest+0.05)/3-0.05 negative, so every role is asked for a luminance below
// black, bisection has nothing to search, and all six mix fully to #000000.
// The map is then a solid rectangle indistinguishable from no basemap at all,
// while the summary still says it was drawn. Reproduced: with that one ink
// changed, the band runs 0.0061 down to -0.0092 and the palette collapses to
// one colour.
//
// Returning ok from the branch that computed it, rather than letting a caller
// re-derive the polarity from the interval's own ordering, is also what stops
// the second bug this caused: localPalette picked its hatch from quiet > loud,
// which is true for BOTH a light theme and a collapsed dark one, so a dark
// theme with an inverted band got the light hatch as well.
func (i MapInks) band() (quiet, loud float64, light, ok bool, binding string) {
	dimmest, brightest := 1.0, 0.0
	dimmestName, brightestName := "", ""
	for _, c := range []struct {
		name string
		c    color.Color
	}{{"Foreground", i.Foreground}, {"Dim", i.Dim}, {"Accent", i.Accent}, {"Highlight", i.Highlight}} {
		l := luminance(c.c)
		if l < dimmest {
			dimmest, dimmestName = l, c.name
		}
		if l > brightest {
			brightest, brightestName = l, c.name
		}
	}
	quiet = luminance(i.Background)
	if quiet < dimmest {
		// A dark background: the map lies ABOVE it and below the overlay inks.
		loud = (dimmest+0.05)/3 - 0.05
		if ceiling := 4.5*(quiet+0.05) - 0.05; ceiling < loud {
			loud = ceiling
		}
		ok, binding = loud > quiet, dimmestName
	} else {
		// A light background: the map lies BELOW it and above the overlay inks.
		light = true
		loud = 3*(brightest+0.05) - 0.05
		if floor := (quiet+0.05)/4.5 - 0.05; floor > loud {
			loud = floor
		}
		ok, binding = loud < quiet, brightestName
	}
	return quiet, quiet + mapHeadroom*(loud-quiet), light, ok, binding
}

// CheckContrast reports whether the map derived from these inks can carry
// them.
//
// This is the guarantee that replaces --basemap-dim rather than a lint. A
// basemap drawn in the consumer's own colours is only allowed to skip the
// wash if something enforces the separation the wash was compensating for,
// and this is that something. Every failing pair is named, because a palette
// fails in patterns -- every map ink against one overlay colour, usually --
// and being told one at a time turns reading the report into guesswork.
func (i MapInks) CheckContrast() error {
	p, err := i.localPalette()
	if err != nil {
		return fmt.Errorf("tilemap: %w", err)
	}
	if err := p.CheckContrast(i.overlay()); err != nil {
		return fmt.Errorf("tilemap: the map these colours derive cannot carry them: %w", err)
	}
	return nil
}

// atLuminance mixes an anchor toward black or toward white until it lands on
// a target luminance.
//
// Bisection rather than algebra because the target is a luminance and the
// mixing happens in sRGB, where the relationship between the two is a gamma
// curve applied per channel and then weighted -- invertible in principle, and
// not in any form worth reading. Both mixes are monotone in luminance, which
// is all bisection needs, and twenty-four halvings settle far finer than the
// eight bits the result is rounded to.
func atLuminance(anchor color.RGBA, target float64) color.RGBA {
	up := target > luminance(anchor)
	end := color.RGBA{A: 0xff}
	if up {
		end = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	}
	lo, hi := 0.0, 1.0
	var out color.RGBA
	for range 24 {
		m := (lo + hi) / 2
		out = mixRGBA(anchor, end, m)
		if (luminance(out) > target) == up {
			hi = m
		} else {
			lo = m
		}
	}
	return out
}

// mixRGBA blends a toward b by t, in sRGB.
func mixRGBA(a, b color.RGBA, t float64) color.RGBA {
	ch := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.RGBA{R: ch(a.R, b.R), G: ch(a.G, b.G), B: ch(a.B, b.B), A: 0xff}
}

// atChroma desaturates c until its CIELAB chroma is at most limit, and
// returns it unchanged when it already is.
//
// # Why the mixing happens in linear light
//
// The colour is mixed toward the grey of its OWN luminance, in linear light
// rather than in sRGB, and both halves of that are load-bearing. Relative
// luminance is a weighted sum of the linear channels, so any blend of two
// colours of equal luminance, taken linearly, has that same luminance
// exactly -- which means desaturating cannot move an ink out of the band
// atLuminance placed it in, and every WCAG ratio the band was computed from
// still holds. The same mix taken in sRGB would darken the colour on the way
// (the classic gamma-blend artefact) and quietly invalidate the placement.
//
// # Why bisection, and why the rounded colour is what gets measured
//
// Chroma falls monotonically as the mix runs toward the grey: the neutral
// point has a = b = 0 and each of a and b shrinks toward it without turning
// round, so a halving search is enough and needs no inverse of the CIELAB
// transform. The candidate measured at each step is the eight-bit colour, not
// the exact one, so the bound holds for the colour that is actually stored
// rather than for one a rounding step later.
func atChroma(c color.RGBA, limit float64) color.RGBA {
	if chroma(c) <= limit {
		return c
	}
	target := luminance(c)
	// Aimed a little under the cap rather than at it, because the caller
	// rounds afterwards: a mix sitting exactly on the boundary leaves
	// nearestStorable nothing to move to, since every neighbour closer in
	// luminance is also less grey and therefore over the line. The slack is
	// spent on luminance accuracy, which is the quantity a contrast
	// guarantee is written in; chroma is a ceiling, and sitting a little
	// under a ceiling costs nothing.
	aim := limit * chromaHeadroom
	lo, hi := 0.0, 1.0
	for range 24 {
		m := (lo + hi) / 2
		if chroma(mixToward(c, target, m)) > aim {
			lo = m
		} else {
			hi = m
		}
	}
	// hi, not lo: the loop only ever moves hi to a mix that MET the bound,
	// and hi = 1 is the grey itself, so the returned colour always does.
	return mixToward(c, target, hi)
}

// nearestStorable picks, from an eight-bit colour and its immediate
// neighbours, the one whose luminance sits closest to target while keeping
// chroma within limit.
//
// It is the last step of deriving an ink and it exists because eight bits per
// channel is coarse where this file works. Down in a dark theme's band a
// single channel step is worth about 0.0006 of luminance, so the colour
// atLuminance and atChroma arrive at can sit half a step -- more, when the
// three channels round the same way -- from the luminance it was asked for:
// measured at 0.0005 for the derived green, against the 0.0009 of clearance
// mapHeadroom provides. That constant exists because a contrast check decided
// by a rounding mode is a coin toss rather than a guarantee, and letting the
// rounding eat its margin would put the coin back on the table. So the
// rounding is CHOSEN rather than accepted, and a neighbour that would buy
// luminance accuracy by breaking the chroma cap is not taken.
//
// Twenty-seven candidates, visited in a fixed order and compared with a
// strict improvement, so the same input always yields the same colour; a
// render that changed palette between runs would be untestable.
func nearestStorable(out color.RGBA, target, limit float64) color.RGBA {
	best, bestErr := out, math.Abs(luminance(out)-target)
	for dr := -1; dr <= 1; dr++ {
		for dg := -1; dg <= 1; dg++ {
			for db := -1; db <= 1; db++ {
				cand := color.RGBA{
					R: step(out.R, dr), G: step(out.G, dg), B: step(out.B, db), A: 0xff,
				}
				if chroma(cand) > limit {
					continue
				}
				if e := math.Abs(luminance(cand) - target); e < bestErr {
					best, bestErr = cand, e
				}
			}
		}
	}
	return best
}

// step moves a channel by one, saturating rather than wrapping at either end.
func step(v uint8, d int) uint8 {
	switch {
	case d < 0 && v == 0:
		return 0
	case d > 0 && v == 0xff:
		return 0xff
	}
	return uint8(int(v) + d)
}

// mixToward blends c by t toward the neutral grey of relative luminance y,
// in linear light.
func mixToward(c color.RGBA, y, t float64) color.RGBA {
	ch := func(v uint8) uint8 {
		return encodeSRGB(linearSRGB(v)*(1-t) + y*t)
	}
	return color.RGBA{R: ch(c.R), G: ch(c.G), B: ch(c.B), A: 0xff}
}

// chroma is CIE 1976 C*ab: how far a colour lies from the neutral axis, in
// the space L* is measured in.
//
// C*ab rather than a saturation ratio or an HSL figure because it is the
// number the map's own reference palettes were measured in -- see
// maxRoleChroma -- and because it is roughly perceptually uniform, so one
// ceiling means about the same thing to blue as it does to orange. HSL
// saturation would not: a dark, vivid blue and a dark, muddy brown can share
// an HSL saturation and look nothing alike.
//
// The white point is D65, matching sRGB's own, so a neutral grey comes out at
// exactly zero and the cap is never spent on a rounding artefact.
func chroma(c color.RGBA) float64 {
	r, g, b := linearSRGB(c.R), linearSRGB(c.G), linearSRGB(c.B)
	x := 0.4124564*r + 0.3575761*g + 0.1804375*b
	y := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z := 0.0193339*r + 0.1191920*g + 0.9503041*b
	fx, fy, fz := labF(x/whiteX), labF(y/whiteY), labF(z/whiteZ)
	return math.Hypot(500*(fx-fy), 200*(fy-fz))
}

// whiteX and whiteZ are the white point the conversion above normalises by,
// and they are the row sums of the matrix immediately above it rather than
// D65's published chromaticity.
//
// That is the difference between a grey coming out at exactly zero chroma and
// coming out near it. The matrix and the white point have to be the same
// white or every neutral colour carries a small false chroma -- measured at
// 0.0009 to 0.0034 across the grey ramp with D65's tabulated values, which is
// a residue this file would then spend part of maxRoleChroma on for no
// reason.
const (
	whiteX = 0.4124564 + 0.3575761 + 0.1804375
	whiteY = 0.2126729 + 0.7151522 + 0.0721750
	whiteZ = 0.0193339 + 0.1191920 + 0.9503041
)

// labF is CIELAB's compressing nonlinearity, linear near zero so that the
// transform stays well-behaved for the very dark inks a dark theme's band is
// made of.
func labF(t float64) float64 {
	const epsilon = 216.0 / 24389.0 // (6/29)^3
	const kappa = 24389.0 / 27.0    // (29/3)^3
	if t > epsilon {
		return math.Cbrt(t)
	}
	return (kappa*t + 16) / 116
}

// linearSRGB and encodeSRGB are sRGB's transfer function and its inverse.
//
// They are here for the CIELAB conversion above, which needs linear light and
// cannot get it from osmbase's contrast ratio the way luminance() does -- a
// ratio collapses three channels into one number and chroma is precisely what
// that number throws away. They are NOT a second route to luminance: nothing
// in this file weights these into a Y, so there is still exactly one answer
// to "how bright is this", and it is the one the check will judge by.
func linearSRGB(v uint8) float64 {
	c := float64(v) / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func encodeSRGB(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 0xff
	}
	s := 12.92 * v
	if v > 0.0031308 {
		s = 1.055*math.Pow(v, 1/2.4) - 0.055
	}
	return uint8(s*255 + 0.5)
}

// luminance is WCAG 2's relative luminance, obtained by inverting osmbase's
// own contrast ratio against black.
//
// Deliberately not a third implementation of the formula. fitdash already
// carries one in internal/panel and osmbase carries one behind the exported
// ratio; the number this file reasons with has to be the number the check
// will judge it by, and reading it back out of the check's own function makes
// that true by construction rather than by two files agreeing today.
func luminance(c color.Color) float64 {
	return 0.05*osm.ContrastRatio(c, color.RGBA{A: 0xff}) - 0.05
}

// rgba converts through the NON-premultiplied model, which is the conversion
// WCAG's formula is defined over: color.RGBAModel would multiply a
// translucent colour's channels by its alpha and darken it, and a theme
// colour with any transparency would then be judged as a different colour
// from the one that gets drawn.
func rgba(c color.Color) color.RGBA {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return color.RGBA{R: n.R, G: n.G, B: n.B, A: 0xff}
}
