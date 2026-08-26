package panel

import (
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
