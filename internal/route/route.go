// Package route projects a GPS track into pixels.
//
// It deliberately depends on nothing else in this project -- not the panel
// contract, not the drawing surface -- so the geometry can be tested as
// geometry. The panel that draws a route lives with the other panels and uses
// what is here.
//
// Downsampling lives here rather than upstream in the activity library, and
// that is a deliberate boundary: how many points survive depends on how many
// PIXELS the route has to be drawn in, which is a presentation question the
// library has no business answering.
package route

import (
	"math"
	"sort"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// Point is one position on a route, with the instant it was recorded.
type Point struct {
	Lat, Lon float64
	Time     time.Time
}

// DefaultMaxPoints caps how many points are DRAWN.
//
// A route is drawn a few hundred pixels across at most, so beyond roughly this
// many points consecutive samples land on the same pixel and the extra
// segments are invisible work -- repeated on every frame of the render, which
// is where it stops being free. An hour at 1 Hz is 3,600 samples; keeping 500
// of them changes nothing anyone can see.
//
// It applies to the OUTLINE only, and that distinction was a bug before it was
// a comment. Looking the current position up in the downsampled list quantises
// it: a four-hour ride's 14,400 fixes reduced to 500 leaves the dot frozen for
// 29 seconds and then jumping several hundred metres, and even a 25-minute run
// moves it in 3-second steps. The fixes and the drawn points are now separate
// things, and only the drawing is thinned.
//
// SpanIndices below looks like it should follow the same rule and does not,
// deliberately: a highlighted SPAN of the route has to be drawn as a stretch
// of the OUTLINE itself, and the outline only exists at this thinned
// resolution. A mark resolved against every fix would name a stretch the
// outline never draws, and the coloured line would float off the dim one
// under it. The position dot and a highlight's span are answering different
// questions -- "where is it right now" versus "which part of the drawn line
// is this" -- and each is right to use the list the other must not.
const DefaultMaxPoints = 500

// FromTrack extracts the route, keeping at most maxPoints of it.
//
// Samples without a GPS fix are skipped rather than interpolated across. The
// resulting gap is drawn as a straight line between the fixes either side,
// which is honest for a route outline -- it is visibly a chord rather than a
// path -- and is the alternative to either inventing positions or breaking the
// outline into disconnected pieces.
func FromTrack(track *fitactivity.Track, maxPoints int) []Point {
	if track == nil {
		return nil
	}
	if maxPoints < 2 {
		maxPoints = 2
	}

	fixes := make([]Point, 0, len(track.Samples))
	for _, s := range track.Samples {
		if !s.HasGPS {
			continue
		}
		fixes = append(fixes, Point{Lat: s.Lat, Lon: s.Lon, Time: s.Time})
	}
	if len(fixes) <= maxPoints {
		return fixes
	}

	// Uniform stride, with the LAST point always kept. Dropping the end of a
	// route would leave the outline stopping short of where the activity
	// finished, and on a loop it would leave a visible gap at the join.
	out := make([]Point, 0, maxPoints)
	stride := float64(len(fixes)-1) / float64(maxPoints-1)
	for i := 0; i < maxPoints-1; i++ {
		out = append(out, fixes[int(float64(i)*stride)])
	}
	return append(out, fixes[len(fixes)-1])
}

// Projection maps a route's coordinates into a plane, north up.
//
// Web Mercator, read from internal/tilemap, which owns it. This used to be an
// equirectangular projection with longitude scaled by the cosine of the
// route's mean latitude -- a linear approximation of Mercator about that
// latitude, and an excellent one over the few kilometres an activity covers.
// It was replaced because a route is going to be drawn over map imagery, and
// every tile service on earth is defined in Mercator: an approximation that
// is excellent in the middle of a twenty-kilometre route is several pixels
// out at its ends, which is the difference between a line on the road and a
// line beside it.
//
// The change SIMPLIFIED this type rather than complicating it. Mercator has
// no per-view parameter, so there is no longer a mean latitude two views
// could disagree about -- which is why fitting a sub-range is now just Fit
// over the subset, and the separate constructor that existed to share the
// old scaling is gone.
type Projection struct {
	minX, minY, spanX, spanY float64
}

// Fit builds a projection covering pts, reporting false when there is nothing
// to draw -- fewer than two points, or every point at the same place, which is
// what a track recorded indoors with a stuck fix looks like.
func Fit(pts []Point) (Projection, bool) {
	if len(pts) < 2 {
		return Projection{}, false
	}

	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		x, y := tilemap.Project(p.Lat, p.Lon)
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	spanX, spanY := maxX-minX, maxY-minY
	if spanX <= 0 && spanY <= 0 {
		return Projection{}, false
	}
	return Projection{minX: minX, minY: minY, spanX: spanX, spanY: spanY}, true
}

// Place returns pixel coordinates for pts, fitted into a w by h box and
// centred in it.
//
// Aspect is PRESERVED -- one scale for both axes, the smaller of the two that
// would fit -- because a route stretched to fill its box is a different shape
// from the one that was run. An out-and-back becomes a loop, and a lap of a
// track becomes an oval of the wrong proportions. Mercator is conformal, so
// one scale for both axes is also what keeps angles right.
//
// North is up with no flip anywhere, because the projection's y axis already
// runs south -- see tilemap's note on why it is defined that way.
func (p Projection) Place(pts []Point, w, h float64) (xs, ys []float64) {
	at, ok := p.Placer(w, h)
	if !ok || len(pts) == 0 {
		return nil, nil
	}
	xs = make([]float64, len(pts))
	ys = make([]float64, len(pts))
	for i, pt := range pts {
		xs[i], ys[i] = at(pt)
	}
	return xs, ys
}

// Placer returns the function Place applies to each point, so a caller can
// place ONE point without building a slice for it.
//
// The route panel needs exactly that: the position dot belongs at the current
// fix, which is not one of the downsampled points the outline is drawn from.
// Sharing the closure rather than reimplementing the arithmetic is what keeps
// the dot on the line it is supposed to be travelling along.
func (p Projection) Placer(w, h float64) (func(Point) (x, y float64), bool) {
	scale, ok := p.scaleFor(w, h)
	if !ok {
		return nil, false
	}

	// Centre what is left over, so a route that is wide and flat sits in the
	// middle of a square box rather than against its top edge.
	offX := (w - p.spanX*scale) / 2
	offY := (h - p.spanY*scale) / 2

	return func(pt Point) (float64, float64) {
		x, y := tilemap.Project(pt.Lat, pt.Lon)
		return offX + (x-p.minX)*scale, offY + (y-p.minY)*scale
	}, true
}

// scaleFor is the pixels-per-projected-unit that fits this projection into a
// w by h box, and the one place that arithmetic lives.
//
// Placer and CoverProjected both need it and used to compute it separately,
// along with a third copy that has since gone. Three answers to one question
// is how a basemap ends up a few pixels off the route it was fetched for --
// the two calls have to agree exactly or the imagery sits beside the line
// instead of under it, and nothing but a pixel comparison would notice.
func (p Projection) scaleFor(w, h float64) (float64, bool) {
	if w <= 0 || h <= 0 {
		return 0, false
	}
	scale := math.Inf(1)
	if p.spanX > 0 {
		scale = math.Min(scale, w/p.spanX)
	}
	if p.spanY > 0 {
		scale = math.Min(scale, h/p.spanY)
	}
	if math.IsInf(scale, 1) || scale <= 0 {
		return 0, false
	}
	return scale, true
}

// CoverProjected is the rectangle of the projected plane that a w by h box
// covers when this projection is fitted into it.
//
// It is the projection's own bounds WIDENED to the box. Placer preserves
// aspect and centres what is left over, so the box shows more ground than the
// route's own extent on one axis -- and imagery fetched for the route's
// extent would sit letterboxed inside the box with the line flush against its
// edges. Asking what the box covers is what lets a basemap reach the panel's
// edges with the route inset in the middle of it.
//
// Reports false on a box with no area, matching Placer: a caller that got a
// placer gets a cover too, and one that did not has nothing to draw either
// way. It used to fall back silently to the un-widened bounds, which was a
// second answer to the same question that only differed when something had
// already gone wrong.
func (p Projection) CoverProjected(w, h float64) (minX, minY, spanX, spanY float64, ok bool) {
	scale, ok := p.scaleFor(w, h)
	if !ok {
		return 0, 0, 0, 0, false
	}
	halfX, halfY := w/scale/2, h/scale/2
	cx, cy := p.minX+p.spanX/2, p.minY+p.spanY/2
	return cx - halfX, cy - halfY, 2 * halfX, 2 * halfY, true
}

// IndexAt returns the last point recorded at or before at, or -1 when the
// whole route is still ahead.
//
// Binary search rather than a scan, because this is called once per frame and
// a scan over 500 points times 46,000 frames is 23 million comparisons to
// answer a question with a logarithmic answer. Points are time-ordered because
// the samples they came from are.
func IndexAt(pts []Point, at time.Time) int {
	i := sort.Search(len(pts), func(i int) bool { return pts[i].Time.After(at) })
	return i - 1
}

// SpanIndices locates the drawn vertices of pts -- the THINNED, drawn list,
// never the full fix list; see DefaultMaxPoints's own note on why this is
// the opposite rule from IndexAt's -- that fall inside [from, to).
//
// GUARANTEE: when ok is true, i0 < i1 and pts[i0:i1+1] is a genuinely
// drawable polyline -- at least two vertices, never one. A caller may rely
// on that without checking it again.
//
// i0 is the first index with pts[i].Time >= from; i1 is the last index with
// pts[i].Time < to.
//
//   - Two or more drawn vertices already fall inside the span (i0 < i1):
//     they are returned as they are.
//
//   - Exactly one drawn vertex falls inside the span (i0 == i1): a single
//     point is not a polyline, so it is extended to include a neighbour --
//     preferring the vertex after the span, the same direction time in the
//     span itself runs, and falling back to the one before it when the span
//     reaches the route's own last point. This is the case a one-vertex
//     fixture cannot even pose: it only shows up once a highlight is
//     shorter than roughly the outline's own stride, which is the ordinary
//     case for a short highlight over a long activity (at DefaultMaxPoints
//     over a four-hour ride, one vertex is 29 seconds from the next), and
//     which no fixture under DefaultMaxPoints can ever produce, because
//     under the cap the stride is one sample.
//
//   - No drawn vertex falls inside the span at all (i0 > i1): the whole
//     span lies strictly between two consecutive vertices. Rather than
//     drawing nothing, the single chord [i0-1, i0] that straddles the span
//     is returned instead: the mark is placed at the resolution the
//     outline was actually drawn at, honestly, the same minBlockFraction
//     reasoning the highlight strip already applies to a block that would
//     otherwise round to nothing.
//
// Both extensions above require a neighbour to exist. When pts has fewer
// than two points there is no chord to build under any span, and ok is
// false -- but this cannot happen through RoutePanel or cmd's own use of
// this function, both of which already require at least two drawn points
// before calling it at all.
//
// When the span lies entirely before the first point or entirely after the
// last, ok is false and i0/i1 are meaningless. Widening to the nearest chord
// here would draw a mark claiming the route passed through that place, which
// is a claim about position this package does not invent (see FromTrack's own
// note on why a GPS gap is drawn as a visible chord rather than papered over).
func SpanIndices(pts []Point, from, to time.Time) (i0, i1 int, ok bool) {
	n := len(pts)
	i0 = sort.Search(n, func(i int) bool { return !pts[i].Time.Before(from) })
	i1 = sort.Search(n, func(i int) bool { return !pts[i].Time.Before(to) }) - 1

	switch {
	case i0 < i1:
		return i0, i1, true
	case i0 == i1:
		// Exactly one drawn vertex inside the span. Extend to a neighbour
		// so the result is a drawable polyline rather than a single point
		// -- see the doc comment above for which side and why.
		if i1+1 < n {
			return i0, i1 + 1, true
		}
		if i0-1 >= 0 {
			return i0 - 1, i1, true
		}
		// n == 1: the whole list is a single point, so no chord can ever
		// be built from it.
		return 0, 0, false
	case i0 > 0 && i0 < n:
		// The bracketing chord: the span sits strictly between pts[i0-1] and
		// pts[i0], both of which exist.
		return i0 - 1, i0, true
	default:
		// i0 == 0 means even the first point is at or after `from`, so the
		// span ends before the route begins. i0 == n means no point reaches
		// `from` at all, so the span starts after the route ends. Either
		// way there is nothing to bracket.
		return 0, 0, false
	}
}

// Lerp blends two projections of the same plane, for animating a view from
// one to the other. t <= 0 returns a, t >= 1 returns b.
//
// Both are in the same coordinate system by construction -- Mercator has no
// per-view parameter -- so blending their bounds is meaningful with nothing to
// check first. That was not true of the projection this replaced, where two
// views fitted independently had different longitude scalings and blending
// them sheared the shape as it scaled.
//
// The CENTRE moves linearly and the SPAN scales geometrically, which is not
// an affectation: a viewport whose width shrinks linearly from 4km to 200m
// covers most of that ground in the first few frames and then crawls, because
// what the eye reads as "zoom speed" is the RATIO between successive frames,
// not the difference. Interpolating the span geometrically holds that ratio
// constant, so the zoom appears to travel at an even rate throughout. It is
// the same reason a map application's zoom control is exponential.
//
// An axis with no extent -- a route stretch running exactly east-west has no
// spanY -- cannot be scaled geometrically, since no number of doublings
// reaches zero from a positive start. Those fall back to linear, which is
// well defined at both ends and correct for the only case that produces them.
func Lerp(a, b Projection, t float64) Projection {
	switch {
	case !(t > 0): // NaN included: an undefined weight animates nothing
		return a
	case t >= 1:
		return b
	}
	cx := lerp(a.minX+a.spanX/2, b.minX+b.spanX/2, t)
	cy := lerp(a.minY+a.spanY/2, b.minY+b.spanY/2, t)
	spanX := scaleLerp(a.spanX, b.spanX, t)
	spanY := scaleLerp(a.spanY, b.spanY, t)
	return Projection{
		minX:  cx - spanX/2,
		minY:  cy - spanY/2,
		spanX: spanX,
		spanY: spanY,
	}
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// scaleLerp interpolates a span geometrically, falling back to linear when
// either end is zero -- see Lerp.
func scaleLerp(a, b, t float64) float64 {
	if a <= 0 || b <= 0 {
		return lerp(a, b, t)
	}
	return a * math.Pow(b/a, t)
}
