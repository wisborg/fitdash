// Package tilemap fetches map imagery for a geographic view and knows the
// coordinate system that imagery is defined in.
//
// It is deliberately shaped as a library that happens to live here. Nothing in
// it knows what fitdash is: it takes degrees and pixels and returns an
// image.Image, and it depends on the standard library, golang.org/x/image and
// a YAML parser. There is one consumer today, which is why it is not its own
// module -- this project's rule is that a package earns a repository when a
// SECOND program needs it, the way fitactivity and output did. Keeping the
// seam clean is what makes that a move rather than a rewrite if it happens.
package tilemap

import "math"

// Web Mercator, the projection every tile service on earth is defined in.
//
// This package owns it, and internal/route reads it from here, because the
// projection belongs to the tile world rather than to the route: a route is
// drawn in Mercator so that its line lands on the roads underneath it. One
// implementation, in the package whose subject it is.
//
// Coordinates are normalised to the unit square, x east from the antimeridian
// and y SOUTH from the north edge. South-positive is not a detail to gloss:
// it is the convention tile numbering already uses, and it is the direction
// screen pixels already run, so a projection expressed this way needs no flip
// anywhere and cannot acquire one by mistake.

// MercatorLimit is the latitude where the projection is cut off, north and
// south.
//
// Mercator sends the poles to infinity, so every implementation truncates
// somewhere; this is the value the tile world settled on, chosen because it
// makes the projected world exactly square. A caller is not expected to
// handle it -- Project clamps -- but a route recorded above it would be
// drawn at the limit rather than at its true latitude, which is a lie no
// activity can actually provoke.
const MercatorLimit = 85.05112877980659

// Project converts a latitude and longitude in degrees to normalised Mercator
// coordinates in the unit square.
func Project(lat, lon float64) (x, y float64) {
	if lat > MercatorLimit {
		lat = MercatorLimit
	}
	if lat < -MercatorLimit {
		lat = -MercatorLimit
	}
	s := math.Sin(lat * math.Pi / 180)
	y = 0.5 - math.Log((1+s)/(1-s))/(4*math.Pi)
	// Clamped again, on the OUTPUT. Clamping the latitude alone leaves y a
	// few ulps outside the unit square at the limit -- measured at -7.8e-16
	// and 1+9e-16 -- because MercatorLimit is a decimal approximation of the
	// latitude whose projection is exactly the edge. A caller turning y into
	// a tile index would get -1 for the north pole, so the guarantee is made
	// here rather than left as very nearly true.
	if y < 0 {
		y = 0
	}
	if y > 1 {
		y = 1
	}
	return (lon + 180) / 360, y
}

// Unproject is Project's inverse, for turning a view's own bounds back into
// the degrees a tile service is asked for.
func Unproject(x, y float64) (lat, lon float64) {
	lon = x*360 - 180
	lat = math.Atan(math.Sinh(math.Pi*(1-2*y))) * 180 / math.Pi
	return lat, lon
}

// ZoomFor returns the tile zoom at which span (a width in normalised Mercator
// units) is drawn across pixels screen pixels, and whether a usable one
// exists.
//
// At zoom z the whole world is 256*2^z pixels wide, so this is the z whose
// world width puts the requested span at the requested size. It is returned
// UNROUNDED: a caller choosing an integer zoom has to decide which way to
// round, and the two directions are not equivalent -- rounding down gives an
// image with fewer pixels than the box it fills, which is visibly soft, while
// rounding up gives more and is merely wasted bandwidth.
func ZoomFor(span float64, pixels int) (float64, bool) {
	if span <= 0 || pixels <= 0 {
		return 0, false
	}
	return math.Log2(float64(pixels) / (tileSize * span)), true
}

// tileSize is the edge of one tile in pixels, at scale 1. It is 256 for every
// service this package talks to; a service serving 512-pixel tiles would be
// asking for a different zoom for the same view, which is why this is named
// rather than written into ZoomFor as a constant.
const tileSize = 256
