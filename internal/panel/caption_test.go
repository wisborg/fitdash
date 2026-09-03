package panel

import (
	"image"
	"math"
	"testing"
)

// TestCaptionBaseline_ElapsedAndDistanceAgree is finding 1's own regression
// test: ElapsedPanel and the Distance readout beside it must draw their
// caption ("ELAPSED", "DISTANCE") at the identical Y, in EVERY box shape the
// real layouts can hand them -- not merely the ones where both boxes happen
// to stay wider than they are tall.
//
// It cannot be a pixel-identity comparison of two renders, because both
// panels would call the same (buggy or fixed) prepare logic and a baseline
// bug would be invisible to a byte diff between them; it asserts the two
// painters' own resolved labelY fields agree instead, which is the value a
// baseline bug actually moves.
//
// A private offset reintroduced on either panel -- reverting captionOffset's
// shared capUnit to that panel's own box.W/box.H unit -- makes this fail:
// checked by hand while writing this test, not merely asserted here.
func TestCaptionBaseline_ElapsedAndDistanceAgree(t *testing.T) {
	cases := []struct {
		name   string
		layout Layout
		w, h   int
	}{
		{"landscape at 1080p", LandscapeLayout(), 1920, 1080},
		{"landscape at 4K", LandscapeLayout(), 3840, 2160},
		{"portrait at 1080x1920", PortraitLayout(), 1080, 1920},
		// The forced case finding 1 named: LandscapeLayout resolved into a
		// portrait-shaped frame -- SelectLayout's own documented escape
		// hatch for a shape auto would not have picked -- narrows
		// Distance's own share of the row (roughly a third of its width)
		// past the row's shared height, which is exactly the regime where
		// each panel's own box.W/H unit used to disagree.
		{"landscape forced onto a portrait frame", LandscapeLayout(), 1080, 1920},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, ctx := elapsedFixture(t, c.w, c.h)
			placed, err := c.layout.Resolve(c.w, c.h, nil)
			if err != nil {
				t.Fatal(err)
			}
			elapsedBox := boxOf(t, placed, "elapsed")
			// keep is nil here, so the bottom band's Alt slot keeps its
			// first candidate (the elevation profile) and the only
			// "distance" placement left is the pair leaf beside
			// ElapsedPanel -- see layout_test.go's own comment on
			// wantDistanceCount for why an Alt slot behaves this way.
			distBox := boxOf(t, placed, "distance")

			ep, ok := ElapsedPanel{}.Prepare(ctx, elapsedBox).(*elapsedPainter)
			if !ok {
				t.Fatal("ElapsedPanel.Prepare did not return an *elapsedPainter")
			}
			dp, ok := Distance().Prepare(ctx, distBox).(*readoutPainter)
			if !ok {
				t.Fatal("Distance().Prepare did not return a *readoutPainter")
			}

			if math.Abs(ep.labelY-dp.labelY) > 0.5 {
				t.Errorf("ELAPSED's caption sits at y=%g, DISTANCE's at y=%g (boxes %+v and %+v); "+
					"they must draw on the same baseline", ep.labelY, dp.labelY, elapsedBox, distBox)
			}
		})
	}
}

// solidBarFraction reports what fraction of the columns in box, sampled on
// its own vertical midline, carry ink -- 1.0 for a filled Rect (a rule), and
// much less for scattered glyph ink such as a caption's own descenders,
// which touch only the columns under the specific letters that have one.
// This is what tells a genuine rule apart from incidental label ink sharing
// the same row, which a plain "is there any ink here" count cannot.
func solidBarFraction(img *image.RGBA, box Box, th Theme) float64 {
	br, bg, bb, _ := th.Background.RGBA()
	y := int(box.Y + box.H/2)
	x0, x1 := int(box.X), int(box.X+box.W)
	if x1 <= x0 {
		return 0
	}
	n := 0
	for x := x0; x < x1; x++ {
		if x < 0 || x >= img.Bounds().Dx() || y < 0 || y >= img.Bounds().Dy() {
			continue
		}
		r, g, bl, _ := img.At(x, y).RGBA()
		if r != br || g != bg || bl != bb {
			n++
		}
	}
	return float64(n) / float64(x1-x0)
}

// TestReadout_OnlyDistanceDrawsARule proves the rule flag's two directions
// in pixels, not merely in the struct field: the four readouts that leave
// rule at its zero value must paint no RULE -- a filled bar -- in the row
// one would occupy, and Distance -- the one readout that sets it -- must
// actually paint one rather than merely carry a true field nothing reads.
//
// The row checked is computed from the SAME captionRuleGeometry the
// production code calls, using each readout's own (non-paired) capUnit, so
// the assertion is pinned to where a rule would land if one were drawn, not
// to a guessed coordinate. It cannot be "no ink at all" in that row: the
// caption's own descenders (the bottom of "R", "T", ...) legitimately reach
// close to it, by design -- a rule is meant to read as an underline. What
// distinguishes a rule from a descender is that a rule is a solid bar across
// nearly the whole width and a descender is not, which is what
// solidBarFraction measures.
func TestReadout_OnlyDistanceDrawsARule(t *testing.T) {
	box := Box{X: 40, Y: 40, W: 600, H: 300}
	const solidThreshold = 0.9 // a genuine Rect fills every sampled column

	unruled := []Readout{HeartRate(), Pace(), Power(), Cadence()}
	for _, r := range unruled {
		t.Run(r.Name(), func(t *testing.T) {
			c, img, ctx := elapsedFixture(t, 1920, 1080)
			p, ok := r.Prepare(ctx, box).(*readoutPainter)
			if !ok {
				t.Fatal("Readout.Prepare did not return a *readoutPainter")
			}
			if p.rule {
				t.Fatalf("%s.rule is true; only Distance should set it", r.Name())
			}
			c.Fill(c.Theme.Background)
			p.Static(c)

			unit := box.H
			if box.W < unit {
				unit = box.W
			}
			ry, rw, rh := captionRuleGeometry(unit, box, p.labelY)
			ruleBox := Box{X: p.centerX - rw/2, Y: ry, W: rw, H: rh}
			if frac := solidBarFraction(img, ruleBox, c.Theme); frac >= solidThreshold {
				t.Errorf("%s painted a solid bar (%.0f%% of the row) where only a rule would sit; "+
					"an unruled readout must leave that row without one", r.Name(), frac*100)
			}
		})
	}

	t.Run("distance", func(t *testing.T) {
		c, img, ctx := elapsedFixture(t, 1920, 1080)
		p, ok := Distance().Prepare(ctx, box).(*readoutPainter)
		if !ok {
			t.Fatal("Distance().Prepare did not return a *readoutPainter")
		}
		if !p.rule {
			t.Fatal("Distance().rule is false; it is the one readout that should set it")
		}
		c.Fill(c.Theme.Background)
		p.Static(c)

		ruleBox := Box{X: p.centerX - p.ruleW/2, Y: p.ruleY, W: p.ruleW, H: p.ruleH}
		if frac := solidBarFraction(img, ruleBox, c.Theme); frac < solidThreshold {
			t.Errorf("distance's rule field is true but its own resolved rule box is only %.0f%% filled; "+
				"the field is being held, not drawn as a rule", frac*100)
		}
	})
}

// TestLayouts_ElapsedDistanceRowIsWeighted2to1 pins the ratio itself, derived
// from the weights the real trees carry rather than from a pixel width
// copied out of one render -- a pixel measurement would drift the moment
// anything upstream of this row's own box (a margin, a sibling's weight)
// changed, without the ratio this row actually asks for having moved at all.
func TestLayouts_ElapsedDistanceRowIsWeighted2to1(t *testing.T) {
	for _, l := range []Layout{LandscapeLayout(), PortraitLayout()} {
		t.Run(l.Name, func(t *testing.T) {
			ew, dw, ok := findElapsedDistanceRow(l.Root)
			if !ok {
				t.Fatal("could not find the Row pairing ElapsedPanel with Distance()")
			}
			if ew != 2*dw {
				t.Errorf("elapsed weight %v, distance weight %v; want elapsed at exactly twice distance's weight", ew, dw)
			}
		})
	}
}

// findElapsedDistanceRow walks s for the Row slot whose two children are
// ElapsedPanel and Distance(), in that order -- the pairing layouts.go's own
// comment documents -- and returns their weights.
func findElapsedDistanceRow(s Slot) (elapsedWeight, distanceWeight float64, ok bool) {
	if s.isLeaf() {
		return 0, 0, false
	}
	if s.Dir == Row && len(s.Children) == 2 &&
		s.Children[0].isLeaf() && s.Children[0].Panel.Name() == "elapsed" &&
		s.Children[1].isLeaf() && s.Children[1].Panel.Name() == "distance" {
		return s.Children[0].Weight, s.Children[1].Weight, true
	}
	for _, c := range s.Children {
		if ew, dw, found := findElapsedDistanceRow(c); found {
			return ew, dw, true
		}
	}
	return 0, 0, false
}
