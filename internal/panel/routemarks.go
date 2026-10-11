package panel

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/wisborg/osmbase/perspective"
	"github.com/wisborg/osmbase/render"

	"github.com/wisborg/fitdash/internal/route"
)

// RouteEnds is what a route view marks its start and finish with.
type RouteEnds int

const (
	// EndsPin is a green balloon pin with a "play" triangle at the start
	// and a red one with a checkered head at the finish -- one pin, half of
	// each, where the route ends where it began. It is the zero value, so a
	// Context that says nothing gets the pins.
	EndsPin RouteEnds = iota
	// EndsNone marks neither, as every route view did before there were
	// pins.
	EndsNone
)

// ParseRouteEnds reads --route-ends.
func ParseRouteEnds(s string) (RouteEnds, error) {
	switch s {
	case "pin":
		return EndsPin, nil
	case "none":
		return EndsNone, nil
	}
	return 0, fmt.Errorf("--route-ends %q is invalid; use pin or none", s)
}

// RouteIcon is what a route view marks the current position with.
type RouteIcon int

const (
	// IconDot is a dot in the theme's accent, the zero value: it has no
	// direction, which suits a run that doubles back on itself.
	IconDot RouteIcon = iota
	// IconArrow is a navigation arrow pointing along the direction of
	// travel.
	IconArrow
	// IconPlane is an aircraft seen from above, its nose along the track.
	IconPlane
)

// ParseRouteIcon reads --route-position; "auto" is a plane on a flight and a
// dot on anything else.
func ParseRouteIcon(s string, flight bool) (RouteIcon, error) {
	switch s {
	case "auto":
		if flight {
			return IconPlane, nil
		}
		return IconDot, nil
	case "dot":
		return IconDot, nil
	case "arrow":
		return IconArrow, nil
	case "plane":
		return IconPlane, nil
	}
	return 0, fmt.Errorf("--route-position %q is invalid; use auto, dot, arrow or plane", s)
}

// Sizes of the route's marks, in the position dot's radius: a pin's head big
// enough for its symbol to read, and an icon long enough for its shape to.
const (
	pinHead   = 1.9
	arrowSize = 1.9
	planeSize = 2.6

	// pinRise is how far a pin reaches above its place, in its head's
	// radius: osmbase's pin stands its head's centre 2.1 heads up, and the
	// head, its outline and a little air go above that.
	pinRise = 3.4

	// routeDotFraction is the position dot's radius in a route view, as a
	// fraction of the box's smaller side; the pins and icons are sized in
	// dots.
	routeDotFraction = 0.022

	// pinAir is the room kept above a pin's head, as a fraction of the
	// picture's height, where a view is framed to leave room for one.
	pinAir = 0.01
)

// pinRoom is the room a pin needs above the route's highest point in a
// picture of the given aspect, as a fraction of its height, beyond the
// margin perspective.Frame already keeps: a pin's height is in the box's
// smaller side, which is the height for a wide picture and the width, a
// fraction of it, for a tall one.
func pinRoom(aspect float64) float64 {
	return max(0, pinRise*pinHead*routeDotFraction*min(aspect, 1)+pinAir-perspective.FrameMargin)
}

// Marker draws m with its place at x, y, through osmbase's own drawing of
// it, so a pin or an icon here is the one course draws on its maps. It is
// drawn into an image of its own, just big enough, and composited over the
// frame within the box, and within the clip if one is set: a pin stands up
// from its place, and one at the top of a route would otherwise stand into
// the panel above.
func (c *Canvas) Marker(m render.Marker, x, y float64, within Box) {
	reach := (3.5 + 2) * math.Max(m.Radius, 1)
	side := int(math.Ceil(2*reach)) + 4
	ox, oy := int(math.Floor(x))-side/2, int(math.Floor(y))-side/2
	tmp := image.NewRGBA(image.Rect(0, 0, side, side))
	lx, ly := x-float64(ox), y-float64(oy)
	render.DrawMarkers(tmp, []render.Marker{m}, func(render.Coord) (float64, float64, bool) { return lx, ly, true }, nil)
	dst := image.Rect(ox, oy, ox+side, oy+side)
	visible := dst.Intersect(pixelRect(within))
	if c.clipActive {
		visible = visible.Intersect(c.clip)
	}
	if visible.Empty() {
		return
	}
	draw.Draw(c.img.SubImage(visible).(*image.RGBA), dst, tmp, image.Point{}, draw.Over)
}

// endMarks draws a route's start and finish pins, where place puts them and
// says they are in sight; dotR is the view's position dot's radius.
func endMarks(c *Canvas, box Box, ends RouteEnds, first, last route.Point, dotR float64, place func(route.Point) (x, y float64, ok bool)) {
	if ends != EndsPin {
		return
	}
	sx, sy, sok := place(first)
	fx, fy, fok := place(last)
	r := pinHead * dotR
	pin := func(shape render.MarkerShape, x, y float64) {
		c.Marker(render.Marker{Shape: shape, Radius: r, Halo: math.Max(1, r*0.12)}, x, y, box)
	}
	// A route that ends where it began gets one pin for both: two, one
	// over the other, would hide one end. "Where it began" is where the two
	// pins would overlap -- side by side within a head's width, or one
	// within a pin's height of the other -- since a run that finishes
	// thirty metres from its start drew two pins one into the other.
	if sok && fok && math.Abs(sx-fx) < 2*r && math.Abs(sy-fy) < (pinRise-0.3)*r {
		pin(render.ShapeStartFinishPin, sx, sy)
		return
	}
	if fok {
		pin(render.ShapeFinishPin, fx, fy)
	}
	if sok {
		pin(render.ShapeStartPin, sx, sy)
	}
}

// positionMark draws the current position at x, y as icon says, in ink at
// alpha. heading is the direction of travel on screen, degrees clockwise from
// up, when ok; without one an arrow or a plane would point at random, so
// the dot is drawn instead.
func positionMark(c *Canvas, box Box, icon RouteIcon, x, y, dotR float64, ink color.Color, alpha float64, heading float64, ok bool) {
	faded := Fade(ink, alpha)
	if icon == IconDot || !ok {
		c.Circle(x, y, dotR, faded)
		return
	}
	m := render.Marker{Shape: render.ShapeArrow, Ink: color.RGBAModel.Convert(faded).(color.RGBA), Radius: arrowSize * dotR, Heading: heading}
	if icon == IconPlane {
		m.Shape, m.Radius = render.ShapePlane, planeSize*dotR
	}
	m.Halo = math.Max(1, m.Radius*0.1)
	c.Marker(m, x, y, box)
}

// travelHeading is the direction of travel at f, as an angle on screen: the
// course the file records where it does, otherwise the route's bearing from
// its fixes, turned into the picture by placing the position and a point a
// little ahead of it along that bearing. It is false where there is no
// direction, or the point ahead is out of the picture.
func travelHeading(f Frame, fixes []route.Point, pos route.Point, place func(route.Point) (x, y float64, ok bool)) (float64, bool) {
	bearing, ok := 0.0, false
	if f.HasSample && f.Sample.HasCourse {
		bearing, ok = f.Sample.Course, true
	} else {
		bearing, ok = route.BearingAt(fixes, f.At)
	}
	if !ok {
		return 0, false
	}
	x0, y0, ok0 := place(pos)
	x1, y1, ok1 := place(route.Ahead(pos, bearing, 30))
	if !ok0 || !ok1 || math.Hypot(x1-x0, y1-y0) < 1e-6 {
		return 0, false
	}
	return math.Atan2(x1-x0, -(y1-y0)) * 180 / math.Pi, true
}
