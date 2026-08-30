package panel

import (
	"math"

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

	// Every highlight's span, resolved against pts -- the DRAWN list, per
	// route.SpanIndices's own doc comment on why this is the opposite rule
	// from the position dot above, which deliberately uses p.all. The bounds
	// convert through ctx.Timeline.Start(), never ctx.Track.Samples[0].Time:
	// the timer window and the first record differ by seconds in a real
	// file, and From/To are offsets into the TIMELINE's own elapsed clock
	// (see Highlight's own doc comment), not the track's.
	//
	// SpanIndices already guarantees a drawable pair when ok is true, but
	// "drawable" and "visible" are not the same promise: two adjacent
	// vertices at DefaultMaxPoints can be a handful of pixels apart on a
	// long ride, and the mark would sit invisibly under the position dot
	// for the whole time the highlight is actually playing. extendMarkAlongPolyline
	// grows the span, symmetrically about its own centre, until it measures
	// at least minMarkLengthFraction of the panel's own unit -- see that
	// constant's own comment for why this is the same class of fix as
	// minBlockFraction on the strip.
	if len(ctx.Highlights) > 0 {
		start := ctx.Timeline.Start()
		minLen := unit * minMarkLengthFraction
		p.marks = make([]routeMark, len(ctx.Highlights))
		for i, h := range ctx.Highlights {
			i0, i1, ok := route.SpanIndices(pts, start.Add(h.From), start.Add(h.To))
			if ok {
				i0, i1 = extendMarkAlongPolyline(xs, ys, i0, i1, minLen)
			}
			p.marks[i] = routeMark{ok: ok, i0: i0, i1: i1}
		}
	}
	return p
}

// minMarkLengthFraction is the smallest a highlight's route mark is allowed
// to measure, as a fraction of the panel's own unit -- the same
// min(box.W, box.H) that outlineW, coveredW and dotR are already sized off,
// so the floor scales with the box the same way every other measure on this
// Painter does, rather than as a pixel count that would vanish at 4K or eat
// the whole route at a small size.
//
// This reprises minBlockFraction's reasoning in highlight_panel.go: a
// highlight that resolves to a genuine but tiny span is present in the data
// and invisible in the render, which this project treats as the same
// silent failure as a panel that draws nothing at all. Here it is sharper
// still, because the position dot is drawn last and sits directly on top
// of the mark for the entire time the highlight is actually active, so the
// box can show zero mark pixels on exactly the frames a viewer most wants
// to see one.
const minMarkLengthFraction = 0.08

// extendMarkAlongPolyline grows [i0, i1] outward, one drawn vertex at a
// time, until the pixel-space arc length of pts[i0:i1+1] reaches minLen or
// both ends of the list are exhausted. It alternates which side it grows so
// a mark nowhere near either end of the route lengthens roughly evenly
// about its own centre rather than eating the whole route from one side.
func extendMarkAlongPolyline(xs, ys []float64, i0, i1 int, minLen float64) (int, int) {
	n := len(xs)
	for polylineLength(xs, ys, i0, i1) < minLen && (i0 > 0 || i1 < n-1) {
		if i0 > 0 && (i1 >= n-1 || (i1-i0)%2 == 0) {
			i0--
		} else {
			i1++
		}
	}
	return i0, i1
}

// polylineLength is the sum of the pixel-space segment lengths from i0 to
// i1, along the exact coordinates Dynamic strokes a mark from.
func polylineLength(xs, ys []float64, i0, i1 int) float64 {
	var total float64
	for i := i0; i < i1; i++ {
		dx, dy := xs[i+1]-xs[i], ys[i+1]-ys[i]
		total += math.Hypot(dx, dy)
	}
	return total
}

// routeMark is one highlight's span, already resolved to a pair of indices
// into p.pts/p.xs/p.ys by Prepare. ok is false for a highlight whose span has
// no GPS fix anywhere near it (see route.SpanIndices) -- the honest analogue,
// on a map, of a placeholder: there is no "--" available to draw where a
// position would go, so Dynamic below draws nothing for it, and cmd's own
// render summary says so in words instead (see cmd/render.go's use of the
// same SpanIndices call against the same thinned list).
type routeMark struct {
	ok     bool
	i0, i1 int
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

	// marks is parallel to Context.Highlights and indexed by Frame.Interval,
	// the same convention the highlight strip's own blocks/names/anchorX
	// slices use, so a lookup here can never disagree with which highlight
	// the render loop says is active.
	marks []routeMark
}

// Static draws the whole route, dim. The expensive part, drawn once.
func (p *routePainter) Static(c *Canvas) {
	if len(p.xs) < 2 {
		return
	}
	c.Polyline(p.xs, p.ys, p.outlineW, c.Theme.Dim)
}

// Dynamic draws the covered portion, then every configured highlight's route
// mark, then the current position -- in that order, and the order is load
// bearing for two separate reasons.
//
// The covered prefix comes FIRST because it is drawn wider than the outline
// and OVER the whole prefix so far: a mark drawn before it, or into Static,
// would be erased the moment the render's playhead passes over it. Drawing
// the marks after the covered prefix and before the dot is what keeps a
// highlight visible for the rest of the render once the route has passed
// through it.
//
// The marks are drawn UNCONDITIONALLY (once len(p.xs) >= 2 -- there is a
// route at all) rather than being gated on a current position, and that is
// deliberate: a mark is a property of the highlight's own span -- an offset
// the user typed -- not of whether the activity's GPS has locked on yet, so
// it must be lit from frame one exactly as the highlight strip's own blocks
// are. An earlier version of this method drew the marks below the
// current-position lookup's own early return, so on an activity whose GPS
// acquires after the timeline starts, no mark appeared at all until the
// first fix, at which point every mark still configured popped in on one
// frame -- the opposite of "visible before the render reaches it." The
// covered-prefix lookup just above has the same property already, for the
// same reason: it is keyed off p.pts directly, not off whether a CURRENT
// position is known, so a highlight lying earlier in the route already
// shows a covered prefix before this method ever needs to place a dot.
//
// The dot comes LAST, both because Theme.Accent must sit visibly on top of
// everything else and because it is the one piece of ink here that truly
// cannot be drawn before a position exists: unlike the covered prefix and
// the marks, placing a dot with no current fix would be inventing a
// position, which this program does not do. That is also why it is the only
// part of this method gated on cur < 0.
func (p *routePainter) Dynamic(c *Canvas, f Frame) {
	if len(p.xs) < 2 {
		return
	}

	// The covered portion is a prefix of the DRAWN points, so it is looked up
	// in those -- a prefix of the outline has to end on one of the outline's
	// own vertices or the bright line would not lie on the dim one. Before
	// the first fix, IndexAt returns -1 here exactly as it does against
	// p.all below, so nothing is drawn -- there is nothing covered yet.
	if drawn := route.IndexAt(p.pts, f.At); drawn >= 1 {
		c.Polyline(p.xs[:drawn+1], p.ys[:drawn+1], p.coveredW, c.Theme.Foreground)
	}

	// Every markable highlight draws, not only the one active this frame --
	// the same "lit from frame one, brightened only while playing" rule the
	// highlight strip's own blocks follow, so a viewer can see where a
	// highlight lies before the render reaches it. The colour is always
	// Theme.Highlight, never a highlight's own background=: that colour was
	// chosen to sit BEHIND text, which makes it a poor stroke against Dim,
	// Foreground and Accent -- the three things this mark has to read
	// against (the outline, the covered prefix and the dot). The active
	// highlight brightens using restAlpha, the SAME ramp on highlightRestAlpha
	// the strip applies to Frame.IntervalWeight, so the two agree by
	// construction rather than by two copies of the same arithmetic that
	// merely happen to match.
	for i, m := range p.marks {
		if !m.ok {
			continue
		}
		alpha := highlightRestAlpha
		if i == f.Interval {
			alpha = restAlpha(f.IntervalWeight)
		}
		c.Polyline(p.xs[m.i0:m.i1+1], p.ys[m.i0:m.i1+1], p.coveredW, Fade(c.Theme.Highlight, alpha))
	}

	// The dot comes from the full fix list and is placed directly, so it moves
	// at the rate the device recorded rather than at the rate the outline was
	// thinned to. Before the first fix there is no position to mark, so
	// nothing further is drawn rather than putting a dot at the route's
	// start -- a dot sitting at the beginning would say the runner is there,
	// which is a claim about position this program does not invent.
	cur := route.IndexAt(p.all, f.At)
	if cur < 0 {
		return
	}
	x, y := p.place(p.all[cur])
	c.Circle(x, y, p.dotR, c.Theme.Accent)
}
