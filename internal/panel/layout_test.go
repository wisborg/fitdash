package panel

import (
	"math"
	"testing"
)

// testPanel is a Panel that draws nothing and accepts everything. The layout
// tests exercise placement only, and which panels are placed is decided by the
// keep predicate Resolve is given rather than by Accepts -- which is exactly
// what let the layout be built and tested before a render context existed.
type testPanel string

func (p testPanel) Name() string          { return string(p) }
func (p testPanel) Accepts(*Context) bool { return true }

func (p testPanel) Prepare(*Context, Box) Painter { return nopPainter{} }

type nopPainter struct{ NoStatic }

func (nopPainter) Dynamic(*Canvas, Frame) {}

func leaf(name string, weight float64) Slot {
	return Slot{Panel: testPanel(name), Weight: weight}
}

// boxOf finds the placement for the named panel.
func boxOf(t *testing.T, placed []Placed, name string) Box {
	t.Helper()
	for _, p := range placed {
		if p.Panel.Name() == name {
			return p.Box
		}
	}
	t.Fatalf("panel %q was not placed; got %v", name, names(placed))
	return Box{}
}

func names(placed []Placed) []string {
	out := make([]string, len(placed))
	for i, p := range placed {
		out[i] = p.Panel.Name()
	}
	return out
}

// closeTo compares pixel extents, which are computed by division and so land
// on values a float64 cannot represent exactly.
func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// assertTiles checks that the boxes are pairwise disjoint and together cover
// exactly the area they were given.
//
// This is the property that makes a rectangle the right shape for a Box at
// all: overlap is what an anchor-and-grow model permits and this one must not.
// Checking areas as well as overlap catches a gap, which overlap alone cannot
// see -- and a one-pixel seam between neighbours is a visible hairline of
// background, not a rounding curiosity.
func assertTiles(t *testing.T, placed []Placed, within Box) {
	t.Helper()
	area := assertNoOverlap(t, placed)
	if want := within.W * within.H; !closeTo(area, want) {
		t.Errorf("placed boxes cover %g of the available %g; the difference is a gap or an overlap", area, want)
	}
}

// assertNoOverlap checks the boxes are pairwise disjoint and returns the area
// they cover.
//
// Separate from assertTiles because a layout with a margin or per-slot padding
// covers LESS than its frame by design, so demanding exact coverage there
// would fail against correct output. Disjointness holds in both cases and is
// the property that makes a rectangle the right shape for a Box; exact
// coverage only means something for a layout with no gaps built into it.
func assertNoOverlap(t *testing.T, placed []Placed) float64 {
	t.Helper()
	var area float64
	for i, p := range placed {
		if p.Box.W <= 0 || p.Box.H <= 0 {
			t.Errorf("%s has a degenerate box %+v", p.Panel.Name(), p.Box)
		}
		area += p.Box.W * p.Box.H
		for j := i + 1; j < len(placed); j++ {
			q := placed[j]
			overlapW := math.Min(p.Box.X+p.Box.W, q.Box.X+q.Box.W) - math.Max(p.Box.X, q.Box.X)
			overlapH := math.Min(p.Box.Y+p.Box.H, q.Box.Y+q.Box.H) - math.Max(p.Box.Y, q.Box.Y)
			if overlapW > 1e-6 && overlapH > 1e-6 {
				t.Errorf("%s %+v overlaps %s %+v", p.Panel.Name(), p.Box, q.Panel.Name(), q.Box)
			}
		}
	}
	return area
}

// threePanelRow is the fixture most tests here use: three equal panels in a
// row, no margin and no padding, so every expected number is a clean division
// of the frame and can be worked out by hand in the assertion.
func threePanelRow() Layout {
	return Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			leaf("a", 0), leaf("b", 0), leaf("c", 0),
		}},
	}
}

// TestResolve_DividesByWeightAndTiles pins the basic division with values
// derived by hand: three equal panels across 1200 px are 400 px each.
func TestResolve_DividesByWeightAndTiles(t *testing.T) {
	placed, err := threePanelRow().Resolve(1200, 600, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 3 {
		t.Fatalf("placed %v, want three panels", names(placed))
	}
	for i, name := range []string{"a", "b", "c"} {
		b := boxOf(t, placed, name)
		if !closeTo(b.W, 400) || !closeTo(b.H, 600) {
			t.Errorf("%s box is %gx%g, want 400x600", name, b.W, b.H)
		}
		if !closeTo(b.X, float64(i)*400) || !closeTo(b.Y, 0) {
			t.Errorf("%s sits at (%g,%g), want (%g,0)", name, b.X, b.Y, float64(i)*400)
		}
	}
	assertTiles(t, placed, Box{W: 1200, H: 600})
}

// TestResolve_ClosesUpAroundADeclinedPanel is the test the whole layout model
// exists to make pass.
//
// The expectation is derived, not observed: with one of three equal panels
// pruned, the survivors' denominator drops from three to two, so each takes
// half the frame rather than a third. If closing up were a redistribution
// special case rather than a consequence of pruning before dividing, this is
// where the special case would be wrong.
func TestResolve_ClosesUpAroundADeclinedPanel(t *testing.T) {
	l := threePanelRow()
	keep := func(p Panel) bool { return p.Name() != "b" }

	placed, err := l.Resolve(1200, 600, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := names(placed); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("placed %v, want [a c]", got)
	}

	a, c := boxOf(t, placed, "a"), boxOf(t, placed, "c")
	if !closeTo(a.W, 600) {
		t.Errorf("a is %g wide, want 600 -- the survivors must grow into the gap", a.W)
	}
	if !closeTo(c.W, 600) {
		t.Errorf("c is %g wide, want 600", c.W)
	}
	// And c must have MOVED, not merely widened: a gap left where b was would
	// satisfy the widths above while leaving a hole in the middle.
	if !closeTo(c.X, 600) {
		t.Errorf("c starts at %g, want 600 -- it must move left into the gap, not just widen", c.X)
	}
	assertTiles(t, placed, Box{W: 1200, H: 600})
}

// TestResolve_ScalesWithoutPixelConstants runs one tree at three frame sizes
// and checks the boxes are the same FRACTIONS of the frame each time.
//
// This is what "layouts scale, they do not pin pixels" means operationally. A
// pixel constant that crept into the layout maths would show up here as a
// fraction that changes with the frame size.
func TestResolve_ScalesWithoutPixelConstants(t *testing.T) {
	l := Layout{
		Name:      "test",
		Margin:    0.02,
		FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			leaf("wide", 2), {Dir: Col, Children: []Slot{leaf("top", 0), leaf("bottom", 0)}},
		}},
	}
	sizes := []struct {
		name string
		w, h int
	}{
		{"1080p", 1920, 1080},
		{"4K", 3840, 2160},
		{"portrait", 1080, 1920},
	}

	type frac struct{ x, y, w, h float64 }
	got := map[string]map[string]frac{}
	for _, s := range sizes {
		placed, err := l.Resolve(s.w, s.h, nil)
		if err != nil {
			t.Fatalf("%s: Resolve: %v", s.name, err)
		}
		got[s.name] = map[string]frac{}
		for _, p := range placed {
			got[s.name][p.Panel.Name()] = frac{
				p.Box.X / float64(s.w), p.Box.Y / float64(s.h),
				p.Box.W / float64(s.w), p.Box.H / float64(s.h),
			}
		}
	}

	// 1080p and 4K are the same aspect ratio, so every fraction must match
	// exactly -- 4K is 1080p scaled by two and nothing else.
	for _, name := range []string{"wide", "top", "bottom"} {
		a, b := got["1080p"][name], got["4K"][name]
		if !closeTo(a.x, b.x) || !closeTo(a.y, b.y) || !closeTo(a.w, b.w) || !closeTo(a.h, b.h) {
			t.Errorf("%s occupies %+v of a 1080p frame but %+v of a 4K one; a pixel constant has crept in", name, a, b)
		}
	}

	// Portrait is a different aspect ratio, so the fractions legitimately
	// differ -- the margin is a fraction of the SMALLER dimension, which is
	// now the width. What must still hold is that the arrangement is intact.
	for _, s := range sizes {
		placed, err := l.Resolve(s.w, s.h, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(placed) != 3 {
			t.Fatalf("%s: placed %v, want three panels", s.name, names(placed))
		}
		wide, top, bottom := boxOf(t, placed, "wide"), boxOf(t, placed, "top"), boxOf(t, placed, "bottom")
		if !closeTo(wide.W, top.W*2) {
			t.Errorf("%s: wide is %g and top is %g; the 2:1 weight must hold at every size", s.name, wide.W, top.W)
		}
		if !closeTo(top.H, bottom.H) {
			t.Errorf("%s: top is %g tall and bottom %g; equal weights must halve the column", s.name, top.H, bottom.H)
		}
		if top.Y >= bottom.Y {
			t.Errorf("%s: top sits at y=%g and bottom at y=%g; the column is out of order", s.name, top.Y, bottom.Y)
		}
	}
}

// TestResolve_WeightsDivideProportionally derives each share by hand.
func TestResolve_WeightsDivideProportionally(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root:      Slot{Dir: Row, Children: []Slot{leaf("a", 1), leaf("b", 3)}},
	}
	placed, err := l.Resolve(800, 100, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// 1:3 across 800 px is 200 and 600.
	if b := boxOf(t, placed, "a"); !closeTo(b.W, 200) {
		t.Errorf("a is %g wide, want 200", b.W)
	}
	if b := boxOf(t, placed, "b"); !closeTo(b.W, 600) {
		t.Errorf("b is %g wide, want 600", b.W)
	}
	assertTiles(t, placed, Box{W: 800, H: 100})
}

// TestResolve_ZeroWeightMeansOne pins the default, which is what lets an
// evenly divided split be written with no weights at all.
func TestResolve_ZeroWeightMeansOne(t *testing.T) {
	explicit := Layout{Name: "t", FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{leaf("a", 1), leaf("b", 1)}}}
	implicit := Layout{Name: "t", FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{leaf("a", 0), leaf("b", 0)}}}

	e, err := explicit.Resolve(1000, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	i, err := implicit.Resolve(1000, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if boxOf(t, e, name) != boxOf(t, i, name) {
			t.Errorf("%s: an unset weight must behave as 1", name)
		}
	}
}

// TestResolve_PrunesAnEmptiedSplitEntirely covers the case where every panel
// under a split declines: the split must vanish rather than occupy a share of
// its parent's box that nothing draws in.
func TestResolve_PrunesAnEmptiedSplitEntirely(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			leaf("keep", 0),
			{Dir: Col, Children: []Slot{leaf("gone1", 0), leaf("gone2", 0)}},
		}},
	}
	keep := func(p Panel) bool { return p.Name() == "keep" }

	placed, err := l.Resolve(1000, 500, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 1 {
		t.Fatalf("placed %v, want only [keep]", names(placed))
	}
	if b := boxOf(t, placed, "keep"); !closeTo(b.W, 1000) {
		t.Errorf("keep is %g wide, want the whole 1000 -- an emptied split must not reserve space", b.W)
	}
}

// TestResolve_SingleSurvivorTakesTheWholeBox pins that "a split with one
// survivor becomes that survivor" needs no special case: one child's share of
// the weights is all of them.
func TestResolve_SingleSurvivorTakesTheWholeBox(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Col, Children: []Slot{
			leaf("a", 3), leaf("b", 1), leaf("c", 5),
		}},
	}
	keep := func(p Panel) bool { return p.Name() == "b" }
	placed, err := l.Resolve(400, 900, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 1 {
		t.Fatalf("placed %v, want [b]", names(placed))
	}
	// Its own weight of 1 is now the entire denominator, so it takes
	// everything -- the weight is relative, not absolute.
	if b := boxOf(t, placed, "b"); !closeTo(b.H, 900) || !closeTo(b.Y, 0) {
		t.Errorf("b box is %+v, want the full 400x900 at the origin", b)
	}
}

// --- Alt: the ordered-alternatives slot -------------------------------------

// TestResolve_AltTakesTheFirstSurvivorWhole pins the mechanism's basic claim:
// an Alt slot does not divide its box among its children the way a Row or Col
// would -- the first child that survives keep gets the WHOLE box, and the
// later ones are not even asked. altKeep below would place "b" if it were
// ever consulted (it is not rejected), so a regression that divided the box
// between "a" and "b", or that asked keep("b") at all, would show up as
// either a narrower "a" or a placed "b".
func TestResolve_AltTakesTheFirstSurvivorWhole(t *testing.T) {
	asked := map[string]bool{}
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			leaf("other", 0),
			{Dir: Alt, Children: []Slot{leaf("a", 0), leaf("b", 0)}},
		}},
	}
	keep := func(p Panel) bool {
		asked[p.Name()] = true
		return true
	}
	placed, err := l.Resolve(1000, 400, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := names(placed); len(got) != 2 || got[0] != "other" || got[1] != "a" {
		t.Fatalf("placed %v, want [other a] -- \"a\" is the first Alt candidate and survives, so it alone represents the slot", got)
	}
	if asked["b"] {
		t.Error("keep was asked about \"b\", the second Alt candidate; Alt must not consult a later candidate once an earlier one survives")
	}
	a := boxOf(t, placed, "a")
	if !closeTo(a.W, 500) {
		t.Errorf("\"a\" is %g wide, want 500 (half of 1000, the Alt slot's own share as the row's other equal-weight child) -- "+
			"an Alt slot must not divide its OWN box among its children", a.W)
	}
	assertTiles(t, placed, Box{W: 1000, H: 400})
}

// TestResolve_AltFallsThroughToALaterSurvivor is the fallback half of the
// same claim: when the first candidate declines, the SECOND one takes the
// slot whole, not a share of it -- this is the behaviour --bottom-band
// depends on (see layouts.go's Alt comment): rejecting the first candidate by
// name must hand the whole band to the second, not merely remove the first
// from a shared division.
func TestResolve_AltFallsThroughToALaterSurvivor(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root:      Slot{Dir: Alt, Children: []Slot{leaf("first", 0), leaf("second", 0)}},
	}
	keep := func(p Panel) bool { return p.Name() != "first" }
	placed, err := l.Resolve(800, 300, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := names(placed); len(got) != 1 || got[0] != "second" {
		t.Fatalf("placed %v, want [second]", got)
	}
	if b := boxOf(t, placed, "second"); !closeTo(b.W, 800) || !closeTo(b.H, 300) {
		t.Errorf("\"second\" box is %+v, want the whole 800x300 -- a surviving later candidate takes the slot whole, not a share of it", b)
	}
}

// TestResolve_AltWithNoSurvivorsPrunesLikeAnyOtherSplit checks an Alt slot
// where every candidate declines behaves exactly as a Row or Col in the same
// position would: it vanishes and its siblings grow into its space, rather
// than reserving a box nothing draws in.
func TestResolve_AltWithNoSurvivorsPrunesLikeAnyOtherSplit(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			leaf("keep", 0),
			{Dir: Alt, Children: []Slot{leaf("gone1", 0), leaf("gone2", 0)}},
		}},
	}
	keep := func(p Panel) bool { return p.Name() == "keep" }
	placed, err := l.Resolve(1000, 500, keep)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 1 {
		t.Fatalf("placed %v, want only [keep]", names(placed))
	}
	if b := boxOf(t, placed, "keep"); !closeTo(b.W, 1000) {
		t.Errorf("keep is %g wide, want the whole 1000 -- an Alt slot with no survivors must not reserve space", b.W)
	}
}

// TestResolve_SingleCandidateAltBehavesAsALeaf checks the degenerate case of
// one candidate: it must simply take the box, exactly as a bare leaf would,
// with no observable difference from removing the Alt wrapper entirely.
func TestResolve_SingleCandidateAltBehavesAsALeaf(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root:      Slot{Dir: Alt, Children: []Slot{leaf("only", 0)}},
	}
	placed, err := l.Resolve(640, 480, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 1 {
		t.Fatalf("placed %v, want [only]", names(placed))
	}
	if b := boxOf(t, placed, "only"); !closeTo(b.W, 640) || !closeTo(b.H, 480) {
		t.Errorf("\"only\" box is %+v, want the whole 640x480", b)
	}
}

// TestLayout_ValidateRejectsAWeightedAltChild pins the one Validate rule Alt
// adds: a weight on an Alt child does nothing (an Alt slot never divides
// space between its children) and would mislead a reader exactly as the dead
// 4:1 weights this slot replaced in layouts.go would have, so it is refused
// rather than silently ignored.
func TestLayout_ValidateRejectsAWeightedAltChild(t *testing.T) {
	l := Layout{
		Name:      "t",
		FontScale: 0.03,
		Root:      Slot{Dir: Alt, Children: []Slot{leaf("a", 2), leaf("b", 0)}},
	}
	if err := l.Validate(); err == nil {
		t.Error("Validate accepted an Alt child with a non-zero weight")
	}
	if _, err := l.Resolve(1000, 1000, nil); err == nil {
		t.Error("Resolve accepted an Alt child with a non-zero weight")
	}

	// A weight of exactly zero (the "unset" value, per weightOf) is fine: it
	// is indistinguishable from a weight nobody spelled at all.
	ok := Layout{
		Name:      "t",
		FontScale: 0.03,
		Root:      Slot{Dir: Alt, Children: []Slot{leaf("a", 0), leaf("b", 0)}},
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate rejected an Alt slot whose children carry no weight at all: %v", err)
	}
}

// TestResolve_EverythingDeclinedPlacesNothing covers the degenerate case
// without an error: an activity that carries nothing any panel can show is a
// real input, and the caller decides what to do about an empty frame.
func TestResolve_EverythingDeclinedPlacesNothing(t *testing.T) {
	placed, err := threePanelRow().Resolve(1000, 500, func(Panel) bool { return false })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 0 {
		t.Errorf("placed %v, want nothing", names(placed))
	}
}

// TestResolve_MarginAndPadAreFractionsOfTheSmallerDimension pins which
// dimension the fractions measure against, computed by hand.
//
// The smaller dimension, so a gap between two panels is the same gap however
// the frame is shaped. Measuring against the width would make every gap in a
// portrait frame a third of what it is in landscape.
func TestResolve_MarginAndPadAreFractionsOfTheSmallerDimension(t *testing.T) {
	l := Layout{
		Name:      "test",
		Margin:    0.1,
		FontScale: 0.03,
		Root:      Slot{Panel: testPanel("only")},
	}
	// min(2000, 1000) = 1000, so the margin is 100 px on every side.
	placed, err := l.Resolve(2000, 1000, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	b := boxOf(t, placed, "only")
	if !closeTo(b.X, 100) || !closeTo(b.Y, 100) || !closeTo(b.W, 1800) || !closeTo(b.H, 800) {
		t.Errorf("box is %+v, want {100 100 1800 800}", b)
	}

	// Rotated, the smaller dimension is the width, and the margin follows it.
	placed, err = l.Resolve(1000, 2000, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	b = boxOf(t, placed, "only")
	if !closeTo(b.X, 100) || !closeTo(b.W, 800) || !closeTo(b.H, 1800) {
		t.Errorf("rotated box is %+v, want {100 100 800 1800}", b)
	}
}

// TestResolve_PadInsetsEachSlot checks padding applies per slot and that the
// gap between two padded neighbours is the sum of their pads.
func TestResolve_PadInsetsEachSlot(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			{Panel: testPanel("a"), Pad: 0.01},
			{Panel: testPanel("b"), Pad: 0.01},
		}},
	}
	// min(1000,1000)=1000, so each pad is 10 px on every side.
	placed, err := l.Resolve(1000, 1000, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	a, b := boxOf(t, placed, "a"), boxOf(t, placed, "b")
	if !closeTo(a.X, 10) || !closeTo(a.W, 480) {
		t.Errorf("a is %+v, want x=10 w=480", a)
	}
	if !closeTo(b.X, 510) || !closeTo(b.W, 480) {
		t.Errorf("b is %+v, want x=510 w=480", b)
	}
	if gap := b.X - (a.X + a.W); !closeTo(gap, 20) {
		t.Errorf("the gap between a and b is %g, want 20 (both pads)", gap)
	}
}

// TestResolve_RefusesAFrameTooSmallForTheArrangement pins the failure rather
// than a silent zero-size box.
//
// A panel handed a zero-width box draws nothing, which is indistinguishable
// from a panel that crashed -- the exact confusion this project's absent-data
// rules exist to prevent, arriving here through geometry instead. Failing
// where the size was supplied is the only place the user can act on it.
func TestResolve_RefusesAFrameTooSmallForTheArrangement(t *testing.T) {
	// A margin is inset on BOTH sides, so 0.5 of the smaller dimension is
	// what consumes a frame entirely -- 0.4 leaves 20 px of a 100 px frame
	// and is perfectly resolvable.
	l := Layout{Name: "test", Margin: 0.5, FontScale: 0.03, Root: Slot{Panel: testPanel("only")}}
	if _, err := l.Resolve(100, 100, nil); err == nil {
		t.Error("Resolve accepted a margin consuming the whole frame")
	}
	if _, err := l.Resolve(1000, 1000, nil); err == nil {
		t.Error("the same margin must fail at any size; it is a fraction, not a pixel count")
	}

	// And a pad that eats a child's share.
	deep := Layout{Name: "test", FontScale: 0.03,
		Root: Slot{Dir: Row, Children: []Slot{
			{Panel: testPanel("a"), Pad: 0.3}, {Panel: testPanel("b"), Pad: 0.3},
		}}}
	if _, err := deep.Resolve(100, 100, nil); err == nil {
		t.Error("Resolve accepted pads consuming a child's whole share")
	}
}

// TestLayout_ValidateRejectsMalformedTrees keeps a half-built tree from
// resolving into something surprising.
func TestLayout_ValidateRejectsMalformedTrees(t *testing.T) {
	cases := []struct {
		name string
		l    Layout
	}{
		{"no font scale", Layout{Name: "t", Root: Slot{Panel: testPanel("a")}}},
		{"negative margin", Layout{Name: "t", FontScale: 0.03, Margin: -0.1, Root: Slot{Panel: testPanel("a")}}},
		{"negative weight", Layout{Name: "t", FontScale: 0.03,
			Root: Slot{Dir: Row, Children: []Slot{leaf("a", -1), leaf("b", 1)}}}},
		{"negative pad", Layout{Name: "t", FontScale: 0.03,
			Root: Slot{Panel: testPanel("a"), Pad: -0.1}}},
		{"a slot that is neither", Layout{Name: "t", FontScale: 0.03, Root: Slot{}}},
		{"both panel and children", Layout{Name: "t", FontScale: 0.03,
			Root: Slot{Panel: testPanel("a"), Children: []Slot{leaf("b", 1)}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.l.Validate(); err == nil {
				t.Error("Validate accepted it")
			}
			if _, err := c.l.Resolve(1000, 1000, nil); err == nil {
				t.Error("Resolve accepted it")
			}
		})
	}
}

// TestResolve_NestedSplitsTileExactly is the anti-seam test. Uneven weights at
// two depths make the boundaries land on values a float64 cannot represent
// exactly, which is where a hairline of background between two panels would
// come from if each child's extent were computed independently rather than
// from one boundary to the next.
func TestResolve_NestedSplitsTileExactly(t *testing.T) {
	l := Layout{
		Name:      "test",
		FontScale: 0.03,
		Root: Slot{Dir: Col, Children: []Slot{
			{Weight: 3, Dir: Row, Children: []Slot{leaf("a", 1), leaf("b", 2), leaf("c", 4)}},
			{Weight: 7, Dir: Row, Children: []Slot{
				leaf("d", 5),
				{Weight: 3, Dir: Col, Children: []Slot{leaf("e", 1), leaf("f", 1), leaf("g", 1)}},
			}},
		}},
	}
	placed, err := l.Resolve(1913, 1071, nil) // deliberately not round numbers
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(placed) != 7 {
		t.Fatalf("placed %v, want seven panels", names(placed))
	}
	assertTiles(t, placed, Box{W: 1913, H: 1071})
}

// --- the merged strip's decline table, over the REAL trees -----------------

// namesRejecting builds a Resolve keep predicate from the panel names it
// should reject, so a test case reads as the set of things an activity does
// NOT carry rather than as a closure.
func namesRejecting(names ...string) func(Panel) bool {
	reject := make(map[string]bool, len(names))
	for _, n := range names {
		reject[n] = true
	}
	return func(p Panel) bool { return !reject[p.Name()] }
}

// TestResolve_DeclineCombinationsOverTheRealLayoutsLeaveNoUnclaimedRectangle
// covers the bottom strip's decline combinations against the REAL
// LandscapeLayout and PortraitLayout
// trees -- never a synthetic fixture -- at three frame sizes, with a keep
// predicate that rejects by Panel.Name() the way an activity's own Accepts
// answers would.
//
// This is the cheapest possible way to cover arrangements nobody will ever
// look at: Resolve takes an arbitrary keep function, so a combination
// fittest cannot produce today -- no elevation, no distance; both are
// derived from the same synthetic track and there is no switch to omit
// either (see fittest.Options) -- is exercised at the LAYOUT level with no
// FIT fixture and no pixel drawn.
//
// The property under test is the one pruneSlot's own doc comment promises:
// a declining leaf is removed BEFORE its siblings' space is divided, so no
// combination should ever leave an empty box or a rectangle nothing claims.
// "Positive" and "no two overlap" are checked directly on the boxes Resolve
// actually returns. "Tiles its parent" is checked by reconstructing each
// leaf's PRE-PAD allocation: every leaf in both real trees carries the SAME
// Pad (0.01; see layouts.go, including both children of the bottom strip's
// Alt slot), so expanding a leaf's final box back out by that pad recovers
// exactly the rectangle its enclosing split handed it, and the reconstructed
// rectangles must then tile the margin-inset frame exactly -- checked with
// the same assertTiles helper the zero-pad synthetic fixtures above already
// use. A real gap (a box some pruning step forgot to grow into) would show up
// here as reconstructed area short of the frame; real overlap would show up
// as two reconstructed boxes overlapping by more than a shared boundary.
//
// wantDistanceCount pins WHICH of the bottom strip's two Alt candidates
// actually drew, in the case that geometry alone cannot distinguish: the
// strip's total area is identical whether the profile or the readout took
// it, so a regression that put them back in a Row beside each other --
// restoring the second distance indicator this branch removed -- would
// satisfy every geometric assertion below and still be wrong. It is a count
// rather than a bool because "distance" now names two leaves in both real
// trees: the pair leaf beside ElapsedPanel (layouts.go), placed whenever
// keep does not reject the name outright, and the bottom strip's own Alt
// candidate. "All present" must show the elevation profile and NOT the
// readout in the Alt slot (elevation is the first Alt candidate and, given a
// real track, always accepts here) -- so the count is 1, from the pair leaf
// alone. "No elevation" must show the readout there too, since the Alt slot
// falls through to it -- so the count is 2. The other cases follow the same
// rule mechanically once elevation's own presence in the keep predicate is
// known.
//
// The last case is --bottom-band distance's own coverage at this layer:
// render.New's keep filter for that flag is nothing more than rejecting
// "elevation" by name (see internal/render.New), so it is exercised here the
// same way an activity's own absence is, combined with the marker strip's
// independent decline to confirm the two compose without either leaking into
// the other's rectangle.
func TestResolve_DeclineCombinationsOverTheRealLayoutsLeaveNoUnclaimedRectangle(t *testing.T) {
	const leafPad = 0.01

	cases := []struct {
		name              string
		keep              func(Panel) bool
		wantDistanceCount int // how many "distance" panels must be among placed
	}{
		// "distance" now names TWO leaves in both real trees, not one: the
		// readout beside ElapsedPanel (layouts.go's elapsed/distance split,
		// unconditional -- it is not one of the bottom strip's Alt
		// candidates) and the readout inside the bottom strip's Alt slot
		// (conditional on elevation losing or being absent). A keep that
		// rejects "distance" by name removes BOTH, because Resolve cannot
		// tell the two apart by name any more than a real activity's own
		// Accepts could -- an activity missing the distance metric is
		// missing it everywhere it would be read, not just in one box. Every
		// count below is therefore "1 for the always-present pair leaf" plus
		// "1 more if the Alt slot's own candidate is distance", unless
		// "distance" itself is rejected, which drops straight to 0.
		{"all present", nil, 1},
		{"no GPS (indoor ride)", namesRejecting("route"), 1},
		{"no elevation, distance present", namesRejecting("elevation"), 2},
		// Elevation cannot survive without distance in reality (its own
		// Accepts requires Carries(MetricDistance)), so an activity with
		// no distance also has no elevation. Both are rejected explicitly
		// here, so neither Alt candidate survives, the pair leaf is also
		// rejected by the same name, and distance is absent everywhere --
		// exactly as it would be from a real activity with no distance.
		{"no distance", namesRejecting("distance", "elevation"), 0},
		{"no highlights and no labels", namesRejecting("markers"), 1},
		{"no GPS + no elevation + no power (rower)", namesRejecting("route", "elevation", "power"), 2},
		{"distance only, nothing else", namesRejecting(
			"route", "heart-rate", "pace", "power", "cadence", "elevation", "markers"), 2},
		// --bottom-band distance is render.New's own keep filter rejecting
		// "elevation" by name (see internal/render's elevationPanelName and
		// its keep closure in New) -- geometrically identical to "no
		// elevation, distance present" above, since Resolve cannot tell a
		// user's flag apart from an activity's own absence, and is not asked
		// to (see Context.BottomBand's doc comment for why that distinction
		// belongs to New's keep filter and not to this layer). What this
		// case adds is the marker strip's OWN independent decline (no
		// --highlight or --label) alongside it, on an activity that
		// otherwise carries everything -- proving the two prunings compose
		// with no interaction: the bottom band still swaps to distance and
		// the marker row still closes up, and neither one's rectangle
		// leaks into the other's.
		{"--bottom-band distance, and no highlights or labels either", namesRejecting("elevation", "markers"), 2},
	}

	sizes := []struct {
		name string
		w, h int
	}{
		{"1080p", 1920, 1080},
		{"4K", 3840, 2160},
		{"portrait", 1080, 1920},
	}

	for _, l := range []Layout{LandscapeLayout(), PortraitLayout()} {
		for _, s := range sizes {
			for _, c := range cases {
				t.Run(l.Name+"/"+s.name+"/"+c.name, func(t *testing.T) {
					placed, err := l.Resolve(s.w, s.h, c.keep)
					if err != nil {
						t.Fatalf("Resolve: %v", err)
					}
					if len(placed) == 0 {
						t.Fatal("nothing placed -- ElapsedPanel accepts unconditionally, so every combination here keeps at least one panel")
					}

					gotDistanceCount := 0
					for _, p := range placed {
						if p.Panel.Name() == "distance" {
							gotDistanceCount++
						}
					}
					if gotDistanceCount != c.wantDistanceCount {
						t.Errorf("distance placed %d time(s), want %d -- one is the pair leaf beside ElapsedPanel "+
							"(present whenever keep does not reject \"distance\" by name), the other is the bottom "+
							"strip's Alt slot showing the readout rather than the elevation profile", gotDistanceCount, c.wantDistanceCount)
					}

					// Positive, disjoint, exactly as drawn.
					assertNoOverlap(t, placed)

					// Undo each leaf's own pad to recover what its
					// enclosing split actually allocated it, and check
					// those allocations tile the margin-inset frame with
					// no gap and no overlap.
					unit := math.Min(float64(s.w), float64(s.h))
					expanded := make([]Placed, len(placed))
					for i, p := range placed {
						expanded[i] = Placed{Panel: p.Panel, Box: p.Box.inset(-leafPad * unit)}
					}
					frame := Box{W: float64(s.w), H: float64(s.h)}.inset(l.MarginPx(s.w, s.h))
					assertTiles(t, expanded, frame)
				})
			}
		}
	}
}
