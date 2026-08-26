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
// Equirectangular, with longitude scaled by the cosine of the route's mean
// latitude. That scaling is what keeps a route the right SHAPE: a degree of
// longitude is a degree of latitude only at the equator, and at 56 degrees
// north it is barely half as wide, so plotting raw lat/lon stretches a route
// sideways by a factor of two. The approximation is excellent over the few
// kilometres a single activity covers.
type Projection struct {
	cosLat                   float64
	minX, minY, spanX, spanY float64
}

// Fit builds a projection covering pts, reporting false when there is nothing
// to draw -- fewer than two points, or every point at the same place, which is
// what a track recorded indoors with a stuck fix looks like.
func Fit(pts []Point) (Projection, bool) {
	if len(pts) < 2 {
		return Projection{}, false
	}

	var meanLat float64
	for _, p := range pts {
		meanLat += p.Lat
	}
	meanLat /= float64(len(pts))
	cosLat := math.Cos(meanLat * math.Pi / 180)

	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		x, y := p.Lon*cosLat, p.Lat
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	spanX, spanY := maxX-minX, maxY-minY
	if spanX <= 0 && spanY <= 0 {
		return Projection{}, false
	}
	return Projection{cosLat: cosLat, minX: minX, minY: minY, spanX: spanX, spanY: spanY}, true
}

// Place returns pixel coordinates for pts, fitted into a w by h box and
// centred in it.
//
// Aspect is PRESERVED -- one scale for both axes, the smaller of the two that
// would fit -- because a route stretched to fill its box is a different shape
// from the one that was run. An out-and-back becomes a loop, and a lap of a
// track becomes an oval of the wrong proportions.
//
// The y axis is flipped so north is up, which is the only orientation anyone
// reads a map in.
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
	if w <= 0 || h <= 0 {
		return nil, false
	}

	scale := math.Inf(1)
	if p.spanX > 0 {
		scale = math.Min(scale, w/p.spanX)
	}
	if p.spanY > 0 {
		scale = math.Min(scale, h/p.spanY)
	}
	if math.IsInf(scale, 1) {
		return nil, false
	}

	// Centre what is left over, so a route that is wide and flat sits in the
	// middle of a square box rather than against its top edge.
	offX := (w - p.spanX*scale) / 2
	offY := (h - p.spanY*scale) / 2

	return func(pt Point) (float64, float64) {
		return offX + (pt.Lon*p.cosLat-p.minX)*scale,
			offY + (p.minY+p.spanY-pt.Lat)*scale // flipped: north up
	}, true
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
