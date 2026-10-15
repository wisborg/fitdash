package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/plot"
)

// plotMetric is one thing `fitdash plot` can draw: a reading of a sample,
// in the units the chart is drawn in, and how its axis reads.
type plotMetric struct {
	// name is how --y spells it: inspect's name, lower case, hyphenated --
	// "heart-rate" -- or a developer field's own name as inspect lists it.
	name string
	// inspect is the metric's name in inspect's report, from which its
	// name is taken, so plot and inspect name a file's metrics alike.
	inspect string
	// unit is the unit its readings are drawn in.
	unit string
	// from names where the readings come from, where more than one sensor
	// could have taken them: "Stryd" or "watch", for power.
	from string
	kind plot.Kind
	// inverted runs the axis backwards: a pace, faster higher.
	inverted bool
	// value is a sample's reading, and whether it has one.
	value func(fitactivity.Sample) (float64, bool)
}

// flagName is inspect's name for a metric as --y spells it.
func flagName(inspectName string) string {
	return strings.ReplaceAll(strings.ToLower(inspectName), " ", "-")
}

// plotMetrics are the metrics plot knows, for an activity of sport whose
// readings are written in u and whose power is read from power: every one
// inspect counts that has a reading to draw, pace beside speed, and the
// activity's developer fields.
//
// power is read as the render reads it, from the source --power-source
// names, so a chart of a run's power is the power its dashboard shows: on
// a run with a Stryd footpod, by default, the footpod's, not the watch's,
// which can read half as much again. powerFrom names the source the title
// gives it.
//
// Position is not among them: a latitude against time is not a chart
// anyone wants, and the route is what a map is for. Distance is, since a
// chart of distance against time is how far the activity had gone, which
// is.
func plotMetrics(sport string, u units.Set, devFields []inspect.Metric, power fitactivity.PowerSource, powerFrom string) []plotMetric {
	cadenceFactor, cadenceUnit := fitactivity.CadenceUnit(sport)
	ms := []plotMetric{
		{inspect: inspect.MetricHeartRate, unit: "bpm", value: func(s fitactivity.Sample) (float64, bool) {
			return float64(s.HeartRate), s.HasHeartRate
		}},
		{inspect: inspect.MetricPower, unit: "W", from: powerFrom, value: func(s fitactivity.Sample) (float64, bool) {
			return s.ResolvedPower(power)
		}},
		{inspect: inspect.MetricCadence, unit: cadenceUnit, value: func(s fitactivity.Sample) (float64, bool) {
			return float64(s.Cadence) * cadenceFactor, s.HasCadence
		}},
		{inspect: inspect.MetricSpeed, unit: u.Speed.Name, value: func(s fitactivity.Sample) (float64, bool) {
			return u.Speed.FromSI(s.Speed), s.HasSpeed
		}},
		// A pace is the time a distance takes, and standing still takes
		// forever: no pace at all, rather than one drawn off the bottom of
		// the scale that a reader would take for very slow.
		{name: "pace", inspect: inspect.MetricSpeed, unit: u.Pace.Name, kind: plot.Clock, inverted: true,
			value: func(s fitactivity.Sample) (float64, bool) {
				return u.Pace.FromSI(s.Speed), s.HasSpeed && s.Speed > 0
			}},
		{inspect: inspect.MetricElevation, unit: u.Elevation.Name, value: func(s fitactivity.Sample) (float64, bool) {
			return u.Elevation.FromSI(s.Elevation), s.HasElevation
		}},
		{inspect: inspect.MetricDistance, unit: u.Distance.Name, value: func(s fitactivity.Sample) (float64, bool) {
			return u.Distance.FromSI(s.Distance), s.HasDistance
		}},
		{inspect: inspect.MetricTemperature, unit: u.Temperature.Name, value: func(s fitactivity.Sample) (float64, bool) {
			return u.Temperature.FromSI(float64(s.Temperature)), s.HasTemperature
		}},
		{inspect: inspect.MetricVerticalOscillation, unit: "mm", value: func(s fitactivity.Sample) (float64, bool) {
			return s.VerticalOscillation, s.HasVerticalOscillation
		}},
		{inspect: inspect.MetricStanceTime, unit: "ms", value: func(s fitactivity.Sample) (float64, bool) {
			return s.StanceTime, s.HasStanceTime
		}},
		{inspect: inspect.MetricStepLength, unit: "mm", value: func(s fitactivity.Sample) (float64, bool) {
			return s.StepLength, s.HasStepLength
		}},
	}
	for i := range ms {
		if ms[i].name == "" {
			ms[i].name = flagName(ms[i].inspect)
		}
	}
	// A developer field is spelled as the file names it: its name is the
	// device's, "Leg Spring Stiffness", and there is no other to give.
	for _, d := range devFields {
		name := d.Name
		ms = append(ms, plotMetric{name: name, inspect: name, unit: d.Unit, value: func(s fitactivity.Sample) (float64, bool) {
			v, ok := s.DevFields[name]
			return v, ok
		}})
	}
	return ms
}

// findPlotMetric is the metric --y names -- spelled exactly, or failing
// that without regard to case -- or an error listing the ones there are.
//
// Exactly first, because a footpod's developer field can share a name
// with the watch's own metric but for its case: Stryd records "Power"
// beside the watch's power, and the two are different readings.
func findPlotMetric(ms []plotMetric, name string) (plotMetric, error) {
	for _, m := range ms {
		if m.name == name {
			return m, nil
		}
	}
	for _, m := range ms {
		if strings.EqualFold(m.name, name) {
			return m, nil
		}
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.name
	}
	sort.Strings(names)
	return plotMetric{}, fmt.Errorf("--y %q is not a metric plot knows; it knows %s", name, strings.Join(names, ", "))
}

// coverage is the fraction of track's samples carrying m's reading,
// counted from the reading the chart draws -- so power's is the chosen
// source's, and pace's leaves out standing still, which has none.
func (m plotMetric) coverage(track *fitactivity.Track) float64 {
	if len(track.Samples) == 0 {
		return 0
	}
	n := 0
	for _, s := range track.Samples {
		if _, ok := m.value(s); ok {
			n++
		}
	}
	return float64(n) / float64(len(track.Samples))
}

// powerFrom names the source power is read from, for a chart's title:
// "Stryd" or "watch". Under auto, which prefers Stryd and falls back to
// the watch, it is the one the activity has; a file that records neither
// has no power to chart, and none is named.
func powerFrom(track *fitactivity.Track, src fitactivity.PowerSource) string {
	switch {
	case src == fitactivity.PowerStryd, src == fitactivity.PowerAuto && track.HasPower(fitactivity.PowerStryd):
		return "Stryd"
	case track.HasPower(fitactivity.PowerNative):
		return "watch"
	}
	return ""
}
