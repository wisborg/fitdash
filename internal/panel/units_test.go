package panel

import (
	"testing"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/units"

	"github.com/wisborg/fitdash/internal/inspect"
)

const mile = 1609.344

// metric is the units every panel drew in before Context.Units, and still
// the ones a Context without them gets.
var metric, _ = units.Of(units.Metric)

var imperial, _ = units.Of(units.Imperial)

// A Context that never set its units -- every fixture, every render before
// --units -- writes metric.
func TestContextUnits_ZeroIsMetric(t *testing.T) {
	var none *Context
	if none.units() != metric || (&Context{}).units() != metric || (&Context{Units: imperial}).units() != imperial {
		t.Error("a Context's units are not its own, or not metric when unset")
	}
}

// The distance readout is in miles under imperial: its unit, its value, and
// the digits it reserves, which follow the reading in miles -- 160 km is
// 99.4 mi, so it keeps the two decimals a 160 km reading would have lost.
func TestDistanceReadoutInMiles(t *testing.T) {
	ctx := ctxWith(inspect.MetricDistance)
	ctx.Units = imperial
	r := Distance()
	r = r.bind(ctx, r)
	v, ok := r.value(fitactivity.Sample{HasDistance: true, Distance: 2 * mile})
	if r.unit != "mi" || !ok || v != 2 {
		t.Errorf("unit %q, 2 miles read as %v %v", r.unit, v, ok)
	}
	if intDigits, decimals := distanceLayout(imperial.Distance.FromSI(160000), false); intDigits != 2 || decimals != 2 {
		t.Errorf("160 km in miles lays out as %d.%d digits, want 2.2", intDigits, decimals)
	}
	// Bound against an activity of 120 km, 74.6 mi: two decimals in miles,
	// where in kilometres the third integer digit would have taken one.
	long := &fitactivity.Track{}
	for i := 0; i <= 120; i++ {
		long.Samples = append(long.Samples, fitactivity.Sample{HasDistance: true, Distance: float64(i) * 1000})
	}
	longCtx := &Context{Track: long, Report: inspect.Build(long), Units: imperial}
	if r := Distance(); r.bind(longCtx, r).template != "88.88" {
		t.Errorf("120 km in miles reserves %q, want 88.88", r.bind(longCtx, r).template)
	}
	longCtx.Units = metric
	if r := Distance(); r.bind(longCtx, r).template != "888.8" {
		t.Errorf("120 km in kilometres reserves %q, want 888.8", r.bind(longCtx, r).template)
	}
	ctx.Units = units.Set{}
	r = Distance()
	if r = r.bind(ctx, r); r.unit != "km" {
		t.Errorf("unset units: %q, want km", r.unit)
	}
}

// Pace under imperial is minutes a mile: its unit, its text, and its floor,
// which is the slowest pace a mile fits "88:88" at -- slower in metres a
// second than the kilometre's.
func TestPaceReadoutInMiles(t *testing.T) {
	ctx := ctxWith(inspect.MetricSpeed)
	ctx.Units = imperial
	r := Pace()
	r = r.bind(ctx, r)
	if r.unit != "min/mi" || r.format(mile/480) != "8:00" {
		t.Errorf("unit %q, 8:00/mi written %q", r.unit, r.format(mile/480))
	}
	floor := minPaceSpeed(paceTemplate, units.MinutesPerMile)
	if floor <= minPaceSpeed(paceTemplate, units.MinutesPerKilometre) {
		t.Errorf("the mile's floor %v is not above the kilometre's", floor)
	}
	if _, ok := r.value(fitactivity.Sample{HasSpeed: true, Speed: floor * 0.99}); ok {
		t.Error("a speed whose pace a mile overflows the template was let through")
	}
	if _, ok := r.value(fitactivity.Sample{HasSpeed: true, Speed: floor * 1.01}); !ok {
		t.Error("a speed whose pace a mile fits was refused")
	}
}

// The pace gauge's ends snap to half minutes of the unit its labels are in:
// a run between 6:50 and 9:10 a mile -- its smoothed ends a little inside
// those -- gets ends of 6:30 and 9:30 a mile, where in kilometres the same
// speeds would snap to other numbers altogether.
func TestPaceGaugeScaleInMiles(t *testing.T) {
	lo, hi := mile/550, mile/410
	ctx := &Context{Track: paceRampTrack(lo, (hi-lo)/100, 101)}
	got, ok := paceGaugeScale(ctx, Pace().value, units.MinutesPerMile)
	if !ok {
		t.Fatal("no scale")
	}
	if slow, fast := units.MinutesPerMile.FromSI(got.floor), units.MinutesPerMile.FromSI(got.ceiling); !gaugeAlmostEqual(slow, 570) || !gaugeAlmostEqual(fast, 390) {
		t.Errorf("ends at %v and %v s a mile, want 570 and 390", slow, fast)
	}
	// And through the readout itself, bound to imperial: its scale is the
	// mile's, not the kilometre's.
	ctx.Units = imperial
	r := Pace()
	r = r.bind(ctx, r)
	if s, ok := r.scale(ctx, r); !ok || !gaugeAlmostEqual(units.MinutesPerMile.FromSI(s.floor), 570) {
		t.Errorf("the bound pace readout's scale: %+v %v, want a slow end of 9:30 a mile", s, ok)
	}
}

// Speed is in the unit chosen, whole from 10 up and to a tenth below; at a
// standstill it reads 0, a reading, and with no speed it is absent.
func TestSpeedReadout(t *testing.T) {
	ctx := ctxWith(inspect.MetricSpeed)
	r := Speed()
	r = r.bind(ctx, r)
	v, ok := r.value(fitactivity.Sample{HasSpeed: true, Speed: 10})
	if r.unit != "km/h" || !ok || r.format(v) != "36" {
		t.Errorf("10 m/s: unit %q, %q %v", r.unit, r.format(v), ok)
	}
	if v, ok := r.value(fitactivity.Sample{HasSpeed: true, Speed: 1.5}); !ok || r.format(v) != "5.4" {
		t.Errorf("1.5 m/s: %q %v, want 5.4", r.format(v), ok)
	}
	if v, ok := r.value(fitactivity.Sample{HasSpeed: true}); !ok || r.format(v) != "0.0" {
		t.Errorf("standing still: %q %v, want a reading of 0.0", r.format(v), ok)
	}
	if _, ok := r.value(fitactivity.Sample{Speed: 3}); ok {
		t.Error("a sample with no speed read as one")
	}
	ctx.Units = units.Set{Distance: units.NauticalMile, Elevation: units.Foot, Speed: units.Knot, Pace: units.MinutesPerMile}
	r = Speed()
	r = r.bind(ctx, r)
	if v, _ := r.value(fitactivity.Sample{HasSpeed: true, Speed: 1852.0 / 3600 * 250}); r.unit != "kn" || r.format(v) != "250" {
		t.Errorf("a flight: %q %q", r.format(v), r.unit)
	}
	if !r.HasGaugeRule() || !Speed().Accepts(ctx) {
		t.Error("speed has no gauge, or declines an activity with speed")
	}
}

// The profile's labels: kilometres or miles, and below one of them metres or
// feet; heights in metres or feet.
func TestProfileLabelsInOtherUnits(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{formatDistance(5*mile, imperial), "5.0 mi"},
		{formatDistance(300, imperial), "984 ft"},
		{formatDistance(1200, imperial), "3937 ft"},
		{formatDistance(300, metric), "300 m"},
		{formatDistance(4200, metric), "4.2 km"},
		{formatElevation(100, units.Foot), "328 ft"},
		{formatElevation(100, units.Metre), "100 m"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
}

// The climb panel writes its gain and loss in the elevation unit chosen.
func TestClimbPanelInFeet(t *testing.T) {
	ctx := elevationContext(hillTrack(0, 5000, 300))
	ctx.Units = imperial
	p, ok := (ClimbPanel{}).Prepare(ctx, Box{W: 400, H: 100}).(*climbPainter)
	if !ok || p.unit != units.Foot {
		t.Errorf("the climb painter writes in %+v", p)
	}
}

// auto is pace for the sports counted in steps and speed for every other,
// an unknown one included; pace and speed are themselves; anything else is
// refused naming what there is.
func TestResolveSpeedReadout(t *testing.T) {
	for _, c := range []struct{ mode, sport, want string }{
		{"auto", "running", SpeedReadoutPace},
		{"auto", "Hiking", SpeedReadoutPace},
		{"auto", "walking", SpeedReadoutPace},
		{"auto", "cycling", SpeedReadoutSpeed},
		{"auto", "", SpeedReadoutSpeed},
		{"", "running", SpeedReadoutPace},
		{"pace", "cycling", SpeedReadoutPace},
		{"speed", "running", SpeedReadoutSpeed},
	} {
		if got, err := ResolveSpeedReadout(c.mode, c.sport); err != nil || got != c.want {
			t.Errorf("ResolveSpeedReadout(%q, %q) = %q, %v; want %q", c.mode, c.sport, got, err, c.want)
		}
	}
	if _, err := ResolveSpeedReadout("velocity", "running"); err == nil || err.Error() != `--speed-readout "velocity" is invalid; use auto, pace, speed` {
		t.Errorf("an unknown mode: %v", err)
	}
}

// The readout not chosen is removed, the chosen one kept; the balance
// column's pace, and every other panel, are untouched; an unchosen Context is
// pace.
func TestSpeedReadoutKeeps(t *testing.T) {
	speed := &Context{SpeedReadout: SpeedReadoutSpeed}
	pace := &Context{SpeedReadout: SpeedReadoutPace}
	for _, c := range []struct {
		ctx  *Context
		p    Panel
		want bool
	}{
		{speed, Pace(), false},
		{speed, Speed(), true},
		{speed, balancePace(), true},
		{speed, HeartRate(), true},
		{pace, Pace(), true},
		{pace, Speed(), false},
		{&Context{}, Pace(), true},
		{&Context{}, Speed(), false},
	} {
		if got := SpeedReadoutKeeps(c.ctx, c.p); got != c.want {
			t.Errorf("SpeedReadout %q keeps %s: %v, want %v", c.ctx.SpeedReadout, c.p.Name(), got, c.want)
		}
	}
}
