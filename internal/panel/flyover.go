package panel

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"

	"github.com/wisborg/fitdash/internal/route"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// The flyover's camera, past what Route3D says: how much video it takes to
// follow the activity's position and its turns, and to come down from the
// overview at the start and go back up at the end. See route.FlightOptions.
const (
	flyoverSteady = time.Second
	flyoverTurn   = 3 * time.Second
	flyoverSwoop  = 3 * time.Second
	// flyoverNamesSmooth is how much video either side each name's
	// strength is averaged over: long enough that no name changes faster
	// than the eye follows, short enough that one still arrives with its
	// place. See perspective.NamePlan.
	flyoverNamesSmooth = 300 * time.Millisecond
)

// Flyer is a basemap that can lay itself out for a camera flying over it;
// tilemap.Local is one.
type Flyer interface {
	Flight(ctx context.Context, region slice.Bounds, exaggeration float64) (tilemap.FlightMap, error)
}

// Flight is the camera for every frame of a flyover of the route whose GPS
// fixes are fixes, framed for a picture of the given aspect, with the
// ground's heights from ground (nil for none). The one place the flight is
// worked out, for the panel and for the command's offer of the map the
// flight will see: the two have to agree.
func (r Route3D) Flight(fixes, drawn []route.Point, tl Timeline, aspect float64, ground route.Ground) ([]perspective.Camera, error) {
	overview, err := r.Camera(drawn, aspect)
	if err != nil {
		return nil, err
	}
	if ground == nil {
		ground = func(osm.Coord) (float64, bool) { return 0, false }
	}
	cams := route.Flight(fixes, tl.Frames(), tl.At, overview, ground, route.FlightOptions{
		Distance: r.Distance, SteadyDistance: r.SteadyDistance, Pitch: r.Pitch, FOV: r.FOV, FPS: tl.FPS(),
		Steady: flyoverSteady, Turn: flyoverTurn, Rise: r.Rise, Swoop: flyoverSwoop,
	})
	if cams == nil {
		return nil, fmt.Errorf("the route has too few positions to fly over")
	}
	return cams, nil
}

// FlightGround is the ground every camera of a flight sees in a picture of
// the given aspect: what the map and elevation have to cover.
func FlightGround(cams []perspective.Camera, aspect float64) slice.Bounds {
	var b slice.Bounds
	for i, c := range cams {
		a := tilemap.DrapeArea(c, aspect)
		if i == 0 {
			b = a
			continue
		}
		b.West, b.South = math.Min(b.West, a.West), math.Min(b.South, a.South)
		b.East, b.North = math.Max(b.East, a.East), math.Max(b.North, a.North)
	}
	return b
}

// flyoverPainter is the route panel as a flyover: a camera following the
// activity over the map draped on the ground, a picture a frame.
//
// It is the one painter whose picture is drawn per frame, because its
// camera moves every frame. What can be worked out once is, in Prepare: the
// camera of every frame, and the map laid out as a pyramid of tiles over the
// ground the whole flight sees, each drawn once when first seen. Dynamic
// draws the frame's picture from them and places the route through its
// camera. The pyramid keeps the tiles it has drawn, which is the one thing
// that changes in it across frames: a cache, which changes no picture -- a
// tile is drawn the same whenever it is first wanted.
type flyoverPainter struct {
	box    Box
	inks   *RouteInks // ctx.RouteInks
	cams   []perspective.Camera
	flight tilemap.FlightMap
	air    perspective.Options
	pts    []route.Point // drawn
	all    []route.Point // every fix, for the position
	marks  []routeMark

	outlineW, coveredW, dotR float64
	mapCredit                string
	creditPx, creditFitted   float64
	mapNote                  string
	suppressMap              bool
}

// prepareFlyover plans the flight and lays out the map for it; ok is false,
// with why, when it cannot, and the caller draws the 3d still or the flat
// route instead, saying so.
func prepareFlyover(ctx *Context, box Box) (_ *flyoverPainter, why string, ok bool) {
	f, ok := ctx.Basemap.(Flyer)
	if !ok {
		return nil, "a flyover needs a map drawn here, --basemap local", false
	}
	p := &flyoverPainter{box: box, inks: ctx.RouteInks}
	p.all = route.FromTrack(ctx.Track, len(ctx.Track.Samples)+1)
	p.pts = route.FromTrack(ctx.Track, route3DMaxPoints)
	if len(p.pts) < 2 {
		return nil, "the route has too few positions to fly over", false
	}
	aspect := box.W / box.H
	// The cameras without the ground's heights first: where they look does
	// not depend on them, and that is the ground to lay the map out over.
	cams, err := ctx.Route3D.Flight(p.all, p.pts, ctx.Timeline, aspect, nil)
	if err != nil {
		return nil, err.Error(), false
	}
	c, cancel := context.WithTimeout(context.Background(), basemapTimeout)
	defer cancel()
	p.flight, err = f.Flight(c, FlightGround(cams, aspect), ctx.Route3D.Exaggeration)
	if err != nil {
		return nil, "the map could not be laid out for a flyover: " + err.Error(), false
	}
	if p.cams, err = ctx.Route3D.Flight(p.all, p.pts, ctx.Timeline, aspect, p.flight.Ground); err != nil {
		return nil, err.Error(), false
	}
	air := ctx.Route3D.Air
	p.air = perspective.Options{Width: int(math.Ceil(box.W)), Height: int(math.Ceil(box.H)), Sky: air, Horizon: air, Haze: air}

	// The names, planned over the whole flight before any frame is drawn,
	// so none flickers. Without a plan each picture works out its own, as
	// it can, and the note says so.
	smooth := int(math.Round(flyoverNamesSmooth.Seconds() * ctx.Timeline.FPS()))
	namesErr := p.flight.PlanNames(context.Background(), p.cams, p.air, smooth)

	unit := math.Min(box.W, box.H)
	p.outlineW = maxf(1.5, unit*0.008)
	p.coveredW = maxf(2.5, unit*0.014)
	p.dotR = maxf(3, unit*0.022)
	p.creditPx = maxf(9, unit*0.05)
	p.mapCredit = p.flight.Attribution()

	// The distance followed from past the swoops: one, or the range of
	// them when each frame chose its own.
	swoop := min(int(math.Round(flyoverSwoop.Seconds()*ctx.Timeline.FPS())), len(p.cams)/3)
	near, far := math.Inf(1), 0.0
	for _, c := range p.cams[swoop : len(p.cams)-swoop] {
		near, far = math.Min(near, c.Distance), math.Max(far, c.Distance)
	}
	follow := "from " + formatFollow(near)
	if far > near*1.05 {
		follow = "from " + formatFollow(near) + " to " + formatFollow(far)
	}
	p.mapNote = fmt.Sprintf("flown over in 3d, following %s, %g° down", follow, ctx.Route3D.Pitch)
	switch {
	case ctx.Route3D.Distance > 0:
	case ctx.Route3D.SteadyDistance:
		p.mapNote += " (one distance, chosen for how fast the ground moves)"
	default:
		p.mapNote += " (the distance chosen in each frame for how fast the ground moves there)"
	}
	if ctx.Route3D.Rise > 0 {
		p.mapNote += fmt.Sprintf(", gliding over the ground's rises over %s", ctx.Route3D.Rise)
	}
	if ctx.Route3D.Exaggeration != 1 {
		p.mapNote += fmt.Sprintf(", hills %g times their height", ctx.Route3D.Exaggeration)
	}
	if p.flight.Leveled() {
		p.mapNote += "; the ground is drawn level: there is no elevation for it"
	}
	if namesErr != nil {
		p.mapNote += "; the names could not be planned, and may flicker: " + namesErr.Error()
	}
	if len(ctx.Highlights) > 0 {
		start := ctx.Timeline.Start()
		p.marks = make([]routeMark, len(ctx.Highlights))
		zoomed := false
		for i, hl := range ctx.Highlights {
			i0, i1, ok := route.SpanIndices(p.pts, start.Add(hl.From), start.Add(hl.To))
			p.marks[i] = routeMark{ok: ok, i0: i0, i1: i1}
			zoomed = zoomed || hl.Zoom
		}
		if zoomed {
			p.mapNote += "; a highlight's zoom is not drawn in a flyover, whose camera follows the activity already"
		}
	}
	return p, "", true
}

// Static fits the map's credit to the box, once; everything else is drawn
// per frame, over the box.
func (p *flyoverPainter) Static(c *Canvas) {
	drawMapCredit(c, p.box, p.mapCredit, p.creditPx, &p.creditFitted)
}

// Dynamic draws the frame's picture, the route over it through the frame's
// camera -- the whole of it dim, the part covered bright, each highlight's
// stretch -- the current position, and the credit.
func (p *flyoverPainter) Dynamic(c *Canvas, f Frame) {
	if p.suppressMap || f.Index < 0 || f.Index >= len(p.cams) {
		return
	}
	pic, err := p.flight.Picture(context.Background(), f.Index, p.cams[f.Index], p.air)
	if err != nil {
		// A frame whose map could not be drawn is not drawn as something
		// else: the box keeps the background, and the route is still
		// placed -- through nothing, so it is not. The summary's coverage
		// says the map was short.
		return
	}
	img := pic.Image
	c.Image(img, Box{X: p.box.X, Y: p.box.Y, W: float64(img.Bounds().Dx()), H: float64(img.Bounds().Dy())}, 1)

	n := len(p.pts)
	xs, ys := make([]float64, n), make([]float64, n)
	in, seen := make([]bool, n), make([]bool, n)
	for i, pt := range p.pts {
		xs[i], ys[i], in[i], seen[i] = p.place(pic, pt)
	}
	s := route3DPainter{xs: xs, ys: ys, in: in, seen: seen}
	s.stroke(c, 0, n-1, p.outlineW, routeInks(p.inks, true, c.Theme).Dim, 1)
	if drawn := route.IndexAt(p.pts, f.At); drawn >= 1 {
		s.stroke(c, 0, drawn, p.coveredW, routeInks(p.inks, true, c.Theme).Foreground, 1)
	}
	for i, m := range p.marks {
		if !m.ok {
			continue
		}
		alpha := highlightRestAlpha
		if i == f.Interval {
			alpha = restAlpha(f.IntervalWeight)
		}
		s.stroke(c, m.i0, m.i1, p.coveredW, routeInks(p.inks, true, c.Theme).Highlight, alpha)
	}
	if cur := route.IndexAt(p.all, f.At); cur >= 0 {
		if x, y, in, seen := p.place(pic, p.all[cur]); in {
			alpha := 1.0
			if !seen {
				alpha = hiddenAlpha
			}
			c.Circle(x, y, p.dotR, Fade(routeInks(p.inks, true, c.Theme).Accent, alpha))
		}
	}
	fitted := p.creditFitted
	drawMapCredit(c, p.box, p.mapCredit, p.creditPx, &fitted)
}

// place is where pt is drawn in the frame through pic's camera, whether it
// is in the picture at all, and whether it is seen there.
func (p *flyoverPainter) place(pic *perspective.Picture, pt route.Point) (x, y float64, in, seen bool) {
	co := osm.Coord{Lat: pt.Lat, Lon: pt.Lon}
	x, y, in = pic.Project(co)
	if !in {
		return 0, 0, false, false
	}
	_, _, seen = pic.Locate(co)
	return x + p.box.X, y + p.box.Y, true, seen
}

// formatFollow is a distance followed from: metres under a kilometre, a
// tenth of one under a hundred, whole ones above.
func formatFollow(m float64) string {
	switch {
	case m < 1000:
		return fmt.Sprintf("%.0f m", m)
	case m < 100_000:
		return fmt.Sprintf("%.1f km", m/1000)
	}
	return fmt.Sprintf("%.0f km", m/1000)
}

// BasemapNote says how the route was flown over, and anything declined.
func (p *flyoverPainter) BasemapNote() string { return p.mapNote }

// BasemapDrew is true: a flyover exists only once its map was laid out.
func (p *flyoverPainter) BasemapDrew() bool { return true }

// SuppressBasemap leaves the flyover out of the next draws, or puts it
// back; see BasemapSuppressor. The whole box is the map, and the route on
// it is placed through the picture, so suppressed the panel draws nothing
// -- which also spares the pictures of the frames sampled.
func (p *flyoverPainter) SuppressBasemap(v bool) { p.suppressMap = v }
