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
	// choose it from how fast the ground moves in the video (AutoDistance); Pitch
	// how far it looks below the horizontal, and FOV its field of view, in
	// degrees, 0 for perspective.DefaultFOV.
	Distance, Pitch, FOV float64
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
	if o.Distance == 0 {
		o.Distance = AutoDistance(lat, lon, o.FPS)
	}

	// The direction of travel: from where the activity is half the turning
	// window before to where it is half after, held where it does not move.
	half := max(1, frameCount(o.Turn)/2)
	heading := make([]float64, frames)
	prev := math.NaN()
	for i := range frames {
		a, b := max(0, i-half), min(frames-1, i+half)
		if h, ok := bearing(lat[a], lon[a], lat[b], lon[b]); ok {
			prev = h
		}
		heading[i] = prev
	}
	// Before the activity first moves, the direction it first moves in.
	first := math.NaN()
	for _, h := range heading {
		if !math.IsNaN(h) {
			first = h
			break
		}
	}
	if math.IsNaN(first) {
		first = overview.Heading
	}
	for i := range heading {
		if !math.IsNaN(heading[i]) {
			break
		}
		heading[i] = first
	}

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
			Distance: o.Distance, Heading: heading[i], Pitch: o.Pitch, FOV: o.FOV,
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
	swoop := min(frameCount(o.Swoop), frames/3)
	for i := 0; i < swoop; i++ {
		t := smoothstep(float64(i) / float64(swoop))
		cams[i] = blendCamera(overview, cams[i], t)
		j := frames - 1 - i
		cams[j] = blendCamera(overview, cams[j], t)
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

// bearing is the compass bearing from one place to another, in degrees;
// false when they are less than a metre apart, where there is no telling.
func bearing(lat0, lon0, lat1, lon1 float64) (float64, bool) {
	north := (lat1 - lat0) * 110_574
	east := (lon1 - lon0) * 111_320 * math.Cos(lat0*math.Pi/180)
	if math.Hypot(north, east) < 1 {
		return 0, false
	}
	return math.Mod(math.Atan2(east, north)*180/math.Pi+360, 360), true
}

// movingAverage is v averaged over k either side of each value, fewer at
// the ends.
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
		a, b := max(0, i-k), min(len(v)-1, i+k)
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
