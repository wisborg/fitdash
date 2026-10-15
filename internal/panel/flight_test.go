package panel

import (
	"math"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
)

// On a flight the climb and gradient panels give way to altitude, vertical
// speed and track; on anything else those three are not placed, and every
// other panel is the filter's no concern.
func TestFlightKeeps(t *testing.T) {
	flight, run := &Context{Flight: true}, &Context{}
	for _, c := range []struct {
		p               Panel
		onFlight, else_ bool
	}{
		{ClimbPanel{}, false, true},
		{GradientPanel{}, false, true},
		{Altitude(), true, false},
		{VerticalSpeed(), true, false},
		{Track(), true, false},
		{Speed(), true, true},
		{Cadence(), true, true},
	} {
		if got := FlightKeeps(flight, c.p); got != c.onFlight {
			t.Errorf("%s on a flight: kept %v, want %v", c.p.Name(), got, c.onFlight)
		}
		if got := FlightKeeps(run, c.p); got != c.else_ {
			t.Errorf("%s off a flight: kept %v, want %v", c.p.Name(), got, c.else_)
		}
	}
}

func TestFlightTitle(t *testing.T) {
	for _, c := range []struct {
		flights []fitactivity.Flight
		want    string
	}{
		{nil, ""},
		{[]fitactivity.Flight{{Number: "TG950", Callsign: "THA950", From: "BKK", To: "CPH"}}, "TG950 BKK → CPH"},
		{[]fitactivity.Flight{{Callsign: "THA950"}}, "THA950"},
		{[]fitactivity.Flight{{Number: "QF592", From: "SYD", To: "OOL"}, {Number: "QF595", From: "OOL", To: "SYD"}}, "QF592 SYD → OOL · QF595 OOL → SYD"},
	} {
		if got := FlightTitle(c.flights); got != c.want {
			t.Errorf("FlightTitle(%+v) = %q, want %q", c.flights, got, c.want)
		}
	}
}

// A flight's aircraft is its model and registration, its type designator
// where the file has no model; a journey's legs flown by one aircraft name
// it once, and a file naming no aircraft names none.
func TestFlightAircraft(t *testing.T) {
	for _, c := range []struct {
		flights []fitactivity.Flight
		want    string
	}{
		{nil, ""},
		{[]fitactivity.Flight{{Number: "XY1"}}, ""},
		{[]fitactivity.Flight{{Aircraft: "Example 320", AircraftType: "A20N", Registration: "XX-ABC"}}, "Example 320 · XX-ABC"},
		{[]fitactivity.Flight{{AircraftType: "A20N"}}, "A20N"},
		{[]fitactivity.Flight{{Registration: "XX-ABC"}}, "XX-ABC"},
		{[]fitactivity.Flight{
			{Number: "XY1", Aircraft: "Example 320", Registration: "XX-ABC"},
			{Number: "XY2", Aircraft: "Example 320", Registration: "XX-ABC"},
			{Number: "XY3", Aircraft: "Example 330", Registration: "XX-DEF"},
		}, "Example 320 · XX-ABC / Example 330 · XX-DEF"},
	} {
		if got := FlightAircraft(c.flights); got != c.want {
			t.Errorf("FlightAircraft(%+v) = %q, want %q", c.flights, got, c.want)
		}
	}
}

// climbTrack climbs at rate metres a second for an hour, a sample a second,
// heading due east at 200 m/s.
func climbTrack(rate float64) *fitactivity.Track {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := &fitactivity.Track{}
	for i := range 3600 {
		tr.Samples = append(tr.Samples, fitactivity.Sample{
			Time: t0.Add(time.Duration(i) * time.Second), HasGPS: true,
			Lat: 10, Lon: 20 + float64(i)*200/(111_320*math.Cos(10*math.Pi/180)),
			HasElevation: true, Elevation: 1000 + rate*float64(i),
		})
	}
	return tr
}

func TestFlightReadouts(t *testing.T) {
	tr := climbTrack(10) // 10 m/s: 1968.5 ft/min
	ft := units.Set{Elevation: units.Foot}
	ctx := &Context{Track: tr, Units: ft}
	mid := tr.Samples[1800]

	alt := Altitude()
	alt = alt.bind(ctx, alt)
	if v, ok := alt.value(mid); !ok || math.Abs(v-(1000+18000)/0.3048) > 1e-6 || alt.unit != "ft" {
		t.Errorf("altitude %v %s (ok %v), want %v ft", v, alt.unit, ok, (1000+18000)/0.3048)
	}

	vs := VerticalSpeed()
	vs = vs.bind(ctx, vs)
	v, ok := vs.value(mid)
	if !ok || math.Abs(v-600/0.3048) > 1e-6 || vs.unit != "ft/min" {
		t.Errorf("vertical speed %v %s (ok %v), want %v ft/min", v, vs.unit, ok, 600/0.3048)
	}
	if got := vs.format(v); got != "+1970" {
		t.Errorf("formatted %q, want +1970", got)
	}
	if got := formatVerticalSpeed(-3); got != "0" {
		t.Errorf("a level flight's rate formatted %q, want 0", got)
	}
	if got := formatVerticalSpeed(-812); got != "-810" {
		t.Errorf("a descent formatted %q, want -810", got)
	}
	// At the first sample there is no altitude a window before it.
	if _, ok := vs.value(tr.Samples[0]); ok {
		t.Error("a vertical speed with nothing before it to measure from")
	}

	track := Track()
	track = track.bind(ctx, track)
	if v, ok := track.value(mid); !ok || math.Abs(v-90) > 0.5 {
		t.Errorf("track from the fixes %v (ok %v), want 90", v, ok)
	}
	// Moving, the route's own bearing, whatever the recorded course says:
	// a tracker's course can be noise.
	withCourse := mid
	withCourse.HasCourse, withCourse.Course = true, 271
	if v, ok := track.value(withCourse); !ok || math.Abs(v-90) > 0.5 {
		t.Errorf("track %v (ok %v) with a recorded 271, want the route's 90", v, ok)
	}
	// Bound to a timeline, the bearing is taken over a moment of video:
	// still the route's.
	ctx.Timeline, _ = NewTimeline(tr.Samples[0].Time, time.Hour, 10, 600)
	timed := Track()
	timed = timed.bind(ctx, timed)
	if v, ok := timed.value(withCourse); !ok || math.Abs(v-90) > 0.5 {
		t.Errorf("track over a moment of video %v (ok %v), want 90", v, ok)
	}
	if got := track.format(359.7); got != "000°" {
		t.Errorf("359.7 formatted %q, want 000°", got)
	}

	// Standing still: no direction.
	still := climbTrack(0)
	for i := range still.Samples {
		still.Samples[i].Lon = 20
	}
	stood := Track()
	stood = stood.bind(&Context{Track: still}, stood)
	if _, ok := stood.value(still.Samples[1800]); ok {
		t.Error("a track for an aircraft standing still")
	}
	// Standing still with a recorded course -- at a gate -- it is that.
	parked := still.Samples[1800]
	parked.HasCourse, parked.Course = true, 14
	if v, ok := stood.value(parked); !ok || v != 14 {
		t.Errorf("standing still, track %v (ok %v), want the recorded 14", v, ok)
	}
}
