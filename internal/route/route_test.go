package route

import (
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
)

var epoch = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// TestFit_ScalesLongitudeByLatitude is the projection's one real piece of
// geometry, and the reason it is not simply lat/lon plotted directly.
//
// A degree of longitude equals a degree of latitude only at the equator. At 60
// degrees north it is half as wide, so a square kilometre plotted raw comes
// out twice as wide as it is tall. The expected ratio here is derived from
// cos(60) = 0.5, not read off a run.
func TestFit_ScalesLongitudeByLatitude(t *testing.T) {
	// A patch one degree across in both directions, centred at 60 degrees.
	pts := []Point{
		{Lat: 59.5, Lon: 10.0}, {Lat: 60.5, Lon: 11.0},
	}
	p, ok := Fit(pts)
	if !ok {
		t.Fatal("Fit refused a two-point route with extent")
	}

	// One degree of latitude spans 1.0; one degree of longitude spans
	// cos(60 degrees) = 0.5.
	if diff := p.spanY - 1.0; math.Abs(diff) > 1e-9 {
		t.Errorf("latitude span = %v, want 1.0", p.spanY)
	}
	if diff := p.spanX - 0.5; math.Abs(diff) > 1e-3 {
		t.Errorf("longitude span = %v, want about 0.5 (cos 60)", p.spanX)
	}

	// At the equator the two are equal, which is the case that would hide a
	// missing cosine entirely -- and is why the test above is at 60 degrees.
	equator, ok := Fit([]Point{{Lat: -0.5, Lon: 10}, {Lat: 0.5, Lon: 11}})
	if !ok {
		t.Fatal("Fit refused an equatorial route")
	}
	if diff := equator.spanX - equator.spanY; math.Abs(diff) > 1e-3 {
		t.Errorf("at the equator the spans should match: %v vs %v", equator.spanX, equator.spanY)
	}
}

// TestPlace_PreservesAspectAndPutsNorthUp pins the two properties a route
// drawing must have.
//
// Stretching a route to fill its box changes its SHAPE: an out-and-back
// becomes a loop, and a lap of a track becomes an oval of the wrong
// proportions. And north-up is the only orientation anyone reads a map in.
func TestPlace_PreservesAspectAndPutsNorthUp(t *testing.T) {
	// A route twice as tall as it is wide, centred ON the equator so the mean
	// latitude is exactly 0 and its cosine exactly 1, letting the arithmetic
	// below be done by hand.
	//
	// Symmetric about the equator, not merely touching it: latitudes of 0 and
	// 2 have a MEAN of 1, whose cosine is 0.99985, and the resulting 199.97
	// pixel width is correct behaviour that fails an assertion of 200. The
	// projection scales longitude by the cosine of the route's mean latitude,
	// so a test that wants that factor to be 1 has to put the mean at 0.
	pts := []Point{
		{Lat: -1, Lon: 0}, {Lat: 1, Lon: 1}, {Lat: -1, Lon: 1}, {Lat: 1, Lon: 0},
	}
	p, ok := Fit(pts)
	if !ok {
		t.Fatal("Fit refused")
	}

	// Into a 400x400 box: the taller axis binds, so the scale is 400/2 = 200
	// and the route is 200 wide, centred, leaving 100 either side.
	xs, ys := p.Place(pts, 400, 400)
	if len(xs) != 4 || len(ys) != 4 {
		t.Fatalf("Place returned %d,%d coordinates for 4 points", len(xs), len(ys))
	}

	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for i := range xs {
		minX, maxX = math.Min(minX, xs[i]), math.Max(maxX, xs[i])
		minY, maxY = math.Min(minY, ys[i]), math.Max(maxY, ys[i])
	}
	if w := maxX - minX; math.Abs(w-200) > 1e-6 {
		t.Errorf("route is %v wide, want 200 (aspect preserved, not stretched to 400)", w)
	}
	if h := maxY - minY; math.Abs(h-400) > 1e-6 {
		t.Errorf("route is %v tall, want 400", h)
	}
	if math.Abs(minX-100) > 1e-6 {
		t.Errorf("route starts at x=%v, want 100 -- the spare width should be split, not left on one side", minX)
	}

	// North up: the northernmost point must have the SMALLEST y, because
	// pixel y grows downward.
	northIdx, southIdx := 1, 0 // lat +1 and lat -1
	if !(ys[northIdx] < ys[southIdx]) {
		t.Errorf("the northern point is at y=%v and the southern at y=%v; north must be up",
			ys[northIdx], ys[southIdx])
	}
}

// TestFit_RefusesWhatCannotBeDrawn covers the degenerate inputs that are real:
// a track recorded indoors whose watch reported one stuck position, and a
// single fix.
func TestFit_RefusesWhatCannotBeDrawn(t *testing.T) {
	if _, ok := Fit(nil); ok {
		t.Error("Fit accepted no points")
	}
	if _, ok := Fit([]Point{{Lat: 55, Lon: 12}}); ok {
		t.Error("Fit accepted a single point")
	}
	same := []Point{{Lat: 55, Lon: 12}, {Lat: 55, Lon: 12}, {Lat: 55, Lon: 12}}
	if _, ok := Fit(same); ok {
		t.Error("Fit accepted a route with no extent; a stuck fix is not a path")
	}
}

// TestFromTrack_KeepsTheEndsAndSkipsMissingFixes pins the downsampling.
//
// Keeping the last point matters: dropping it leaves the outline stopping
// short of where the activity finished, and on a loop it leaves a visible gap
// at the join.
func TestFromTrack_KeepsTheEndsAndSkipsMissingFixes(t *testing.T) {
	samples := make([]fitactivity.Sample, 1000)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time: epoch.Add(time.Duration(i) * time.Second),
			// Every tenth sample has no fix: a dropout, which must be skipped
			// rather than interpolated across or treated as 0,0.
			HasGPS: i%10 != 0,
			Lat:    55 + float64(i)*1e-4,
			Lon:    12 + float64(i)*1e-4,
		}
	}
	track := &fitactivity.Track{Samples: samples}

	pts := FromTrack(track, 100)
	if len(pts) != 100 {
		t.Fatalf("kept %d points, want 100", len(pts))
	}
	for i, p := range pts {
		if p.Lat == 0 && p.Lon == 0 {
			t.Fatalf("point %d is at 0,0; a sample with no fix was kept", i)
		}
	}
	// The first and last kept points must be the route's own first and last
	// FIXES -- sample 0 has none, so the first fix is sample 1.
	if want := samples[1].Time; !pts[0].Time.Equal(want) {
		t.Errorf("first point is at %v, want the first fix at %v", pts[0].Time, want)
	}
	if want := samples[999].Time; !pts[len(pts)-1].Time.Equal(want) {
		t.Errorf("last point is at %v, want the last fix at %v", pts[len(pts)-1].Time, want)
	}

	// A track already under the cap is returned whole.
	short := &fitactivity.Track{Samples: samples[:50]}
	if got, want := len(FromTrack(short, 500)), 45; got != want {
		t.Errorf("a short track yielded %d points, want %d (50 samples less 5 without a fix)", got, want)
	}
	if FromTrack(nil, 100) != nil {
		t.Error("FromTrack(nil) should return nothing")
	}
}

// TestIndexAt_FindsTheLastPointAtOrBefore pins the lookup the dot's position
// depends on, including the two boundaries.
func TestIndexAt_FindsTheLastPointAtOrBefore(t *testing.T) {
	pts := make([]Point, 10)
	for i := range pts {
		pts[i] = Point{Time: epoch.Add(time.Duration(i) * time.Second)}
	}
	cases := []struct {
		name string
		at   time.Time
		want int
	}{
		{"before the route begins", epoch.Add(-time.Second), -1},
		{"exactly the first point", epoch, 0},
		{"between two points", epoch.Add(3500 * time.Millisecond), 3},
		{"exactly a point", epoch.Add(4 * time.Second), 4},
		{"the last point", epoch.Add(9 * time.Second), 9},
		{"after the route ends", epoch.Add(time.Hour), 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IndexAt(pts, c.at); got != c.want {
				t.Errorf("IndexAt = %d, want %d", got, c.want)
			}
		})
	}
	if got := IndexAt(nil, epoch); got != -1 {
		t.Errorf("IndexAt on no points = %d, want -1", got)
	}
}
