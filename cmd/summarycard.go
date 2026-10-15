package cmd

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/panel"
)

// flyoverHold is how long a full-frame flyover holds its overview at the
// start, before the camera comes down, and at the end, after it has gone
// back up: long enough to read the summary card shown over it.
const flyoverHold = 3 * time.Second

// sportTitles are the words a card is titled with for a sport, where the
// file names no flight; anything else is titled with the sport's own name.
var sportTitles = map[string]string{
	"running": "Run", "cycling": "Ride", "walking": "Walk", "hiking": "Hike",
	"swimming": "Swim", "flying": "Flight", "trail_running": "Trail run",
}

// summaryCard is a full-frame flyover's summary of the activity: what it was
// -- and, for a flight, the aircraft that flew it -- and when, how far and how long, and the figures that say most about it --
// a flight's top speed and cruising level, anything else's climb and its
// average pace or speed -- each written in the render's units and left out
// where the file does not have it.
func summaryCard(track *fitactivity.Track, timer *fitactivity.TimerModel, elev *fitactivity.ElevationModel, u units.Set, speedReadout string, flight bool, pushback time.Time) panel.SummaryCard {
	var card panel.SummaryCard
	card.Title = panel.FlightTitle(track.Flights)
	if card.Title == "" {
		card.Title = sportTitles[track.Sport]
		if card.Title == "" && track.Sport != "" {
			card.Title = strings.ToUpper(track.Sport[:1]) + strings.ReplaceAll(track.Sport[1:], "_", " ")
		}
	}
	if aircraft := panel.FlightAircraft(track.Flights); aircraft != "" {
		card.Lines = append(card.Lines, aircraft)
	}
	start, end := timer.Window()
	if day, ok := localDay(track, start); ok {
		card.Lines = append(card.Lines, day)
	}

	var figures []string
	dist, hasDist := distanceCovered(track)
	if hasDist {
		figures = append(figures, fmt.Sprintf("%.1f %s", u.Distance.FromSI(dist), u.Distance.Name))
	}
	took := timer.Elapsed(end)
	if flight && !pushback.IsZero() && pushback.Before(end) {
		took = end.Sub(pushback)
	}
	figures = append(figures, panel.FormatClock(took))
	card.Lines = append(card.Lines, strings.Join(figures, " · "))

	var more []string
	if flight {
		if top, ok := maxOf(track, func(s fitactivity.Sample) (float64, bool) { return s.Speed, s.HasSpeed }); ok {
			more = append(more, "max "+units.FormatSpeed(top, u.Speed))
		}
		if high, ok := maxOf(track, func(s fitactivity.Sample) (float64, bool) { return s.Elevation, s.HasElevation }); ok {
			more = append(more, cruisingLevel(high, u.Elevation))
		}
	} else {
		if elev != nil && !elev.Empty() {
			more = append(more, fmt.Sprintf("↑ %.0f %s", u.Elevation.FromSI(elev.TotalGain()), u.Elevation.Name))
		}
		if active := timer.Active(end); hasDist && active > 0 {
			speed := dist / active.Seconds()
			if speedReadout == panel.SpeedReadoutSpeed {
				more = append(more, "avg "+units.FormatSpeed(speed, u.Speed))
			} else {
				more = append(more, "avg "+units.FormatPace(speed, u.Pace))
			}
		}
	}
	if len(more) > 0 {
		card.Lines = append(card.Lines, strings.Join(more, " · "))
	}
	return card
}

// localDay is the day the activity began where it began, "25 Jul 2026":
// its start in UTC, moved an hour for every fifteen degrees of the first
// fix's longitude. A FIT file's times are UTC and nothing here knows the
// place's time zone, so this is the sun's time rather than the clock's --
// the right day except within an hour or two of midnight, where a UTC date
// is wrong for half the world: a flight leaving Bangkok just after midnight
// would be dated the day before.
func localDay(track *fitactivity.Track, start time.Time) (string, bool) {
	if start.IsZero() {
		return "", false
	}
	for _, s := range track.Samples {
		if s.HasGPS {
			offset := time.Duration(math.Round(s.Lon/15)) * time.Hour
			return start.UTC().Add(offset).Format("2 Jan 2006"), true
		}
	}
	return start.UTC().Format("2 Jan 2006"), true
}

// distanceCovered is how far the activity went, by its own distance record.
func distanceCovered(track *fitactivity.Track) (float64, bool) {
	first, last := -1.0, -1.0
	for _, s := range track.Samples {
		if !s.HasDistance {
			continue
		}
		if first < 0 {
			first = s.Distance
		}
		last = s.Distance
	}
	if first < 0 || last <= first {
		return 0, false
	}
	return last - first, true
}

// maxOf is the largest value of the samples that have one.
func maxOf(track *fitactivity.Track, value func(fitactivity.Sample) (float64, bool)) (float64, bool) {
	top, found := math.Inf(-1), false
	for _, s := range track.Samples {
		if v, ok := value(s); ok {
			top, found = math.Max(top, v), true
		}
	}
	return top, found
}

// cruisingLevel is an aircraft's highest altitude as a pilot says it, a
// flight level in hundreds of feet, "FL360" -- or, where the render is in
// metres, the height in metres, since a flight level is feet by definition.
func cruisingLevel(metres float64, u units.Unit) string {
	if u.Name == units.Foot.Name {
		return fmt.Sprintf("FL%03.0f", math.Round(metres/0.3048/100))
	}
	return fmt.Sprintf("max %.0f %s", u.FromSI(metres), u.Name)
}
