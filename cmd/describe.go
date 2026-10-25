package cmd

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/workout"
	"github.com/wisborg/output"
	"github.com/wisborg/output/table"

	"github.com/wisborg/fitdash/internal/panel"
)

var describeOpts struct {
	power    string
	units    string
	unitEach []string
}

var describeCmd = &cobra.Command{
	Use:   "describe ACTIVITY [ACTIVITY ...]",
	Short: "Say what a workout was: its intervals, its warm-up and cool-down, its climbs and descents",
	Long: `describe reads an activity and says what the workout was, from the recording
alone: the warm-up, the sets of intervals -- how many, how long or how far,
at what effort, with what recoveries -- the cool-down, the stops, and the
climbs and descents along the way. A steady run is said to be steady.

Effort is measured by the best the activity records: power (from
--power-source, as the render reads it), grade-adjusted pace for a run or a
walk without power, speed, and heart rate last of all, since it lags the
effort it follows by the better part of a minute.

A set is three or more hard stretches of much the same effort, each with a
recovery after it of at least a tenth of its length, jogged, walked or
stood: a steady run stopped at the lights is not a set. A climb gains five
metres or more at three per cent or more, and a descent loses as much as
steeply; the same hill met lap after lap is said once, with how often.

In text, the description and then each set's intervals; in CSV, the
intervals; in JSON and YAML, all of it, in metres, seconds and the effort's
own unit.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runDescribe,
}

func init() {
	f := describeCmd.Flags()
	f.StringVar(&describeOpts.power, "power-source", "auto", "which power effort is measured by, as the render's "+
		"--power-source: \"auto\" (default: a Stryd footpod's where the activity has it, the file's own otherwise), "+
		"\"stryd\" or \"native\"")
	f.StringVar(&describeOpts.units, "units", string(units.Metric), "the units distances, speeds and paces are written in "+
		"-- \"metric\" or \"imperial\"; a flight's default is nautical miles, feet and knots")
	f.StringArrayVar(&describeOpts.unitEach, "unit", nil, "one quantity's unit, over --units (repeatable) -- "+
		"distance=km|mi|nm, elevation=m|ft, speed=km/h|mph|kn|m/s, pace=min/km|min/mi")
	root.AddCommand(describeCmd)
}

func runDescribe(cmd *cobra.Command, args []string) error {
	track, sources, err := decodeActivitiesWith(readActivity(cmd), args)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	u, err := parseUnits(describeOpts.units, describeOpts.unitEach)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	if (track.Sport == "flying" || len(track.Flights) > 0) && !cmd.Flags().Changed("units") {
		if u, err = aviationUnits(describeOpts.unitEach); err != nil {
			return fmt.Errorf("describe: %w", err)
		}
	}
	power, err := parsePowerSource(describeOpts.power)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	// The elevation model the render's profile is drawn on, so a climb
	// said here is the climb a render of the same activity shows.
	a := workout.Analyse(track, workout.Options{Power: power, Elevation: panel.DefaultElevationTuning(track)})
	d := describer{track: track, a: a, u: u, powerFrom: powerFrom(track, power), speedDerived: derivedSpeed(sources)}

	out := cmd.OutOrStdout()
	doc := output.Document{Data: d.json(), Table: d.intervalTable()}
	if format.Format == output.Text {
		d.writeText(out, sources)
		if len(a.Sets) == 0 {
			return nil
		}
		fmt.Fprintln(out)
	}
	if err := doc.Write(out, format.Format); err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	return nil
}

// describer turns an analysis into words, in the render's units.
type describer struct {
	track        *fitactivity.Track
	a            workout.Analysis
	u            units.Set
	powerFrom    string
	speedDerived bool
}

// effort writes an effort in its signal's own terms: power in watts,
// grade-adjusted speed as a pace, speed in the speed unit, heart rate in
// beats a minute.
func (d describer) effort(v float64) string {
	switch d.a.Signal {
	case workout.SignalPower:
		return fmt.Sprintf("%.0f W", v)
	case workout.SignalGradeAdjustedSpeed:
		return units.FormatPace(v, d.u.Pace)
	case workout.SignalSpeed:
		return units.FormatSpeed(v, d.u.Speed)
	case workout.SignalHeartRate:
		return fmt.Sprintf("%.0f bpm", v)
	}
	return fmt.Sprintf("%.0f", v)
}

// signalName names what effort was measured by, with its source where it
// has one.
func (d describer) signalName() string {
	switch d.a.Signal {
	case workout.SignalPower:
		if d.powerFrom != "" {
			return "power (" + d.powerFrom + ")"
		}
	case workout.SignalGradeAdjustedSpeed:
		return "grade-adjusted pace"
	case workout.SignalSpeed:
		if d.speedDerived {
			return "speed (from distance)"
		}
	}
	return string(d.a.Signal)
}

// distance writes metres in the distance unit, or in metres under a
// kilometre where the unit is the kilometre.
func (d describer) distance(m float64) string {
	if d.u.Distance.Name == units.Kilometre.Name && m < 1000 {
		return fmt.Sprintf("%.0f m", m)
	}
	return fmt.Sprintf("%.2f %s", d.u.Distance.FromSI(m), d.u.Distance.Name)
}

// clock writes a duration as a clock: "4:55", "1:46:31".
func clock(dur time.Duration) string {
	s := int(dur.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// region is a stretch of the activity outside the sets -- before the
// first, between two, after the last -- as its moving time, distance and
// effort.
type region struct {
	moving   time.Duration
	distance float64
	effort   float64
	hasDist  bool
}

// regionBetween sums the segments starting in [from, to).
func (d describer) regionBetween(from, to time.Time) region {
	var r region
	var sum float64
	for _, s := range d.a.Segments {
		if s.Start.Before(from) || !s.Start.Before(to) {
			continue
		}
		dur := s.Duration()
		r.moving += dur
		sum += s.Effort * dur.Seconds()
		if m, ok := s.Distance(); ok {
			r.distance += m
			r.hasDist = true
		}
	}
	if r.moving > 0 {
		r.effort = sum / r.moving.Seconds()
	}
	return r
}

func (d describer) regionText(r region) string {
	parts := []string{clock(r.moving)}
	if r.hasDist {
		parts = append(parts, d.distance(r.distance))
	}
	parts = append(parts, "at ~"+d.effort(r.effort))
	return strings.Join(parts, ", ")
}

// writeText writes the description.
func (d describer) writeText(w io.Writer, sources []activitySource) {
	tw := &lines{w: w}
	path := sources[0].Path
	if len(sources) > 1 {
		path = fmt.Sprintf("%s and %d more", filepath.Base(path), len(sources)-1)
	}
	fmt.Fprintln(w, path)
	if d.track.Title != "" {
		fmt.Fprintln(w, d.track.Title)
	}

	start, end := fitactivity.BuildTimerModel(d.track).Window()
	var head []string
	if d.track.Sport != "" {
		head = append(head, strings.ReplaceAll(d.track.Sport, "_", " "))
	}
	if first, last, ok := distanceSpan(d.track); ok {
		head = append(head, d.distance(last-first))
	}
	moving := time.Duration(0)
	for _, s := range d.a.Segments {
		moving += s.Duration()
	}
	head = append(head, fmt.Sprintf("%s (moving %s)", clock(end.Sub(start)), clock(moving)))
	if d.a.Signal != "" {
		head = append(head, "effort by "+d.signalName())
	}
	fmt.Fprintln(w, strings.Join(head, " · "))
	fmt.Fprintln(w)

	if d.a.Signal == "" {
		tw.add("effort", "the activity records nothing to measure it by")
	} else if len(d.a.Sets) == 0 {
		d.writeSteady(tw)
	} else {
		d.writeSets(tw, start, end)
	}
	d.writeStops(tw)
	d.writeHills(tw)
	tw.flush()
}

// writeSteady says what a workout with no sets was: its main effort, and
// any other it spent a tenth of its moving time at -- "steady" where
// there is no other, "moving" where there is.
func (d describer) writeSteady(tw *lines) {
	var moving time.Duration
	main := 0
	for i, l := range d.a.Levels {
		moving += l.Duration
		if l.Duration > d.a.Levels[main].Duration {
			main = i
		}
	}
	if moving == 0 {
		return
	}
	text := fmt.Sprintf("%s at ~%s", clock(d.a.Levels[main].Duration), d.effort(d.a.Levels[main].Effort))
	label := "steady"
	for i, l := range d.a.Levels {
		if i != main && l.Duration >= moving/10 {
			text += fmt.Sprintf(", %s at ~%s", clock(l.Duration), d.effort(l.Effort))
			label = "moving"
		}
	}
	tw.add(label, text)
}

// writeSets says what lay before, between and after the sets, and each
// set as a whole.
func (d describer) writeSets(tw *lines, start, end time.Time) {
	from := start
	for k, s := range d.a.Sets {
		first, last := s.Intervals[0], s.Intervals[len(s.Intervals)-1]
		if r := d.regionBetween(from, first.Start); r.moving > 0 {
			name := "between"
			if k == 0 {
				name = "warm-up"
			}
			tw.add(name, d.regionText(r))
		}
		label, text := d.setText(s)
		tw.add(label, text)
		from = last.End
	}
	if r := d.regionBetween(from, end.Add(time.Second)); r.moving > 0 {
		tw.add("cool-down", d.regionText(r))
	}
}

// setText is a set as a label -- "11 × 1 km", "5 × 2:00" -- and the rest
// of its line: each interval's typical time or distance, its effort, its
// recoveries, and any interval far from the others.
func (d describer) setText(s workout.Set) (string, string) {
	n := len(s.Intervals)
	var durs, dists, efforts []float64
	hasDist := true
	for _, iv := range s.Intervals {
		durs = append(durs, iv.Duration().Seconds())
		efforts = append(efforts, iv.Effort)
		m, ok := iv.Distance()
		dists = append(dists, m)
		hasDist = hasDist && ok
	}
	medDur, medDist, medEffort := median(durs), median(dists), median(efforts)
	// By distance where the intervals were alike in distance, within a
	// tenth, and more alike than in time or near a round distance -- a
	// track session, a set of kilometres -- and by time otherwise: a
	// workout set by the clock, on a machine or by power. A set even in
	// both is told by whichever was surely its target: 798 m is 800 m,
	// where 3:15 is no round time.
	byDistance := hasDist && spread(dists) <= 0.1 && (spread(dists) <= spread(durs) || d.nearRound(medDist))
	var label, each string
	if byDistance {
		label = fmt.Sprintf("%d × %s", n, d.roundDistance(medDist))
		each = "~" + clock(time.Duration(medDur)*time.Second) + " each"
	} else {
		label = fmt.Sprintf("%d × %s", n, clock(roundDuration(time.Duration(medDur)*time.Second)))
		if hasDist {
			each = "~" + d.distance(medDist) + " each"
		}
	}
	// An interval far from the set's effort is said -- the first of a set
	// run at a different pace, a last one cut short -- where there are
	// one or two. Where more stray, the set's efforts varied by design, a
	// workout whose targets change, and their range is said instead.
	var strays []string
	lo, hi := efforts[0], efforts[0]
	for i, iv := range s.Intervals {
		lo, hi = math.Min(lo, iv.Effort), math.Max(hi, iv.Effort)
		if math.Abs(iv.Effort-medEffort) > 0.05*medEffort {
			strays = append(strays, fmt.Sprintf("the %s at %s", ordinal(i+1, n), d.effort(iv.Effort)))
		}
	}
	effort := "at ~" + d.effort(medEffort)
	if len(strays) > 2 {
		effort = fmt.Sprintf("at %s to %s", d.effort(lo), d.effort(hi))
		strays = nil
	}
	var parts []string
	if each != "" {
		parts = append(parts, each)
	}
	parts = append(parts, effort)
	if len(s.Recoveries) > 0 {
		var rest []float64
		for _, r := range s.Recoveries {
			rest = append(rest, r.Duration().Seconds())
		}
		parts = append(parts, "~"+clock(time.Duration(median(rest))*time.Second)+" recoveries")
	}
	parts = append(parts, strays...)
	return label, strings.Join(parts, ", ")
}

// roundDistance writes a set's typical interval distance as the round
// distance it was surely meant to be -- within eight per cent of 400 m, of
// a kilometre, of a mile -- or rounded to the nearest 50 m or tenth of the
// unit where it is near none.
func (d describer) roundDistance(m float64) string {
	metric := d.u.Distance.Name == units.Kilometre.Name
	if metric {
		for _, r := range []float64{100, 200, 300, 400, 500, 600, 800, 1000, 1200, 1500, 2000, 3000, 5000} {
			if math.Abs(m-r) <= 0.08*r {
				if r >= 1000 {
					return fmt.Sprintf("%g km", r/1000)
				}
				return fmt.Sprintf("%.0f m", r)
			}
		}
		if m < 1000 {
			return fmt.Sprintf("%.0f m", math.Round(m/50)*50)
		}
	} else {
		mi := units.Mile.FromSI(m)
		for _, r := range []float64{0.25, 0.5, 0.75, 1, 1.5, 2, 3} {
			if math.Abs(mi-r) <= 0.08*r {
				return fmt.Sprintf("%g mi", r)
			}
		}
	}
	return fmt.Sprintf("%.1f %s", d.u.Distance.FromSI(m), d.u.Distance.Name)
}

// nearRound says m is within three per cent of a round distance in the
// render's distance unit.
func (d describer) nearRound(m float64) bool {
	rounds := []float64{100, 200, 300, 400, 500, 600, 800, 1000, 1200, 1500, 2000, 3000, 5000}
	if d.u.Distance.Name != units.Kilometre.Name {
		rounds = []float64{}
		for _, mi := range []float64{0.25, 0.5, 0.75, 1, 1.5, 2, 3} {
			rounds = append(rounds, units.Mile.ToSI(mi))
		}
	}
	for _, r := range rounds {
		if math.Abs(m-r) <= 0.03*r {
			return true
		}
	}
	return false
}

// roundDuration rounds an interval's typical length to the nearest five
// seconds under two minutes and the nearest fifteen above: what a workout
// is set in.
func roundDuration(dur time.Duration) time.Duration {
	if dur < 2*time.Minute {
		return dur.Round(5 * time.Second)
	}
	return dur.Round(15 * time.Second)
}

// ordinal names the i-th of n: "first", "last", or "2nd", "3rd", "11th".
func ordinal(i, n int) string {
	switch {
	case i == 1:
		return "first"
	case i == n:
		return "last"
	}
	suffix := "th"
	if i%100 < 11 || i%100 > 13 {
		switch i % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", i, suffix)
}

// writeStops says how often the activity stopped, and for how long at
// most.
func (d describer) writeStops(tw *lines) {
	if len(d.a.Stops) == 0 {
		return
	}
	longest := d.a.Stops[0]
	var total time.Duration
	for _, s := range d.a.Stops {
		total += s.Duration()
		if s.Duration() > longest.Duration() {
			longest = s
		}
	}
	text := fmt.Sprintf("%d, %s in all, the longest %s", len(d.a.Stops), clock(total), clock(longest.Duration()))
	if len(d.a.Stops) == 1 {
		text = clock(total)
	}
	tw.add("stops", text)
}

// writeHills says the climbs and then the descents: the same hill met
// again and again once, with how often, and the others each, biggest
// first.
func (d describer) writeHills(tw *lines) {
	d.writeRises(tw, "climb", "climbs", "+", climbRises(d.a.Climbs))
	d.writeRises(tw, "descent", "descents", "−", descentRises(d.a.Descents))
}

// writeRises says rises under label, or plural where there are several,
// each height signed with sign.
func (d describer) writeRises(tw *lines, label, plural, sign string, rs []rise) {
	if len(rs) == 0 {
		return
	}
	groups := sameHill(rs)
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].height()*float64(len(groups[i])) > groups[j].height()*float64(len(groups[j]))
	})
	var texts []string
	for _, g := range groups {
		r := g[0]
		text := fmt.Sprintf("%s%s at %.1f%%", sign, d.elevation(g.height()), 100*g.grade())
		if l, ok := g.length(); ok {
			text += " over " + d.climbLength(l)
		}
		if len(g) > 1 {
			text = fmt.Sprintf("%d × %s, the same hill", len(g), text)
		} else {
			text += " from " + d.distance(r.StartDistance)
		}
		texts = append(texts, text)
	}
	if len(rs) > 1 {
		label = plural
	}
	tw.add(label, texts[0])
	for _, t := range texts[1:] {
		tw.add("", t)
	}
}

// climbLength is how far a climb goes on, to the nearest ten metres
// under a kilometre: where a climb begins and ends is the profile's
// judgement, not a measurement to the metre.
func (d describer) climbLength(m float64) string {
	if d.u.Distance.Name == units.Kilometre.Name && m < 1000 {
		return fmt.Sprintf("%.0f m", math.Max(10, 10*math.Round(m/10)))
	}
	return d.distance(m)
}

// elevation is a height, unsigned: "12 m".
func (d describer) elevation(m float64) string {
	return fmt.Sprintf("%.0f %s", d.u.Elevation.FromSI(m), d.u.Elevation.Name)
}

// rise is a climb or a descent as describe says it: where it is, how high
// it rose or fell, and how steeply. The two are said alike, so they are
// grouped and worded by one code.
type rise struct {
	workout.Span
	height, grade float64
	lat, lon      float64
	hasGPS        bool
}

func climbRises(cs []workout.Climb) []rise {
	rs := make([]rise, len(cs))
	for i, c := range cs {
		rs[i] = rise{Span: c.Span, height: c.Gain, grade: c.Grade, lat: c.Lat, lon: c.Lon, hasGPS: c.HasGPS}
	}
	return rs
}

func descentRises(ds []workout.Descent) []rise {
	rs := make([]rise, len(ds))
	for i, c := range ds {
		rs[i] = rise{Span: c.Span, height: c.Loss, grade: c.Grade, lat: c.Lat, lon: c.Lon, hasGPS: c.HasGPS}
	}
	return rs
}

// hill is rises of the same hill.
type hill []rise

func (h hill) height() float64 {
	var g float64
	for _, r := range h {
		g += r.height
	}
	return g / float64(len(h))
}

func (h hill) grade() float64 {
	var g float64
	for _, r := range h {
		g += r.grade
	}
	return g / float64(len(h))
}

// length is how far the hill's rises go on, on average; ok is false when
// one of them has no distance to measure by.
func (h hill) length() (float64, bool) {
	var l float64
	for _, r := range h {
		d, ok := r.Distance()
		if !ok {
			return 0, false
		}
		l += d
	}
	return l / float64(len(h)), true
}

// sameHill groups rises that start within 60 m of one another and rise or
// fall within a third of one another: a loop's hill, met lap after lap. A
// rise without a fix is a hill of its own.
func sameHill(rs []rise) []hill {
	var hills []hill
	for _, r := range rs {
		placed := false
		for i, h := range hills {
			first := h[0]
			if r.hasGPS && first.hasGPS && greatCircleMetres(r.lat, r.lon, first.lat, first.lon) <= 60 &&
				math.Abs(r.height-first.height) <= first.height/3 {
				hills[i] = append(h, r)
				placed = true
				break
			}
		}
		if !placed {
			hills = append(hills, hill{r})
		}
	}
	return hills
}

func greatCircleMetres(lat0, lon0, lat1, lon1 float64) float64 {
	const rad = math.Pi / 180
	dLat, dLon := (lat1-lat0)*rad, (lon1-lon0)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat0*rad)*math.Cos(lat1*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * 6_371_008.8 * math.Asin(math.Min(1, math.Sqrt(a)))
}

// intervalTable is every set's intervals, one row each.
func (d describer) intervalTable() *table.Table {
	t := table.New(
		table.Column{Header: "set", Align: table.Right},
		table.Column{Header: "#", Align: table.Right},
		table.Column{Header: "start", Align: table.Right},
		table.Column{Header: "time", Align: table.Right},
		table.Column{Header: "distance", Align: table.Right},
		table.Column{Header: "effort", Align: table.Right},
		table.Column{Header: "heart rate", Align: table.Right},
		table.Column{Header: "recovery", Align: table.Right},
	)
	start, _ := fitactivity.BuildTimerModel(d.track).Window()
	for k, s := range d.a.Sets {
		for i, iv := range s.Intervals {
			dist, hr, rest := "", "", ""
			if m, ok := iv.Distance(); ok {
				dist = d.distance(m)
			}
			if iv.HasHeartRate {
				hr = fmt.Sprintf("%.0f bpm", iv.HeartRate)
			}
			if i < len(s.Recoveries) {
				rest = clock(s.Recoveries[i].Duration())
			}
			t.MustAppend(k+1, i+1, clock(iv.Start.Sub(start)), clock(iv.Duration()), dist, d.effort(iv.Effort), hr, rest)
		}
	}
	return t
}

// lines writes label-and-text lines with the labels in one column.
type lines struct {
	w    io.Writer
	rows [][2]string
}

func (l *lines) add(label, text string) { l.rows = append(l.rows, [2]string{label, text}) }

func (l *lines) flush() {
	width := 0
	for _, r := range l.rows {
		width = max(width, len([]rune(r[0])))
	}
	for _, r := range l.rows {
		fmt.Fprintf(l.w, "%-*s  %s\n", width, r[0], r[1])
	}
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// spread is how much xs vary, as their standard deviation over their
// mean.
func spread(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	if m == 0 {
		return 0
	}
	var v float64
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return math.Sqrt(v/float64(len(xs))) / m
}
