package route

import (
	"time"
)

// Leg is one flight of a recording, and where it was on the ground at
// either end: when it began to move after standing at the gate, when it
// took off, when it landed and when it stopped again. A flight recorded from
// the seat carries both ends -- the taxi out and the taxi in -- and between
// them hours at hundreds of kilometres an hour, so a render paced for the
// cruise passes the airports in a frame or two. A recording of a journey
// carries several, with a stopover between each and the next.
//
// A zero time is an end the recording does not show: one started after
// take-off has no Moving and no TakeOff, one stopped before the aircraft
// stood still again has no Stopped.
type Leg struct {
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
	// Between two legs with no stand found -- an aircraft that never stood
	// still for long, or a recording with no fix to tell -- a leg's Stopped
	// and the next one's Moving are the same instant, halfway between.
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

// FindAirports finds the flights of a recording, in order, and where each
// was on the ground, from its fixes; none when the fixes are not a flight's
// -- nothing held cruiseSpeed for cruiseHold -- or are too few to tell.
//
// A flight is a stretch above takeOffSpeed that somewhere holds the cruise;
// one that never does -- a fast train between two flights -- is ground.
func FindAirports(fixes []Point) []Leg {
	if len(fixes) < 2 {
		return nil
	}
	start, end := fixes[0].Time, fixes[len(fixes)-1].Time
	if end.Sub(start) < cruiseHold {
		return nil
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
			speed[i] = MetresBetween(lat[a], lon[a], lat[b], lon[b]) / (float64(b-a) * airportStep.Seconds())
		}
	}

	// The flights: each stretch above takeOffSpeed, kept when it holds the
	// cruise somewhere.
	type span struct{ from, to int } // grid indices, inclusive
	var flights []span
	hold := int(cruiseHold / airportStep)
	for i := 0; i < n; {
		if speed[i] < takeOffSpeed {
			i++
			continue
		}
		j, run, cruising := i, 0, false
		for ; j < n && speed[j] >= takeOffSpeed; j++ {
			if speed[j] >= cruiseSpeed {
				run++
				cruising = cruising || run >= hold
			} else {
				run = 0
			}
		}
		if cruising {
			flights = append(flights, span{i, j - 1})
		}
		i = j
	}
	if len(flights) == 0 {
		return nil
	}

	stand := int(standTime / airportStep)
	legs := make([]Leg, len(flights))
	for k, f := range flights {
		// The ground this leg's ends are looked for in: back to the
		// previous flight's landing, on to the next one's take-off.
		lo, hi := 0, n-1
		loFix, hiFix := start, end
		if k > 0 {
			lo, loFix = flights[k-1].to+1, at(flights[k-1].to+1)
		}
		if k < len(flights)-1 {
			hi, hiFix = flights[k+1].from-1, at(flights[k+1].from-1)
		}
		l := &legs[k]
		if f.from > 0 {
			l.TakeOff = at(f.from)
			l.Moving = loFix
			if k > 0 {
				l.Moving = time.Time{} // settled below, with the previous leg's Stopped
			}
			// The latest stand before take-off, and the last moment the
			// aircraft was still near where it stood -- measured from the
			// stand's start, where it has certainly not yet moved. Within
			// standRadius is still standing, so this comes out late by the
			// time the taxi takes to cover it: half a minute at a walking-
			// pace pushback's end, and the same early at the far end.
			for s := f.from - stand; s >= lo; s-- {
				if standing(lat, lon, s, s+stand) {
					e := s + stand
					for e < f.from && MetresBetween(lat[s], lon[s], lat[e], lon[e]) <= standRadius {
						e++
					}
					l.Moving = at(e - 1)
					break
				}
			}
			for j := len(fixes) - 1; j > 0; j-- {
				if !fixes[j].Time.Before(l.TakeOff) {
					continue
				}
				if fixes[j].Time.Before(loFix) {
					break
				}
				if fixes[j].Time.Sub(fixes[j-1].Time) >= standTime {
					if fixes[j].Time.After(l.Moving) {
						l.Moving = fixes[j].Time
					}
					break
				}
			}
		}
		if f.to < n-1 {
			l.Landing = at(f.to)
			if k == len(flights)-1 {
				l.Stopped = hiFix
			}
			// The earliest stand after landing, and the first moment the
			// aircraft was already near where it stood. Measured from the
			// stand's end, where it has certainly stopped: its start can be
			// up to standRadius short of the stop.
			for s := f.to + 1; s+stand <= hi; s++ {
				if standing(lat, lon, s, s+stand) {
					e := s + stand
					b := e
					for b > f.to && MetresBetween(lat[e], lon[e], lat[b], lon[b]) <= standRadius {
						b--
					}
					l.Stopped = at(b + 1)
					break
				}
			}
			for j := 0; j < len(fixes)-1; j++ {
				if !fixes[j].Time.After(l.Landing) {
					continue
				}
				if fixes[j].Time.After(hiFix) {
					break
				}
				if fixes[j+1].Time.Sub(fixes[j].Time) >= standTime {
					if l.Stopped.IsZero() || fixes[j].Time.Before(l.Stopped) {
						l.Stopped = fixes[j].Time
					}
					break
				}
			}
		}
	}
	// A stopover with no stand found either way: the two legs meet halfway.
	for k := 0; k+1 < len(legs); k++ {
		a, b := &legs[k], &legs[k+1]
		if a.Stopped.IsZero() || b.Moving.IsZero() || b.Moving.Before(a.Stopped) {
			mid := a.Landing.Add(b.TakeOff.Sub(a.Landing) / 2)
			a.Stopped, b.Moving = mid, mid
		}
	}
	return legs
}

// standing reports whether every position from a to b is within standRadius
// of a's.
func standing(lat, lon []float64, a, b int) bool {
	for i := a + 1; i <= b; i++ {
		if MetresBetween(lat[a], lon[a], lat[i], lon[i]) > standRadius {
			return false
		}
	}
	return true
}
