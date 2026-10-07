package panel

import (
	"context"
	"fmt"
	"image/color"
	"math"

	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"

	"github.com/wisborg/fitdash/internal/route"
)

// Route3D is how the route panel draws in perspective: the ground shaped by
// its heights and seen from a camera in the sky, the route lying over it.
//
// It is configuration, like a Panel: the camera itself depends on the box the
// layout gives the panel, so it is resolved in Prepare, by Camera.
type Route3D struct {
	// Heading is the compass bearing the camera looks along, in degrees, 0
	// north; ignored when AutoHeading, which takes whichever bearing shows
	// the route largest.
	Heading     float64
	AutoHeading bool
	// Pitch is how far below the horizontal the camera looks, in degrees,
	// and FOV its vertical field of view; 0 is perspective.DefaultFOV.
	Pitch, FOV float64
	// Exaggeration is how many times their height hills are drawn.
	Exaggeration float64
	// Air is the colour of the sky and of the haze the distance fades
	// into: the theme's background, set by the command as it sets the
	// local map's inks from the theme, so the map dissolves into the frame
	// rather than into a sky of its own -- a pale blue band across a night
	// theme's dashboard.
	Air color.RGBA
}

// Camera is the camera that takes in every point of pts in a picture of the
// given aspect, width over height.
//
// The one place it is worked out, for the panel and for the command's offer
// of the map and elevation the view will need: the two have to agree on the
// ground the camera sees, or the offer fetches one area and the render draws
// another.
func (r Route3D) Camera(pts []route.Point, aspect float64) (perspective.Camera, error) {
	coords := make([]osm.Coord, len(pts))
	for i, p := range pts {
		coords[i] = osm.Coord{Lat: p.Lat, Lon: p.Lon}
	}
	cam := perspective.Camera{Heading: r.Heading, Pitch: r.Pitch, FOV: r.FOV}
	if r.AutoHeading {
		return cam.BestHeading(coords, aspect)
	}
	return cam.Frame(coords, aspect)
}

// route3DMaxPoints is how many of the route's points are drawn in 3D: more
// than flat, because each segment is a straight line through the air between
// two points on the ground, and over a hill a long one cuts into it.
const route3DMaxPoints = 2000

// hiddenAlpha is how faintly a stretch of route a hill hides is drawn, and
// the dot while it is behind one: there, but not seen. Not drawn at all, the
// line would break off at the ridge and the dot vanish as if the recording
// had stopped -- a confident claim the data does not make.
const hiddenAlpha = 0.3

// Draper is a basemap that can draw itself in perspective; tilemap.Local is
// one. A provider that only serves flat images, such as a tile service,
// cannot: the ground's shape is not in its pictures.
type Draper interface {
	Drape(ctx context.Context, cam perspective.Camera, o perspective.Options, exaggeration float64) (*perspective.Picture, bool, error)
}

// route3DPainter is the route panel drawn in perspective. Everything placed
// through the camera is placed in Prepare, once; Dynamic only picks which of
// it to draw.
type route3DPainter struct {
	box Box
	pic *perspective.Picture
	// pts are the drawn points, all every fix, as for the flat painter;
	// xs, ys are pts placed in the frame, in is whether each is in the
	// picture at all and seen whether nearer ground leaves it in view.
	pts, all []route.Point
	xs, ys   []float64
	in, seen []bool

	outlineW, coveredW, dotR float64
	marks                    []routeMark

	mapDim       float64
	mapCredit    string
	mapNote      string
	creditPx     float64
	creditFitted float64
	suppressMap  bool
}

// prepare3D resolves the camera for this box, drapes the map, and places the
// route through it; ok is false, with why, when it cannot -- no basemap that
// can drape, a route that cannot be framed, a drape that failed -- and the
// caller draws the flat route instead, saying so.
func prepare3D(ctx *Context, box Box) (_ *route3DPainter, why string, ok bool) {
	d, ok := ctx.Basemap.(Draper)
	if !ok {
		return nil, "a 3d route needs a map drawn here, --basemap local", false
	}
	p := &route3DPainter{box: box}
	p.all = route.FromTrack(ctx.Track, len(ctx.Track.Samples)+1)
	p.pts = route.FromTrack(ctx.Track, route3DMaxPoints)
	if len(p.pts) < 2 {
		return nil, "the route has too few positions to draw in 3d", false
	}
	cam, err := ctx.Route3D.Camera(p.pts, box.W/box.H)
	if err != nil {
		return nil, "the route could not be framed in 3d: " + err.Error(), false
	}
	w, h := int(math.Ceil(box.W)), int(math.Ceil(box.H))
	c, cancel := context.WithTimeout(context.Background(), basemapTimeout)
	defer cancel()
	air := ctx.Route3D.Air
	pic, leveled, err := d.Drape(c, cam, perspective.Options{Width: w, Height: h, Sky: air, Horizon: air, Haze: air}, ctx.Route3D.Exaggeration)
	if err != nil {
		return nil, "the map could not be drawn in 3d: " + err.Error(), false
	}
	p.pic = pic

	p.xs, p.ys = make([]float64, len(p.pts)), make([]float64, len(p.pts))
	p.in, p.seen = make([]bool, len(p.pts)), make([]bool, len(p.pts))
	for i, pt := range p.pts {
		x, y, in, seen := p.place(pt)
		p.xs[i], p.ys[i], p.in[i], p.seen[i] = x, y, in, seen
	}

	unit := math.Min(box.W, box.H)
	p.outlineW = maxf(1.5, unit*0.008)
	p.coveredW = maxf(2.5, unit*0.014)
	p.dotR = maxf(3, unit*0.022)
	p.creditPx = maxf(9, unit*0.05)
	p.mapDim = clampWeight(ctx.BasemapDim)
	p.mapCredit = ctx.Basemap.Attribution()

	p.mapNote = fmt.Sprintf("drawn in 3d, looking %s (%.0f°) from %.1f km, %g° down", compass(cam.Heading), cam.Heading, cam.Distance/1000, cam.Pitch)
	if ctx.Route3D.Exaggeration != 1 {
		p.mapNote += fmt.Sprintf(", hills %g times their height", ctx.Route3D.Exaggeration)
	}
	if leveled {
		p.mapNote += "; the ground is drawn level: there is no elevation for it"
	}

	// The highlights' stretches, as the flat painter resolves them and from
	// the same drawn list -- but with no zoom: a 3d camera framed on one
	// stretch would fly between views the map was not draped for.
	if len(ctx.Highlights) > 0 {
		start := ctx.Timeline.Start()
		p.marks = make([]routeMark, len(ctx.Highlights))
		zoomed := false
		for i, hl := range ctx.Highlights {
			i0, i1, ok := route.SpanIndices(p.pts, start.Add(hl.From), start.Add(hl.To))
			if ok {
				i0, i1 = extendMarkAlongPolyline(p.xs, p.ys, i0, i1, unit*minMarkLengthFraction)
			}
			p.marks[i] = routeMark{ok: ok, i0: i0, i1: i1}
			zoomed = zoomed || hl.Zoom
		}
		if zoomed {
			p.mapNote += "; a highlight's zoom is not drawn in 3d, where the whole route stays in view"
		}
	}
	return p, "", true
}

// place is where pt is drawn in the frame, whether it is in the picture at
// all, and whether it is seen there.
func (p *route3DPainter) place(pt route.Point) (x, y float64, in, seen bool) {
	c := osm.Coord{Lat: pt.Lat, Lon: pt.Lon}
	x, y, in = p.pic.Project(c)
	if !in {
		return 0, 0, false, false
	}
	_, _, seen = p.pic.Locate(c)
	return x + p.box.X, y + p.box.Y, true, seen
}

// Static draws the draped map, washed as the flat map is, the whole route
// dim over it, and the credit.
func (p *route3DPainter) Static(c *Canvas) {
	if !p.suppressMap {
		img := p.pic.Image
		c.Image(img, Box{X: p.box.X, Y: p.box.Y, W: float64(img.Bounds().Dx()), H: float64(img.Bounds().Dy())}, 1)
		if p.mapDim > 0 {
			c.Rect(p.box, Fade(c.Theme.Background, p.mapDim))
		}
	}
	p.stroke(c, 0, len(p.pts)-1, p.outlineW, c.Theme.Dim, 1)
	if !p.suppressMap {
		drawMapCredit(c, p.box, p.mapCredit, p.creditPx, &p.creditFitted)
	}
}

// Dynamic draws the covered part of the route, every highlight's stretch and
// the current position, in the flat painter's order and for its reasons --
// see routePainter.Dynamic.
func (p *route3DPainter) Dynamic(c *Canvas, f Frame) {
	if drawn := route.IndexAt(p.pts, f.At); drawn >= 1 {
		p.stroke(c, 0, drawn, p.coveredW, c.Theme.Foreground, 1)
	}
	for i, m := range p.marks {
		if !m.ok {
			continue
		}
		alpha := highlightRestAlpha
		if i == f.Interval {
			alpha = restAlpha(f.IntervalWeight)
		}
		p.stroke(c, m.i0, m.i1, p.coveredW, c.Theme.Highlight, alpha)
	}
	cur := route.IndexAt(p.all, f.At)
	if cur < 0 {
		return
	}
	x, y, in, seen := p.place(p.all[cur])
	if !in {
		return
	}
	alpha := 1.0
	if !seen {
		alpha = hiddenAlpha
	}
	c.Circle(x, y, p.dotR, Fade(c.Theme.Accent, alpha))
}

// stroke draws the route from point i0 to i1, at alpha where it is seen and
// fainter where a hill hides it, leaving out any segment with an end outside
// the picture. Runs of segments alike are one polyline, so the line is
// joined where it bends rather than drawn as separate sticks.
func (p *route3DPainter) stroke(c *Canvas, i0, i1 int, width float64, col color.Color, alpha float64) {
	style := func(i int) int { // of the segment from i to i+1: 0 not drawn, 1 hidden, 2 seen
		switch {
		case !p.in[i] || !p.in[i+1]:
			return 0
		case p.seen[i] && p.seen[i+1]:
			return 2
		}
		return 1
	}
	for i := i0; i < i1; {
		s := style(i)
		j := i + 1
		for j < i1 && style(j) == s {
			j++
		}
		switch s {
		case 2:
			c.Polyline(p.xs[i:j+1], p.ys[i:j+1], width, Fade(col, alpha))
		case 1:
			c.Polyline(p.xs[i:j+1], p.ys[i:j+1], width, Fade(col, alpha*hiddenAlpha))
		}
		i = j
	}
}

// compass is a bearing as the nearest of eight compass points.
func compass(deg float64) string {
	points := []string{"north", "north-east", "east", "south-east", "south", "south-west", "west", "north-west"}
	return points[int(math.Mod(math.Round(deg/45), 8)+8)%8]
}

// BasemapNote says how the route was drawn in 3d, and anything it declined.
func (p *route3DPainter) BasemapNote() string { return p.mapNote }

// BasemapDrew is true: a 3d painter exists only once its map was draped.
func (p *route3DPainter) BasemapDrew() bool { return true }

// SuppressBasemap leaves the draped map out of the next draws, or puts it
// back; see BasemapSuppressor.
func (p *route3DPainter) SuppressBasemap(v bool) { p.suppressMap = v }
