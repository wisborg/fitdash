package cmd

import (
	"fmt"
	"strings"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/workout"

	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/plot"
)

// The kinds of stretch --shade marks, as it names them.
const (
	shadeIntervals = "intervals"
	shadeClimbs    = "climbs"
	shadeDescents  = "descents"
)

// parseShades is the kinds of shade --shade names, each once, in the
// order a legend lists them.
func parseShades(names []string) ([]plot.ShadeKind, error) {
	want := map[plot.ShadeKind]bool{}
	for _, n := range names {
		switch strings.TrimSpace(n) {
		case shadeIntervals:
			want[plot.ShadeInterval] = true
		case shadeClimbs:
			want[plot.ShadeClimb] = true
		case shadeDescents:
			want[plot.ShadeDescent] = true
		default:
			return nil, fmt.Errorf("--shade %q: use %s, %s or %s", n, shadeIntervals, shadeClimbs, shadeDescents)
		}
	}
	var kinds []plot.ShadeKind
	for _, k := range plot.ShadeKinds {
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
func plotShades(track *fitactivity.Track, kinds []plot.ShadeKind, x string, u units.Set, power fitactivity.PowerSource) (shades []plot.Shade, notes []string) {
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
	add := func(s workout.Span, k plot.ShadeKind) {
		if from, to, ok := at(s); ok && to > from {
			shades = append(shades, plot.Shade{From: from, To: to, Kind: k})
		}
	}
	for _, k := range kinds {
		n := len(shades)
		switch k {
		case plot.ShadeInterval:
			for _, set := range a.Sets {
				for _, iv := range set.Intervals {
					add(iv.Span, k)
				}
			}
		case plot.ShadeClimb:
			for _, c := range a.Climbs {
				add(c.Span, k)
			}
		case plot.ShadeDescent:
			for _, c := range a.Descents {
				add(c.Span, k)
			}
		}
		if len(shades) == n {
			notes = append(notes, fmt.Sprintf("no %s found to shade", k.Name()))
		}
	}
	return shades, notes
}

// shadeKindCount is how many kinds of shade there are among shades: the
// rows a braille chart gives them.
func shadeKindCount(shades []plot.Shade) int {
	seen := map[plot.ShadeKind]bool{}
	n := 0
	for _, s := range shades {
		if !seen[s.Kind] {
			seen[s.Kind] = true
			n++
		}
	}
	return n
}
