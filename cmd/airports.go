package cmd

import (
	"fmt"
	"io"
	"math"
	"strconv"
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
// at the gate, and the airports' paced stretches with their eases.
type airportPlan struct {
	found    bool
	airports route.Airports
	leave    []panel.Cut
	paces    []panel.Pacing

	// departure and arrival are the airports' own stretches, without the
	// eases, as offsets; zero when not paced. dropped names an airport left
	// at the base rate because a --highlight covers it.
	departure, arrival [2]time.Duration
	dropped            []string
}

// left is the time the plan leaves out.
func (p airportPlan) left() time.Duration {
	var d time.Duration
	for _, c := range p.leave {
		d += c.Removed()
	}
	return d
}

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

// findAirports finds a flight's airports in the track and the time to leave
// out at its gates, as offsets from the activity's window start. The paces
// come later, once the base rate is known: see paceAirports.
func findAirports(track *fitactivity.Track, timer *fitactivity.TimerModel) airportPlan {
	a, ok := route.FindAirports(route.FromTrack(track, len(track.Samples)+1))
	if !ok {
		return airportPlan{}
	}
	start, end := timer.Window()
	p := airportPlan{found: true, airports: a}
	if !a.Moving.IsZero() && a.Moving.After(start) {
		p.leave = append(p.leave, panel.Cut{From: 0, To: a.Moving.Sub(start)})
	}
	if !a.Stopped.IsZero() && a.Stopped.Before(end) {
		p.leave = append(p.leave, panel.Cut{From: a.Stopped.Sub(start), To: end.Sub(start)})
	}
	return p
}

// paceAirports gives each airport of p its own stretch of video at base
// rate base, eased to the base rate on the side toward the flight, and
// leaves an airport any highlight overlaps at the base rate. An airport
// whose ground stretch would take less than airportVideo even at the base
// rate is already slow enough, and is left as it is.
func paceAirports(p airportPlan, timer *fitactivity.TimerModel, base float64, highlights []panel.Highlight) airportPlan {
	if !p.found {
		return p
	}
	start, end := timer.Window()
	span := end.Sub(start)
	a := p.airports

	var dep, arr []panel.Pacing
	// The ease of each may reach no further than halfway to the other's
	// stretch, so a short flight's two eases meet rather than overlap.
	depEnd, arrStart := time.Duration(-1), time.Duration(-1)
	if !a.TakeOff.IsZero() {
		depEnd = min(a.TakeOff.Sub(start)+airportMargin, span)
	}
	if !a.Landing.IsZero() {
		arrStart = max(a.Landing.Sub(start)-airportMargin, 0)
	}
	limitAfter, limitBefore := span, time.Duration(0)
	if depEnd >= 0 && arrStart >= 0 {
		mid := (depEnd + arrStart) / 2
		limitAfter, limitBefore = mid, mid
	}
	if depEnd >= 0 {
		from := time.Duration(0)
		if !a.Moving.IsZero() {
			from = max(a.Moving.Sub(start), 0)
		}
		if core, ok := airportPacing(from, depEnd, base); ok {
			dep = append([]panel.Pacing{core}, easeAfter(depEnd, limitAfter, core.Rate, base)...)
			p.departure = [2]time.Duration{from, depEnd}
		}
	}
	if arrStart >= 0 {
		to := span
		if !a.Stopped.IsZero() {
			to = min(a.Stopped.Sub(start), span)
		}
		if core, ok := airportPacing(arrStart, to, base); ok {
			arr = append(easeBefore(arrStart, limitBefore, core.Rate, base), core)
			p.arrival = [2]time.Duration{arrStart, to}
		}
	}
	if overlapsAny(dep, highlights) {
		dep, p.departure = nil, [2]time.Duration{}
		p.dropped = append(p.dropped, "departure")
	}
	if overlapsAny(arr, highlights) {
		arr, p.arrival = nil, [2]time.Duration{}
		p.dropped = append(p.dropped, "arrival")
	}
	p.paces = append(dep, arr...)
	return p
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
// the render does not show, said in the one place it can be.
func writeAirportSummary(out io.Writer, p airportPlan, tl panel.Timeline, wanted bool) {
	if !wanted || renderOpts.quiet {
		return
	}
	if !p.found {
		if renderOpts.airports == airportsOn {
			fmt.Fprintf(out, "airports: --airports on, but this activity is not a flight -- nothing held 400 km/h for five minutes -- so nothing was paced\n")
		}
		return
	}
	stretch := func(name string, s [2]time.Duration) string {
		if s[1] <= s[0] {
			return ""
		}
		frames := tl.IndexAt(s[1]) - tl.IndexAt(s[0])
		rate := (s[1] - s[0]).Seconds() / (float64(frames) / tl.FPS())
		return fmt.Sprintf("%s %s-%s -> %ss (%sx)", name, panel.FormatClock(s[0]), panel.FormatClock(s[1]),
			strconv.FormatFloat(float64(frames)/tl.FPS(), 'f', 1, 64), formatMultiplier(rate))
	}
	var parts []string
	if s := stretch("departure", p.departure); s != "" {
		parts = append(parts, s)
	}
	if s := stretch("arrival", p.arrival); s != "" {
		parts = append(parts, s)
	}
	if len(parts) > 0 {
		line := parts[0]
		if len(parts) > 1 {
			line += ", " + parts[1]
		}
		fmt.Fprintf(out, "airports: %s, easing to the base rate toward the flight; %s of video added\n",
			line, panel.FormatClock(tl.PacedExtra()))
	} else if len(p.dropped) == 0 {
		fmt.Fprintf(out, "airports: a flight, but the base rate already gives its airports their time; nothing was paced\n")
	}
	for _, d := range p.dropped {
		fmt.Fprintf(out, "airports: the %s is covered by a --highlight, which paces it instead\n", d)
	}
	for _, c := range p.leave {
		where := "after the aircraft stopped at the arrival gate"
		if c.From == 0 {
			where = "standing before the aircraft first moved"
		}
		fmt.Fprintf(out, "airports: %s %s left out (%s-%s)\n", panel.FormatClock(c.Removed()), where,
			panel.FormatClock(c.From), panel.FormatClock(c.To))
	}
}
