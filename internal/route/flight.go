package route

import (
	"math"
	"slices"
	"time"

	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"
)

// FlightOptions are how a camera flies over a route: where it sits behind
// the activity, and how smoothly it follows. The durations are VIDEO time,
// because what they smooth is what the viewer sees move: at sixty times
// real time, three seconds of video is three minutes of the activity, and
// the camera turns with the route's general direction rather than with
// every switchback.
type FlightOptions struct {
	// Distance is from the camera to the point it follows, in metres, 0 to
	// choose it in each frame from how fast the ground moves in the video
	// there (see autoDistances), or once for the whole video with
	// SteadyDistance (AutoDistance); Pitch
	// how far it looks below the horizontal, and FOV its field of view, in
	// degrees, 0 for perspective.DefaultFOV.
	Distance, Pitch, FOV float64
	SteadyDistance       bool
	// FPS is the video's frame rate, which turns the durations into frames.
	FPS float64
	// Steady is how much video the position the camera follows is averaged
	// over, so the jitter of a GPS fix does not shake the picture; Turn,
	// how much the direction it looks along is taken over.
	Steady, Turn time.Duration
	// Rise is how much video the height of the point followed is averaged
	// over: 0 follows the ground under it exactly, which in hilly country
	// is a camera bobbing over every bank it passes; longer glides over
	// them.
	Rise time.Duration
	// Swoop is how much video the camera takes to come down from the
	// overview to follow the activity at the start, and to go back up at
	// the end; at most a third of the video each.
	Swoop time.Duration
	// Lead and Tail are frames at the start and the end held on the
	// overview, before the swoop down and after the swoop up: a timeline's
	// holds (panel.Timeline.Holds), where a full-frame flyover shows its
	// summary over the whole route.
	Lead, Tail int
}

// Ground is the height of the ground at c in metres, false where it is not
// known.
type Ground func(c osm.Coord) (float64, bool)

// Flight is the camera for every frame of a video over the route whose GPS
// fixes are fixes, frame i showing the activity at at(i): following the
// activity from behind, looking along its direction of travel, except over
// the swoops at either end, where it comes down from overview and goes back
// up to it.
//
// The heights of the point followed come from ground, and are set on every
// camera; where ground knows none anywhere along the flight, none is set
// and each picture takes the ground's own, which is the level ground a map
// without elevation is draped over.
//
// It needs at least two fixes and one frame; with fewer there is nothing to
// fly over, and it returns nil.
func Flight(fixes []Point, frames int, at func(i int) time.Time, overview perspective.Camera, ground Ground, o FlightOptions) []perspective.Camera {
	if len(fixes) < 2 || frames < 1 || !(o.FPS > 0) {
		return nil
	}
	frameCount := func(d time.Duration) int { return max(0, int(math.Round(d.Seconds()*o.FPS))) }

	// Where the activity is at each frame, then steadied.
	lat, lon := make([]float64, frames), make([]float64, frames)
	for i := range frames {
		lat[i], lon[i] = positionAt(fixes, at(i))
	}
	lat, lon = movingAverage(lat, frameCount(o.Steady)/2), movingAverage(lon, frameCount(o.Steady)/2)
	distance := make([]float64, frames)
	switch {
	case o.Distance > 0:
		for i := range distance {
			distance[i] = o.Distance
		}
	case o.SteadyDistance:
		d := AutoDistance(lat, lon, o.FPS)
		for i := range distance {
			distance[i] = d
		}
	default:
		distance = autoDistances(lat, lon, o.FPS)
	}
	heading := headings(lat, lon, frameCount(o.Turn), o.FPS, overview.Heading)

	// The height of the point followed, averaged; unknown stretches take
	// the nearest known height either side, so the average is not dragged
	// toward sea level by a gap in the elevation.
	height := make([]float64, frames)
	known := false
	// Read at most groundSamples times, evenly through the flight, and
	// interpolated between: a real-time render of a long activity is tens
	// of thousands of frames, and the camera's height is averaged over
	// many of them anyway.
	step := max(1, frames/groundSamples)
	var read []int // the frames the ground is read at
	for i := 0; i < frames; i += step {
		read = append(read, i)
	}
	if read[len(read)-1] != frames-1 {
		read = append(read, frames-1)
	}
	for _, i := range read {
		h, ok := ground(osm.Coord{Lat: lat[i], Lon: lon[i]})
		if !ok {
			h = math.NaN()
		}
		known = known || ok
		height[i] = h
	}
	for k := 0; k+1 < len(read); k++ {
		a, b := read[k], read[k+1]
		for i := a + 1; i < b; i++ {
			height[i] = height[a] + (height[b]-height[a])*float64(i-a)/float64(b-a)
		}
	}
	if known {
		fillGaps(height)
		height = movingAverage(height, frameCount(o.Rise)/2)
	}

	cams := make([]perspective.Camera, frames)
	for i := range frames {
		cams[i] = perspective.Camera{
			Target:   osm.Coord{Lat: lat[i], Lon: lon[i]},
			Distance: distance[i], Heading: heading[i], Pitch: o.Pitch, FOV: o.FOV,
		}
		if known {
			cams[i].TargetHeight, cams[i].HasTargetHeight = height[i], true
		}
	}

	// The swoops: from the overview down to the camera following, and back.
	if known {
		if h, ok := ground(overview.Target); ok {
			overview.TargetHeight, overview.HasTargetHeight = h, true
		} else {
			overview.TargetHeight, overview.HasTargetHeight = height[0], true
		}
	}
	lead, tail := min(max(o.Lead, 0), frames), min(max(o.Tail, 0), frames)
	moving := max(0, frames-lead-tail)
	swoop := min(frameCount(o.Swoop), moving/3)
	for i := 0; i < swoop; i++ {
		t := smoothstep(float64(i) / float64(swoop))
		cams[lead+i] = blendCamera(overview, cams[lead+i], t)
		j := frames - tail - 1 - i
		cams[j] = blendCamera(overview, cams[j], t)
	}
	for i := 0; i < lead; i++ {
		cams[i] = overview
	}
	for i := frames - tail; i < frames; i++ {
		cams[i] = overview
	}
	return cams
}

// The camera's distance when it is chosen: so that the ground under it moves
// autoPace of the distance each second of video, between the ends.
//
// The pace is what both runs the flyover was first judged on came to: a
// synthetic 7 km run in 20 seconds and a 21 km trail run in 60, each about
// 360 metres of ground a second of video, followed from 800 metres. A flight
// across a continent at the speed-up a minute of video takes moves a
// hundred and fifty kilometres a second, which the same pace follows from
// three hundred; a walk drawn at its own speed would be followed from a few
// metres, and is held at the nearest end.
const (
	autoPace        = 0.45 // of the distance, a second of video
	AutoDistanceMin = 150.0
	AutoDistanceMax = 400_000.0
)

// AutoDistance is the distance to follow from, in metres, for an activity
// at lat, lon in each frame of a video at fps: the typical speed of the
// ground in the video -- the median over the frames where it moves at all
// -- over autoPace, held between AutoDistanceMin and AutoDistanceMax. One
// distance for the whole video: a camera that closed in where the activity
// slowed would move toward the viewer and away again for no reason the
// viewer could see.
func AutoDistance(lat, lon []float64, fps float64) float64 {
	var speeds []float64
	for i := 1; i < len(lat); i++ {
		north := (lat[i] - lat[i-1]) * 110_574
		east := (lon[i] - lon[i-1]) * 111_320 * math.Cos(lat[i]*math.Pi/180)
		if v := math.Hypot(north, east) * fps; v > 0.01 {
			speeds = append(speeds, v)
		}
	}
	if len(speeds) == 0 {
		return AutoDistanceMin
	}
	slices.Sort(speeds)
	d := speeds[len(speeds)/2] / autoPace
	return math.Max(AutoDistanceMin, math.Min(AutoDistanceMax, d))
}

// groundSamples is the most times Flight reads the ground's height.
const groundSamples = 2000

// positionAt is where the activity was at t: between the fixes either side
// of it, or the first or last before or after them all.
func positionAt(fixes []Point, t time.Time) (lat, lon float64) {
	i := IndexAt(fixes, t)
	switch {
	case i < 0:
		return fixes[0].Lat, fixes[0].Lon
	case i >= len(fixes)-1:
		return fixes[len(fixes)-1].Lat, fixes[len(fixes)-1].Lon
	}
	a, b := fixes[i], fixes[i+1]
	span := b.Time.Sub(a.Time)
	if span <= 0 {
		return a.Lat, a.Lon
	}
	f := float64(t.Sub(a.Time)) / float64(span)
	return a.Lat + (b.Lat-a.Lat)*f, a.Lon + (b.Lon-a.Lon)*f
}

// movingAverage is v averaged over k either side of each value, as many
// either side as there are on both near the ends.
func movingAverage(v []float64, k int) []float64 {
	if k <= 0 {
		return v
	}
	sum := make([]float64, len(v)+1)
	for i, x := range v {
		sum[i+1] = sum[i] + x
	}
	out := make([]float64, len(v))
	for i := range v {
		// As far either side as there is on both: a window cut short on
		// one side only leans the average toward the other, which bends
		// a straight line at the start of a video into a curve and reads
		// as slowing down where nothing did.
		r := min(k, i, len(v)-1-i)
		a, b := i-r, i+r
		out[i] = (sum[b+1] - sum[a]) / float64(b-a+1)
	}
	return out
}

// fillGaps replaces each NaN in v with the nearest known value, the one
// before it where there is one.
func fillGaps(v []float64) {
	last := math.NaN()
	for i := range v {
		if math.IsNaN(v[i]) {
			v[i] = last
		} else {
			last = v[i]
		}
	}
	next := math.NaN()
	for i := len(v) - 1; i >= 0; i-- {
		if math.IsNaN(v[i]) {
			v[i] = next
		} else {
			next = v[i]
		}
	}
}

// blendCamera is the camera t of the way from a to b: the distance changed
// by a constant ratio a frame -- which is what reads as an even zoom -- the
// heading turned the short way round, and the point looked at closing on
// b's as the camera does.
//
// Closing in the picture, not on the ground. Moved straight across the
// ground, the point looked at hangs back near a's while the camera comes
// down, a few hundred metres from b's, which from close up is the camera
// looking well ahead of the activity. Its offset from b's is a's, shrunk
// with the distance and by what is left of the blend: as a share of the
// distance -- about where b's point is in the picture -- it goes from a's
// share to nothing evenly.
func blendCamera(a, b perspective.Camera, t float64) perspective.Camera {
	turn := math.Remainder(b.Heading-a.Heading, 360)
	d := a.Distance * math.Pow(b.Distance/a.Distance, t)
	k := (1 - t) * d / a.Distance
	c := perspective.Camera{
		Target:   osm.Coord{Lat: b.Target.Lat + (a.Target.Lat-b.Target.Lat)*k, Lon: b.Target.Lon + (a.Target.Lon-b.Target.Lon)*k},
		Distance: d,
		Heading:  math.Mod(a.Heading+turn*t+360, 360),
		Pitch:    lerp(a.Pitch, b.Pitch, t),
		FOV:      lerp(lens(a.FOV), lens(b.FOV), t),
	}
	if a.HasTargetHeight && b.HasTargetHeight {
		c.TargetHeight, c.HasTargetHeight = lerp(a.TargetHeight, b.TargetHeight, t), true
	}
	return c
}

// lens is a field of view, 0 being the default one: a blend from 0 would
// open the lens to nothing and back.
func lens(fov float64) float64 {
	if fov == 0 {
		return perspective.DefaultFOV
	}
	return fov
}

// smoothstep eases 0 to 1 as t goes 0 to 1, starting and stopping gently.
func smoothstep(t float64) float64 {
	t = math.Max(0, math.Min(1, t))
	return t * t * (3 - 2*t)
}

// headings is the compass bearing the camera looks along in each frame: the
// direction of travel, held where the route gives no direction, turned
// gently.
//
// The direction of travel was the bearing from where the activity is half
// the turning window before a frame to where it is half after. That is a
// direction only where the route runs roughly straight across the window.
// On laps of a park or across a turnaround -- a parkrun is both, and at a
// hundred and twenty times real time a 3-second window is three minutes of
// running -- the two ends of it are close together in any direction, and
// the camera swung round by ninety degrees from one frame to the next.
//
// So each frame's direction is weighed by how much of a direction it is:
// how far the activity gets across the window as a share of how far it
// runs, squared -- near 1 along a straight, near 0 round a loop or back on
// itself. The weighted directions are averaged over the turning window
// either side, so the camera starts turning ahead of a bend as much as it
// ends after it, and holds its course where the route is no guide.
// Last, its turning is held to maxTurnRate, both ways through the video, so
// that a turnaround is a swing of a few seconds and never a flip.
func headings(lat, lon []float64, turn int, fps, fallback float64) []float64 {
	n := len(lat)
	half := max(1, turn/2)
	step := make([]float64, n) // metres from the frame before
	for i := 1; i < n; i++ {
		step[i] = MetresBetween(lat[i-1], lon[i-1], lat[i], lon[i])
	}
	run := make([]float64, n+1)
	for i := range n {
		run[i+1] = run[i] + step[i]
	}
	vx, vy := make([]float64, n), make([]float64, n)
	for i := range n {
		a, b := max(0, i-half), min(n-1, i+half)
		east, north := offsetMetres(lat[a], lon[a], lat[b], lon[b])
		chord := math.Hypot(east, north)
		path := run[b+1] - run[a+1]
		if chord < 1 || path <= 0 {
			continue
		}
		w := math.Pow(math.Min(1, chord/path), 2)
		vx[i], vy[i] = w*east/chord, w*north/chord
	}
	// Averaged over the window, twice -- near enough a bell curve, with an
	// edge: a bend further off than the window does not reach the camera
	// at all, where an exponential's tail turned it a degree toward a
	// corner ten seconds away.
	for range 2 {
		vx, vy = movingAverage(vx, half), movingAverage(vy, half)
	}
	// The camera starts along the first direction there is -- the
	// overview's with none at all -- and turns toward each frame's as fast
	// as maxTurnRate times how much of a direction it is: in full where the
	// route runs straight, hardly at all round laps, where the averaged
	// direction is weak and wanders. Turning toward a weak direction at full
	// rate, the camera still turned through more than two full circles in
	// eight laps of a park.
	h := make([]float64, n)
	cur := fallback
	for i := range n {
		if s := math.Hypot(vx[i], vy[i]); s > 1e-6 {
			cur = math.Atan2(vx[i], vy[i]) * 180 / math.Pi
			break
		}
	}
	limit := maxTurnRate / fps
	for i := range n {
		if s := math.Hypot(vx[i], vy[i]); s > 1e-6 {
			want := math.Atan2(vx[i], vy[i]) * 180 / math.Pi
			l := limit * math.Min(1, s/confidentDirection)
			cur += math.Max(-l, math.Min(l, math.Remainder(want-cur, 360)))
		}
		h[i] = math.Mod(math.Mod(cur, 360)+360, 360)
	}
	return h
}

// maxTurnRate is the fastest the camera turns, in degrees a second of
// video: a turnaround takes about four seconds. It turns that fast only
// toward a direction at least confidentDirection strong -- the averaged,
// weighted direction of travel, which is 1 on a long straight.
const (
	maxTurnRate        = 45.0
	confidentDirection = 0.5
)

// autoDistances is the distance to follow from in each frame, chosen from
// how fast the ground moves in the video there: the local speed over
// autoPace, as AutoDistance takes the typical one, so that a flight is
// followed from close by on the runway and from high above at cruise, and
// a run slowed by a highlight is followed from closer while it plays.
//
// Smoothed in proportion, not in metres -- what reads as a steady zoom is a
// steady ratio a frame -- over distanceSmooth either side, and held to
// doubling or halving in no less than distanceDoubling: from a gate to
// cruise is ten doublings, which take twenty seconds of video at most,
// however fast the climb plays. Where the activity stands still the speed
// says nothing of how far to stand off -- it is not slow, it is stopped --
// and those frames take the distance either side of them, so the camera
// does not swoop down to every traffic light.
func autoDistances(lat, lon []float64, fps float64) []float64 {
	n := len(lat)
	// The speed in each frame is the mean over the moving frames within
	// distanceSmooth of it: a stop is not counted at all, and the frames
	// slowing into it and out of it are outweighed by those moving on.
	// Taken frame by frame, the stopped ones bridged, those few slow frames
	// either side of a stop still pulled a cruising camera down to a
	// third of its height.
	moving := make([]float64, n+1) // metres moved, frames moving: running sums
	count := make([]float64, n+1)
	for i := range n {
		moving[i+1], count[i+1] = moving[i], count[i]
		if i == 0 {
			continue
		}
		if step := MetresBetween(lat[i-1], lon[i-1], lat[i], lon[i]); step*fps > stoppedSpeed {
			moving[i+1] += step
			count[i+1]++
		}
	}
	w := max(1, int(math.Round(distanceSmooth.Seconds()*fps)))
	logd := make([]float64, n)
	known := false
	for i := range n {
		logd[i] = math.NaN()
		a, b := max(0, i-w), min(n-1, i+w)
		if c := count[b+1] - count[a]; c > 0 {
			v := (moving[b+1] - moving[a]) / c * fps
			logd[i] = math.Log(math.Max(AutoDistanceMin, math.Min(AutoDistanceMax, v/autoPace)))
			known = true
		}
	}
	if !known {
		for i := range logd {
			logd[i] = math.Log(AutoDistanceMin)
		}
	} else {
		fillGaps(logd)
	}
	k := 1 - math.Exp(-1/(distanceSmooth.Seconds()*fps))
	for i := 1; i < n; i++ {
		logd[i] = logd[i-1] + k*(logd[i]-logd[i-1])
	}
	for i := n - 2; i >= 0; i-- {
		logd[i] = logd[i+1] + k*(logd[i]-logd[i+1])
	}
	limit := math.Ln2 / (distanceDoubling.Seconds() * fps)
	clamp := func(d float64) float64 { return math.Max(-limit, math.Min(limit, d)) }
	for i := 1; i < n; i++ {
		logd[i] = logd[i-1] + clamp(logd[i]-logd[i-1])
	}
	for i := n - 2; i >= 0; i-- {
		logd[i] = logd[i+1] + clamp(logd[i]-logd[i+1])
	}
	out := make([]float64, n)
	for i := range logd {
		out[i] = math.Exp(logd[i])
	}
	return out
}

// How the distance in each frame is smoothed: see autoDistances. A speed
// under stoppedSpeed, in metres a second of video, is standing still.
const (
	distanceSmooth   = 3 * time.Second
	distanceDoubling = 2 * time.Second
	stoppedSpeed     = 0.5
)

// offsetMetres is how far east and north one place is of another.
func offsetMetres(lat0, lon0, lat1, lon1 float64) (east, north float64) {
	return (lon1 - lon0) * 111_320 * math.Cos(lat0*math.Pi/180), (lat1 - lat0) * 110_574
}

// MetresBetween is the distance between two places, in metres, on the
// ground flattened around them: near enough for the few kilometres between
// two fixes.
func MetresBetween(lat0, lon0, lat1, lon1 float64) float64 {
	return math.Hypot(offsetMetres(lat0, lon0, lat1, lon1))
}
