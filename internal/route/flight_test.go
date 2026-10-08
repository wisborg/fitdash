package route

import (
	"math"
	"testing"
	"time"

	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"
)

var flightStart = time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

// metres east and north of a point, as a fix at t seconds.
func fixAt(east, north float64, t int) Point {
	const lat0, lon0 = -33.70, 151.10
	return Point{
		Lat:  lat0 + north/110_574,
		Lon:  lon0 + east/(111_320*math.Cos(lat0*math.Pi/180)),
		Time: flightStart.Add(time.Duration(t) * time.Second),
	}
}

// An L: 600 s east at 2 m/s, then 600 s north.
func lRoute() []Point {
	var pts []Point
	for t := 0; t <= 600; t += 5 {
		pts = append(pts, fixAt(float64(2*t), 0, t))
	}
	for t := 605; t <= 1200; t += 5 {
		pts = append(pts, fixAt(1200, float64(2*(t-600)), t))
	}
	return pts
}

func flatGround(osm.Coord) (float64, bool) { return 50, true }

// 1200 s of activity in 40 s of video at 30 fps: thirty times real time.
const flightFrames = 1200

func atFrame(i int) time.Time { return flightStart.Add(time.Duration(i) * time.Second) }

func flightOpts() FlightOptions {
	return FlightOptions{Distance: 800, Pitch: 35, FPS: 30, Steady: time.Second, Turn: 3 * time.Second, Swoop: 3 * time.Second}
}

var overviewCam = perspective.Camera{Target: osm.Coord{Lat: -33.697, Lon: 151.106}, Distance: 4000, Heading: 200, Pitch: 50}

// Past the swoop, the camera follows the activity: it looks at where the
// activity is, along the way it is going.
func TestFlightFollowsTheActivity(t *testing.T) {
	cams := Flight(lRoute(), flightFrames, atFrame, overviewCam, flatGround, flightOpts())
	if len(cams) != flightFrames {
		t.Fatalf("%d cameras for %d frames", len(cams), flightFrames)
	}
	c := cams[300] // 300 s in: 600 m east, going east
	want := fixAt(600, 0, 300)
	if d := math.Hypot((c.Target.Lat-want.Lat)*110_574, (c.Target.Lon-want.Lon)*92_600); d > 5 {
		t.Errorf("at 300 s the camera looks %.1f m from the activity", d)
	}
	if math.Abs(c.Heading-90) > 1 {
		t.Errorf("going east, the camera looks along %.1f°", c.Heading)
	}
	if c := cams[900]; math.Abs(math.Remainder(c.Heading, 360)) > 1 {
		t.Errorf("going north, the camera looks along %.1f°", c.Heading)
	}
	if c.Distance != 800 || c.Pitch != 35 {
		t.Errorf("following from %g m at %g°, want 800 m at 35°", c.Distance, c.Pitch)
	}
}

// At the corner the camera turns over the turning window, not in a frame.
func TestFlightTurnsGradually(t *testing.T) {
	cams := Flight(lRoute(), flightFrames, atFrame, overviewCam, flatGround, flightOpts())
	most := 0.0
	for i := 100; i < flightFrames-100; i++ {
		most = math.Max(most, math.Abs(math.Remainder(cams[i].Heading-cams[i-1].Heading, 360)))
	}
	// 90° over the 90 frames of a 3 s window is about 1° a frame.
	if most > 3 {
		t.Errorf("the camera turned %.1f° in one frame", most)
	}
}

// The height followed is the ground's, averaged over Rise: 0 steps up a
// cliff with the ground, two seconds glides up it.
func TestFlightRiseSmoothsTheHeight(t *testing.T) {
	cliff := func(c osm.Coord) (float64, bool) {
		if c.Lon > 151.10+600/(111_320*math.Cos(-33.70*math.Pi/180)) {
			return 150, true
		}
		return 50, true
	}
	steepest := func(rise time.Duration) float64 {
		o := flightOpts()
		o.Rise = rise
		cams := Flight(lRoute(), flightFrames, atFrame, overviewCam, cliff, o)
		most := 0.0
		for i := 200; i < 400; i++ {
			if !cams[i].HasTargetHeight {
				t.Fatal("no height set where the ground's is known")
			}
			most = math.Max(most, math.Abs(cams[i].TargetHeight-cams[i-1].TargetHeight))
		}
		return most
	}
	if got := steepest(0); got < 99 {
		t.Errorf("following the ground, the camera rose at most %.1f m in a frame, want the cliff's 100", got)
	}
	// 100 m over the 60 frames of 2 s is under 2 m a frame.
	if got := steepest(2 * time.Second); got > 2 {
		t.Errorf("smoothed over 2 s, the camera rose %.1f m in a frame", got)
	}
}

// The flight starts and ends on the overview, and has come down to follow
// the activity once the swoop is over.
func TestFlightSwoopsFromTheOverviewAndBack(t *testing.T) {
	cams := Flight(lRoute(), flightFrames, atFrame, overviewCam, flatGround, flightOpts())
	for _, c := range []perspective.Camera{cams[0], cams[flightFrames-1]} {
		if c.Target != overviewCam.Target || c.Distance != overviewCam.Distance || c.Heading != overviewCam.Heading || c.Pitch != overviewCam.Pitch {
			t.Errorf("an end of the flight is %+v, want the overview %+v", c, overviewCam)
		}
	}
	if c := cams[90]; c.Distance != 800 {
		t.Errorf("once the 3 s swoop is over the camera is %g m off, want 800", c.Distance)
	}
	// Coming down evenly: the distance shrinks by a steady-ish ratio,
	// never growing on the way down.
	for i := 1; i < 90; i++ {
		if cams[i].Distance > cams[i-1].Distance {
			t.Fatalf("frame %d: the camera went back up on the way down", i)
		}
	}
}

// Without elevation anywhere, no height is claimed: each picture takes its
// ground's own, the level ground it is draped over.
func TestFlightClaimsNoHeightWithoutElevation(t *testing.T) {
	none := func(osm.Coord) (float64, bool) { return 0, false }
	for i, c := range Flight(lRoute(), flightFrames, atFrame, overviewCam, none, flightOpts()) {
		if c.HasTargetHeight {
			t.Fatalf("frame %d claims a height of %g m with no elevation to have it from", i, c.TargetHeight)
		}
	}
}

// A flight longer than the ground is read for is interpolated between the
// readings: the height still climbs the cliff, steadily, without stepping
// back down or past it.
func TestFlightReadsTheGroundSparinglyOverALongFlight(t *testing.T) {
	cliff := func(c osm.Coord) (float64, bool) {
		if c.Lon > 151.10+600/(111_320*math.Cos(-33.70*math.Pi/180)) {
			return 150, true
		}
		return 50, true
	}
	reads := 0
	counted := func(c osm.Coord) (float64, bool) { reads++; return cliff(c) }
	const frames = 6000
	at := func(i int) time.Time { return flightStart.Add(time.Duration(i) * 200 * time.Millisecond) }
	cams := Flight(lRoute(), frames, at, overviewCam, counted, flightOpts())
	if reads > groundSamples+2 {
		t.Errorf("the ground was read %d times for %d frames, want at most about %d", reads, frames, groundSamples)
	}
	for i := 1000; i < 2000; i++ {
		d := cams[i].TargetHeight - cams[i-1].TargetHeight
		if d < -1e-9 || cams[i].TargetHeight > 150+1e-9 {
			t.Fatalf("frame %d: the height went from %.2f to %.2f climbing a cliff from 50 to 150", i, cams[i-1].TargetHeight, cams[i].TargetHeight)
		}
	}
	if cams[2500].TargetHeight != 150 {
		t.Errorf("well past the cliff the height is %.2f, want 150", cams[2500].TargetHeight)
	}
}

// Coming down, the camera closes on the activity as it comes: the gap
// between what it looks at and where the activity is, as a share of how
// far off the camera is -- about where the dot sits in the picture -- only
// ever shrinks. Moved straight across the ground instead, the point looked
// at hung back toward the overview's middle while the camera came down,
// and the camera looked ahead of the activity for most of the swoop.
func TestFlightSwoopClosesOnTheActivity(t *testing.T) {
	o := flightOpts()
	cams := Flight(lRoute(), flightFrames, atFrame, overviewCam, flatGround, o)
	o.Swoop = 0
	chase := Flight(lRoute(), flightFrames, atFrame, overviewCam, flatGround, o)
	prev := math.Inf(1)
	for i := 0; i <= 90; i++ {
		gap := math.Hypot((cams[i].Target.Lat-chase[i].Target.Lat)*110_574, (cams[i].Target.Lon-chase[i].Target.Lon)*92_600)
		share := gap / cams[i].Distance
		if share > prev+1e-9 {
			t.Fatalf("frame %d: the camera looks %.3f of its distance from the activity, up from %.3f", i, share, prev)
		}
		prev = share
	}
	// Halfway down, the dot is about halfway in from where it started.
	start := math.Hypot((overviewCam.Target.Lat-chase[0].Target.Lat)*110_574, (overviewCam.Target.Lon-chase[0].Target.Lon)*92_600) / overviewCam.Distance
	mid := math.Hypot((cams[45].Target.Lat-chase[45].Target.Lat)*110_574, (cams[45].Target.Lon-chase[45].Target.Lon)*92_600) / cams[45].Distance
	if r := mid / start; r < 0.35 || r > 0.65 {
		t.Errorf("halfway through the swoop the activity is %.2f of the way it started from the middle, want about a half", r)
	}
}

// eastward is n frame positions moving east at metres a frame from 151.10,
// standing still for stopped frames in the middle.
func eastward(n int, metres float64, stopped int) (lat, lon []float64) {
	lon0 := 151.10
	perDeg := 111_320 * math.Cos(-33.70*math.Pi/180)
	x := 0.0
	for i := range n {
		if i < n/2 || i >= n/2+stopped {
			x += metres
		}
		lat, lon = append(lat, -33.70), append(lon, lon0+x/perDeg)
	}
	return lat, lon
}

// The distance follows from how fast the ground moves in the video: the
// runs the flyover was judged on, about 360 metres of ground a second of
// video, are followed from about 800 metres; stops do not count, being not
// the speed of the ground but its absence; and the ends hold.
func TestAutoDistance(t *testing.T) {
	lat, lon := eastward(300, 12, 0) // 12 m a frame at 30 fps: 360 m/s
	if d := AutoDistance(lat, lon, 30); math.Abs(d-800) > 10 {
		t.Errorf("360 m/s of video followed from %.0f m, want about 800", d)
	}
	lat, lon = eastward(300, 12, 140) // nearly half the video stopped
	if d := AutoDistance(lat, lon, 30); math.Abs(d-800) > 10 {
		t.Errorf("with stops, followed from %.0f m, want about 800 still", d)
	}
	lat, lon = eastward(300, 0, 0)
	if d := AutoDistance(lat, lon, 30); d != AutoDistanceMin {
		t.Errorf("nothing moving followed from %.0f m, want the nearest, %.0f", d, AutoDistanceMin)
	}
	lat, lon = eastward(300, 50_000, 0) // 1,500 km a second of video
	if d := AutoDistance(lat, lon, 30); d != AutoDistanceMax {
		t.Errorf("1,500 km/s followed from %.0f m, want the furthest, %.0f", d, AutoDistanceMax)
	}
}

// A flight given no distance follows from the one chosen for it.
func TestFlightChoosesItsDistance(t *testing.T) {
	o := flightOpts()
	o.Distance = 0
	cams := Flight(lRoute(), flightFrames, atFrame, overviewCam, flatGround, o)
	// 2 m/s of activity, a second of it a frame at 30 fps: 60 m/s of video,
	// under the nearest the camera comes.
	if d := cams[600].Distance; d != AutoDistanceMin {
		t.Errorf("followed from %.0f m, want %.0f", d, AutoDistanceMin)
	}
}
