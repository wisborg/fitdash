package cmd

import (
	"math"
	"strings"
	"testing"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/plot"
)

// --shade names intervals, climbs and descents, in any order, once each;
// anything else is refused.
func TestParseShades(t *testing.T) {
	kinds, err := parseShades([]string{"descents", "climbs", " intervals", "climbs"})
	if err != nil || len(kinds) != 3 || kinds[0] != plot.ShadeInterval || kinds[1] != plot.ShadeClimb || kinds[2] != plot.ShadeDescent {
		t.Errorf("parseShades = %v, %v; want intervals, climbs, descents", kinds, err)
	}
	if _, err := parseShades([]string{"hills"}); err == nil || !strings.Contains(err.Error(), "intervals, climbs or descents") {
		t.Errorf("hills: %v", err)
	}
	if kinds, err := parseShades(nil); err != nil || kinds != nil {
		t.Errorf("no --shade: %v, %v; want nothing shaded", kinds, err)
	}
}

// A set's intervals are shaded where they are on the axis -- seconds from
// the start against time, the distance unit's against distance -- and a
// kind the activity has none of is said to be missing.
func TestPlotShades(t *testing.T) {
	parts := []stretch{{600, 220, 3}}
	for i := 0; i < 5; i++ {
		parts = append(parts, stretch{200, 330, 4})
		if i < 4 {
			parts = append(parts, stretch{90, 120, 2})
		}
	}
	parts = append(parts, stretch{600, 220, 3})
	tr := session(parts...)
	metric, _ := units.Of(units.Metric)
	both := []plot.ShadeKind{plot.ShadeInterval, plot.ShadeClimb}

	shades, notes := plotShades(tr, both, "time", metric, fitactivity.PowerAuto)
	if len(shades) != 5 {
		t.Fatalf("%d shades %+v, want the five intervals", len(shades), shades)
	}
	// The first interval runs from 600 s to 800 s, give or take a bin.
	if s := shades[0]; s.Kind != plot.ShadeInterval || math.Abs(s.From-600) > 10 || math.Abs(s.To-800) > 10 {
		t.Errorf("the first interval is shaded %+v, want 600 to 800 s", s)
	}
	if len(notes) != 1 || notes[0] != "no climbs found to shade" {
		t.Errorf("notes %q, want only that there are no climbs", notes)
	}

	byDist, _ := plotShades(tr, both[:1], "distance", metric, fitactivity.PowerAuto)
	// 600 s at 3 m/s is 1.8 km; 200 s at 4 m/s, 800 m more.
	if s := byDist[0]; math.Abs(s.From-1.8) > 0.05 || math.Abs(s.To-2.6) > 0.05 {
		t.Errorf("against distance the first interval is %+v, want 1.8 to 2.6 km", s)
	}

	if shades, notes := plotShades(tr, nil, "time", metric, fitactivity.PowerAuto); shades != nil || notes != nil {
		t.Errorf("unasked, %+v and %q", shades, notes)
	}
}

// Nothing is shaded unless asked. Asked, the shades are drawn within the
// height asked for -- the synthetic activity's power climbs a ramp three
// times, and the top of each is a hard stretch, an interval -- and a kind
// the activity has none of, here climbs, is said to be missing rather
// than silently left out.
func TestPlotShadeFlag(t *testing.T) {
	path := writePlotActivity(t)
	plain, _, err := runPlotCmd(t, path, "--y", "heart-rate", "--width", "70", "--height", "15")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "intervals") || strings.Contains(plain, "climbs") {
		t.Errorf("shaded without --shade:\n%s", plain)
	}
	out, errOut, err := runPlotCmd(t, path, "--y", "heart-rate", "--width", "70", "--height", "15", "--shade", "intervals,climbs")
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "no climbs found to shade\n" {
		t.Errorf("stderr %q, want only that there are no climbs", errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 15 {
		t.Errorf("%d lines, want 15", len(lines))
	}
	if last := lines[len(lines)-1]; !strings.HasSuffix(last, "━ intervals") || strings.Count(lines[len(lines)-2], "━") < 3 {
		t.Errorf("no row of intervals over a legend of them:\n%s", out)
	}
	if _, _, err := runPlotCmd(t, path, "--y", "heart-rate", "--shade", "hills"); err == nil {
		t.Error("--shade hills was accepted")
	}
}
