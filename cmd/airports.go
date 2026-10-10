package cmd

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/route"
)

// --airports' values.
const (
	airportsAuto = "auto"
	airportsOn   = "on"
	airportsOff  = "off"
)

const (
	// airportVideo is how much video each airport's ground stretch takes:
	// the taxi out and the roll, or the roll and the taxi in. Ten minutes of
	// taxiing in twelve seconds is fifty times real time, a few hundred
	// metres of airfield a second -- close enough for a flyover's camera to
	// come down to the taxiways, and short enough that the airports do not
	// outlast the flight in a minute's video.
	airportVideo = 12 * time.Second

	// airportMargin is how far into the air an airport's stretch reaches
	// past the take-off, and before the landing: the moment the wheels
	// leave the runway is at the pace of the taxi, not of the climb.
	airportMargin = 30 * time.Second

	// airportDoubling is the video time over which the rate eases from an
	// airport's to the base rate doubles, the way a flyover's camera
	// distance follows it -- a little faster than the camera may change,
	// so the airports do not take much longer than airportVideo, and slow
	// enough that the camera is not left far behind an aircraft that has
	// suddenly gone a hundred times faster.
	airportDoubling = time.Second

	// airportEaseStep is the video time each step of the ease lasts.
	airportEaseStep = 4.0 / 30 * float64(time.Second)
)

// airportPlan is what --airports does to a render: the stretches left out
// at the gates, and the airports' paced stretches with their eases.
type airportPlan struct {
	legs  []route.Leg
	leave []panel.Cut
	paces []panel.Pacing

	// paced is each airport given its own stretch, without its ease, for the
	// summary; dropped names each a --highlight covers and paces instead.
	paced   []airportStretch
	dropped []string
}

// airportStretch is one airport's ground stretch, as offsets.
type airportStretch struct {
	name     string
	from, to time.Duration
}

// found reports whether the activity is a flight at all.
func (p airportPlan) found() bool { return len(p.legs) > 0 }

// left is the time the plan leaves out.
func (p airportPlan) left() time.Duration {
	var d time.Duration
	for _, c := range p.leave {
		d += c.Removed()
	}
	return d
}

// groundWord is what a stopover's seam is called on screen: the aircraft
// stood at an airport, which is not a pause anybody took.
const groundWord = "ON THE GROUND"

// airportsWanted reads --airports: whether a flight's airports are to be
// found and paced.
func airportsWanted() (bool, error) {
	switch renderOpts.airports {
	case airportsAuto:
		return renderOpts.routeView == "flyover", nil
	case airportsOn:
		return true, nil
	case airportsOff:
		return false, nil
	}
	return false, fmt.Errorf("render: --airports %q is invalid; use auto, on or off", renderOpts.airports)
}

// findAirports finds a flight's legs in the track and the time to leave out
// at its gates, as offsets from the activity's window start: before the
// aircraft first moved, between each stop and the next pushback -- a seam
// whose card says the aircraft was on the ground -- and after it last
// stopped. The paces come later, once the base rate is known: see
// paceAirports.
func findAirports(track *fitactivity.Track, timer *fitactivity.TimerModel) airportPlan {
	legs := route.FindAirports(route.FromTrack(track, len(track.Samples)+1))
	if len(legs) == 0 {
		return airportPlan{}
	}
	start, end := timer.Window()
	p := airportPlan{legs: legs}
	if first := legs[0].Moving; !first.IsZero() && first.After(start) {
		p.leave = append(p.leave, panel.Cut{From: 0, To: first.Sub(start)})
	}
	for k := 0; k+1 < len(legs); k++ {
		if stop, move := legs[k].Stopped, legs[k+1].Moving; move.After(stop) {
			p.leave = append(p.leave, panel.Cut{From: stop.Sub(start), To: move.Sub(start), Word: groundWord})
		}
	}
	if last := legs[len(legs)-1].Stopped; !last.IsZero() && last.Before(end) {
		p.leave = append(p.leave, panel.Cut{From: last.Sub(start), To: end.Sub(start)})
	}
	return p
}

// paceAirports gives each airport of p -- every departure and arrival, a
// stopover's as much as the journey's ends -- its own stretch of video at
// base rate base, eased to the base rate on the side toward the flight, and
// leaves an airport any highlight overlaps at the base rate. An airport
// whose ground stretch would take less than airportVideo even at the base
// rate is already slow enough, and is left as it is.
func paceAirports(p airportPlan, timer *fitactivity.TimerModel, base float64, highlights []panel.Highlight) airportPlan {
	if !p.found() {
		return p
	}
	start, end := timer.Window()
	span := end.Sub(start)
	name := func(k int, what string) string {
		if len(p.legs) == 1 {
			return what
		}
		return fmt.Sprintf("leg %d %s", k+1, what)
	}
	var paces []panel.Pacing
	for k, l := range p.legs {
		// Each of a leg's two eases may reach no further than halfway to
		// the other's stretch, so a short flight's eases meet rather than
		// overlap.
		depEnd, arrStart := time.Duration(-1), time.Duration(-1)
		if !l.TakeOff.IsZero() {
			depEnd = min(l.TakeOff.Sub(start)+airportMargin, span)
		}
		if !l.Landing.IsZero() {
			arrStart = max(l.Landing.Sub(start)-airportMargin, 0)
		}
		limitAfter, limitBefore := span, time.Duration(0)
		if depEnd >= 0 && arrStart >= 0 {
			mid := (depEnd + arrStart) / 2
			limitAfter, limitBefore = mid, mid
		}
		if depEnd >= 0 {
			from := max(l.Moving.Sub(start), 0)
			if core, ok := airportPacing(from, depEnd, base); ok {
				dep := append([]panel.Pacing{core}, easeAfter(depEnd, limitAfter, core.Rate, base)...)
				paces = p.add(paces, dep, airportStretch{name(k, "departure"), from, depEnd}, highlights)
			}
		}
		if arrStart >= 0 {
			to := min(l.Stopped.Sub(start), span)
			if core, ok := airportPacing(arrStart, to, base); ok {
				arr := append(easeBefore(arrStart, limitBefore, core.Rate, base), core)
				paces = p.add(paces, arr, airportStretch{name(k, "arrival"), arrStart, to}, highlights)
			}
		}
	}
	p.paces = paces
	return p
}

// add appends an airport's paces to paces and records it as paced, or, when
// a highlight overlaps any of them, records it as left to the highlight.
func (p *airportPlan) add(paces, airport []panel.Pacing, s airportStretch, highlights []panel.Highlight) []panel.Pacing {
	if overlapsAny(airport, highlights) {
		p.dropped = append(p.dropped, s.name)
		return paces
	}
	p.paced = append(p.paced, s)
	return append(paces, airport...)
}

// airportPacing is the stretch from..to given airportVideo, or false when
// the base rate already gives it that much or more.
func airportPacing(from, to time.Duration, base float64) (panel.Pacing, bool) {
	if to <= from {
		return panel.Pacing{}, false
	}
	rate := (to - from).Seconds() / airportVideo.Seconds()
	if rate >= base {
		return panel.Pacing{}, false
	}
	return panel.Pacing{From: from, To: to, Rate: rate}, true
}

// easeAfter is the steps from rate up to base, from from onward and ending
// at limit at the latest: each lasts airportEaseStep of video, at a rate
// doubling every airportDoubling.
func easeAfter(from, limit time.Duration, rate, base float64) []panel.Pacing {
	var out []panel.Pacing
	for _, r := range easeRates(rate, base) {
		if from >= limit {
			break
		}
		to := min(from+time.Duration(r*airportEaseStep), limit)
		out = append(out, panel.Pacing{From: from, To: to, Rate: r})
		from = to
	}
	return out
}

// easeBefore is easeAfter mirrored: the steps from base down to rate,
// ending at to and beginning at limit at the earliest.
func easeBefore(to, limit time.Duration, rate, base float64) []panel.Pacing {
	var out []panel.Pacing
	for _, r := range easeRates(rate, base) {
		if to <= limit {
			break
		}
		from := max(to-time.Duration(r*airportEaseStep), limit)
		out = append([]panel.Pacing{{From: from, To: to, Rate: r}}, out...)
		to = from
	}
	return out
}

// easeRates is the rates of an ease from rate to base, slowest first, not
// including either end.
func easeRates(rate, base float64) []float64 {
	step := math.Pow(2, airportEaseStep/float64(airportDoubling))
	var out []float64
	for r := rate * step; r < base; r *= step {
		out = append(out, r)
	}
	return out
}

// overlapsAny reports whether any of paces overlaps any of highlights.
func overlapsAny(paces []panel.Pacing, highlights []panel.Highlight) bool {
	for _, p := range paces {
		for _, h := range highlights {
			if p.From < h.To && h.From < p.To {
				return true
			}
		}
	}
	return false
}

// isFlight is whether the activity is a flight, for the time panel: a
// track whose sport is flying -- a Flightradar24 flight says so -- or one
// whose flights were found, whether or not --airports paces them.
func isFlight(track *fitactivity.Track, p airportPlan) bool {
	if track.Sport == "flying" || p.found() {
		return true
	}
	return len(route.FindAirports(route.FromTrack(track, len(track.Samples)+1))) > 0
}

// pausedWithin is how much of the stretch c, as offsets from start, the
// activity spent paused.
func pausedWithin(timer *fitactivity.TimerModel, start time.Time, c panel.Cut) time.Duration {
	var d time.Duration
	for _, p := range timer.Pauses() {
		from, to := max(p.Start.Sub(start), c.From), min(p.End.Sub(start), c.To)
		if to > from {
			d += to - from
		}
	}
	return d
}

// writeAirportSummary reports what --airports did: which ground stretches
// it paced and at what rate, and how long at the gates it left out -- time
// the render does not show, said in the one place it can be besides a
// stopover's own card.
func writeAirportSummary(out io.Writer, p airportPlan, tl panel.Timeline, wanted bool) {
	if !wanted || renderOpts.quiet {
		return
	}
	if !p.found() {
		if renderOpts.airports == airportsOn {
			fmt.Fprintf(out, "airports: --airports on, but this activity is not a flight -- nothing held 400 km/h for five minutes -- so nothing was paced\n")
		}
		return
	}
	var parts []string
	for _, s := range p.paced {
		frames := tl.IndexAt(s.to) - tl.IndexAt(s.from)
		video := float64(frames) / tl.FPS()
		parts = append(parts, fmt.Sprintf("%s %s-%s -> %ss (%sx)", s.name, panel.FormatClock(s.from), panel.FormatClock(s.to),
			strconv.FormatFloat(video, 'f', 1, 64), formatMultiplier((s.to-s.from).Seconds()/video)))
	}
	if len(parts) > 0 {
		flights := "a flight"
		if len(p.legs) > 1 {
			flights = fmt.Sprintf("%d flights", len(p.legs))
		}
		fmt.Fprintf(out, "airports: %s; %s, easing to the base rate toward the flight; %s of video added\n",
			flights, strings.Join(parts, ", "), panel.FormatClock(tl.PacedExtra()))
	} else if len(p.dropped) == 0 {
		fmt.Fprintf(out, "airports: a flight, but the base rate already gives its airports their time; nothing was paced\n")
	}
	for _, d := range p.dropped {
		fmt.Fprintf(out, "airports: the %s is covered by a --highlight, which paces it instead\n", d)
	}
	for _, c := range p.leave {
		var where string
		switch {
		case c.Word != "":
			where = "on the ground at a stopover, marked on screen as it passes"
		case c.From == 0:
			where = "standing before the aircraft first moved"
		default:
			where = "after the aircraft stopped at the arrival gate"
		}
		fmt.Fprintf(out, "airports: %s %s left out (%s-%s)\n", panel.FormatClock(c.Removed()), where,
			panel.FormatClock(c.From), panel.FormatClock(c.To))
	}
}
