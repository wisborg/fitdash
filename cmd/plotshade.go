package cmd

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/workout"
	"github.com/wisborg/output/chart"
	"github.com/wisborg/output/chart/picture"

	"github.com/wisborg/fitdash/internal/panel"
)

// The kinds of stretch --shade marks, as it names them.
const (
	shadeIntervals = "intervals"
	shadeClimbs    = "climbs"
	shadeDescents  = "descents"
)

// shadeKind is a kind of stretch --shade marks: its index in the chart's
// band kinds, which shadeBandKinds makes in this order.
type shadeKind int

const (
	shadeInterval shadeKind = iota
	shadeClimb
	shadeDescent
)

// shadeKinds are the kinds, in the order a legend lists them.
var shadeKinds = []shadeKind{shadeInterval, shadeClimb, shadeDescent}

// shadeNames are the kinds as --shade and a legend name them.
var shadeNames = []string{shadeInterval: shadeIntervals, shadeClimb: shadeClimbs, shadeDescent: shadeDescents}

// shadeBandKinds are the chart's band kinds for --shade, in theme: named
// as the flag names them, an interval marked with a heavy rule, a climb
// with a slope up and a descent with one down; an interval in the line's
// own colour, as the hard part of the trace it is, a climb in the
// chrome's, which keeps it apart from an interval where the two overlap,
// and a descent in the theme's highlight, apart from both. All are faint,
// behind the grid and the line, so a shaded stretch is still read as
// easily as any other. Braille, which has no colours, passes no theme,
// and the kinds have none.
func shadeBandKinds(theme panel.Theme) []chart.BandKind {
	fade := func(c color.Color, a float64) color.Color {
		if c == nil {
			return nil
		}
		return picture.Faded(c, a)
	}
	return []chart.BandKind{
		shadeInterval: {Name: shadeNames[shadeInterval], Mark: '━', Colour: fade(theme.Accent, 0.14)},
		shadeClimb:    {Name: shadeNames[shadeClimb], Mark: '╱', Colour: fade(theme.Dim, 0.2)},
		shadeDescent:  {Name: shadeNames[shadeDescent], Mark: '╲', Colour: fade(theme.Highlight, 0.14)},
	}
}

// parseShades is the kinds of shade --shade names, each once, in the
// order a legend lists them.
func parseShades(names []string) ([]shadeKind, error) {
	want := map[shadeKind]bool{}
	for _, n := range names {
		switch strings.TrimSpace(n) {
		case shadeIntervals:
			want[shadeInterval] = true
		case shadeClimbs:
			want[shadeClimb] = true
		case shadeDescents:
			want[shadeDescent] = true
		default:
			return nil, fmt.Errorf("--shade %q: use %s, %s or %s", n, shadeIntervals, shadeClimbs, shadeDescents)
		}
	}
	var kinds []shadeKind
	for _, k := range shadeKinds {
		if want[k] {
			kinds = append(kinds, k)
		}
	}
	return kinds, nil
}

// plotShades is the stretches of kinds the workout analysis finds in
// track -- the analysis describe puts into words, so a chart shades the
// intervals, climbs and descents describe says -- on the x axis x, "time" or
// "distance", in u. A stretch without a place on that axis -- a climb
// without time, an interval without distance -- is left out. notes says
// each kind asked for that the activity has none of, so an unshaded chart
// is not taken for one the flag did nothing to.
func plotShades(track *fitactivity.Track, kinds []shadeKind, x string, u units.Set, power fitactivity.PowerSource) (shades []chart.Band, notes []string) {
	if len(kinds) == 0 || len(track.Samples) == 0 {
		return nil, nil
	}
	a := workout.Analyse(track, workout.Options{Power: power, Elevation: panel.DefaultElevationTuning(track)})
	start, _ := fitactivity.BuildTimerModel(track).Window()
	at := func(s workout.Span) (from, to float64, ok bool) {
		if x == "distance" {
			return u.Distance.FromSI(s.StartDistance), u.Distance.FromSI(s.EndDistance), s.HasDistance
		}
		return s.Start.Sub(start).Seconds(), s.End.Sub(start).Seconds(), !s.Start.IsZero() && !s.End.IsZero()
	}
	add := func(s workout.Span, k shadeKind) {
		if from, to, ok := at(s); ok && to > from {
			shades = append(shades, chart.Band{From: from, To: to, Kind: int(k)})
		}
	}
	for _, k := range kinds {
		n := len(shades)
		switch k {
		case shadeInterval:
			for _, set := range a.Sets {
				for _, iv := range set.Intervals {
					add(iv.Span, k)
				}
			}
		case shadeClimb:
			for _, c := range a.Climbs {
				add(c.Span, k)
			}
		case shadeDescent:
			for _, c := range a.Descents {
				add(c.Span, k)
			}
		}
		if len(shades) == n {
			notes = append(notes, fmt.Sprintf("no %s found to shade", shadeNames[k]))
		}
	}
	return shades, notes
}

// shadeKindCount is how many kinds of shade there are among shades: the
// rows a braille chart gives them.
func shadeKindCount(shades []chart.Band) int {
	seen := map[int]bool{}
	n := 0
	for _, s := range shades {
		if !seen[s.Kind] {
			seen[s.Kind] = true
			n++
		}
	}
	return n
}
