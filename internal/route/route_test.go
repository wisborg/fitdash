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

// TestSpanIndices_ResolvesAgainstTheDrawnList is the geometry the panel's own
// highlight mark depends on, tested with no pixels involved.
//
// The point list here is deliberately small -- SpanIndices takes whatever
// list it is handed, and the interesting behaviour (a bracketing chord when
// no vertex falls inside a span) is a property of the SPAN relative to the
// point spacing, not of the fixture's size. The 3000-point, over-the-cap
// fixture belongs to the panel-level tests in internal/panel, which are what
// prove this function is actually called with the drawn list rather than the
// full one.
func TestSpanIndices_ResolvesAgainstTheDrawnList(t *testing.T) {
	pts := make([]Point, 10)
	for i := range pts {
		pts[i] = Point{Time: epoch.Add(time.Duration(i) * time.Second)}
	}

	cases := []struct {
		name     string
		from, to time.Time
		wantI0   int
		wantI1   int
		wantOK   bool
	}{
		{
			name: "span containing several vertices",
			from: epoch.Add(2 * time.Second), to: epoch.Add(6 * time.Second),
			// pts[2..5] all have Time in [2s, 6s).
			wantI0: 2, wantI1: 5, wantOK: true,
		},
		{
			name: "span between two consecutive vertices",
			from: epoch.Add(3300 * time.Millisecond), to: epoch.Add(3700 * time.Millisecond),
			// No point's Time falls in [3.3s, 3.7s); the bracketing chord is
			// pts[3] (3s) and pts[4] (4s).
			wantI0: 3, wantI1: 4, wantOK: true,
		},
		{
			name: "span exactly on a vertex",
			from: epoch.Add(4 * time.Second), to: epoch.Add(4500 * time.Millisecond),
			// pts[4].Time == from, and it is < to, so it is the only vertex
			// inside the span -- but a single point is not a drawable
			// polyline (Canvas.Polyline needs two), so a successful result
			// must extend it to a neighbour. pts[5] exists, so it extends
			// forward, the same direction the span itself runs.
			wantI0: 4, wantI1: 5, wantOK: true,
		},
		{
			name: "span entirely before the first point",
			from: epoch.Add(-5 * time.Second), to: epoch.Add(-1 * time.Second),
			wantOK: false,
		},
		{
			name: "span entirely after the last point",
			from: epoch.Add(20 * time.Second), to: epoch.Add(25 * time.Second),
			wantOK: false,
		},
		{
			name: "empty point list",
			from: epoch, to: epoch.Add(time.Second),
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list := pts
			if c.name == "empty point list" {
				list = nil
			}
			i0, i1, ok := SpanIndices(list, c.from, c.to)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (i0=%d, i1=%d)", ok, c.wantOK, i0, i1)
			}
			if !ok {
				return
			}
			if i0 != c.wantI0 || i1 != c.wantI1 {
				t.Errorf("SpanIndices = (%d, %d), want (%d, %d)", i0, i1, c.wantI0, c.wantI1)
			}
		})
	}
}

// TestSpanIndices_OneVertexSpanIsDrawable is F1, isolated: a span that
// contains exactly one drawn vertex must not be reported as a success that
// only a one-point "polyline" can satisfy. Canvas.Polyline draws nothing for
// a single point, so a caller that slices pts[i0:i1+1] on i0==i1 gets a
// segment that puts zero ink on screen while SpanIndices still says ok. That
// silently drops both the mark AND the "not marked" summary line owed to
// a genuinely unmarkable highlight -- the worst outcome this
// project names, a hole with nothing said about it.
//
// Before the fix this fails: SpanIndices returns i0 == i1 == 5 with ok true.
// After it, ok is still true (the vertex genuinely is inside the span) but
// i0 and i1 must differ, because a successful result promises a drawable
// polyline.
func TestSpanIndices_OneVertexSpanIsDrawable(t *testing.T) {
	pts := make([]Point, 10)
	for i := range pts {
		pts[i] = Point{Time: epoch.Add(time.Duration(i) * time.Second)}
	}
	// [5.4s, 5.6s) contains only pts[5] (5s... wait, need Time>=from). Use a
	// span that brackets exactly pts[5]: from just before 5s, to just after,
	// short enough that neither pts[4] (4s) nor pts[6] (6s) qualifies.
	from := epoch.Add(5*time.Second - 100*time.Millisecond)
	to := epoch.Add(5*time.Second + 100*time.Millisecond)

	i0, i1, ok := SpanIndices(pts, from, to)
	if !ok {
		t.Fatal("SpanIndices refused a span that genuinely contains a drawn vertex")
	}
	if i0 == i1 {
		t.Fatalf("SpanIndices returned the single vertex %d as both ends of the span; "+
			"pts[%d:%d+1] is one point, which Canvas.Polyline draws as nothing -- "+
			"a successful result must span at least two vertices", i0, i0, i1)
	}
	if i1-i0 != 1 {
		t.Errorf("SpanIndices = (%d, %d), want adjacent indices bracketing pts[5]", i0, i1)
	}
	if i0 != 5 && i1 != 5 {
		t.Errorf("SpanIndices = (%d, %d), want a pair including index 5, the one vertex actually inside the span", i0, i1)
	}
}

// last3000Track builds a 3000-sample, one-fix-per-second track with every
// sample carrying a GPS fix -- six times DefaultMaxPoints, matching
// internal/panel's own over-the-cap fixtures (squareTrack at 3000 fixes),
// so FromTrack's thinning is genuinely exercised rather than the fixture
// being returned whole. The coordinates carry no real-world meaning; only
// the times, and the resulting point count, matter to the two tests below.
func last3000Track(base time.Time) *fitactivity.Track {
	const n = 3000
	samples := make([]fitactivity.Sample, n)
	for i := range samples {
		samples[i] = fitactivity.Sample{
			Time:   base.Add(time.Duration(i) * time.Second),
			HasGPS: true, Lat: 55 + float64(i)*1e-5, Lon: 12 + float64(i)*1e-5,
		}
	}
	return &fitactivity.Track{Samples: samples}
}

// TestSpanIndices_ToExactlyAtTheLastVertexExcludesItHalfOpen and its sibling
// below are the boundary this project's own half-open convention creates at
// the route's own LAST drawn vertex: a highlight whose `to` lands EXACTLY
// on that vertex's own timestamp must exclude it -- the identical [from,to)
// rule Timeline's own frames already follow (a render's last frame is one
// frame short of the activity's end, never the end instant itself). This
// pair is what would catch an off-by-one in EITHER direction: each alone
// could pass an implementation that always drops, or always keeps, the
// final vertex regardless of where `to` actually falls.
//
// This is a real case, not a manufactured one: resolveHighlights clips a
// highlight's own `to` to the activity's own end when it runs past it, and
// on a great many recordings the very last SAMPLE is also the last GPS
// fix, so a highlight explicitly marked "to the finish" resolves to a `to`
// that lands exactly on the route's own final vertex.
//
// The fixture is six times DefaultMaxPoints (3000 fixes thinned to 500),
// matching internal/panel's own over-the-cap fixtures: under the cap every
// fix is a drawn vertex and the stride is one second, which cannot
// distinguish "the vertex at this boundary" from "any vertex at all".
//
// pts[498] and pts[499] (the forced final point -- see FromTrack's own
// "last point always kept" rule) are independently derived from the stride
// arithmetic FromTrack's own doc comment describes: stride =
// (3000-1)/(500-1) = 2999/499 ~ 6.0100, so pts[498] = fixes[int(498*stride)]
// = fixes[2992] (time base+2992s) and pts[499] = fixes[2999] (forced, time
// base+2999s).
func TestSpanIndices_ToExactlyAtTheLastVertexExcludesItHalfOpen(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	pts := FromTrack(last3000Track(base), DefaultMaxPoints)
	if len(pts) != 500 {
		t.Fatalf("precondition: got %d drawn points, want 500 (the fixture must exceed DefaultMaxPoints for this test to mean anything)", len(pts))
	}
	if want := base.Add(2992 * time.Second); !pts[498].Time.Equal(want) {
		t.Fatalf("precondition: pts[498].Time = %v, want %v -- the hand-derived stride no longer matches FromTrack", pts[498].Time, want)
	}
	if want := base.Add(2999 * time.Second); !pts[499].Time.Equal(want) {
		t.Fatalf("precondition: pts[499].Time = %v, want %v -- the fixture's own last vertex moved", pts[499].Time, want)
	}

	from := base.Add(2950 * time.Second)
	to := pts[499].Time // exactly the last drawn vertex's own timestamp

	i0, i1, ok := SpanIndices(pts, from, to)
	if !ok {
		t.Fatal("SpanIndices refused a span that genuinely brackets several drawn vertices")
	}
	if i0 != 491 || i1 != 498 {
		t.Errorf("SpanIndices = (%d, %d), want (491, 498); `to` landing exactly on pts[499]'s own timestamp must exclude it (half-open)", i0, i1)
	}
}

// TestSpanIndices_ToJustPastTheLastVertexIncludesIt is the paired case: a
// `to` one instant past the route's own last drawn vertex must include it.
// Without this sibling, TestSpanIndices_ToExactlyAtTheLastVertexExcludesItHalfOpen
// alone would also pass an implementation that ALWAYS drops the final
// vertex regardless of `to` -- an off-by-one that would silently truncate
// the mark short of the route's own finish on every highlight that runs to
// the activity's end.
func TestSpanIndices_ToJustPastTheLastVertexIncludesIt(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	pts := FromTrack(last3000Track(base), DefaultMaxPoints)
	if len(pts) != 500 {
		t.Fatalf("precondition: got %d drawn points, want 500", len(pts))
	}

	from := base.Add(2950 * time.Second)
	to := pts[499].Time.Add(time.Millisecond) // one tick past the last drawn vertex

	i0, i1, ok := SpanIndices(pts, from, to)
	if !ok {
		t.Fatal("SpanIndices refused a span that genuinely brackets several drawn vertices")
	}
	if i0 != 491 || i1 != 499 {
		t.Errorf("SpanIndices = (%d, %d), want (491, 499); a `to` one tick past the route's own last drawn vertex must include it -- "+
			"an off-by-one here would silently truncate the mark short of the route's own finish", i0, i1)
	}
}

// zoomPts is a small east-north staircase: enough extent on both axes that a
// sub-range is a genuinely different rectangle from the whole.
func zoomPts() []Point {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	pts := make([]Point, 10)
	for i := range pts {
		pts[i] = Point{
			Lat:  55.0 + float64(i)*0.001,
			Lon:  12.0 + float64(i)*0.002,
			Time: base.Add(time.Duration(i) * time.Second),
		}
	}
	return pts
}

// TestProjectionSub_KeepsTheParentsLongitudeScaling is the property the whole
// zoom rests on, and the one a naive implementation (calling Fit on the
// subset) silently gets wrong.
//
// Fit derives cosLat from the points it is given, so a sub-range's own mean
// latitude -- a fraction of a degree from the whole route's -- would give a
// slightly different longitude scaling. The two views would then draw the SAME
// piece of course at subtly different proportions, and animating between them
// would shear the shape as it scaled. Nothing about that is visible in a
// single frame, which is why it is asserted here rather than looked at.
func TestProjectionSub_KeepsTheParentsLongitudeScaling(t *testing.T) {
	pts := zoomPts()
	whole, ok := Fit(pts)
	if !ok {
		t.Fatal("Fit: no projection")
	}

	sub, ok := whole.Sub(pts[6:])
	if !ok {
		t.Fatal("Sub: no projection")
	}
	if sub.cosLat != whole.cosLat {
		t.Errorf("Sub cosLat = %v, want %v (the parent's) -- a sub-view fitted independently shears as it scales",
			sub.cosLat, whole.cosLat)
	}

	// Independently fitting the same subset is what must NOT happen; if it
	// ever produced the same scaling this test would be proving nothing.
	own, ok := Fit(pts[6:])
	if !ok {
		t.Fatal("Fit(subset): no projection")
	}
	if own.cosLat == whole.cosLat {
		t.Skip("this fixture's subset happens to share the whole route's mean latitude; the test cannot distinguish the two rules")
	}
}

// TestProjectionSub_FramesOnlyTheSubset checks the actual point of Sub: a
// smaller rectangle, tight around the points given.
func TestProjectionSub_FramesOnlyTheSubset(t *testing.T) {
	pts := zoomPts()
	whole, _ := Fit(pts)
	sub, ok := whole.Sub(pts[6:])
	if !ok {
		t.Fatal("Sub: no projection")
	}

	if sub.spanX >= whole.spanX || sub.spanY >= whole.spanY {
		t.Errorf("sub span (%v, %v) is not inside the whole (%v, %v)", sub.spanX, sub.spanY, whole.spanX, whole.spanY)
	}
	// Every point of the subset must land inside the sub-view's own box,
	// which is what "the zoomed view shows the whole highlighted stretch"
	// means in coordinates.
	place, ok := sub.Placer(100, 100)
	if !ok {
		t.Fatal("Placer: none")
	}
	for i, pt := range pts[6:] {
		x, y := place(pt)
		if x < -0.001 || x > 100.001 || y < -0.001 || y > 100.001 {
			t.Errorf("subset point %d places at (%v, %v), outside the 100x100 view fitted to it", i, x, y)
		}
	}
}

// TestProjectionSub_RefusesWhatFitRefuses keeps the two entry points agreeing,
// so a caller can treat "no sub-view" exactly as it treats "no route".
func TestProjectionSub_RefusesWhatFitRefuses(t *testing.T) {
	pts := zoomPts()
	whole, _ := Fit(pts)

	if _, ok := whole.Sub(pts[:1]); ok {
		t.Error("Sub accepted a single point; one point is not an extent")
	}
	if _, ok := whole.Sub(nil); ok {
		t.Error("Sub accepted no points")
	}
	same := []Point{pts[3], pts[3], pts[3]}
	if _, ok := whole.Sub(same); ok {
		t.Error("Sub accepted three copies of one place; a stuck fix has no extent to frame")
	}
}

// TestLerp_EndpointsAndMonotonicZoom pins the animation's two ends exactly and
// its middle loosely, which is the right split: the endpoints are what a
// viewer notices (a zoom that does not finish arriving, or that starts from
// the wrong place), and the path between them only has to be monotone.
func TestLerp_EndpointsAndMonotonicZoom(t *testing.T) {
	pts := zoomPts()
	whole, _ := Fit(pts)
	sub, _ := whole.Sub(pts[6:])

	if got := Lerp(whole, sub, 0); got != whole {
		t.Errorf("Lerp at 0 = %+v, want the starting view %+v", got, whole)
	}
	if got := Lerp(whole, sub, 1); got != sub {
		t.Errorf("Lerp at 1 = %+v, want the destination view %+v", got, sub)
	}
	// Out of range clamps rather than extrapolating: a weight outside [0,1]
	// would otherwise fly the view past its destination.
	if got := Lerp(whole, sub, -0.5); got != whole {
		t.Errorf("Lerp at -0.5 = %+v, want the starting view", got)
	}
	if got := Lerp(whole, sub, 2); got != sub {
		t.Errorf("Lerp at 2 = %+v, want the destination view", got)
	}

	prev := whole.spanX
	for _, at := range []float64{0.25, 0.5, 0.75, 1} {
		span := Lerp(whole, sub, at).spanX
		if span > prev {
			t.Errorf("span grew from %v to %v between weights; the zoom reversed direction", prev, span)
		}
		prev = span
	}
}

// TestLerp_ScalesGeometrically pins the reason the span is not interpolated
// linearly. What the eye reads as zoom speed is the RATIO between successive
// frames, so a linear span crosses most of the distance in the first few
// frames and then crawls. Halfway through, a geometric zoom sits at the
// geometric mean of the two spans, which is strictly smaller than the
// arithmetic one this replaced.
func TestLerp_ScalesGeometrically(t *testing.T) {
	pts := zoomPts()
	whole, _ := Fit(pts)
	sub, _ := whole.Sub(pts[8:])

	mid := Lerp(whole, sub, 0.5).spanX
	geometric := math.Sqrt(whole.spanX * sub.spanX)
	arithmetic := (whole.spanX + sub.spanX) / 2

	if math.Abs(mid-geometric) > 1e-12 {
		t.Errorf("half-way span = %v, want the geometric mean %v", mid, geometric)
	}
	if mid >= arithmetic {
		t.Errorf("half-way span %v is not below the arithmetic mean %v; the zoom is still linear", mid, arithmetic)
	}
}

// TestLerp_AnAxisWithNoExtent covers the route that runs exactly east-west:
// spanY is zero, no number of doublings reaches zero from a positive start,
// and the geometric path is undefined. Linear is well defined at both ends
// and is what that case falls back to.
func TestLerp_AnAxisWithNoExtent(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	flat := []Point{
		{Lat: 55, Lon: 12.000, Time: base},
		{Lat: 55, Lon: 12.004, Time: base.Add(time.Second)},
		{Lat: 55, Lon: 12.008, Time: base.Add(2 * time.Second)},
	}
	whole, ok := Fit(flat)
	if !ok {
		t.Fatal("Fit: no projection for an east-west route")
	}
	sub, ok := whole.Sub(flat[1:])
	if !ok {
		t.Fatal("Sub: no projection")
	}

	mid := Lerp(whole, sub, 0.5)
	if math.IsNaN(mid.spanY) || math.IsInf(mid.spanY, 0) {
		t.Errorf("half-way spanY = %v on a route with no north-south extent", mid.spanY)
	}
	if mid.spanY != 0 {
		t.Errorf("half-way spanY = %v, want 0: neither end has any extent to interpolate", mid.spanY)
	}
	if _, ok := mid.Placer(100, 100); !ok {
		t.Error("the half-way view cannot place a point; an intermediate view must be as drawable as its endpoints")
	}
}
