package panel

import (
	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/route"
)

// RoutePanel draws the GPS track: the whole route dim, the part covered so far
// bright, and the current position as a dot.
//
// This is the panel the static/dynamic split was designed for. The full
// outline never changes across a render and is rasterized once; only the
// covered portion and the dot move. Redrawing the whole outline every frame
// would be the single largest cost in the render.
type RoutePanel struct{}

// Name identifies the panel.
func (RoutePanel) Name() string { return "route" }

// Accepts declines an activity with no route to draw.
//
// It asks for at least TWO fixes rather than merely for some, because a single
// fix is a point rather than a path: a treadmill run whose watch caught one
// position through a window would otherwise place a panel that can only ever
// draw a dot in the middle of an empty box.
func (RoutePanel) Accepts(ctx *Context) bool {
	m, ok := ctx.Report.Metric(inspect.MetricPosition)
	return ok && m.Present >= 2
}

// Prepare projects the route into the box, once.
//
// This is where the projection lives -- as fields on the Painter -- and that
// is the whole reason the phase exists. The sibling HUD project needs a
// mutex-guarded memo map keyed on a slice's backing-array pointer to avoid
// re-projecting on every frame, because its gauges are handed nothing that
// persists between them. Here there is nowhere for such a cache to go, because
// there is nothing to cache: the work happens once and the result is a field.
func (RoutePanel) Prepare(ctx *Context, box Box) Painter {
	p := &routePainter{box: box}

	// Two lists, deliberately. The outline is drawn from a thinned copy,
	// because a route a few hundred pixels wide cannot show more; the position
	// is looked up in ALL the fixes, because thinning them quantises the dot --
	// a four-hour ride reduced to 500 points freezes it for 29 seconds at a
	// time and then jumps it several hundred metres.
	all := route.FromTrack(ctx.Track, len(ctx.Track.Samples)+1)
	pts := route.FromTrack(ctx.Track, route.DefaultMaxPoints)
	proj, ok := route.Fit(pts)
	if !ok {
		// Accepts should have prevented this, but a route can be two fixes at
		// the same coordinate, which passes the count test and has no extent.
		// Drawing nothing is right; the box stays background, which is what
		// the frame is anyway.
		return p
	}

	inset := box.W * 0.06
	if box.H*0.06 < inset {
		inset = box.H * 0.06
	}
	place, ok := proj.Placer(box.W-2*inset, box.H-2*inset)
	if !ok {
		return p
	}
	// One placement function for the outline and the dot, so the dot cannot
	// drift off the line it is meant to be travelling along.
	p.place = func(pt route.Point) (float64, float64) {
		x, y := place(pt)
		return x + box.X + inset, y + box.Y + inset
	}
	xs := make([]float64, len(pts))
	ys := make([]float64, len(pts))
	for i, pt := range pts {
		xs[i], ys[i] = p.place(pt)
	}

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	p.pts, p.all, p.xs, p.ys = pts, all, xs, ys
	p.outlineW = maxf(1.5, unit*0.008)
	p.coveredW = maxf(2.5, unit*0.014)
	p.dotR = maxf(3, unit*0.022)
	return p
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

type routePainter struct {
	box      Box
	pts      []route.Point // thinned, for the outline
	all      []route.Point // every fix, for the position lookup
	place    func(route.Point) (float64, float64)
	xs, ys   []float64
	outlineW float64
	coveredW float64
	dotR     float64
}

// Static draws the whole route, dim. The expensive part, drawn once.
func (p *routePainter) Static(c *Canvas) {
	if len(p.xs) < 2 {
		return
	}
	c.Polyline(p.xs, p.ys, p.outlineW, c.Theme.Dim)
}

// Dynamic draws the covered portion and the current position.
//
// Before the first fix -- which happens when the activity's timeline starts
// before its first GPS lock -- there is nothing covered and no position to
// mark, so it draws neither rather than putting a dot at the route's start.
// A dot sitting at the beginning would say the runner is there, which is a
// claim about position, and this program does not invent those.
func (p *routePainter) Dynamic(c *Canvas, f Frame) {
	if len(p.xs) < 2 {
		return
	}
	// The dot comes from the full fix list and is placed directly, so it moves
	// at the rate the device recorded rather than at the rate the outline was
	// thinned to.
	cur := route.IndexAt(p.all, f.At)
	if cur < 0 {
		return
	}
	// The covered portion is a prefix of the DRAWN points, so it is looked up
	// in those -- a prefix of the outline has to end on one of the outline's
	// own vertices or the bright line would not lie on the dim one.
	if drawn := route.IndexAt(p.pts, f.At); drawn >= 1 {
		c.Polyline(p.xs[:drawn+1], p.ys[:drawn+1], p.coveredW, c.Theme.Foreground)
	}
	x, y := p.place(p.all[cur])
	c.Circle(x, y, p.dotR, c.Theme.Accent)
}
