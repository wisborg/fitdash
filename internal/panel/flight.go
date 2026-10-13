package panel

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/inspect"
)

// A flight's dashboard reads differently from a run's. The climb panel's
// gain and loss and the gradient's slope describe ground a person climbed;
// an aircraft's are its altitude and how fast it is climbing or descending,
// and which way it is heading. So on a flight (Context.Flight) the climb and
// gradient panels give their boxes to Altitude and VerticalSpeed, and the
// gauge column's last box -- cadence, which no flight records -- to Track;
// and on anything else those three are not placed. See FlightKeeps.

const (
	altitudeName      = "altitude"
	verticalSpeedName = "vertical-speed"
	trackName         = "track"
	climbPanelName    = "climb"
	gradientPanelName = "gradient"
)

// FlightKeeps is the render's keep filter's question for a flight: on one,
// the climb and gradient panels are not placed and the flight readouts are;
// on anything else, the other way round. Removed rather than declined, and
// so unreported as missing data, for SpeedReadoutKeeps' reason: a flight's
// climb panel is not missing its data, it is the wrong reading of it.
func FlightKeeps(ctx *Context, p Panel) bool {
	switch p.Name() {
	case climbPanelName, gradientPanelName:
		return !ctx.Flight
	case altitudeName, verticalSpeedName, trackName:
		return ctx.Flight
	}
	return true
}

// Altitude is the aircraft's height above the sea, in the elevation unit:
// feet for a flight unless --units or --unit say otherwise.
func Altitude() Readout {
	r := Readout{
		name: altitudeName, label: "ALTITUDE", metric: inspect.MetricElevation,
		template: "88888", inline: true,
		bind: func(ctx *Context, r Readout) Readout {
			return bindAltitude(r, ctx.units().Elevation)
		},
	}
	return bindAltitude(r, units.Metre)
}

func bindAltitude(r Readout, u units.Unit) Readout {
	r.unit = u.Name
	r.value = func(s fitactivity.Sample) (float64, bool) {
		return u.FromSI(s.Elevation), s.HasElevation
	}
	r.format = func(v float64) string { return fmt.Sprintf("%.0f", v) }
	return r
}

// verticalSpeedWindow is the activity time either side of an instant its
// vertical speed is measured over. A flight tracker reports an altitude in
// hundred-foot steps about every half-minute at cruise, so a rate taken
// from neighbouring reports jumps by hundreds of feet a minute where the
// aircraft held its height; over a minute and a half one step is 70 feet a
// minute, while a climb of two thousand still reads as one.
const verticalSpeedWindow = 45 * time.Second

// VerticalSpeed is how fast the aircraft is climbing, or descending below
// zero, in the elevation unit a minute: ft/min for a flight, as an aircraft's
// own instrument reads it. Measured from the altitude either side of the
// instant (verticalSpeedWindow), and absent where either side has none.
func VerticalSpeed() Readout {
	r := Readout{
		name: verticalSpeedName, label: "VERTICAL SPEED", metric: inspect.MetricElevation,
		template: "-8888", inline: true,
		bind: func(ctx *Context, r Readout) Readout {
			return bindVerticalSpeed(r, ctx.Track, ctx.units().Elevation)
		},
	}
	return bindVerticalSpeed(r, nil, units.Metre)
}

func bindVerticalSpeed(r Readout, track *fitactivity.Track, u units.Unit) Readout {
	r.unit = u.Name + "/min"
	tolerance := fitactivity.DefaultMaxGap
	if track != nil {
		tolerance = track.GapTolerance()
	}
	r.value = func(s fitactivity.Sample) (float64, bool) {
		if track == nil || !s.HasElevation {
			return 0, false
		}
		a, okA := track.AtWithGap(s.Time.Add(-verticalSpeedWindow), tolerance)
		b, okB := track.AtWithGap(s.Time.Add(verticalSpeedWindow), tolerance)
		if !okA || !okB || !a.HasElevation || !b.HasElevation {
			return 0, false
		}
		perSecond := (b.Elevation - a.Elevation) / (2 * verticalSpeedWindow.Seconds())
		return u.FromSI(perSecond * 60), true
	}
	r.format = formatVerticalSpeed
	return r
}

// formatVerticalSpeed is a rate to the nearest ten, signed when it is not
// level: an instrument's "+1500" and "-800", and "0" in level flight.
func formatVerticalSpeed(v float64) string {
	v = math.Round(v/10) * 10
	if v == 0 {
		return "0"
	}
	return fmt.Sprintf("%+.0f", v)
}

// trackWindow is the activity time either side of an instant a direction is
// worked out from the fixes over, where the file records none; and
// trackStill the distance, in metres, the fixes must be apart for it to be
// a direction at all rather than a GPS wandering at a gate.
const (
	trackWindow = 15 * time.Second
	trackStill  = 30.0
)

// Track is the direction the aircraft is going over the ground, in degrees
// from true north: the bearing between where it was a moment of video
// before the instant and where it is a moment after -- the line the route
// draws -- or, standing still, the flight tracker's own where it records
// one. Absent while it stands still with none, where there is no direction
// to give.
//
// The route's bearing rather than the recorded course because a recorded
// course can be noise: a flight tracker's over north-eastern Turkey swung
// fifty degrees every two or three seconds while the aircraft flew straight,
// and at a thousand times real speed the readout jumped with it from frame
// to frame. The moment is headingWindow of video, as the route views' plane
// points, so the two agree; never less than trackWindow of activity, which
// keeps a taxi's bearing from a single noisy fix.
func Track() Readout {
	r := Readout{
		name: trackName, label: "TRACK", metric: inspect.MetricPosition, unit: "deg",
		template: "888°",
		bind: func(ctx *Context, r Readout) Readout {
			return bindTrack(r, ctx.Track, ctx.Timeline)
		},
	}
	return bindTrack(r, nil, Timeline{})
}

func bindTrack(r Readout, track *fitactivity.Track, tl Timeline) Readout {
	tolerance := fitactivity.DefaultMaxGap
	if track != nil {
		tolerance = track.GapTolerance()
	}
	n := 0
	if tl.Frames() > 0 {
		n = max(1, int(math.Round(headingWindow.Seconds()*tl.FPS())))
	}
	r.value = func(s fitactivity.Sample) (float64, bool) {
		recorded := func() (float64, bool) { return s.Course, s.HasCourse }
		if track == nil {
			return recorded()
		}
		before, after := s.Time.Add(-trackWindow), s.Time.Add(trackWindow)
		if n > 0 {
			i := tl.IndexAt(s.Time.Sub(tl.Start()))
			if t := tl.At(i - n); t.Before(before) {
				before = t
			}
			if t := tl.At(i + n); t.After(after) {
				after = t
			}
		}
		a, okA := track.AtWithGap(before, tolerance)
		b, okB := track.AtWithGap(after, tolerance)
		if !okA || !okB || !a.HasGPS || !b.HasGPS {
			return recorded()
		}
		east := (b.Lon - a.Lon) * 111_320 * math.Cos(a.Lat*math.Pi/180)
		north := (b.Lat - a.Lat) * 110_574
		if math.Hypot(east, north) < trackStill {
			return recorded()
		}
		return math.Mod(math.Atan2(east, north)*180/math.Pi+360, 360), true
	}
	r.format = func(v float64) string { return fmt.Sprintf("%03.0f°", math.Mod(math.Round(v), 360)) }
	return r
}

// FlightTitle is what a flight is called on its dashboard: each flight's
// number -- or callsign, where the file has no number -- and the airports
// it flew between, the legs of a journey one after another: "TG950 BKK →
// CPH", "QF592 SYD → OOL · QF595 OOL → SYD". Empty when the file names no
// flight.
func FlightTitle(flights []fitactivity.Flight) string {
	var parts []string
	for _, f := range flights {
		id := f.Number
		if id == "" {
			id = f.Callsign
		}
		s := id
		if f.From != "" && f.To != "" {
			s = strings.TrimSpace(s + " " + f.From + " → " + f.To)
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}
