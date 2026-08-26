package inspect

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"
)

// decodeFixture writes a synthetic activity and decodes it. Every test here
// runs on a generated file rather than a recorded one: a real FIT from a watch
// is personal data, and a committed one would also be an unreviewable binary
// blob nobody could confirm was clean.
// shortOptions is DefaultOptions cut to ten minutes. The default fixture spans
// four and a half hours at 1 Hz, which is the right shape for a decoder test
// and needless work for a report that only counts.
func shortOptions() fittest.Options {
	opts := fittest.DefaultOptions()
	opts.Count = 600
	return opts
}

func decodeFixture(t *testing.T, opts fittest.Options) *fitactivity.Track {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.fit")
	if err := fittest.WriteFile(path, opts); err != nil {
		t.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return track
}

// TestBuild_ReportsEveryKnownMetricIncludingAbsentOnes checks the property the
// package comment claims: an absent metric gets a ROW with zero coverage, not
// no row at all.
//
// This is the difference between "this file has no power meter" and "inspect
// does not know about power", and a reader cannot tell those apart from a
// missing line. A test asserting only that the present metrics appear would
// pass against an implementation that silently dropped every absent one, which
// is exactly the regression worth catching.
func TestBuild_ReportsEveryKnownMetricIncludingAbsentOnes(t *testing.T) {
	track := decodeFixture(t, shortOptions())
	rep := Build(track)

	want := []string{
		"Position", "Elevation", "Speed", "Distance", "Heart rate", "Cadence",
		"Power", "Temperature", "Vertical oscillation", "Stance time", "Step length",
	}
	if len(rep.Metrics) != len(want) {
		t.Fatalf("Metrics has %d rows, want %d", len(rep.Metrics), len(want))
	}
	for i, name := range want {
		if rep.Metrics[i].Name != name {
			t.Errorf("Metrics[%d].Name = %q, want %q", i, rep.Metrics[i].Name, name)
		}
	}
}

// TestBuild_AbsentMetricHasZeroCoverageAndEmptyRange pins the absent-is-not-
// zero rule at this layer.
//
// The failure it guards is a Build that reads Sample values without checking
// the presence flag. That version still produces a row, still produces a
// plausible-looking report, and reports coverage 100% with a range anchored at
// 0 -- so a test asserting only "the row exists" would pass. The assertion has
// to be on Present and on the range.
func TestBuild_AbsentMetricHasZeroCoverageAndEmptyRange(t *testing.T) {
	// PowerWatts 0 writes no power field at all -- not a zero-valued one --
	// which is precisely the "recorded without a power sensor" case, and the
	// reason this metric rather than heart rate is the one tested: the fixture
	// writes heart rate on every record and offers no switch to remove it.
	opts := shortOptions()
	opts.PowerWatts = 0
	track := decodeFixture(t, opts)
	rep := Build(track)

	m := metricNamed(t, rep, "Power")
	if m.Present != 0 {
		t.Errorf("Power Present = %d, want 0 -- a presence flag was not checked", m.Present)
	}
	if got := m.Coverage(rep.Samples); got != 0 {
		t.Errorf("Power Coverage = %v, want 0", got)
	}
	if m.Min != 0 || m.Max != 0 {
		t.Errorf("Power range = [%v,%v], want [0,0] as the documented meaningless value", m.Min, m.Max)
	}
}

// TestBuild_PartialCoverageIsReportedAsPartial is the case the package comment
// is built around: a metric that is neither wholly present nor wholly absent.
//
// A report that only distinguished present from absent would call this "Power:
// yes" and hide the very situation a panel has to cope with. The fixture's
// power stops a fifth of the way in, so the expected coverage is derived from
// the fixture's own parameters rather than read off a run.
func TestBuild_PartialCoverageIsReportedAsPartial(t *testing.T) {
	opts := shortOptions()
	opts.PowerWatts = 250
	opts.PoweredRecords = 120 // of 600
	track := decodeFixture(t, opts)
	rep := Build(track)

	m := metricNamed(t, rep, "Power")
	if m.Present != opts.PoweredRecords {
		t.Errorf("Power Present = %d, want %d", m.Present, opts.PoweredRecords)
	}
	want := float64(opts.PoweredRecords) / float64(rep.Samples)
	if got := m.Coverage(rep.Samples); got != want {
		t.Errorf("Power Coverage = %v, want %v", got, want)
	}
	if got := m.Coverage(rep.Samples); got == 0 || got == 1 {
		t.Errorf("Power Coverage = %v; a partial metric reported as all-or-nothing is the bug this package exists to avoid", got)
	}
}

// TestBuild_PresentMetricSpansItsRealRange confirms the inverse: when the data
// IS there, the range is the data's own and not a placeholder.
//
// Paired with the test above, this is what makes either one discriminating. An
// implementation that reported every metric as absent would pass that test
// alone; one that reported every metric as present would pass neither.
func TestBuild_PresentMetricSpansItsRealRange(t *testing.T) {
	track := decodeFixture(t, shortOptions())
	rep := Build(track)

	m := metricNamed(t, rep, "Heart rate")
	if m.Present != rep.Samples {
		t.Errorf("Heart rate Present = %d, want %d (every sample)", m.Present, rep.Samples)
	}
	if !(m.Min > 0 && m.Max > m.Min) {
		t.Errorf("Heart rate range = [%v,%v], want a non-degenerate range above zero", m.Min, m.Max)
	}
}

// TestCoverage_IsTheFractionOfSamples derives the expected values by hand
// rather than pinning what the code returns, so the test states the definition
// of coverage instead of recording it.
func TestCoverage_IsTheFractionOfSamples(t *testing.T) {
	cases := []struct {
		name    string
		present int
		total   int
		want    float64
	}{
		{"none of ten", 0, 10, 0.0},
		{"one of ten", 1, 10, 0.1},
		{"all of ten", 10, 10, 1.0},
		{"half of four", 2, 4, 0.5},
		// A zero total is the empty-activity case. Reporting 0 is the only
		// answer that is not a division by zero, and it must not be reached by
		// accident: an implementation that divided anyway would return NaN,
		// which formats as "NaN%" in the table rather than failing.
		{"no samples at all", 0, 0, 0.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Metric{Present: c.present}
			if got := m.Coverage(c.total); got != c.want {
				t.Errorf("Coverage(%d) with Present=%d = %v, want %v", c.total, c.present, got, c.want)
			}
		})
	}
}

// TestBuild_ActiveTimeExcludesAPause checks that a paused activity reports
// active time SHORTER than elapsed, and by the length of the pause.
//
// The expected value is derived from the fixture's own parameters, not read
// off a run: a two-minute pause in a ten-minute activity must leave eight
// minutes of active time. Pinning whatever the timer model currently returned
// would turn a future pause-pairing bug into a test failure that looks like a
// spec change.
func TestBuild_ActiveTimeExcludesAPause(t *testing.T) {
	const (
		count = 600 // seconds, one record each
		pause = 2 * time.Minute
	)
	// Coverage runs from the first record to the last, i.e. (count-1) seconds:
	// the final record sits AT the end of the span, not one second past it.
	const wantElapsed = (count - 1) * time.Second

	opts := shortOptions()
	opts.Count = count
	opts.Pauses = []fittest.Pause{{Start: 3 * time.Minute, End: 3*time.Minute + pause}}
	track := decodeFixture(t, opts)
	rep := Build(track)

	if !rep.HasTimerEvents {
		t.Fatal("HasTimerEvents = false; the fixture was built with a pause, so the events must be there")
	}
	// The sample stream stops during the pause, so elapsed is measured between
	// the first and last RECORD, and both bound the pause. Allow a couple of
	// seconds either way for the fixture's own 1 Hz sample spacing.
	const tol = 2 * time.Second
	if d := rep.Elapsed - wantElapsed; d < -tol || d > tol {
		t.Errorf("Elapsed = %v, want ~%v", rep.Elapsed, wantElapsed)
	}
	if d := rep.Active - (wantElapsed - pause); d < -tol || d > tol {
		t.Errorf("Active = %v, want ~%v (elapsed minus the %v pause)", rep.Active, wantElapsed-pause, pause)
	}
}

// TestBuildDevFields_AreSortedByName pins the ordering, which exists so two
// reports of the same file can be diffed. Ranging a map would reorder the rows
// on every invocation, and nothing about the output would look wrong.
func TestBuildDevFields_AreSortedByName(t *testing.T) {
	samples := []fitactivity.Sample{
		{DevFields: map[string]float64{"Zulu": 1, "Alpha": 2}},
		{DevFields: map[string]float64{"Mike": 3}},
	}
	got := buildDevFields(samples)
	want := []string{"Alpha", "Mike", "Zulu"}
	if len(got) != len(want) {
		t.Fatalf("got %d fields, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("field %d = %q, want %q", i, got[i].Name, name)
		}
	}
}

func metricNamed(t *testing.T, rep Report, name string) Metric {
	t.Helper()
	for _, m := range rep.Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no metric named %q in report", name)
	return Metric{}
}

// TestMetricNames_ArePinnedLiterals guards a rename that would otherwise be
// invisible.
//
// Panels ask the report about a metric through these constants, so a rename
// moves both sides together and nothing breaks -- which is exactly why a test
// comparing the panel's name against the report's is circular and proves
// nothing. What a rename DOES change is what the inspect command prints, and
// that is user-visible output. Pinning the literals is the only assertion that
// can see it.
func TestMetricNames_ArePinnedLiterals(t *testing.T) {
	cases := []struct{ got, want string }{
		{MetricPosition, "Position"},
		{MetricElevation, "Elevation"},
		{MetricSpeed, "Speed"},
		{MetricDistance, "Distance"},
		{MetricHeartRate, "Heart rate"},
		{MetricCadence, "Cadence"},
		{MetricPower, "Power"},
		{MetricTemperature, "Temperature"},
		{MetricVerticalOscillation, "Vertical oscillation"},
		{MetricStanceTime, "Stance time"},
		{MetricStepLength, "Step length"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("a metric name changed to %q, want %q -- this is printed to users", c.got, c.want)
		}
	}

	// And every one of them must actually appear in a report, or a panel
	// asking about it would decline on every activity.
	rep := Build(&fitactivity.Track{Samples: []fitactivity.Sample{{}}})
	for _, c := range cases {
		if _, ok := rep.Metric(c.want); !ok {
			t.Errorf("no metric named %q in a report; a panel asking for it would always decline", c.want)
		}
	}
}
