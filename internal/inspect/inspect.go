// Package inspect reports what a recorded activity actually contains, metric
// by metric.
//
// It exists because "different activities carry different metrics" is this
// project's central design problem rather than an edge case -- a pool swim has
// no GPS, an indoor ride has no elevation, a walk has no power -- and the first
// thing anyone building or debugging a panel needs is a truthful answer to
// "what is in this file?".
//
// The unit of the answer is COVERAGE, not presence. A metric is rarely all
// there or all missing: a GPS receiver loses lock under trees, a heart rate
// strap drops out for a minute, a footpod connects two minutes into the run.
// A report that said only "HeartRate: yes" would hide exactly the cases a
// panel has to handle, so every metric here reports the fraction of samples
// that carry it, and the range it spans, and a caller can see the difference
// between a metric that is present and one that is present 4% of the time.
package inspect

import (
	"math"
	"sort"
	"time"

	"github.com/wisborg/fitactivity"
)

// The names Build gives the metrics it counts.
//
// They are constants rather than string literals because a panel asks the
// report whether an activity carries a metric, by name. A literal on each side
// means a rename silently turns every such question into "no" -- the panel
// would decline on every activity, the layout would close up around it, and
// the result looks exactly like a file that genuinely lacks the data.
const (
	MetricPosition            = "Position"
	MetricElevation           = "Elevation"
	MetricSpeed               = "Speed"
	MetricDistance            = "Distance"
	MetricHeartRate           = "Heart rate"
	MetricCadence             = "Cadence"
	MetricPower               = "Power"
	MetricTemperature         = "Temperature"
	MetricVerticalOscillation = "Vertical oscillation"
	MetricStanceTime          = "Stance time"
	MetricStepLength          = "Step length"
)

// Metric is one metric's coverage across an activity.
type Metric struct {
	// Name is the metric as a person reads it ("Heart rate"), not as the
	// Sample field is spelled.
	Name string
	// Unit is empty for a dimensionless or self-evident metric.
	Unit string
	// Present is the number of samples whose presence flag was set. It is
	// deliberately a count rather than a bool: see the package comment.
	Present int
	// Min, Max span the values of the samples that were present. They are
	// meaningless when Present is 0, which is why Coverage exists and why a
	// caller must check it rather than reading a zero range as "0 to 0".
	Min, Max float64
}

// Coverage is the fraction of the activity's samples carrying this metric, in
// [0,1]. total is the activity's sample count; a total of 0 reports 0 rather
// than dividing by it.
func (m Metric) Coverage(total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(m.Present) / float64(total)
}

// Report is the whole answer for one activity.
type Report struct {
	// Path is the file the report describes -- the FIRST one, when several
	// were merged. Paths is all of them.
	Path string
	// Paths is every activity file that went into this report, in time
	// order: one entry normally, several when a workout recorded in pieces
	// was merged into a single activity.
	//
	// The whole report describes the MERGED activity, so a reader checking a
	// coverage figure against their own expectation needs to know which files
	// produced it. A single Path could not say that without becoming a
	// joined string that is no longer a path.
	Paths []string
	// Sport is the FIT session's sport, or "" when the file did not say. An
	// empty sport is normal for some devices and is NOT an error.
	Sport string
	// Samples is the record count -- the denominator for every Metric's
	// Coverage.
	Samples int
	// Start, End bound the ACTIVITY, as its own timer model resolves it --
	// the session's start_time and declared elapsed total where the file
	// carried them, the first and last sample otherwise. This is the same
	// window the renderer lays its timeline over, deliberately: two answers to
	// "how long was this activity" in one program is one too many.
	//
	// It can therefore be marginally wider than the records themselves cover.
	// Both are zero for a file carrying neither session timing nor samples.
	Start, End time.Time
	// Elapsed is End-Start: the wall-clock span of the activity, INCLUDING
	// any time it was paused. Active is the moving time the FIT's
	// own timer events describe, which is what a watch displays. They differ
	// on any activity with a stop in it, and a dashboard has to choose which
	// one its timeline runs on -- so both are reported rather than one being
	// silently preferred here.
	Elapsed, Active time.Duration
	// HasTimerEvents reports whether Active came from real `timer` events. A
	// file without them yields Active == Elapsed, which is indistinguishable
	// from an activity that was genuinely never paused unless you know which
	// you are looking at.
	HasTimerEvents bool
	// Metrics is every metric this package knows how to count, in a fixed
	// order, INCLUDING the ones with zero coverage. Omitting the absent ones
	// would answer a different question: "does this file have power?" is
	// exactly what a reader is asking, and a missing row cannot be
	// distinguished from a metric this package forgot about.
	Metrics []Metric
	// DevFields are the developer fields the file registered (a Stryd
	// footpod's running dynamics, typically), keyed by the name resolved from
	// the file's own FieldDescription messages. The key set is whatever the
	// recording device reported and is not known at compile time, so these
	// are reported separately from the fixed Metrics above.
	DevFields []Metric
}

// Build computes the report for track.
func Build(track *fitactivity.Track) Report {
	r := Report{Path: track.SourcePath, Paths: track.Sources, Sport: track.Sport, Samples: len(track.Samples)}

	// Start, End, Elapsed and Active all come from the timer model, which owns
	// the question of when an activity ran.
	//
	// This USED to derive the window from the first and last sample instead,
	// and the two are not the same: a session declaring 1553.87 seconds of
	// elapsed time whose records span 1553 gave `inspect` one answer and the
	// renderer -- which lays its timeline over the model's window -- another,
	// for the same file, differing by most of a second. Nothing errored; the
	// two commands simply disagreed about how long the activity was. That is
	// the second definition of an activity's extent this project set out not
	// to have, found by rendering a real file and comparing.
	//
	// The sample count is reported separately, which is the honest way to say
	// that records may cover marginally less than the activity claims.
	if tm := fitactivity.BuildTimerModel(track); tm != nil {
		r.HasTimerEvents = tm.HasTimerEvents()
		r.Start, r.End = tm.Window()
		if !r.Start.IsZero() && !r.End.IsZero() {
			r.Elapsed = tm.Elapsed(r.End)
			r.Active = tm.Active(r.End)
		}
	}

	// One accumulator per metric, fed by a single pass. The presence flag is
	// the gate on every one of them: a Sample's value field is meaningless
	// when its flag is false (see fitactivity's package comment), so reading
	// it unconditionally here would fold sentinel-derived zeros into Min and
	// silently anchor every range at 0.
	acc := []struct {
		name string
		unit string
		get  func(fitactivity.Sample) (float64, bool)
	}{
		{MetricPosition, "deg", func(s fitactivity.Sample) (float64, bool) { return s.Lat, s.HasGPS }},
		{MetricElevation, "m", func(s fitactivity.Sample) (float64, bool) { return s.Elevation, s.HasElevation }},
		{MetricSpeed, "m/s", func(s fitactivity.Sample) (float64, bool) { return s.Speed, s.HasSpeed }},
		{MetricDistance, "m", func(s fitactivity.Sample) (float64, bool) { return s.Distance, s.HasDistance }},
		{MetricHeartRate, "bpm", func(s fitactivity.Sample) (float64, bool) { return float64(s.HeartRate), s.HasHeartRate }},
		{MetricCadence, "rpm", func(s fitactivity.Sample) (float64, bool) { return float64(s.Cadence), s.HasCadence }},
		{MetricPower, "W", func(s fitactivity.Sample) (float64, bool) { return float64(s.Power), s.HasPower }},
		{MetricTemperature, "C", func(s fitactivity.Sample) (float64, bool) { return float64(s.Temperature), s.HasTemperature }},
		{MetricVerticalOscillation, "mm", func(s fitactivity.Sample) (float64, bool) {
			return s.VerticalOscillation, s.HasVerticalOscillation
		}},
		{MetricStanceTime, "ms", func(s fitactivity.Sample) (float64, bool) { return s.StanceTime, s.HasStanceTime }},
		{MetricStepLength, "mm", func(s fitactivity.Sample) (float64, bool) { return s.StepLength, s.HasStepLength }},
	}

	r.Metrics = make([]Metric, len(acc))
	for i, a := range acc {
		m := Metric{Name: a.name, Unit: a.unit, Min: math.Inf(1), Max: math.Inf(-1)}
		for _, s := range track.Samples {
			v, ok := a.get(s)
			if !ok {
				continue
			}
			m.Present++
			m.Min = math.Min(m.Min, v)
			m.Max = math.Max(m.Max, v)
		}
		if m.Present == 0 {
			m.Min, m.Max = 0, 0
		}
		r.Metrics[i] = m
	}

	r.DevFields = buildDevFields(track.Samples)
	return r
}

// buildDevFields counts the developer fields across samples. Unlike the fixed
// metrics above, the key set is discovered from the data, so a field is listed
// only if at least one sample carried it -- there is no "absent" row to print
// for a field nobody registered.
//
// The result is sorted by name so two runs over the same file produce the same
// report; ranging a map would otherwise reorder the rows on every invocation
// and make any diff of two reports unreadable.
func buildDevFields(samples []fitactivity.Sample) []Metric {
	byName := map[string]*Metric{}
	for _, s := range samples {
		for k, v := range s.DevFields {
			m, ok := byName[k]
			if !ok {
				m = &Metric{Name: k, Min: math.Inf(1), Max: math.Inf(-1)}
				byName[k] = m
			}
			m.Present++
			m.Min = math.Min(m.Min, v)
			m.Max = math.Max(m.Max, v)
		}
	}
	out := make([]Metric, 0, len(byName))
	for _, m := range byName {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Metric looks up one metric by name, reporting whether the report knows it.
//
// The ok result is not decoration: a caller asking about a name Build never
// produced -- a typo, or a constant that drifted -- would otherwise be told
// the activity lacks the metric, which is the same answer it would get for a
// file that genuinely does. Callers that treat "unknown" and "absent" alike
// should say so deliberately.
func (r Report) Metric(name string) (Metric, bool) {
	for _, m := range r.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	for _, m := range r.DevFields {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}

// Carries reports whether the activity holds any reading of the named metric.
//
// This is the single rule behind every panel's decision to draw or decline,
// and behind what the inspect command prints. A panel deriving it by walking
// the samples could disagree with the report the user just read.
//
// The threshold is any coverage at all rather than a percentage. A metric
// present four per cent of the time is a metric with a very long dropout, and
// the placeholder path already handles dropouts honestly; a percentage
// threshold would need a defensible number and there is not one.
func (r Report) Carries(name string) bool {
	m, ok := r.Metric(name)
	return ok && m.Present > 0
}
