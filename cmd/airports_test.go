package cmd

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/panel"
)

// syntheticFlight is a flight as a phone records it from the seat, every
// leg due east from a fixed point: twenty minutes at the gate logged as
// fixes ten minutes apart, a six-minute taxi, the roll, ninety minutes of
// cruise, the landing, a five-minute taxi in and fifteen minutes at the
// arrival gate.
func syntheticFlight() *fitactivity.Track {
	type leg struct {
		d, every time.Duration
		speed    float64
	}
	legs := []leg{
		{20 * time.Minute, 10 * time.Minute, 0},
		{6 * time.Minute, 5 * time.Second, 5},
		{1 * time.Minute, 2 * time.Second, 40},
		{90 * time.Minute, 10 * time.Second, 230},
		{1 * time.Minute, 2 * time.Second, 70},
		{5 * time.Minute, 5 * time.Second, 6},
		{15 * time.Minute, 5 * time.Second, 0},
	}
	t := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	east := 0.0
	tr := &fitactivity.Track{}
	add := func(at time.Time) {
		tr.Samples = append(tr.Samples, fitactivity.Sample{
			Time: at, HasGPS: true,
			Lat: -33.9, Lon: 151.2 + east/(111_320*math.Cos(-33.9*math.Pi/180)),
		})
	}
	add(t)
	for _, l := range legs {
		steps := int(l.d / l.every)
		for i := 1; i <= steps; i++ {
			east += l.speed * l.every.Seconds()
			add(t.Add(time.Duration(i) * l.every))
		}
		t = t.Add(time.Duration(steps) * l.every)
	}
	return tr
}

// near reports whether got is within half a minute of want: the ends of a
// taxi are found to within the time it takes to cover a stand's radius.
func near(got, want time.Duration) bool {
	return got-want <= 30*time.Second && want-got <= 30*time.Second
}

// A flight's gates are left out at both ends, its airports each take about
// airportVideo, and the eases between them and the cruise climb steadily to
// the base rate without meeting.
func TestAirports_PaceAFlight(t *testing.T) {
	track := syntheticFlight()
	timer := fitactivity.BuildTimerModel(track)
	const base, fps = 400.0, 30.0

	p := findAirports(track, timer)
	if !p.found {
		t.Fatal("the flight was not recognised")
	}
	if len(p.leave) != 2 || p.leave[0].From != 0 || !near(p.leave[0].To, 20*time.Minute) || !near(p.leave[1].From, 123*time.Minute) {
		t.Fatalf("left out %v, want the gate before 20m and the arrival gate from 2h3m", p.leave)
	}
	p = paceAirports(p, timer, base, nil)
	if p.departure[1] <= p.departure[0] || p.arrival[1] <= p.arrival[0] {
		t.Fatalf("departure %v, arrival %v: both airports should be paced", p.departure, p.arrival)
	}
	for i := 1; i < len(p.paces); i++ {
		if p.paces[i].From < p.paces[i-1].To {
			t.Fatalf("pace %d %v overlaps the one before it %v", i, p.paces[i], p.paces[i-1])
		}
	}
	for _, pc := range p.paces {
		if pc.Rate >= base {
			t.Errorf("pace %v is not slower than the base rate %v", pc, base)
		}
	}

	tl, err := panel.NewTimelineForActivityPaced(timer, fps, base, nil, panel.PausesFreeze, p.paces, p.leave)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string][2]time.Duration{"departure": p.departure, "arrival": p.arrival} {
		video := time.Duration(float64(tl.IndexAt(s[1])-tl.IndexAt(s[0])) / fps * float64(time.Second))
		if video < airportVideo-time.Second/2 || video > airportVideo+time.Second/2 {
			t.Errorf("the %s takes %v of video, want about %v", name, video, airportVideo)
		}
	}
	// The ease after the departure climbs: each step faster than the one
	// before, as the aircraft goes faster.
	var ease []panel.Pacing
	for _, pc := range p.paces {
		if pc.From >= p.departure[1] && pc.To <= p.arrival[0] && pc.From < 60*time.Minute {
			ease = append(ease, pc)
		}
	}
	if len(ease) < 3 {
		t.Fatalf("%d steps of ease after the departure; want it spread over seconds of video", len(ease))
	}
	for i := 1; i < len(ease); i++ {
		if ease[i].Rate <= ease[i-1].Rate {
			t.Errorf("ease step %d at %vx is no faster than the one before at %vx", i, ease[i].Rate, ease[i-1].Rate)
		}
	}
	if begun := tl.At(0).Sub(track.Samples[0].Time); begun < 19*time.Minute {
		t.Errorf("the render begins %v in; the gate should be left out", begun)
	}
}

// An airport a --highlight covers is the highlight's to pace; the other
// is still paced.
func TestAirports_HighlightTakesItsAirport(t *testing.T) {
	track := syntheticFlight()
	timer := fitactivity.BuildTimerModel(track)
	p := findAirports(track, timer)
	h := panel.Highlight{Name: "push", From: 21 * time.Minute, To: 23 * time.Minute, Video: 5 * time.Second}
	p = paceAirports(p, timer, 400, []panel.Highlight{h})
	if p.departure != [2]time.Duration{} {
		t.Errorf("the departure was paced over a highlight: %v", p.departure)
	}
	if p.arrival[1] <= p.arrival[0] {
		t.Error("the arrival was dropped with the departure")
	}
	if len(p.dropped) != 1 || p.dropped[0] != "departure" {
		t.Errorf("dropped %v, want [departure]", p.dropped)
	}
	for _, pc := range p.paces {
		if pc.From < h.To && h.From < pc.To {
			t.Errorf("pace %v overlaps the highlight", pc)
		}
	}
}

// A render slow enough already gives the airports their time, and nothing
// is paced; the gates are still left out.
func TestAirports_SlowRenderNeedsNoPacing(t *testing.T) {
	track := syntheticFlight()
	timer := fitactivity.BuildTimerModel(track)
	p := paceAirports(findAirports(track, timer), timer, 10, nil)
	if len(p.paces) != 0 {
		t.Errorf("%d paces at 10x; the taxi already takes over half a minute", len(p.paces))
	}
	if len(p.leave) != 2 {
		t.Errorf("left out %v; the gates go whatever the pace", p.leave)
	}
}

// --video-duration compresses what is left once the gates are out, so the
// video comes out the length asked for.
func TestResolveSpeedup_LeavesOutTheGates(t *testing.T) {
	defer func(v time.Duration) { renderOpts.videoDur = v }(renderOpts.videoDur)
	track := syntheticFlight()
	timer := fitactivity.BuildTimerModel(track)
	p := findAirports(track, timer)

	cmd := &cobra.Command{}
	bindRenderFlags(cmd)
	if err := cmd.Flags().Set("video-duration", "1m"); err != nil {
		t.Fatal(err)
	}
	renderOpts.videoDur = time.Minute
	got, err := resolveSpeedup(cmd, timer, panel.PausesFreeze, p.leave)
	if err != nil {
		t.Fatal(err)
	}
	start, end := timer.Window()
	if want := panel.SpeedupFor(end.Sub(start)-p.left(), time.Minute); got != want {
		t.Errorf("speed-up %v, want %v: the time at the gates is not in the video", got, want)
	}
}

// airportsWanted: auto paces only a flyover's airports.
func TestAirportsWanted(t *testing.T) {
	defer func(a, v string) { renderOpts.airports, renderOpts.routeView = a, v }(renderOpts.airports, renderOpts.routeView)
	for _, c := range []struct {
		airports, view string
		want           bool
	}{
		{"auto", "flyover", true},
		{"auto", "flat", false},
		{"auto", "3d", false},
		{"on", "flat", true},
		{"off", "flyover", false},
	} {
		renderOpts.airports, renderOpts.routeView = c.airports, c.view
		got, err := airportsWanted()
		if err != nil || got != c.want {
			t.Errorf("--airports %s --route-view %s: %v, %v; want %v", c.airports, c.view, got, err, c.want)
		}
	}
	renderOpts.airports = "sometimes"
	if _, err := airportsWanted(); err == nil || !strings.Contains(err.Error(), "--airports") {
		t.Errorf("an unknown --airports was accepted: %v", err)
	}
}
