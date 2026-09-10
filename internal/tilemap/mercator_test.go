package tilemap

import (
	"math"
	"testing"
)

// The coordinates in this file are arbitrary numbers chosen to exercise the
// arithmetic. They are deliberately NOT real places: this repository renders
// personal location data and keeps real coordinates out of fixtures.

// TestProject_KnownAnchors pins the three points of the projection that have
// exact answers, so a sign error anywhere cannot hide.
func TestProject_KnownAnchors(t *testing.T) {
	cases := []struct {
		name         string
		lat, lon     float64
		wantX, wantY float64
	}{
		{"the origin sits at the centre of the world", 0, 0, 0.5, 0.5},
		{"the antimeridian is the left edge", 0, -180, 0, 0.5},
		{"the far antimeridian is the right edge", 0, 180, 1, 0.5},
		{"the northern limit is the top edge", MercatorLimit, 0, 0.5, 0},
		{"the southern limit is the bottom edge", -MercatorLimit, 0, 0.5, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y := Project(c.lat, c.lon)
			if math.Abs(x-c.wantX) > 1e-9 || math.Abs(y-c.wantY) > 1e-9 {
				t.Errorf("Project(%v, %v) = (%v, %v), want (%v, %v)", c.lat, c.lon, x, y, c.wantX, c.wantY)
			}
		})
	}
}

// TestProject_YRunsSouth is the orientation the whole drawing path depends on.
//
// Every consumer of this package treats y as a screen coordinate and applies
// no flip. If y ever ran north, every map and every route drawn over one
// would be mirrored about its own centre -- which on a real route looks like
// a plausible map of somewhere else rather than like a bug.
func TestProject_YRunsSouth(t *testing.T) {
	_, north := Project(60, 10)
	_, south := Project(50, 10)
	if !(north < south) {
		t.Errorf("y at 60N is %v and at 50N is %v; y must increase SOUTHWARD", north, south)
	}
}

// TestProject_ClampsBeyondTheLimit keeps the poles finite. Mercator sends
// them to infinity, and an infinite coordinate would propagate into every
// span, scale and pixel derived from it.
func TestProject_ClampsBeyondTheLimit(t *testing.T) {
	for _, lat := range []float64{89.9, 90, -89.9, -90} {
		x, y := Project(lat, 10)
		if math.IsInf(x, 0) || math.IsNaN(x) || math.IsInf(y, 0) || math.IsNaN(y) {
			t.Errorf("Project(%v, 10) = (%v, %v), which is not a usable coordinate", lat, x, y)
		}
		if y < 0 || y > 1 {
			t.Errorf("Project(%v, 10) gave y=%v, outside the unit square", lat, y)
		}
	}
}

// TestProject_RoundTrips pins Unproject as the actual inverse, which matters
// because the two are used at opposite ends of one pipeline: a view is
// projected to choose a zoom and unprojected to ask the service for it. A
// mismatch would ask for a subtly different rectangle than the one drawn.
func TestProject_RoundTrips(t *testing.T) {
	for _, lat := range []float64{-80, -45, -1, 0, 1, 45, 55.5, 80} {
		for _, lon := range []float64{-179, -90, -0.5, 0, 0.5, 90, 179} {
			x, y := Project(lat, lon)
			gotLat, gotLon := Unproject(x, y)
			if math.Abs(gotLat-lat) > 1e-9 || math.Abs(gotLon-lon) > 1e-9 {
				t.Errorf("round trip of (%v, %v) gave (%v, %v)", lat, lon, gotLat, gotLon)
			}
		}
	}
}

// TestZoomFor pins the zoom arithmetic against the definition rather than
// against a remembered number: at zoom z the world is 256*2^z pixels wide, so
// a span of the whole world at 256 pixels is zoom 0 and each halving of the
// span is one zoom deeper.
func TestZoomFor(t *testing.T) {
	cases := []struct {
		name   string
		span   float64
		pixels int
		want   float64
	}{
		{"the whole world in one tile", 1, tileSize, 0},
		{"half the world in one tile", 0.5, tileSize, 1},
		{"the whole world in two tiles", 1, 2 * tileSize, 1},
		{"a quarter of the world in one tile", 0.25, tileSize, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ZoomFor(c.span, c.pixels)
			if !ok {
				t.Fatal("ZoomFor refused")
			}
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("ZoomFor(%v, %d) = %v, want %v", c.span, c.pixels, got, c.want)
			}
		})
	}

	for _, c := range []struct {
		span   float64
		pixels int
	}{{0, 256}, {-1, 256}, {0.5, 0}, {0.5, -1}} {
		if _, ok := ZoomFor(c.span, c.pixels); ok {
			t.Errorf("ZoomFor(%v, %d) claimed a usable zoom", c.span, c.pixels)
		}
	}
}
