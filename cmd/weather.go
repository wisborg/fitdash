package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/effort"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/weather"

	"github.com/wisborg/fitdash/internal/panel"
)

// weatherPlan is what --weather did, for writeWeatherSummary: the weather
// fetched, or why there is none.
type weatherPlan struct {
	asked    bool
	w        *panel.Weather
	reason   string
	exposure effort.Exposure
	acclim   acclimatisation
}

// resolveWeather fetches the weather the activity was done in, when
// --weather asks for it. A failure is not fatal -- the render goes on
// without the weather, as it goes on without a map that cannot be fetched
// -- and is said in the summary.
func resolveWeather(cmd *cobra.Command, track *fitactivity.Track, flight bool) (weatherPlan, error) {
	x, err := parseExposure(renderOpts.windExposure)
	if err != nil {
		return weatherPlan{}, fmt.Errorf("render: %w", err)
	}
	a, err := parseAcclimatised(renderOpts.acclimatised)
	if err != nil {
		return weatherPlan{}, fmt.Errorf("render: %w", err)
	}
	for _, f := range []string{"wind-exposure", "acclimatised"} {
		if !renderOpts.weather && cmd.Flags().Changed(f) {
			fmt.Fprintf(cmd.ErrOrStderr(), "--%s is only read with --weather\n", f)
		}
	}
	plan := fetchWeather(cmd, track, flight, renderOpts.weather)
	plan.exposure, plan.acclim = x, a
	if plan.w != nil {
		plan.w.Exposure = x
	}
	return plan, nil
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
		plan.w = panel.NewWeather(track, r, effort.Suburban)
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
	c, ok := costsOf(plan, track, plan.exposure, plan.acclim)
	writeCostsNote(out, c, ok)
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

// --wind-exposure's values: how much of the wind at 10 m reaches a runner
// a metre up, by the ground they ran over. See effort.Exposure.
const (
	exposureOpen     = "open"
	exposureSuburban = "suburban"
	exposureUrban    = "urban"
)

var exposures = map[string]effort.Exposure{
	exposureOpen: effort.Open, exposureSuburban: effort.Suburban, exposureUrban: effort.Urban,
}

const windExposureHelp = "with --weather, how much of the wind forecast at 10 m reached you a metre up, by the ground " +
	"you ran over: \"open\" (fields, a beach: 0.60 of it), \"suburban\" (default: houses, gardens and trees, 0.38) or " +
	"\"urban\" (between taller buildings, 0.25) -- the logarithmic profile wind follows over ground that rough. " +
	"It scales the wind's cost, not its direction"

// parseExposure validates --wind-exposure.
func parseExposure(name string) (effort.Exposure, error) {
	if x, ok := exposures[name]; ok {
		return x, nil
	}
	return 0, fmt.Errorf("--wind-exposure %q is invalid; use %s, %s or %s", name, exposureOpen, exposureSuburban, exposureUrban)
}

// exposureName is --wind-exposure's name for x.
func exposureName(x effort.Exposure) string {
	for name, v := range exposures {
		if v == x {
			return name
		}
	}
	return fmt.Sprintf("%.2f", float64(x))
}

// acclimatisation is what --acclimatised says the athlete is adapted to:
// the heat, the altitude. Nothing published says how much acclimatisation
// takes off either loss, so an adjustment the athlete is adapted to is
// left out, and said to be, rather than guessed at.
type acclimatisation struct{ heat, altitude bool }

const acclimatisedHelp = "with --weather, what you were acclimatised to -- \"heat\", \"altitude\", or both, " +
	"comma-separated -- whose cost is then left out: the figures for both are for a runner not acclimatised, and " +
	"nothing published says by how much acclimatisation reduces them"

// parseAcclimatised validates --acclimatised.
func parseAcclimatised(names []string) (acclimatisation, error) {
	var a acclimatisation
	for _, n := range names {
		switch strings.TrimSpace(n) {
		case "heat":
			a.heat = true
		case "altitude":
			a.altitude = true
		case "":
		default:
			return a, fmt.Errorf("--acclimatised %q is invalid; use heat, altitude or both", n)
		}
	}
	return a, nil
}

// conditionCosts are what the conditions cost over a run, by the effort
// model: the wind at the exposure assumed, and the heat and the altitude
// unless the athlete was acclimatised to them.
type conditionCosts struct {
	exposure effort.Exposure
	acclim   acclimatisation

	wind    time.Duration
	hasWind bool

	heat           time.Duration
	wbgtLo, wbgtHi float64
	hasHeat        bool

	altitude    time.Duration
	highest     float64
	hasAltitude bool

	footpodFrom  float64
	hasFootpod   bool
	forecastFrom float64
}

// costsOf is what the conditions plan fetched cost over track, on foot --
// what the effort model is of -- and nothing otherwise.
func costsOf(plan weatherPlan, track *fitactivity.Track, x effort.Exposure, a acclimatisation) (conditionCosts, bool) {
	if plan.w == nil || !onFoot(track.Sport) {
		return conditionCosts{}, false
	}
	c := conditionCosts{exposure: x, acclim: a}
	elev := panel.BuildElevation(track, panel.DefaultElevationTuning(track))
	grade := func(i int) float64 {
		s := track.Samples[i]
		if elev == nil || elev.Empty() || !s.HasDistance {
			return 0
		}
		return elev.GradeAtDistance(s.Distance, 25)
	}
	r := plan.w.Report
	c.wind, c.hasWind = weather.WindCost(r, track, x, grade)
	c.heat, c.wbgtLo, c.wbgtHi, c.hasHeat = weather.HeatCost(r, track)
	c.altitude, c.highest, c.hasAltitude = weather.AltitudeCost(r, track, x, grade)
	if from, _, ok := weather.FootpodWind(track); ok {
		c.footpodFrom, c.hasFootpod = from, true
		if s, ok := weather.Summarise(r, track); ok {
			c.forecastFrom = s.WindFrom
		}
	}
	return c, true
}

// writeCostsNote says what the conditions cost over the run, each with
// what it rests on: the wind at the exposure assumed, which is its largest
// uncertainty; the heat by the WBGT met and the race data it is from; the
// altitude by the highest point and the oxygen uptake lost there -- and
// where a Stryd pod recorded its air power, which way that says the wind
// blew beside the forecast's: a check on the model's direction, its watts
// being the pod's own reckoning from the shoe.
func writeCostsNote(out io.Writer, c conditionCosts, ok bool) {
	if !ok {
		return
	}
	if c.hasWind {
		cost, verb := c.wind, "cost"
		if cost < 0 {
			verb, cost = "gave", -cost
		}
		fmt.Fprintf(out, "  the wind %s about %s over the run, by Pugh's measure of the air's cost, at %s exposure (%.2f of the 10 m wind)\n",
			verb, formatCost(cost), exposureName(c.exposure), float64(c.exposure))
	}
	if c.hasHeat {
		wbgt := fmt.Sprintf("WBGT %.0f °C", c.wbgtLo)
		if fmt.Sprintf("%.0f", c.wbgtLo) != fmt.Sprintf("%.0f", c.wbgtHi) {
			wbgt = fmt.Sprintf("WBGT %.0f–%.0f °C", c.wbgtLo, c.wbgtHi)
		}
		// In the shade: the sun's own heat is not in it (effort.WBGT).
		wbgt += " in the shade"
		switch {
		case c.acclim.heat:
			fmt.Fprintf(out, "  the heat (%s) is left out: you were acclimatised to it\n", wbgt)
		case c.heat < time.Second:
			fmt.Fprintf(out, "  the heat cost nothing: %s, within the %.1f–%.0f °C races go best in\n", wbgt, effort.CoolestBest, effort.WarmestBest)
		default:
			what := "heat"
			if c.wbgtHi < effort.CoolestBest {
				what = "cold"
			}
			fmt.Fprintf(out, "  the %s cost about %s, at %s, by Mantzios and others' race data, for a runner not acclimatised to it\n",
				what, formatCost(c.heat), wbgt)
		}
	}
	if c.hasAltitude {
		switch {
		case c.highest <= effort.AltitudeThreshold:
			// Nothing to say: most runs are here, and a line saying the
			// altitude cost nothing on every one of them is noise.
		case c.acclim.altitude:
			fmt.Fprintf(out, "  the altitude (up to %.0f m) is left out: you were acclimatised to it\n", c.highest)
		default:
			fmt.Fprintf(out, "  the altitude cost about %s, up to %.0f m, where %.1f%% of oxygen uptake is lost on arrival (Wehrlin and Hallén)\n",
				formatCost(c.altitude), c.highest, 100*effort.AltitudeLoss(c.highest))
		}
	}
	if c.hasFootpod {
		fmt.Fprintf(out, "  Stryd's air power was highest running %s, so a wind from there; the forecast's was from %s, %.0f° apart\n",
			weather.Compass(c.footpodFrom), weather.Compass(c.forecastFrom), weather.Apart(c.footpodFrom, c.forecastFrom))
	}
}

// formatCost is a time the wind cost: "25 s", "1 min 12 s", "14 min".
func formatCost(d time.Duration) string {
	switch s := int(d.Round(time.Second).Seconds()); {
	case d >= 10*time.Minute:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
	case s >= 60 && s%60 == 0:
		return fmt.Sprintf("%d min", s/60)
	case s >= 60:
		return fmt.Sprintf("%d min %d s", s/60, s%60)
	default:
		return fmt.Sprintf("%d s", s)
	}
}

// onFoot says the sport is one the effort model is of: running, walking,
// hiking.
func onFoot(sport string) bool {
	s := strings.ToLower(sport)
	return strings.Contains(s, "run") || s == "walking" || s == "hiking"
}
