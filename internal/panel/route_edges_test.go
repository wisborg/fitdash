package panel

import (
	"testing"
	"time"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// TestRoutePanel_BasemapReachesTheEdgesOfAFractionalBox is the hairline bug,
// pinned where it can be seen.
//
// A layout hands a panel fractional edges -- a box running to y=369.9 is the
// normal case, not a corner one -- and the imagery's destination was rounded
// IN while the clip around it was rounded OUT. The last row of the box got no
// map, so a line of plain background ran along the bottom of every basemap.
//
// Every containment test in this file is blind to it, and for one reason: they
// all build boxes out of round numbers, where the two roundings agree. So the
// fixture here is deliberately off the pixel grid, and it checks the far
// edges rather than the middle, which is the only place the two answers
// differ.
func TestRoutePanel_BasemapReachesTheEdgesOfAFractionalBox(t *testing.T) {
	const w, h = 700, 700
	ctx, _ := zoomFixture(t, false, w, h)
	// No attribution, so nothing is drawn over the edges being measured.
	// The credit is anchored to the box's own bottom-right corner and is
	// entitled to cover the map there; a fixture that let it would be
	// measuring the plate rather than the gap.
	ctx.Basemap = uncreditedBasemap{&fakeBasemap{fill: basemapGreen}}
	ctx.BasemapDim = 0

	box := Box{X: 40.3, Y: 40.7, W: 559.4, H: 449.9}
	img, c := zoomCanvas(t, w, h)
	p := RoutePanel{}.Prepare(ctx, box)
	c.Fill(c.Theme.Background)
	p.Static(c)
	p.Dynamic(c, Frame{At: ctx.Timeline.Start().Add(60 * time.Second), Interval: NoHighlight})

	// The last row and column the box touches at all. Half a pixel of the
	// box is still the box: leaving it bare is the gap, and it is the pixel
	// a rounded-down destination stops one short of.
	lastX, lastY := int(box.X+box.W), int(box.Y+box.H)
	edges := []struct {
		name   string
		x0, x1 int
		y0, y1 int
	}{
		{"bottom row", int(box.X) + 2, lastX - 2, lastY, lastY},
		{"right column", lastX, lastX, int(box.Y) + 2, lastY - 2},
	}
	for _, e := range edges {
		bare := 0
		for y := e.y0; y <= e.y1; y++ {
			for x := e.x0; x <= e.x1; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				gr, gg, gb, _ := basemapGreen.RGBA()
				if abs(int(r>>8)-int(gr>>8)) > 40 || abs(int(g>>8)-int(gg>>8)) > 40 || abs(int(b>>8)-int(gb>>8)) > 40 {
					bare++
				}
			}
		}
		if bare > 0 {
			t.Errorf("%s: %d of %d pixels carry no imagery; the map stops short of the box and the background shows as a hairline",
				e.name, bare, (e.x1-e.x0+1)*(e.y1-e.y0+1))
		}
	}
}

// uncreditedBasemap is a provider that owes no attribution, which is a state
// the panel already has to handle -- see TestRoutePanel_NoCreditMeansNoReservedStrip.
type uncreditedBasemap struct{ tilemap.Provider }

func (uncreditedBasemap) Attribution() string { return "" }
