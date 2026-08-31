package panel

import (
	"bytes"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// squareTrack builds a track tracing a square loop at one fix per second,
// starting at base. A closed loop is the useful shape here: it has extent in
// both axes, so a projection that dropped one would be obvious.
func squareTrack(base time.Time, n int) *fitactivity.Track {
	const lat0, lon0, side = 55.0, 12.0, 0.01
	samples := make([]fitactivity.Sample, n)
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n) * 4 // 0..4 around the square
		var dLat, dLon float64
		switch {
		case f < 1:
			dLon = f * side
		case f < 2:
			dLon, dLat = side, (f-1)*side
		case f < 3:
			dLon, dLat = (3-f)*side, side
		default:
			dLat = (4 - f) * side
		}
		samples[i] = fitactivity.Sample{
			Time:   base.Add(time.Duration(i) * time.Second),
			HasGPS: true, Lat: lat0 + dLat, Lon: lon0 + dLon,
		}
	}
	return &fitactivity.Track{Samples: samples}
}

// squareTrackWithLateGPS is squareTrack with the first lockAfter samples'
// GPS fix blanked out, simulating a watch whose timeline starts before its
// GPS has locked on -- the ordinary case TestRoutePanel_BeforeTheFirstFixDrawsNoDot
// already covers for the dot, and the one F4's own regression test below
// needs for a highlight's route mark.
func squareTrackWithLateGPS(base time.Time, n, lockAfter int) *fitactivity.Track {
	track := squareTrack(base, n)
	for i := 0; i < lockAfter && i < len(track.Samples); i++ {
		track.Samples[i].HasGPS = false
		track.Samples[i].Lat, track.Samples[i].Lon = 0, 0
	}
	return track
}

func routeContext(t *testing.T, track *fitactivity.Track, w, h int) *Context {
	t.Helper()
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	return &Context{
		Track: track, Report: inspect.Build(track),
		Width: w, Height: h, FontScale: 0.05, Fonts: faces,
	}
}

// countNear counts pixels close to want, which is how "did the bright line
// grow" is asked of a rendered frame. Anti-aliasing means an exact match would
// find only the interiors of strokes.
func countNear(img *image.RGBA, want color.Color, tol int) int {
	wr, wg, wb, _ := want.RGBA()
	n := 0
	b := img.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if abs(int(r>>8)-int(wr>>8)) <= tol &&
				abs(int(g>>8)-int(wg>>8)) <= tol &&
				abs(int(bl>>8)-int(wb>>8)) <= tol {
				n++
			}
		}
	}
	return n
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// countNearInBox is countNear restricted to a sub-region of the image, which
// is how "did the mark land on its OWN arc and not the opposite one" is asked
// without hand-rolling a second pixel scan.
func countNearInBox(img *image.RGBA, b Box, want color.Color, tol int) int {
	wr, wg, wb, _ := want.RGBA()
	n := 0
	x0, y0 := int(b.X), int(b.Y)
	x1, y1 := int(b.X+b.W), int(b.Y+b.H)
	bounds := img.Bounds()
	for y := y0; y < y1 && y < bounds.Dy(); y++ {
		if y < 0 {
			continue
		}
		for x := x0; x < x1 && x < bounds.Dx(); x++ {
			if x < 0 {
				continue
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			if abs(int(r>>8)-int(wr>>8)) <= tol &&
				abs(int(g>>8)-int(wg>>8)) <= tol &&
				abs(int(bl>>8)-int(wb>>8)) <= tol {
				n++
			}
		}
	}
	return n
}

// TestRoutePanel_CoveredPortionGrowsWithTime is the assertion that a route
// panel is actually animating rather than drawing the whole outline bright
// from the first frame.
//
// It cannot be eyeballed: at a few hundred pixels across, a thin dim stroke
// and a thicker bright one look much the same in a screenshot, which is
// exactly why this is counted rather than looked at. Bright pixels must
// INCREASE monotonically as the activity progresses.
func TestRoutePanel_CoveredPortionGrowsWithTime(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	track := squareTrack(base, 400)
	ctx := routeContext(t, track, 600, 600)

	img := image.NewRGBA(image.Rect(0, 0, 600, 600))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := RoutePanel{}.Prepare(ctx, Box{X: 0, Y: 0, W: 600, H: 600})

	bright := func(offset time.Duration) int {
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{At: base.Add(offset)})
		return countNear(img, c.Theme.Foreground, 12)
	}

	quarters := []int{
		bright(1 * time.Second),
		bright(100 * time.Second),
		bright(200 * time.Second),
		bright(300 * time.Second),
		bright(399 * time.Second),
	}
	for i := 1; i < len(quarters); i++ {
		if quarters[i] <= quarters[i-1] {
			t.Errorf("covered pixels went %d -> %d between quarter %d and %d; the route is not filling in",
				quarters[i-1], quarters[i], i-1, i)
		}
	}
	// And the ends must differ substantially, not by a rounding of the dot.
	if quarters[len(quarters)-1] < 4*quarters[0] {
		t.Errorf("the finished route has %d bright pixels against %d at the start; that is not a route filling in",
			quarters[len(quarters)-1], quarters[0])
	}
}

// TestRoutePanel_StaticDrawsTheWholeOutline pins that the dim outline is
// present from frame one -- it is what tells a viewer where the route GOES,
// as opposed to where it has been.
func TestRoutePanel_StaticDrawsTheWholeOutline(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx := routeContext(t, squareTrack(base, 400), 400, 400)

	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := RoutePanel{}.Prepare(ctx, Box{W: 400, H: 400})

	c.Fill(c.Theme.Background)
	p.Static(c)
	if got := countNear(img, c.Theme.Dim, 12); got == 0 {
		t.Fatal("Static drew no outline; a viewer would see only where the route has been, never where it goes")
	}
}

// TestRoutePanel_BeforeTheFirstFixDrawsNoDot pins a refusal to invent a
// position.
//
// An activity whose timeline begins before its first GPS lock has no position
// to mark. Putting the dot at the route's start would say the runner is there,
// which is a claim about where somebody was -- exactly the kind this program
// does not make up.
func TestRoutePanel_BeforeTheFirstFixDrawsNoDot(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx := routeContext(t, squareTrack(base, 200), 400, 400)

	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := RoutePanel{}.Prepare(ctx, Box{W: 400, H: 400})

	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{At: base.Add(-time.Minute)})
	if got := countNear(img, c.Theme.Accent, 20); got != 0 {
		t.Errorf("%d accent pixels before the first fix; the position dot was placed with no position to place it at", got)
	}

	// And once there IS a fix, the dot appears -- or the test above would pass
	// against a panel that never draws one.
	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{At: base.Add(50 * time.Second)})
	if got := countNear(img, c.Theme.Accent, 20); got == 0 {
		t.Error("no position dot once the route has begun")
	}
}

// TestRoutePanel_AcceptsNeedsTwoFixes pins that a lone fix is not a route.
func TestRoutePanel_AcceptsNeedsTwoFixes(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	none := &fitactivity.Track{Samples: []fitactivity.Sample{{Time: base}}}
	one := &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasGPS: true, Lat: 55, Lon: 12},
		{Time: base.Add(time.Second)},
	}}

	if (RoutePanel{}).Accepts(&Context{Report: inspect.Build(none)}) {
		t.Error("accepted an activity with no GPS at all")
	}
	if (RoutePanel{}).Accepts(&Context{Report: inspect.Build(one)}) {
		t.Error("accepted an activity with a single fix; that is a point, not a path")
	}
	if !(RoutePanel{}).Accepts(&Context{Report: inspect.Build(squareTrack(base, 10))}) {
		t.Error("declined an activity with a real route")
	}
}

// TestRoutePanel_StaysInsideItsBox is the containment check, at shapes where a
// route's own aspect ratio fights the box's.
func TestRoutePanel_StaysInsideItsBox(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	shapes := []struct {
		name   string
		fw, fh int
		box    Box
	}{
		{"a square box", 800, 800, Box{X: 50, Y: 50, W: 400, H: 400}},
		{"a wide short box", 1920, 1080, Box{X: 100, Y: 100, W: 900, H: 300}},
		{"a tall narrow box", 1080, 1920, Box{X: 60, Y: 200, W: 300, H: 900}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			ctx := routeContext(t, squareTrack(base, 300), s.fw, s.fh)
			img := image.NewRGBA(image.Rect(0, 0, s.fw, s.fh))
			faces, _ := NewFaceCache()
			c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
			p := RoutePanel{}.Prepare(ctx, s.box)

			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{At: base.Add(150 * time.Second)})

			in := inkCount(img, s.box, c.Theme)
			if in == 0 {
				t.Fatal("nothing drawn")
			}
			if whole := inkCount(img, Box{W: float64(s.fw), H: float64(s.fh)}, c.Theme); whole != in {
				t.Errorf("%d pixels of route escaped the box", whole-in)
			}
		})
	}
}

// TestRoutePanel_DotFollowsEveryFixNotTheThinnedOutline pins the fix for a bug
// no existing test could see.
//
// The outline is drawn from a downsampled copy, capped at DefaultMaxPoints. The
// position was once looked up in that same copy, which quantised it: a
// four-hour ride's 14,400 fixes reduced to 500 froze the dot for 29 seconds and
// then jumped it several hundred metres, and even a 25-minute run moved it in
// 3-second steps. Every test used a fixture UNDER the cap, where the two lists
// are identical and the bug cannot appear.
//
// This fixture is deliberately well over it.
func TestRoutePanel_DotFollowsEveryFixNotTheThinnedOutline(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000 // six times DefaultMaxPoints
	track := squareTrack(base, fixes)
	ctx := routeContext(t, track, 800, 800)

	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	faces, _ := NewFaceCache()
	c, _ := NewCanvas(img, 20, DefaultTheme(), faces)
	p := RoutePanel{}.Prepare(ctx, Box{W: 800, H: 800})

	// The centroid of the accent-coloured dot.
	dotAt := func(offset time.Duration) (float64, float64) {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{At: base.Add(offset)})
		ar, ag, ab, _ := c.Theme.Accent.RGBA()
		var sx, sy, n float64
		for y := 0; y < 800; y++ {
			for x := 0; x < 800; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r == ar && g == ag && b == ab {
					sx, sy, n = sx+float64(x), sy+float64(y), n+1
				}
			}
		}
		if n == 0 {
			t.Fatalf("no dot at offset %v", offset)
		}
		return sx / n, sy / n
	}

	// Consecutive SECONDS must move the dot. With the position taken from the
	// thinned list it would sit still for six of them at this fixture size.
	moved := 0
	px, py := dotAt(1000 * time.Second)
	for i := 1; i <= 6; i++ {
		x, y := dotAt(time.Duration(1000+i) * time.Second)
		if x != px || y != py {
			moved++
		}
		px, py = x, y
	}
	if moved < 5 {
		t.Errorf("the dot moved on %d of 6 consecutive seconds; it is quantised to the drawn outline "+
			"rather than following the fixes", moved)
	}
}

// routeHighlightContext builds a Context carrying highlights, on top of
// routeContext -- which does not set Timeline or Highlights, since only the
// highlight-marking tests below need either.
func routeHighlightContext(t *testing.T, track *fitactivity.Track, seconds int, highlights []Highlight, w, h int) *Context {
	t.Helper()
	ctx := routeContext(t, track, w, h)
	base := track.Samples[0].Time
	tl, err := NewTimeline(base, time.Duration(seconds)*time.Second, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Timeline = tl
	ctx.Highlights = highlights
	return ctx
}

// TestRoutePanel_HighlightMarksItsOwnArcNotTheOppositeOne is the panel-level
// counterpart of TestSpanIndices_ResolvesAgainstTheDrawnList: it is what
// would have caught the DOT bug's mirror image, had this feature existed
// then -- a highlight resolved against the wrong list marks the wrong arc, or
// no arc at all, and a fixture at or under DefaultMaxPoints cannot tell the
// difference because every point is a drawn vertex there. This fixture is
// six times the cap, matching TestRoutePanel_DotFollowsEveryFixNotTheThinnedOutline's
// own reasoning for why it must be.
//
// squareTrack's first quarter (i in [0, fixes/4)) traces the SOUTH edge of
// the square at the minimum latitude, which the north-up projection places
// in the BOTTOM half of the box; its third quarter traces the NORTH edge,
// placed in the TOP half. A highlight over the first quarter must mark the
// bottom half and must not mark the top half at all.
func TestRoutePanel_HighlightMarksItsOwnArcNotTheOppositeOne(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000 // six times DefaultMaxPoints
	track := squareTrack(base, fixes)
	highlight := Highlight{Name: "South", From: 0, To: (fixes / 4) * time.Second}
	ctx := routeHighlightContext(t, track, fixes, []Highlight{highlight}, 800, 800)

	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := RoutePanel{}.Prepare(ctx, Box{W: 800, H: 800})

	c.Fill(c.Theme.Background)
	p.Static(c)
	// Early in the south edge, and the highlight itself is what is playing
	// (Interval 0, full weight) -- the mark must already be visible before
	// the render has even reached the end of its own span, exactly as the
	// highlight strip's blocks are lit from frame one.
	p.Dynamic(c, Frame{At: base.Add(100 * time.Second), Interval: 0, IntervalWeight: 1})

	south := countNearInBox(img, Box{X: 0, Y: 400, W: 800, H: 400}, c.Theme.Highlight, 20)
	north := countNearInBox(img, Box{X: 0, Y: 0, W: 800, H: 400}, c.Theme.Highlight, 20)
	if south == 0 {
		t.Fatal("no highlight-coloured pixels in the southern half; a highlight covering the south edge drew no mark at all")
	}
	if north != 0 {
		t.Errorf("%d highlight-coloured pixels in the northern half, against a highlight covering only the south edge; "+
			"the mark is not landing on its own arc", north)
	}
}

// TestRoutePanel_SubStrideHighlightStillMarksTheOutline is the test for the
// real hazard here, distinct from which list is used: at
// DefaultMaxPoints over this fixture's 3000-second length, the outline's own
// stride is (3000-1)/(500-1) =~ 6.01 seconds between drawn vertices. A
// highlight shorter than that stride contains no drawn vertex at all, and
// without route.SpanIndices' bracketing rule it would draw nothing -- present
// in the data, invisible on the map, and invisible to any test whose fixture
// sits at or under the cap: there the stride is one second and a 3-second
// highlight would straddle several vertices on its own, passing whether or
// not the bracketing rule existed. This fixture is deliberately six times the
// cap for exactly that reason.
func TestRoutePanel_SubStrideHighlightStillMarksTheOutline(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000 // six times DefaultMaxPoints; see the doc comment above
	track := squareTrack(base, fixes)
	// Three seconds, well under the ~6-second stride this fixture thins to.
	highlight := Highlight{Name: "Blip", From: 1000 * time.Second, To: 1003 * time.Second}
	ctx := routeHighlightContext(t, track, fixes, []Highlight{highlight}, 800, 800)

	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := RoutePanel{}.Prepare(ctx, Box{W: 800, H: 800})

	c.Fill(c.Theme.Background)
	p.Static(c)
	// Well PAST the highlight's own span: the mark has to survive the render
	// passing it, and sampling from inside the span would let the position
	// dot -- Theme.Accent, drawn last, deliberately on top -- sit directly on
	// top of the one short segment being tested and hide it from this pixel
	// search.
	p.Dynamic(c, Frame{At: base.Add(2000 * time.Second), Interval: 0, IntervalWeight: 1})

	if got := countNear(img, c.Theme.Highlight, 20); got == 0 {
		t.Fatal("a 3-second highlight, shorter than the outline's own ~6-second stride at this fixture size, drew no mark at all")
	}
}

// TestRoutePanel_HighlightMarksBeforeTheFirstGPSFix is F4's regression test:
// a highlight's route mark must be visible from frame one, even on an
// activity whose GPS acquires after the render's timeline starts -- exactly
// the same "lit before the render reaches it" promise
// TestRoutePanel_HighlightMarksItsOwnArcNotTheOppositeOne already pins for a
// frame with a known position. Before the fix, Dynamic returned early
// whenever the current position lookup failed, so nothing -- not even a
// configured highlight's mark -- drew until the very first fix, at which
// point every mark popped in on a single frame.
//
// The fixture is over DefaultMaxPoints for the same reason every other test
// in this file that resolves a highlight against the route is: under the
// cap the thinned and full fix lists are identical, which cannot tell a
// correctly-ordered Dynamic from one that merely got lucky.
func TestRoutePanel_HighlightMarksBeforeTheFirstGPSFix(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000 // six times DefaultMaxPoints
	// The first 500 seconds have no GPS fix at all -- the watch has not
	// locked on yet -- and the highlight sits well after that, inside the
	// part of the track that does have fixes.
	track := squareTrackWithLateGPS(base, fixes, 500)
	highlight := Highlight{Name: "East", From: 1000 * time.Second, To: 1010 * time.Second}
	ctx := routeHighlightContext(t, track, fixes, []Highlight{highlight}, 800, 800)

	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p := RoutePanel{}.Prepare(ctx, Box{W: 800, H: 800})

	c.Fill(c.Theme.Background)
	p.Static(c)
	// Well inside the GPS-less opening stretch: IndexAt against the full fix
	// list returns -1 here, so there is no position to place a dot at, but
	// the highlight's own mark has nothing to do with that. Interval/Weight
	// are set as though this highlight were the one currently playing, the
	// same way TestRoutePanel_SubStrideHighlightStillMarksTheOutline drives
	// a frame that is nowhere near the highlight's own span: they are inputs
	// the render loop hands Dynamic independently of Frame.At, and setting
	// them to full weight is what makes the mark opaque Theme.Highlight
	// rather than the dimmer rest-alpha blend, so countNear below can find
	// it without having to reproduce that blend's arithmetic.
	p.Dynamic(c, Frame{At: base.Add(200 * time.Second), Interval: 0, IntervalWeight: 1})

	if got := countNear(img, c.Theme.Accent, 20); got != 0 {
		t.Errorf("%d accent pixels before the first fix; a position was invented with none to place", got)
	}
	if got := countNear(img, c.Theme.Highlight, 20); got == 0 {
		t.Fatal("no highlight-coloured pixels before the first GPS fix; " +
			"the route mark must be lit from frame one, not only once a position is known")
	}
}

// TestExtendMarkAlongPolyline_GrowsToTheMinimumLength is F2's decision,
// isolated from pixels: given a straight run of vertices one pixel apart
// (so arc length is just the index distance), the extension must grow a
// mark that is too short, hold one that already meets the floor, and clamp
// at either end of the list rather than reading out of bounds.
func TestExtendMarkAlongPolyline_GrowsToTheMinimumLength(t *testing.T) {
	const n = 21
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i := range xs {
		xs[i] = float64(i) // one pixel apart; ys all zero
	}

	cases := []struct {
		name           string
		i0, i1         int
		minLen         float64
		wantI0, wantI1 int
	}{
		{"already long enough: untouched", 10, 12, 1.5, 10, 12},
		{"grows symmetrically about its own centre", 10, 10, 4, 8, 12},
		{"clamped at the start of the list", 0, 1, 6, 0, 6},
		{"clamped at the end of the list", 19, 20, 6, 14, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i0, i1 := extendMarkAlongPolyline(xs, ys, c.i0, c.i1, c.minLen)
			if i0 != c.wantI0 || i1 != c.wantI1 {
				t.Errorf("extendMarkAlongPolyline(%d, %d, %v) = (%d, %d), want (%d, %d)",
					c.i0, c.i1, c.minLen, i0, i1, c.wantI0, c.wantI1)
			}
			if got := polylineLength(xs, ys, i0, i1); got < c.minLen && !(i0 == 0 && i1 == n-1) {
				t.Errorf("result length %v is still below minLen %v, and it did not exhaust the list", got, c.minLen)
			}
		})
	}
}

// TestRoutePanel_ShortHighlightMarkMeetsTheMinimumLength is F2's regression
// test at the panel level: a highlight far shorter than the outline's own
// stride resolves (via route.SpanIndices) to a bracketing chord of only two
// vertices, which can be a handful of pixels long on a route this size --
// present in the data, but invisible under the position dot for the whole
// time the highlight plays, per F2's own finding. The resolved mark must
// measure at least minMarkLengthFraction of the panel's own unit.
func TestRoutePanel_ShortHighlightMarkMeetsTheMinimumLength(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000 // six times DefaultMaxPoints
	track := squareTrack(base, fixes)
	highlight := Highlight{Name: "Blip", From: 1000 * time.Second, To: 1003 * time.Second}
	ctx := routeHighlightContext(t, track, fixes, []Highlight{highlight}, 800, 800)

	p := RoutePanel{}.Prepare(ctx, Box{W: 800, H: 800}).(*routePainter)
	if len(p.marks) != 1 || !p.marks[0].ok {
		t.Fatalf("expected one markable highlight, got %+v", p.marks)
	}

	m := p.marks[0]
	got := polylineLength(p.xs, p.ys, m.i0, m.i1)
	want := 800.0 * minMarkLengthFraction // unit is min(box.W, box.H) = 800 here
	if got < want-1e-6 {
		t.Errorf("mark spans %.1fpx, want at least %.1fpx (minMarkLengthFraction of the panel's unit)", got, want)
	}
}

// squareTrackWithMidGPSDropout is squareTrack with a stretch of samples in
// the MIDDLE of the track blanked out -- real fixes before it AND real
// fixes after it. This differs from squareTrackWithLateGPS's own dropout at
// the very START of the track, which only ever exercises the "before the
// first fix" REFUSAL branch of route.SpanIndices
// (TestRoutePanel_HighlightMarksBeforeTheFirstGPSFix). A highlight landing
// INSIDE a dropout with fixes on both sides takes a different branch
// entirely -- route.SpanIndices' "no drawn vertex falls inside the span"
// bracketing-chord case -- and it needs no special handling at all:
// FromTrack simply skips the absent samples, so
// the span brackets whatever chord is already drawn across the gap.
func squareTrackWithMidGPSDropout(base time.Time, n, dropFrom, dropTo int) *fitactivity.Track {
	track := squareTrack(base, n)
	for i := dropFrom; i < dropTo && i < len(track.Samples); i++ {
		track.Samples[i].HasGPS = false
		track.Samples[i].Lat, track.Samples[i].Lon = 0, 0
	}
	return track
}

// TestRoutePanel_HighlightInsideAMidActivityGPSDropoutMarksTheBracketingChord
// is the case that needs no extra code, and which no fixture before this
// test exercised: a highlight whose whole span falls inside a
// GPS dropout that has REAL fixes on both sides. squareTrackWithLateGPS's
// own dropout only ever covers the very start of the track, so every
// existing test that uses it can only ever prove the "before the first
// fix" refusal -- never this, the ordinary bracketing-chord case that
// applies to any OTHER kind of gap in the middle of a real recording (a
// strap that drops out, a tunnel).
//
// The fixture is six times route.DefaultMaxPoints (3000 fixes thinned to
// 500), matching every other route test in this file that resolves a
// highlight against the drawn list: under the cap, a 200-sample dropout
// still leaves so many one-second-spaced vertices around it that a correct
// bracketing chord and a bug that silently drew nothing could look
// identical in the render.
func TestRoutePanel_HighlightInsideAMidActivityGPSDropoutMarksTheBracketingChord(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000                  // six times DefaultMaxPoints
	const dropFrom, dropTo = 1400, 1600 // a 200s dropout with real fixes on both sides
	track := squareTrackWithMidGPSDropout(base, fixes, dropFrom, dropTo)
	// Entirely inside the dropout: no sample in [1450s, 1550s) carries a fix
	// at all, so no drawn vertex can possibly fall inside this span -- the
	// exact precondition route.SpanIndices' bracketing branch requires.
	highlight := Highlight{Name: "Tunnel", From: 1450 * time.Second, To: 1550 * time.Second}
	ctx := routeHighlightContext(t, track, fixes, []Highlight{highlight}, 800, 800)

	p := RoutePanel{}.Prepare(ctx, Box{W: 800, H: 800}).(*routePainter)
	if len(p.marks) != 1 {
		t.Fatalf("expected one resolved mark, got %d", len(p.marks))
	}
	m := p.marks[0]
	if !m.ok {
		t.Fatal("a highlight inside a mid-activity GPS dropout with real fixes on both sides was refused; " +
			"it should mark the bracketing chord, exactly like any other sub-stride highlight")
	}
	dropStart, dropEnd := base.Add(dropFrom*time.Second), base.Add(dropTo*time.Second)
	if !p.pts[m.i0].Time.Before(dropStart) {
		t.Errorf("mark's first vertex is at %v, which does not precede the dropout beginning at %v; this is not a real bracketing chord",
			p.pts[m.i0].Time, dropStart)
	}
	if p.pts[m.i1].Time.Before(dropEnd) {
		t.Errorf("mark's last vertex is at %v, still before the dropout ends at %v; this is not a real bracketing chord",
			p.pts[m.i1].Time, dropEnd)
	}

	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
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
	// Well past the dropout: the mark must survive the render passing it,
	// the same requirement TestRoutePanel_SubStrideHighlightStillMarksTheOutline
	// already applies, and sampling well outside the dropout keeps the
	// position dot -- drawn last, directly on top -- off the one chord this
	// test is checking.
	p.Dynamic(c, Frame{At: base.Add(2500 * time.Second), Interval: 0, IntervalWeight: 1})

	if got := countNear(img, c.Theme.Highlight, 20); got == 0 {
		t.Fatal("a highlight inside a mid-activity GPS dropout drew no mark at all; " +
			"route.SpanIndices' own doc comment says this needs no special case, and this is what would catch it if that were wrong")
	}
}

// TestRoutePanel_UnmarkableHighlightDrawsNoExtraInk is the pixel-level
// counterpart of cmd/render.go's own "not marked on the route" summary
// line: a highlight with no GPS fixes anywhere near it draws NOTHING
// extra, and the summary says so in words
// instead, because there is no placeholder available on a map. Nothing
// before this test proved the "nothing extra" half at the pixel level -- a
// stray pixel from a degenerate polyline call that happened not to panic
// would still let the summary print the right sentence, and no existing
// test could have told the two apart.
//
// The comparison is against a render with NO highlight configured at all,
// not merely a scan for Theme.Highlight-coloured pixels: a byte-for-byte
// match rules out every OTHER way an unmarkable highlight could leave a
// mark this panel does not intend, including one a colour-only scan would
// miss (a mis-blended antialiasing fringe from a call made with a
// degenerate index pair, say).
func TestRoutePanel_UnmarkableHighlightDrawsNoExtraInk(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixes = 3000 // six times DefaultMaxPoints
	// The first 1000 seconds have no GPS fix at all; the highlight sits
	// entirely inside that stretch, well before the route's own first
	// drawn vertex -- route.SpanIndices' "entirely before the first point"
	// refusal.
	track := squareTrackWithLateGPS(base, fixes, 1000)
	highlight := Highlight{Name: "Warmup", From: 200 * time.Second, To: 400 * time.Second}

	plainCtx := routeContext(t, track, 800, 800)
	highlightedCtx := routeHighlightContext(t, track, fixes, []Highlight{highlight}, 800, 800)

	plain := RoutePanel{}.Prepare(plainCtx, Box{W: 800, H: 800})
	highlighted := RoutePanel{}.Prepare(highlightedCtx, Box{W: 800, H: 800}).(*routePainter)
	if len(highlighted.marks) != 1 || highlighted.marks[0].ok {
		t.Fatalf("precondition: expected one UNMARKABLE highlight, got %+v", highlighted.marks)
	}

	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	render := func(p Painter, at time.Duration) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 800, 800))
		c, err := NewCanvas(img, 20, DefaultTheme(), faces)
		if err != nil {
			t.Fatal(err)
		}
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{At: base.Add(at), Interval: 0, IntervalWeight: 1})
		return img
	}

	// Both before AND after the route's own first fix, since the position
	// dot's own presence or absence is a different code path this
	// comparison must not accidentally depend on.
	for _, at := range []time.Duration{500 * time.Second, 1500 * time.Second} {
		want := render(plain, at)
		got := render(highlighted, at)
		if !bytes.Equal(want.Pix, got.Pix) {
			t.Errorf("at %v: an unmarkable highlight changed %d pixels against a render with no highlight configured at all; "+
				"it must draw nothing extra", at, countDiffPixels(want, got))
		}
	}
}

// countDiffPixels counts pixels differing in colour OR alpha between two
// equally-sized RGBA images.
func countDiffPixels(a, b *image.RGBA) int {
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] || a.Pix[i+3] != b.Pix[i+3] {
			n++
		}
	}
	return n
}
