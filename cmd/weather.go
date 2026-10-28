package cmd

import (
	"io"

	"github.com/spf13/cobra"

	cw "github.com/wisborg/cobrakit/weather"
	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/weather"

	"github.com/wisborg/fitdash/internal/panel"
)

// weatherPlan is what --weather did, through cobrakit's weather -- the
// weather fetched, or why there is none -- and the panel's view of it.
type weatherPlan struct {
	cw.Plan
	w *panel.Weather
}

// renderWeatherHelp is the render's --weather help.
const renderWeatherHelp = "show the weather the activity was done in -- temperature, dew point, and the wind turned " +
	"to your direction of travel -- in a panel beside the climb and gradient and on the start and end cards. It is " +
	"Open-Meteo's model weather, at a place every 10 km along the route, and NOTE that fetching it sends the area and " +
	"hours of your activity to open-meteo.com; it is kept under your user cache directory, so a second render sends " +
	"nothing. A weather that cannot be fetched is never fatal: the render goes on without it and the summary says " +
	"why. On a run the panel also says what the wind is costing, as seconds on the pace the same effort would have " +
	"run in still air, and the summary what it, the heat and the altitude cost over the run"

// resolveWeather fetches the weather the activity was done in, when
// --weather asks for it, for the panel and the summary.
func resolveWeather(cmd *cobra.Command, o cw.Options, track *fitactivity.Track, flight bool) (weatherPlan, error) {
	p, err := cw.Resolve(cmd, o, track, flight)
	if err != nil {
		return weatherPlan{}, err
	}
	wp := weatherPlan{Plan: p}
	if p.Report != nil {
		wp.w = panel.NewWeather(track, p.Report, p.Exposure)
	}
	return wp, nil
}

// grade is the grade at each sample on the elevation model the render's
// profile is drawn on, so a cost is on the hills the video shows.
func grade(track *fitactivity.Track) func(int) float64 {
	return cw.Grade(track, panel.BuildElevation(track, panel.DefaultElevationTuning(track)))
}

// writeWeatherSummary says what --weather drew from, whether this run sent
// anything, the credit, and on a run what the conditions cost.
func writeWeatherSummary(out io.Writer, p weatherPlan, track *fitactivity.Track, u units.Set) {
	if renderOpts.quiet {
		return
	}
	cw.WriteNote(out, p.Plan, track, u, "the video")
	c, ok := cw.CostsOf(p.Plan, track, u, grade(track))
	cw.WriteCosts(out, c, ok)
}

// weatherCardLine is the line --weather adds to the start and end cards:
// the weather over the activity, and the credit beside it.
func weatherCardLine(w *panel.Weather, track *fitactivity.Track, u units.Set) (string, bool) {
	if w == nil {
		return "", false
	}
	s, ok := weather.Summarise(w.Report, track)
	if !ok {
		return "", false
	}
	return cw.Line(s, u) + " · Open-Meteo.com", true
}
