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
	p.inset, p.base = inset, proj

	unit := box.H
	if box.W < unit {
		unit = box.W
	}
	p.pts, p.all = pts, all
	p.creditPx = maxf(9, unit*0.05)

	// Room kept along the bottom for the attribution, so the route is not
	// drawn underneath it.
	//
	// The credit sits in the panel's bottom-right corner and the route is
	// fitted to fill the panel, so a course whose south-east corner is its
	// furthest point runs straight under the words -- reported from real use,
	// on a real activity. Reserving the strip effectively zooms the route out
	// a little: it costs a few percent of size and buys a course that is
	// never hidden behind the credit it obliges.
	//
	// Only when imagery was ASKED for, and dropped again below if none
	// arrived: a render whose fetch failed has to come out identical to one
	// that never asked, down to the bytes, and that includes where the route
	// was placed.
	if ctx.Basemap != nil {
		p.reserveBottom = p.creditPx * creditReserveFraction
	}
	if !p.layOut(proj) {
		return p
	}

	p.outlineW = maxf(1.5, unit*0.008)
	p.coveredW = maxf(2.5, unit*0.014)
	p.dotR = maxf(3, unit*0.022)

	p.resolveMarks(ctx, proj, unit)

	// Fetched here, after the marks exist, because the zoomed views are
	// defined by them -- and in Prepare at all because this is the phase that
	// runs once. Everything below this line runs per frame.
	if ctx.Basemap != nil {
		p.baseMap, p.zoomMaps, p.mapNote = fetchBasemaps(ctx, p, proj, p.marks)
		if p.baseMap != nil {
			p.mapCredit = ctx.Basemap.Attribution()
			p.mapDim = clampWeight(ctx.BasemapDim)
		} else if p.reserveBottom > 0 {
			// No imagery, so no credit, so nothing to keep room for. Laid
			// out again rather than left with an empty strip, because a
			// failed fetch has to render identically to no --basemap at all.
			p.reserveBottom = 0
			if !p.layOut(proj) {
				return p
			}
			p.resolveMarks(ctx, proj, unit)
		}
	}
	return p
}

// creditReserveFraction is how much of the credit's own text size is kept
// clear beneath the route: the line itself, the plate's padding above and
// below it, and a little air.
const creditReserveFraction = 2.2

// layOut resolves the placement everything drawn from this projection shares
// -- the placer, the drawn coordinates, and the viewport imagery is
// composited against.
//
// One function because those three have to agree exactly. The viewport comes
// from the SAME placement the route uses rather than from the box, so the
// imagery fetched for it lands under the line rather than beside it.
func (p *routePainter) layOut(proj route.Projection) bool {
	place, ok := p.placer(proj)
	if !ok {
		return false
	}
	p.place = place
	p.xs, p.ys = p.placeAll(p.pts, place)

	w, h := p.placeArea()
	if vx, vy, vw, vh, ok := proj.CoverBox(w, h, p.box.W, p.box.H, p.inset, p.inset); ok {
		// Static composites against this and runs BEFORE Dynamic ever writes
		// it, so a render that never zooms would otherwise composite against
		// a zero rectangle and draw no map at all -- on exactly the renders
		// where the basemap is simplest.
		p.viewport = [4]float64{vx, vy, vw, vh}
	}
	return true
}

// placeArea is the region inside the box the route itself is fitted into:
// the box less its inset, less any room kept for the attribution.
func (p *routePainter) placeArea() (w, h float64) {
	return p.box.W - 2*p.inset, p.box.H - 2*p.inset - p.reserveBottom
}

// resolveMarks resolves every highlight's stretch of the drawn outline, and
// the zoomed view that goes with one that asked for it.
//
// A method rather than a block inside Prepare because it has to be re-runnable:
// the marks are indices into the DRAWN coordinates, and a basemap fetch that
// fails re-lays the route out without the credit's reserved strip -- which
// moves those coordinates, and would leave marks pointing at the old ones.
func (p *routePainter) resolveMarks(ctx *Context, proj route.Projection, unit float64) {
	p.marks, p.anyZoom = nil, false
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
	if len(ctx.Highlights) == 0 {
		return
	}
	start := ctx.Timeline.Start()
	minLen := unit * minMarkLengthFraction
	p.marks = make([]routeMark, len(ctx.Highlights))
	for i, h := range ctx.Highlights {
		i0, i1, ok := route.SpanIndices(p.pts, start.Add(h.From), start.Add(h.To))
		if ok {
			i0, i1 = extendMarkAlongPolyline(p.xs, p.ys, i0, i1, minLen)
		}
		m := routeMark{ok: ok, i0: i0, i1: i1}
		// The zoom is fitted to the very vertices the mark is drawn
		// from, not to the raw span, so the two cannot disagree: what
		// the zoomed view frames is exactly the stretch drawn in
		// Theme.Highlight, including whatever extendMarkAlongPolyline
		// added to make it visible in the first place.
		//
		// A highlight whose span has no GPS anywhere near it (ok
		// false) gets no zoom, for the same reason it gets no mark:
		// there is no stretch of course to frame, and framing the
		// nearest one would claim the highlight happened there.
		if h.Zoom && ok {
			// Fit over the marked vertices, not a constructor of its
			// own: Mercator has no per-view parameter, so a sub-view is
			// simply this projection re-fitted to fewer points. The
			// previous projection needed a Sub that shared its mean
			// latitude, or the two views sheared against each other as
			// the zoom scaled between them.
			m.zoom, m.zoomOK = route.Fit(p.pts[i0 : i1+1])
			p.anyZoom = p.anyZoom || m.zoomOK
		}
		p.marks[i] = m
	}
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

	// zoom is the view this highlight reframes the map to while it plays,
	// and zoomOK whether it has one at all -- false both for a highlight
	// that never asked (the default) and for one whose span could not be
	// fitted. The pair follows the same absence rule as every reading in
	// this project: a zero Projection is a legitimate-looking value that
	// would silently place the whole route in a corner.
	zoom   route.Projection
	zoomOK bool
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

	// base and inset are what placer needs to rebuild a placement function
	// for a projection other than the whole-route one. Kept because a
	// zooming render resolves a new projection every frame; a render with
	// no zoom never reads them again after Prepare.
	base  route.Projection
	inset float64

	// base and zooms are the fetched imagery, parallel to marks. A render
	// with no --basemap leaves them nil and every basemap branch below falls
	// away.
	baseMap   *basemapView
	zoomMaps  []*basemapView
	mapDim    float64
	mapCredit string
	mapNote   string
	creditPx  float64
	// suppressMap leaves the imagery out while the render measures where it
	// is drawing -- see BasemapSuppressor.
	suppressMap bool
	// reserveBottom is the strip kept clear beneath the route for the
	// attribution, so a course reaching the panel's bottom-right corner is
	// not drawn underneath the credit it obliges.
	reserveBottom float64
	// creditFitted is creditPx shrunk to fit the box, resolved on the first
	// draw because it needs the canvas's font metrics, which Prepare cannot
	// reach.
	creditFitted float64

	// viewport is the projected rectangle the current frame is looking at:
	// minX, minY, spanX, spanY. Written by Dynamic before anything composites
	// against it, and left at the base view for a render that never zooms.
	viewport [4]float64

	// anyZoom is true when at least one highlight resolved a zoom view, and
	// it is what moves the outline out of Static and into Dynamic -- see
	// Static. It is deliberately "any highlight actually got one" rather
	// than "any highlight asked", so a render whose every zoom= was
	// declined for want of GPS keeps the cheap static outline it would have
	// had if the flag had never been typed.
	anyZoom bool

	// marks is parallel to Context.Highlights and indexed by Frame.Interval,
	// the same convention the highlight strip's own blocks/names/anchorX
	// slices use, so a lookup here can never disagree with which highlight
	// the render loop says is active.
	marks []routeMark
}

// placer builds the function that puts a point of proj into this painter's
// box, inset and all. Every placement in this panel goes through it, so the
// outline, the marks and the dot cannot be laid out by three subtly
// different arithmetics -- which is the same guarantee Prepare's original
// single closure gave, kept while the projection stopped being fixed.
func (p *routePainter) placer(proj route.Projection) (func(route.Point) (float64, float64), bool) {
	w, h := p.placeArea()
	place, ok := proj.Placer(w, h)
	if !ok {
		return nil, false
	}
	return func(pt route.Point) (float64, float64) {
		x, y := place(pt)
		return x + p.box.X + p.inset, y + p.box.Y + p.inset
	}, true
}

func (p *routePainter) placeAll(pts []route.Point, place func(route.Point) (float64, float64)) (xs, ys []float64) {
	xs = make([]float64, len(pts))
	ys = make([]float64, len(pts))
	for i, pt := range pts {
		xs[i], ys[i] = place(pt)
	}
	return xs, ys
}

// frameProjection is the view the map is drawn in on this frame: the
// whole-route projection, the active highlight's zoom, or -- while a zooming
// highlight is ramping in or out -- a blend of the two.
//
// It rides Frame.IntervalWeight, the same 0->1->0 ramp the highlight's own
// fade and the strip's block brightness already use, rather than a second
// ramp computed here. That is what makes the map finish arriving exactly as
// the highlight finishes lighting up, instead of the two drifting a frame
// apart in a way only a side-by-side comparison would catch.
//
// The second return says whether the result differs from the base view, so
// the common frame -- no highlight, or one that does not zoom -- can reuse
// the placements Prepare already computed instead of redoing 500 of them.
func (p *routePainter) frameProjection(f Frame) (route.Projection, bool) {
	if !p.anyZoom || f.Interval < 0 || f.Interval >= len(p.marks) {
		return p.base, false
	}
	m := p.marks[f.Interval]
	if !m.zoomOK || f.IntervalWeight <= 0 {
		return p.base, false
	}
	return route.Lerp(p.base, m.zoom, f.IntervalWeight), true
}

// Static draws the whole route, dim. The expensive part, drawn once.
//
// It draws NOTHING when a zoom is configured, and the outline moves into
// Dynamic instead. A static base is by definition one image reused for every
// frame, and a zooming route has a different outline on every frame of its
// transitions -- so there is no longer one image to cache. The alternative
// considered was to keep the static outline and paint over the box before
// redrawing it, which is worse in two ways: it needs the panel to know the
// background colour it is covering, and under --highlight-style wash that
// colour is the blend of two bases the loop computed, so the route's box
// would be the one rectangle in the frame that the wash did not reach.
//
// The cost is real and is confined to renders that asked for it: the outline
// is restroked every frame for the whole render, not only during a highlight,
// because Static has no way to draw it for the other frames only. That is the
// price of the flag, and it is off by default.
func (p *routePainter) Static(c *Canvas) {
	if len(p.xs) < 2 || p.anyZoom {
		return
	}
	p.drawBasemap(c, p.baseMap, nil, 0)
	c.Polyline(p.xs, p.ys, p.outlineW, c.Theme.Dim)
	p.drawCredit(c)
}

// drawBasemap composites the imagery for a viewport, washes it toward the
// background, and is a no-op when there is none.
//
// The wash is not decoration. Map imagery is busy, mid-toned and full of its
// own colour, and the route line, the covered prefix and the position dot all
// have to read against it -- so the map is pushed back toward the theme's
// background until it is context rather than content. Without it the panel
// becomes a map with a hard-to-find line on it, which inverts what the panel
// is for.
//
// front, when non-nil, is cross-faded over back at weight: the two images are
// the same ground at different scales, and a zoom moves the viewport between
// them. Drawing back first and front over it means a missing zoomed image
// simply leaves the whole-course one showing, magnified, rather than leaving
// a hole.
func (p *routePainter) drawBasemap(c *Canvas, back, front *basemapView, weight float64) {
	if p.suppressMap || (back == nil && front == nil) {
		return
	}
	minX, minY, spanX, spanY := p.viewport[0], p.viewport[1], p.viewport[2], p.viewport[3]
	// The whole-course view is skipped on the frames where the zoomed one
	// buries it. At full weight the viewport IS the zoomed view's own
	// rectangle, so that image covers the box edge to edge and opaquely,
	// and everything underneath is resampled only to be painted out -- at
	// the zoom's own magnification, which on a tight zoom is the single most
	// expensive thing a frame does. Those frames are also the bulk of a
	// zooming highlight: the viewport moves during two short ramps and sits
	// still between them.
	//
	// Only when it genuinely covers, and hides says what "genuinely" means.
	// A zoomed image that failed to arrive, or a partial cross-fade, leaves
	// the whole-course map showing through exactly as before -- which is the
	// fallback this pair of draws exists for.
	if !front.hides(p.box, minX, minY, spanX, spanY, weight) {
		back.drawInto(c, p.box, minX, minY, spanX, spanY, 1)
	}
	if front != nil && weight > 0 {
		front.drawInto(c, p.box, minX, minY, spanX, spanY, weight)
	}
	if p.mapDim > 0 {
		c.Rect(p.box, Fade(c.Theme.Background, p.mapDim))
	}
}

// drawCredit puts the provider's required attribution in the frame.
//
// It is drawn by the panel, every frame the imagery appears in, because a
// video has nowhere else to put it: there is no map widget, no corner
// control, no link to follow, and the file is distributed on its own. Every
// service whose terms were read for this requires visible credit, and one of
// them forbids moving it to end credits -- so it goes where the map is.
//
// Never conditional on space. A panel too small for the credit is a panel too
// small for the imagery, and the honest response is to keep the obligation
// and let the map be small.
func (p *routePainter) drawCredit(c *Canvas) {
	if p.suppressMap || p.baseMap == nil || p.mapCredit == "" {
		return
	}
	// The plate's margin, sized from the text it surrounds rather than
	// written as a pixel count -- the same rule outlineW, coveredW, dotR and
	// creditPx already follow in this file. A fixed 3 pixels is oversized
	// beside the clamped-small credit on a little panel and invisible at 4K.
	pad := maxf(2, p.creditPx*0.25)

	// Shrunk to fit the panel, once. The credit is a fixed sentence and the
	// box is whatever the layout gave this panel, so at a small size or a
	// square shape the string is simply wider than the box -- measured at
	// 1100 pixels in a 720-pixel box. Letting it overflow would push most of
	// the obligation off the panel and draw the rest across its neighbour.
	if p.creditFitted == 0 {
		px, err := c.FitTextSize(p.mapCredit, p.box.W-2*pad, p.creditPx)
		if err != nil || px <= 0 {
			px = p.creditPx
		}
		p.creditFitted = px
	}
	w, h, err := c.MeasureText(p.mapCredit, p.creditFitted)
	if err != nil {
		return
	}
	// ay of 0.5 with y as the text's CENTRE line, which is the convention
	// every other panel here uses -- gg's anchor moves the baseline DOWN by
	// ay*height, so anchoring at 1 puts the text below the box entirely.
	x := p.box.X + p.box.W - pad
	y := p.box.Y + p.box.H - pad - h/2

	// A plate behind the text, and Foreground on top of it.
	//
	// Attribution has to be legible over imagery this program has never
	// seen: a style may be pale, dark, or a photograph, and a credit drawn
	// in a chrome colour straight onto it is a credit that vanishes on some
	// maps and not others. Since it is an obligation rather than decoration
	// it gets guaranteed contrast instead of a colour that usually works --
	// a nearly-opaque wash of the theme's own background, which reads on any
	// imagery because it is not the imagery.
	c.Rect(Box{X: x - w - pad, Y: y - h/2 - pad, W: w + 2*pad, H: h + 2*pad},
		Fade(c.Theme.Background, 0.72))
	_ = c.Text(p.mapCredit, x, y, 1, 0.5, p.creditFitted, c.Theme.Foreground)
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

	// The view for THIS frame, and the placements that go with it. On the
	// common frame -- every frame of a render with no zoom, and every frame
	// outside a zooming highlight -- this is the projection Prepare already
	// resolved and the placements it already computed.
	place, xs, ys := p.place, p.xs, p.ys
	proj, zoomed := p.frameProjection(f)
	// The viewport in projected units, which is what the basemap is
	// composited against. Recorded even when nothing zoomed, so Static and
	// Dynamic composite against the same rectangle.
	w, h := p.placeArea()
	if vpX, vpY, vpW, vpH, ok := proj.CoverBox(w, h, p.box.W, p.box.H, p.inset, p.inset); ok {
		p.viewport = [4]float64{vpX, vpY, vpW, vpH}
	}
	if zoomed {
		zoomPlace, ok := p.placer(proj)
		if !ok {
			// A blend of two placeable projections is placeable, so this
			// is unreachable; falling back to the base view rather than
			// returning keeps a map on screen if it ever is reached.
			zoomed = false
		} else {
			place = zoomPlace
			xs, ys = p.placeAll(p.pts, zoomPlace)
		}
	}
	// Everything below draws through place/xs/ys, so it follows the frame's
	// view. Clipping is what makes that safe: a zoomed view puts most of the
	// route outside the box by design, and without this the outline would
	// stroke straight across the panels beside it. Only the zoom path pays
	// for the clip; the default render is untouched.
	if p.anyZoom {
		c.Clipped(p.box, func() { p.draw(c, f, place, xs, ys) })
		// The credit is drawn OUTSIDE the clip, and it has to be. Under a
		// clip mask gg draws a string by rasterizing it into a fresh
		// full-frame image and compositing that across the whole frame
		// through the mask -- 33 MB allocated and eight million pixels
		// touched at 4K, to place one short line inside one panel. Profiled
		// on a zooming render it was 47% of the frame, more than the map
		// and the route together.
		//
		// Nothing is given up for it. drawCredit fits the text to the box
		// and anchors it to the box's own bottom-right corner, so the plate
		// and the line are inside the box by construction rather than by
		// the clip -- which is also why this reads as putting the
		// obligation somewhere it cannot be trimmed, rather than as taking
		// a guard away.
		p.drawCredit(c)
		return
	}
	p.draw(c, f, place, xs, ys)
}

// draw is Dynamic's body, taking the frame's own placement so the outline,
// the covered prefix, the marks and the dot are all laid out by one view --
// the property that kept the dot on the line when the projection was fixed,
// preserved now that it is not.
func (p *routePainter) draw(c *Canvas, f Frame, place func(route.Point) (float64, float64), xs, ys []float64) {
	// The dim outline, when Static did not draw it. Under a zoom there is
	// no single image to cache it in (see Static), so it is restroked here,
	// first, exactly as Static would have laid it down -- everything below
	// is drawn over it in the same order either way.
	if p.anyZoom {
		var front *basemapView
		weight := 0.0
		if f.Interval >= 0 && f.Interval < len(p.zoomMaps) {
			front, weight = p.zoomMaps[f.Interval], f.IntervalWeight
		}
		p.drawBasemap(c, p.baseMap, front, weight)
		c.Polyline(xs, ys, p.outlineW, c.Theme.Dim)
	}

	// The covered portion is a prefix of the DRAWN points, so it is looked up
	// in those -- a prefix of the outline has to end on one of the outline's
	// own vertices or the bright line would not lie on the dim one. Before
	// the first fix, IndexAt returns -1 here exactly as it does against
	// p.all below, so nothing is drawn -- there is nothing covered yet.
	if drawn := route.IndexAt(p.pts, f.At); drawn >= 1 {
		c.Polyline(xs[:drawn+1], ys[:drawn+1], p.coveredW, c.Theme.Foreground)
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
		c.Polyline(xs[m.i0:m.i1+1], ys[m.i0:m.i1+1], p.coveredW, Fade(c.Theme.Highlight, alpha))
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
	x, y := place(p.all[cur])
	c.Circle(x, y, p.dotR, c.Theme.Accent)
}
