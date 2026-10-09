package route

import (
	"time"
)

// Airports is where a flight was on the ground at either end: when it began
// to move after standing at the gate, when it took off, when it landed and
// when it stopped again. A flight recorded from the seat carries both ends --
// the taxi out and the taxi in -- and between them hours at hundreds of
// kilometres an hour, so a render paced for the cruise passes the airports in
// a frame or two.
//
// A zero time is an end the recording does not show: one started after
// take-off has no Moving and no TakeOff, one stopped before the aircraft
// stood still again has no Stopped.
type Airports struct {
	// Moving is when the aircraft began to move after its last long stand
	// before take-off -- the gate, or a remote stand -- and is the first fix
	// when the recording began with it already moving.
	Moving time.Time

	// TakeOff and Landing are when the aircraft passed takeOffSpeed on the
	// way up and on the way down: the take-off roll near its end, the
	// landing roll near its start.
	TakeOff, Landing time.Time

	// Stopped is when the aircraft came to its first long stand after
	// landing, and is the last fix when the recording ended still moving.
	Stopped time.Time
}

const (
	// cruiseSpeed is the speed, in m/s, a stretch must hold for cruiseHold
	// for the track to be taken as a flight: 400 km/h. A jet cruises at
	// twice that and a turboprop above it; the fastest trains in service
	// run below it, and a car does not come near it for minutes on end.
	// A light aircraft is slower, and is not recognised: its airports pass
	// at its own pace like any other activity's stretch.
	cruiseSpeed = 400 / 3.6
	cruiseHold  = 5 * time.Minute

	// takeOffSpeed is the speed, in m/s, that marks the take-off and the
	// landing: 200 km/h, near the end of a jet's take-off roll and the
	// start of its landing roll, and well above anything taxiing does.
	takeOffSpeed = 200 / 3.6

	// speedWindow is the time a speed is measured over, either side of an
	// instant. A seat's GPS, behind a window, is noisy fix to fix -- one
	// landing in the example data reads 434 km/h for one second -- and over
	// half a minute that averages out, while a take-off still shows within
	// seconds of where it was.
	speedWindow = 15 * time.Second

	// standTime and standRadius are what counts as standing: a stretch of
	// at least standTime throughout which the aircraft stayed within
	// standRadius metres of where it began. A hold at the runway or in a
	// queue of departures is a minute or two, and is part of the taxi; a
	// gate is longer. A GPS indoors wanders, so the radius allows for tens
	// of metres of it.
	//
	// No fix for standTime counts as a stand as well. A phone logs
	// sparsely while nothing moves, and the example flights stand at the
	// gate as a few fixes ten minutes apart; read as a straight line
	// between them, that is a slow drift of a few hundred metres -- GPS
	// wander, or the pushback -- that never stays inside the radius.
	standTime   = 5 * time.Minute
	standRadius = 150.0

	// airportStep is the spacing the track is looked at with.
	airportStep = 5 * time.Second
)

// FindAirports finds where a flight was on the ground, from its fixes;
// false when the fixes are not a flight's -- nothing held cruiseSpeed for
// cruiseHold -- or are too few to tell.
func FindAirports(fixes []Point) (Airports, bool) {
	if len(fixes) < 2 {
		return Airports{}, false
	}
	start, end := fixes[0].Time, fixes[len(fixes)-1].Time
	if end.Sub(start) < cruiseHold {
		return Airports{}, false
	}
	n := int(end.Sub(start)/airportStep) + 1
	at := func(i int) time.Time { return start.Add(time.Duration(i) * airportStep) }
	lat, lon := make([]float64, n), make([]float64, n)
	for i := range n {
		lat[i], lon[i] = positionAt(fixes, at(i))
	}
	w := int(speedWindow / airportStep)
	speed := make([]float64, n)
	for i := range n {
		a, b := max(0, i-w), min(n-1, i+w)
		if b > a {
			speed[i] = metresBetween(lat[a], lon[a], lat[b], lon[b]) / (float64(b-a) * airportStep.Seconds())
		}
	}

	// A flight: some stretch at cruiseSpeed throughout.
	hold, run, flying := int(cruiseHold/airportStep), 0, false
	for _, v := range speed {
		if v >= cruiseSpeed {
			run++
			if run >= hold {
				flying = true
				break
			}
		} else {
			run = 0
		}
	}
	if !flying {
		return Airports{}, false
	}

	var a Airports
	first, last := -1, -1
	for i, v := range speed {
		if v >= takeOffSpeed {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	// A recording that begins at speed shows no take-off: it was already
	// flying, or on its roll, at its first fix. Likewise at the end.
	if first > 0 && speed[0] < takeOffSpeed {
		a.TakeOff = at(first)
		a.Moving = start
		stand := int(standTime / airportStep)
		// The latest stand before take-off, and the last moment the
		// aircraft was still near where it stood -- measured from the
		// stand's start, where it has certainly not yet moved. Within
		// standRadius is still standing, so this comes out late by the
		// time the taxi takes to cover it: half a minute at a walking-pace
		// pushback's end, and the same early at the far end.
		for s := first - stand; s >= 0; s-- {
			if standing(lat, lon, s, s+stand) {
				e := s + stand
				for e < first && metresBetween(lat[s], lon[s], lat[e], lon[e]) <= standRadius {
					e++
				}
				a.Moving = at(e - 1)
				break
			}
		}
		for j := len(fixes) - 1; j > 0; j-- {
			if !fixes[j].Time.Before(a.TakeOff) {
				continue
			}
			if fixes[j].Time.Sub(fixes[j-1].Time) >= standTime {
				if fixes[j].Time.After(a.Moving) {
					a.Moving = fixes[j].Time
				}
				break
			}
		}
	}
	if last < n-1 && speed[n-1] < takeOffSpeed {
		a.Landing = at(last)
		a.Stopped = end
		stand := int(standTime / airportStep)
		// The earliest stand after landing, and the first moment the
		// aircraft was already near where it stood. Measured from the
		// stand's end, where it has certainly stopped: its start can be up
		// to standRadius short of the stop.
		for s := last + 1; s+stand < n; s++ {
			if standing(lat, lon, s, s+stand) {
				e := s + stand
				b := e
				for b > last && metresBetween(lat[e], lon[e], lat[b], lon[b]) <= standRadius {
					b--
				}
				a.Stopped = at(b + 1)
				break
			}
		}
		for j := 0; j < len(fixes)-1; j++ {
			if !fixes[j].Time.After(a.Landing) {
				continue
			}
			if fixes[j+1].Time.Sub(fixes[j].Time) >= standTime {
				if fixes[j].Time.Before(a.Stopped) {
					a.Stopped = fixes[j].Time
				}
				break
			}
		}
	}
	if a.TakeOff.IsZero() && a.Landing.IsZero() {
		return Airports{}, false
	}
	return a, true
}

// standing reports whether every position from a to b is within standRadius
// of a's.
func standing(lat, lon []float64, a, b int) bool {
	for i := a + 1; i <= b; i++ {
		if metresBetween(lat[a], lon[a], lat[i], lon[i]) > standRadius {
			return false
		}
	}
	return true
}
