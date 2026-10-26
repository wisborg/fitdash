package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/weather"

	"github.com/wisborg/fitdash/internal/panel"
)

// weatherPlan is what --weather did, for writeWeatherSummary: the weather
// fetched, or why there is none.
type weatherPlan struct {
	asked  bool
	w      *panel.Weather
	reason string
}

// resolveWeather fetches the weather the activity was done in, when
// --weather asks for it. A failure is not fatal -- the render goes on
// without the weather, as it goes on without a map that cannot be fetched
// -- and is said in the summary.
func resolveWeather(cmd *cobra.Command, track *fitactivity.Track, flight bool) weatherPlan {
	return fetchWeather(cmd, track, flight, renderOpts.weather)
}

// fetchWeather is resolveWeather for any command that has a --weather of
// its own: asked says whether it was given.
func fetchWeather(cmd *cobra.Command, track *fitactivity.Track, flight, asked bool) weatherPlan {
	if !asked {
		return weatherPlan{}
	}
	plan := weatherPlan{asked: true}
	if flight {
		// Open-Meteo's is the weather at the ground, and a flight spent
		// its hours kilometres above it.
		plan.reason = "a flight is above the weather at the ground, which is all there is to fetch"
		return plan
	}
	dir, err := weather.DefaultCacheDir()
	if err != nil {
		dir = ""
	}
	r, err := weather.New(dir).Fetch(fetchContext(cmd), track)
	switch {
	case errors.Is(err, weather.ErrNoPosition):
		plan.reason = "the activity records no position to ask the weather for"
	case err != nil:
		plan.reason = err.Error()
	default:
		plan.w = panel.NewWeather(track, r)
	}
	return plan
}

// writeWeatherSummary says what --weather drew from, and whether this run
// told Open-Meteo where and when the activity was -- said only when it did,
// as the basemap's line is, so the one line about what was sent is never
// noise -- with the credit the data's licence asks for wherever they are
// shown, the video's viewers included.
func writeWeatherSummary(out io.Writer, plan weatherPlan, track *fitactivity.Track, u units.Set) {
	if renderOpts.quiet {
		return
	}
	writeWeatherNote(out, plan, track, u)
}

// writeWeatherNote is writeWeatherSummary for any command: what the
// weather was, where it came from, and the credit.
func writeWeatherNote(out io.Writer, plan weatherPlan, track *fitactivity.Track, u units.Set) {
	if !plan.asked {
		return
	}
	if plan.w == nil {
		fmt.Fprintf(out, "weather: none -- %s\n", plan.reason)
		return
	}
	origin := "the area and hours of this activity were sent to open-meteo.com"
	if plan.w.Report.Cached {
		origin = "served from your cache; nothing was sent this run"
	}
	line := "weather: " + origin
	if s, ok := weather.Summarise(plan.w.Report, track); ok {
		line = "weather: " + weatherLine(s, u) + " -- " + origin
	}
	fmt.Fprintln(out, line)
	fmt.Fprintf(out, "  a model's weather, not a measurement: the wind is at 10 m in the open. %s (CC BY 4.0, %s), "+
		"from ECMWF's IFS and Copernicus's ERA5 (contains modified Copernicus Climate Change Service information); "+
		"credit it wherever you publish the video\n", weather.Credit, weather.CreditURL)
}

// weatherLine is the weather over an activity in a line: "14-16 °C, dew
// point 9 °C, wind 15 km/h from WSW, gusts 25". A card and the summary
// say the same thing in the same words.
func weatherLine(s weather.Summary, u units.Set) string {
	t := func(v float64) float64 { return u.Temperature.FromSI(v) }
	temp := fmt.Sprintf("%.0f%s", t(s.TemperatureMin), u.Temperature.Name)
	if lo, hi := fmt.Sprintf("%.0f", t(s.TemperatureMin)), fmt.Sprintf("%.0f", t(s.TemperatureMax)); lo != hi {
		temp = lo + "–" + hi + u.Temperature.Name
	}
	parts := []string{temp, fmt.Sprintf("dew point %.0f%s", t(s.DewPoint), u.Temperature.Name)}
	if s.WindSpeed < 0.3 {
		parts = append(parts, "calm")
	} else {
		wind := fmt.Sprintf("wind %.0f %s from %s", u.Speed.FromSI(s.WindSpeed), u.Speed.Name, weather.Compass(s.WindFrom))
		if gust := u.Speed.FromSI(s.Gusts); gust >= u.Speed.FromSI(s.WindSpeed)+5 {
			wind += fmt.Sprintf(", gusts %.0f", gust)
		}
		parts = append(parts, wind)
	}
	return strings.Join(parts, ", ")
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
	return weatherLine(s, u) + " · Open-Meteo.com", true
}
