package route

import (
	"math"
	"testing"
	"time"
)

// leg is a stretch of a synthetic track: d long at speed m/s due east,
// with a fix every every.
type leg struct {
	d, every time.Duration
	speed    float64
}

// track lays legs end to end from a fixed origin, eastward, with a fix
// every leg's own interval; a leg at speed 0 is a stand. jitter, when not
// zero, moves alternate fixes of a stand that many metres north, as a GPS
// indoors wanders.
func track(jitter float64, legs ...leg) []Point {
	t := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	east := 0.0
	var pts []Point
	add := func(at time.Time, north float64) {
		pts = append(pts, Point{Lat: -33.9 + north/110_574, Lon: 151.2 + east/(111_320*math.Cos(-33.9*math.Pi/180)), Time: at})
	}
	add(t, 0)
	for _, l := range legs {
		steps := int(l.d / l.every)
		for i := 1; i <= steps; i++ {
			east += l.speed * l.every.Seconds()
			north := 0.0
			if l.speed == 0 && i%2 == 1 {
				north = jitter
			}
			add(t.Add(time.Duration(i)*l.every), north)
		}
		t = t.Add(time.Duration(steps) * l.every)
	}
	return pts
}

// near reports whether got is within tol of want, an offset from start.
// The ends of a taxi are found to within the time it takes to cover
// standRadius, so their tolerance is half a minute.
func near(got, start time.Time, want, tol time.Duration) bool {
	d := got.Sub(start) - want
	return d >= -tol && d <= tol
}

// A flight as a phone records it from the seat: a long stand at the gate
// logged as a few fixes far apart, a taxi with a hold at the runway, the
// roll, the cruise, the landing, a taxi in and a stand at the arrival gate
// where the GPS wanders indoors.
func TestFindAirportsFindsBothEnds(t *testing.T) {
	fixes := track(40,
		leg{20 * time.Minute, 10 * time.Minute, 0}, // gate, two fixes
		leg{6 * time.Minute, 5 * time.Second, 5},   // taxi out
		leg{2 * time.Minute, 5 * time.Second, 0},   // hold short
		leg{1 * time.Minute, 2 * time.Second, 40},  // roll and climb
		leg{90 * time.Minute, 10 * time.Second, 230},
		leg{1 * time.Minute, 2 * time.Second, 70}, // approach and roll
		leg{5 * time.Minute, 5 * time.Second, 6},  // taxi in
		leg{15 * time.Minute, 5 * time.Second, 0}, // arrival gate
	)
	start := fixes[0].Time
	legs := FindAirports(fixes)
	if len(legs) != 1 {
		t.Fatalf("%d flights found, want 1", len(legs))
	}
	a := legs[0]
	if !near(a.Moving, start, 20*time.Minute, 30*time.Second) {
		t.Errorf("moving at %v, want 20m: the gate, logged as fixes ten minutes apart, is a stand", a.Moving.Sub(start))
	}
	// The roll reaches takeOffSpeed only averaged with the cruise after it.
	if !near(a.TakeOff, start, 29*time.Minute, 30*time.Second) {
		t.Errorf("take-off at %v, want about 29m", a.TakeOff.Sub(start))
	}
	if !near(a.Landing, start, 120*time.Minute, 15*time.Second) {
		t.Errorf("landing at %v, want about 2h, the end of the roll", a.Landing.Sub(start))
	}
	if !near(a.Stopped, start, 125*time.Minute, 30*time.Second) {
		t.Errorf("stopped at %v, want 2h5m: the arrival gate's wander is inside a stand", a.Stopped.Sub(start))
	}
	// The roll begins as the hold short ends, and ends as the taxi in
	// begins: faster than a taxi between, give or take the speed's window.
	if !near(a.Roll, start, 28*time.Minute, speedWindow) {
		t.Errorf("the roll began at %v, want 28m, the end of the hold", a.Roll.Sub(start))
	}
	if !near(a.RolledOut, start, 120*time.Minute, speedWindow) {
		t.Errorf("the roll ended at %v, want 2h, the start of the taxi in", a.RolledOut.Sub(start))
	}
}

// SpeedAt is the speed over the window either side: the leg's own inside
// one, and between two legs' a blend.
func TestSpeedAt(t *testing.T) {
	fixes := track(0, leg{10 * time.Minute, 5 * time.Second, 5}, leg{10 * time.Minute, 5 * time.Second, 40})
	start := fixes[0].Time
	for _, c := range []struct {
		at   time.Duration
		want float64
	}{
		{5 * time.Minute, 5},
		{15 * time.Minute, 40},
		{10 * time.Minute, 22.5},
	} {
		if got := SpeedAt(fixes, start.Add(c.at)); math.Abs(got-c.want) > 0.1 {
			t.Errorf("speed at %v: %.2f m/s, want %v", c.at, got, c.want)
		}
	}
	if got := SpeedAt(fixes[:1], start); got != 0 {
		t.Errorf("one fix's speed %v, want 0", got)
	}
}

// The hold at the runway is shorter than a stand, so it is part of the
// taxi, not the gate.
func TestFindAirportsKeepsAHoldInTheTaxi(t *testing.T) {
	fixes := track(0,
		leg{10 * time.Minute, 5 * time.Second, 0},
		leg{3 * time.Minute, 5 * time.Second, 5},
		leg{3 * time.Minute, 5 * time.Second, 0}, // a long hold, still under standTime
		leg{3 * time.Minute, 5 * time.Second, 5},
		leg{1 * time.Minute, 2 * time.Second, 40},
		leg{60 * time.Minute, 10 * time.Second, 230},
	)
	start := fixes[0].Time
	legs := FindAirports(fixes)
	if len(legs) != 1 {
		t.Fatalf("%d flights found, want 1", len(legs))
	}
	a := legs[0]
	if !near(a.Moving, start, 10*time.Minute, 30*time.Second) {
		t.Errorf("moving at %v, want 10m: the hold is part of the taxi", a.Moving.Sub(start))
	}
	if !a.Landing.IsZero() || !a.Stopped.IsZero() {
		t.Errorf("landing %v, stopped %v: a recording that ends in the cruise shows neither", a.Landing, a.Stopped)
	}
}

// A recording begun in the air has no departure to find.
func TestFindAirportsStartedInTheAir(t *testing.T) {
	fixes := track(0,
		leg{60 * time.Minute, 10 * time.Second, 230},
		leg{1 * time.Minute, 2 * time.Second, 70},
		leg{5 * time.Minute, 5 * time.Second, 6},
	)
	legs := FindAirports(fixes)
	if len(legs) != 1 {
		t.Fatalf("%d flights found, want 1", len(legs))
	}
	a := legs[0]
	if !a.Moving.IsZero() || !a.TakeOff.IsZero() {
		t.Errorf("moving %v, take-off %v: the recording began in the cruise", a.Moving, a.TakeOff)
	}
	if !a.Stopped.Equal(fixes[len(fixes)-1].Time) {
		t.Errorf("stopped at %v, want the last fix: it ended still taxiing", a.Stopped)
	}
}

// Fast, but not a flight: a high-speed train holds 320 km/h for an hour
// between stations, and a car on a motorway less.
func TestFindAirportsIgnoresTrainsAndCars(t *testing.T) {
	for name, speed := range map[string]float64{"train": 320 / 3.6, "car": 130 / 3.6} {
		fixes := track(0,
			leg{10 * time.Minute, 5 * time.Second, 0},
			leg{60 * time.Minute, 5 * time.Second, speed},
			leg{10 * time.Minute, 5 * time.Second, 0},
		)
		if legs := FindAirports(fixes); len(legs) > 0 {
			t.Errorf("a %s at %.0f km/h was taken for a flight", name, speed*3.6)
		}
	}
}

// A journey of two flights: each leg has its own airports, and the
// stopover between them is the first leg's arrival and the second's
// departure, with the stand between left to neither.
func TestFindAirportsFindsAStopover(t *testing.T) {
	fixes := track(40,
		leg{20 * time.Minute, 10 * time.Minute, 0},
		leg{6 * time.Minute, 5 * time.Second, 5},
		leg{1 * time.Minute, 2 * time.Second, 40},
		leg{60 * time.Minute, 10 * time.Second, 230},
		leg{1 * time.Minute, 2 * time.Second, 70},
		leg{5 * time.Minute, 5 * time.Second, 6},   // taxi in
		leg{90 * time.Minute, 15 * time.Minute, 0}, // the stopover, sparse
		leg{4 * time.Minute, 5 * time.Second, 5},   // taxi out again
		leg{1 * time.Minute, 2 * time.Second, 40},
		leg{60 * time.Minute, 10 * time.Second, 230},
		leg{1 * time.Minute, 2 * time.Second, 70},
		leg{5 * time.Minute, 5 * time.Second, 6},
		leg{15 * time.Minute, 5 * time.Second, 0},
	)
	start := fixes[0].Time
	legs := FindAirports(fixes)
	if len(legs) != 2 {
		t.Fatalf("%d flights found, want 2", len(legs))
	}
	if !near(legs[0].Stopped, start, 93*time.Minute, 30*time.Second) {
		t.Errorf("first leg stopped at %v, want 1h33m, the stopover's gate", legs[0].Stopped.Sub(start))
	}
	if !near(legs[1].Moving, start, 183*time.Minute, 30*time.Second) {
		t.Errorf("second leg moving at %v, want 3h3m, the pushback after the stopover", legs[1].Moving.Sub(start))
	}
	if !legs[0].Landing.Before(legs[0].Stopped) || !legs[1].Moving.Before(legs[1].TakeOff) {
		t.Errorf("the stopover's ends are out of order: %+v", legs)
	}
	if !near(legs[1].Stopped, start, 254*time.Minute, 30*time.Second) {
		t.Errorf("second leg stopped at %v, want 4h14m", legs[1].Stopped.Sub(start))
	}
}

// A fast train between two flights is ground, not a third flight.
func TestFindAirportsTrainIsNotAFlight(t *testing.T) {
	fixes := track(0,
		leg{10 * time.Minute, 5 * time.Second, 0},
		leg{3 * time.Minute, 5 * time.Second, 5},
		leg{1 * time.Minute, 2 * time.Second, 40},
		leg{60 * time.Minute, 10 * time.Second, 230},
		leg{1 * time.Minute, 2 * time.Second, 70},
		leg{3 * time.Minute, 5 * time.Second, 6},
		leg{10 * time.Minute, 5 * time.Second, 0},
		leg{40 * time.Minute, 5 * time.Second, 300 / 3.6}, // the train
		leg{10 * time.Minute, 5 * time.Second, 0},
		leg{3 * time.Minute, 5 * time.Second, 5},
		leg{1 * time.Minute, 2 * time.Second, 40},
		leg{60 * time.Minute, 10 * time.Second, 230},
	)
	if legs := FindAirports(fixes); len(legs) != 2 {
		t.Errorf("%d flights found, want 2: the train between them is not one", len(legs))
	}
}
