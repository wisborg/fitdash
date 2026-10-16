package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
	"github.com/wisborg/fitactivity/units"
	"github.com/wisborg/fitactivity/workout"
)

// stretch is a made-up stretch of a run: seconds long at power watts and
// speed metres a second.
type stretch struct {
	secs         int
	power, speed float64
}

// session is a run of stretches, a sample a second, its distance summed
// from its speed and its power wobbling a little as a runner's does.
func session(parts ...stretch) *fitactivity.Track {
	tr := &fitactivity.Track{Sport: "running"}
	t0 := time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC)
	at, dist := 0, 0.0
	for _, p := range parts {
		for i := 0; i < p.secs; i++ {
			at++
			dist += p.speed
			tr.Samples = append(tr.Samples, fitactivity.Sample{
				Time: t0.Add(time.Duration(at) * time.Second), HasSpeed: true, Speed: p.speed,
				HasDistance: true, Distance: dist, HasPower: true, Power: uint16(p.power + float64(at%7*3-9)),
			})
		}
	}
	return tr
}

func describeText(t *testing.T, tr *fitactivity.Track) string {
	t.Helper()
	metric, _ := units.Of(units.Metric)
	d := describer{track: tr, a: workout.Analyse(tr, workout.Options{}), u: metric, powerFrom: "native"}
	var b bytes.Buffer
	d.writeText(&b, []activitySource{{Path: "run.fit"}})
	return b.String()
}

// Six 800s with recoveries between, a warm-up and a cool-down: said as
// such, by distance, since the intervals were alike in distance, with
// their effort and recoveries.
func TestDescribeASet(t *testing.T) {
	parts := []stretch{{600, 220, 3}}
	for i := 0; i < 6; i++ {
		parts = append(parts, stretch{200, 330, 4})
		if i < 5 {
			parts = append(parts, stretch{90, 120, 2})
		}
	}
	parts = append(parts, stretch{600, 220, 3})
	text := describeText(t, session(parts...))
	for _, want := range []string{"warm-up", "6 × 800 m", "at ~330 W", "~1:30 recoveries", "cool-down", "effort by power (native)"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
}

// A steady run is said to be steady, with no set.
func TestDescribeSteady(t *testing.T) {
	text := describeText(t, session(stretch{1800, 280, 3}))
	if !strings.Contains(text, "steady  29:59 at ~280 W") || strings.Contains(text, "×") {
		t.Errorf("a steady half hour reads:\n%s", text)
	}
}

// A set run at changing targets says the range of its efforts, not each
// one that strayed.
func TestDescribeAVariedSet(t *testing.T) {
	parts := []stretch{{300, 200, 3}}
	for i, p := range []float64{290, 340, 310, 360, 300} {
		// Faster as the target is higher, so the intervals differ in
		// distance as a workout set by power does.
		parts = append(parts, stretch{120, p, p / 80})
		if i < 4 {
			parts = append(parts, stretch{60, 150, 3})
		}
	}
	parts = append(parts, stretch{300, 200, 3})
	text := describeText(t, session(parts...))
	if !strings.Contains(text, "5 × 2:00") || !strings.Contains(text, " to ") || strings.Contains(text, "the 2nd") {
		t.Errorf("a set at changing targets reads:\n%s", text)
	}
}

func TestOrdinal(t *testing.T) {
	for i, want := range map[int]string{1: "first", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 30: "last"} {
		if got := ordinal(i, 30); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", i, got, want)
		}
	}
}

// An interval's distance is said as the round distance it was meant to
// be, where it is near one.
func TestRoundDistance(t *testing.T) {
	metric, _ := units.Of(units.Metric)
	imperial, _ := units.Of(units.Imperial)
	for _, c := range []struct {
		u    units.Set
		m    float64
		want string
	}{
		{metric, 415, "400 m"}, {metric, 1030, "1 km"}, {metric, 720, "700 m"}, {metric, 2600, "2.6 km"},
		{imperial, 1620, "1 mi"}, {imperial, 410, "0.25 mi"},
	} {
		if got := (describer{u: c.u}).roundDistance(c.m); got != c.want {
			t.Errorf("%.0f m in %s reads %q, want %q", c.m, c.u.Distance.Name, got, c.want)
		}
	}
}

// Climbs starting at the same place and gaining alike are one hill, met
// again; a climb elsewhere, or of a different size, is its own.
func TestSameHill(t *testing.T) {
	at := func(lat, gain float64) rise {
		return rise{lat: lat, lon: 151, hasGPS: true, height: gain}
	}
	hills := sameHill([]rise{at(-33.7, 13), at(-33.70003, 12), at(-33.7, 14), at(-33.71, 13), at(-33.7, 30)})
	if len(hills) != 3 || len(hills[0]) != 3 {
		t.Errorf("hills %v, want the three alike as one and two of their own", hills)
	}
}

// A climb says how far it goes on, beside its gain, grade and start; the
// same hill met again, how far it goes on each time. Descents are said
// beneath, as climbs are, their height lost.
func TestDescribeClimbLength(t *testing.T) {
	metric, _ := units.Of(units.Metric)
	span := func(from, to float64) workout.Span {
		return workout.Span{StartDistance: from, EndDistance: to, HasDistance: true}
	}
	climb := func(lat, from, to, gain float64) workout.Climb {
		return workout.Climb{Span: span(from, to), Gain: gain, Grade: gain / (to - from), Lat: lat, Lon: 151, HasGPS: true}
	}
	a := workout.Analysis{
		Climbs: []workout.Climb{
			climb(-33.7, 1000, 1342, 12), climb(-33.7, 4000, 4338, 12), climb(-33.7, 7000, 7346, 12),
			climb(-33.8, 9000, 10400, 40),
		},
		Descents: []workout.Descent{{Span: span(10600, 11050), Loss: 21, Grade: 21.0 / 450, Lat: -33.81, Lon: 151, HasGPS: true}},
	}
	var b bytes.Buffer
	tw := &lines{w: &b}
	(describer{a: a, u: metric}).writeHills(tw)
	tw.flush()
	text := b.String()
	for _, want := range []string{
		"climbs   +40 m at 2.9% over 1.40 km from 9.00 km",
		"         3 × +12 m at 3.5% over 340 m, the same hill",
		"descent  −21 m at 4.7% over 450 m from 10.60 km",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	if strings.Index(text, "descent") < strings.Index(text, "the same hill") {
		t.Errorf("descents before climbs:\n%s", text)
	}
}

// JSON gives the analysis in metres, seconds and the effort's own unit.
func TestDescribeJSON(t *testing.T) {
	opts := fittest.DefaultOptions()
	opts.Count, opts.PowerWatts = 900, 250
	path := filepath.Join(t.TempDir(), "steady.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatal(err)
	}
	saved := format
	defer func() { format = saved }()
	if err := format.Set("json"); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{RunE: runDescribe}
	cmd.Flags().AddFlagSet(describeCmd.Flags())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var j struct {
		Signal     string `json:"signal"`
		EffortUnit string `json:"effort_unit"`
		Segments   []struct {
			DurationS float64  `json:"duration_s"`
			Effort    float64  `json:"effort"`
			DistanceM *float64 `json:"distance_m"`
		} `json:"segments"`
		Sets []any `json:"sets"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if j.Signal != "power" || j.EffortUnit != "W" || len(j.Segments) == 0 || j.Sets == nil {
		t.Errorf("JSON %+v", j)
	}
	if s := j.Segments[0]; s.DistanceM == nil || s.Effort < 240 || s.Effort > 260 {
		t.Errorf("the first segment %+v, want its distance and about 250 W", s)
	}
}
