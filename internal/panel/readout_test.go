package panel

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// trackWith builds a track whose samples carry exactly the named metrics.
//
// A real Track rather than a hand-built Report, because the power panel cannot
// answer "does this activity have power" from a report at all -- both sources
// are named "Power" -- and asks fitactivity.Track.HasPower instead. Deriving
// the report FROM the track also means the two cannot disagree in a fixture,
// which is the property the production code is built around.
func trackWith(present ...string) *fitactivity.Track {
	has := map[string]bool{}
	for _, p := range present {
		has[p] = true
	}
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	samples := make([]fitactivity.Sample, 100)
	for i := range samples {
		s := fitactivity.Sample{Time: base.Add(time.Duration(i) * time.Second)}
		if has[inspect.MetricHeartRate] {
			s.HasHeartRate, s.HeartRate = true, uint8(140+i%10)
		}
		if has[inspect.MetricPower] {
			s.HasPower, s.Power = true, uint16(200+i%40)
		}
		if has[strydPower] {
			s.DevFields = map[string]float64{fitactivity.StrydPowerField: float64(180 + i%30)}
		}
		if has[inspect.MetricCadence] {
			s.HasCadence, s.Cadence = true, uint8(80+i%6)
		}
		samples[i] = s
	}
	return &fitactivity.Track{Samples: samples}
}

// strydPower names the footpod's developer field in these fixtures. It is not
// an inspect metric constant: the developer field's name comes from the FIT
// file itself, and fitactivity exposes the one Stryd uses.
const strydPower = "stryd-power"

// ctxWith builds a render context over a track carrying the named metrics.
func ctxWith(present ...string) *Context {
	track := trackWith(present...)
	return &Context{Track: track, Report: inspect.Build(track)}
}

// reportWith is kept for the tests that genuinely only exercise the report.
func reportWith(present ...string) inspect.Report {
	return inspect.Build(trackWith(present...))
}

// TestReadout_MetricNamesResolveInARealReport is the guard for the stringly-
// typed lookup underneath Accepts.
//
// A panel asks the report about its metric BY NAME. If a name drifts -- a
// constant renamed on one side only -- Accepts returns false on every
// activity, the layout closes up around the panel, and the result is
// indistinguishable from a file that genuinely lacks the data. Nothing errors
// and nothing looks wrong.
//
// Report.Metric's second result is what makes this checkable, and this is the
// test that uses it.
func TestReadout_MetricNamesResolveInARealReport(t *testing.T) {
	// A real report, from Build, rather than one assembled here -- the point
	// is that the panels' names match what Build actually produces.
	rep := inspect.Build(&fitactivity.Track{Samples: []fitactivity.Sample{{}}})

	for _, r := range []Readout{HeartRate(), Power(), Cadence()} {
		if _, ok := rep.Metric(r.metric); !ok {
			t.Errorf("panel %q asks about metric %q, which no report produces; it would decline on every activity",
				r.Name(), r.metric)
		}
	}
}

// TestReadoutPlaceholder_IsNotANumber pins the shape of the placeholder, not
// just its colour.
//
// The drawing tests compare renders and so cannot see this: a placeholder of
// "0" drawn in Theme.Absent differs in pixels from a reading of 0 drawn in
// Theme.Foreground, and every colour assertion passes. But a viewer reading a
// dim "0" has been shown a number where there is no measurement, which is
// exactly the confident lie this project refuses -- the colour is a hint, and
// the text must not need it.
func TestReadoutPlaceholder_IsNotANumber(t *testing.T) {
	if _, err := strconv.ParseFloat(ReadoutPlaceholder, 64); err == nil {
		t.Errorf("ReadoutPlaceholder is %q, which parses as a number; absence must not be shaped like a reading",
			ReadoutPlaceholder)
	}
	for _, r := range ReadoutPlaceholder {
		if r >= '0' && r <= '9' {
			t.Errorf("ReadoutPlaceholder %q contains a digit", ReadoutPlaceholder)
			break
		}
	}
	// The same rule for the clock's placeholder, which is a different constant
	// and could drift on its own.
	for _, r := range ClockPlaceholder {
		if r >= '0' && r <= '9' {
			t.Errorf("ClockPlaceholder %q contains a digit", ClockPlaceholder)
			break
		}
	}
}

// TestReadout_AcceptsFollowsCoverage pins the uniform rule: any coverage at
// all keeps the panel, none declines it.
func TestReadout_AcceptsFollowsCoverage(t *testing.T) {
	withPower := ctxWith(inspect.MetricHeartRate, inspect.MetricPower)
	withoutPower := ctxWith(inspect.MetricHeartRate)

	if !Power().Accepts(withPower) {
		t.Error("Power declined an activity that carries power")
	}
	if Power().Accepts(withoutPower) {
		t.Error("Power accepted an activity that carries none; the layout would hold a box that never fills")
	}
	if !HeartRate().Accepts(withoutPower) {
		t.Error("HeartRate declined an activity that carries heart rate")
	}

	// A metric present only part of the time is KEPT: that is a dropout, and
	// the placeholder path handles it honestly. A percentage threshold would
	// need a defensible number and there is not one.
	partial := trackWith(inspect.MetricPower)
	for i := 4; i < len(partial.Samples); i++ {
		partial.Samples[i].HasPower = false
	}
	if !Power().Accepts(&Context{Track: partial, Report: inspect.Build(partial)}) {
		t.Error("Power declined at 4% coverage; partial data is a dropout, not an absence")
	}
}

// TestReadout_DrawsTheReadingAndThePlaceholder is the "did it actually draw"
// test, and the check that absence looks different from a value.
//
// This uses Cadence, not HeartRate, for the "a genuine zero must look like a
// reading" half: a cyclist coasting or a runner between strides can report a
// real zero cadence, where HeartRate's own zero is special-cased as absence
// (see HeartRate's doc comment) and is covered by its own tests below rather
// than by this generic one.
func TestReadout_DrawsTheReadingAndThePlaceholder(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 0, Y: 0, W: 400, H: 300}
	p := Cadence().Prepare(ctx, box)

	shot := func(f Frame) []byte {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, f)
		out := make([]byte, len(img.Pix))
		copy(out, img.Pix)
		return out
	}

	live := shot(Frame{HasSample: true, Sample: fitactivity.Sample{HasCadence: true, Cadence: 87}})
	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("a live reading drew nothing")
	}

	// The absent case, written the way the renderer guarantees it: HasSample
	// false means the ZERO sample, so the panel's single presence check is
	// already correct without a second condition.
	absent := shot(Frame{HasSample: false})
	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("the placeholder drew nothing; the box would be an unexplained hole")
	}
	if string(live) == string(absent) {
		t.Fatal("a missing reading renders identically to a live one")
	}
	if !containsColor(img, c.Theme.Absent) {
		t.Error("the placeholder is not drawn in Theme.Absent; it would read as a value")
	}

	// A reading that is genuinely zero must look like a READING, not like the
	// placeholder. This is the absent-is-not-zero rule from the other side,
	// and it is the direction a panel is most likely to get wrong. Cadence
	// zero is a real reading -- a coasting cyclist or a runner momentarily
	// between strides -- unlike HeartRate's, which this project treats as
	// "not connected yet" (see HeartRate's doc comment).
	zero := shot(Frame{HasSample: true, Sample: fitactivity.Sample{HasCadence: true, Cadence: 0}})
	if string(zero) == string(absent) {
		t.Error("a cadence of 0 renders as the placeholder; a real zero is data")
	}
}

// TestReadout_ChangesWithTheReading guards the frozen-panel failure a "did it
// draw" test cannot see.
func TestReadout_ChangesWithTheReading(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	p := HeartRate().Prepare(ctx, Box{X: 0, Y: 0, W: 400, H: 300})

	shot := func(bpm uint8) []byte {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: bpm}})
		out := make([]byte, len(img.Pix))
		copy(out, img.Pix)
		return out
	}
	if string(shot(148)) == string(shot(152)) {
		t.Error("148 and 152 bpm render identically; the readout is frozen")
	}
	if string(shot(148)) != string(shot(148)) {
		t.Error("the same reading rendered differently twice; the panel is not deterministic")
	}
}

// TestHeartRate_ZeroIsAbsentNotAReading pins where the decision lives, the
// same shape as TestPace_StoppedIsAbsentNotZero: the value accessor is where
// "there is no reading here" is decided, not a formatting special case
// downstream, so the panel's ordinary placeholder path handles it.
//
// A recorded zero is refused even though HasHeartRate is true -- some
// devices write 0 for "no skin contact yet" rather than leaving the sample
// unmarked, and this project treats that as absence. See HeartRate's doc
// comment for why that is this panel's call and not fitactivity's.
func TestHeartRate_ZeroIsAbsentNotAReading(t *testing.T) {
	live := fitactivity.Sample{HasHeartRate: true, HeartRate: 148}
	strapNotYetConnected := fitactivity.Sample{HasHeartRate: true, HeartRate: 0}
	noReading := fitactivity.Sample{}

	hr := HeartRate()
	if _, ok := hr.value(live); !ok {
		t.Error("a live 148 bpm resolved no reading")
	}
	if _, ok := hr.value(strapNotYetConnected); ok {
		t.Error("a recorded 0 bpm resolved a reading; a live heart rate cannot be zero")
	}
	if _, ok := hr.value(noReading); ok {
		t.Error("a sample with no heart rate resolved a reading")
	}
}

// TestHeartRate_DynamicDrawsThePlaceholderForARecordedZero is the pixel-level
// half, on a fixture shaped like the real recording that motivated the fix:
// the strap's opening samples record 0 before it acquires, and later samples
// carry a genuine reading. It must draw the placeholder in Theme.Absent for
// the opening run and a real reading in Theme.Foreground once the strap
// connects, never the reverse and never a "0 bpm" in the foreground.
//
// Verified load bearing by hand: reverting the value accessor to
// `s.HasHeartRate` alone (dropping the `&& s.HeartRate != 0` guard) makes
// this test fail, drawing "0" in Theme.Foreground for the opening samples.
func TestHeartRate_DynamicDrawsThePlaceholderForARecordedZero(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 0, Y: 0, W: 400, H: 300}
	p := HeartRate().Prepare(ctx, box)

	// A fixture shaped like the real recording: the strap's opening samples
	// record zero before it finds skin contact, later ones do not.
	samples := make([]fitactivity.Sample, 0, 40)
	for i := 0; i < 20; i++ {
		samples = append(samples, fitactivity.Sample{HasHeartRate: true, HeartRate: 0})
	}
	for i := 0; i < 20; i++ {
		samples = append(samples, fitactivity.Sample{HasHeartRate: true, HeartRate: uint8(140 + i%10)})
	}

	for i, s := range samples {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{HasSample: true, Sample: s})

		if inkCount(img, box, c.Theme) == 0 {
			t.Fatalf("sample %d drew nothing; the box would be an unexplained hole", i)
		}
		if i < 20 {
			if !containsColor(img, c.Theme.Absent) {
				t.Errorf("sample %d (recorded 0 bpm, strap not yet connected) did not draw in Theme.Absent", i)
			}
			if containsColor(img, c.Theme.Foreground) {
				t.Errorf("sample %d (recorded 0 bpm) drew in Theme.Foreground, as though 0 bpm were a live reading", i)
			}
		} else {
			if !containsColor(img, c.Theme.Foreground) {
				t.Errorf("sample %d (a genuine reading) did not draw in Theme.Foreground", i)
			}
			if containsColor(img, c.Theme.Absent) {
				t.Errorf("sample %d (a genuine reading) drew in Theme.Absent, as though it were unconnected", i)
			}
		}
	}
}

// TestReadout_StaysInsideEveryBoxShape is the resolution and aspect check.
func TestReadout_StaysInsideEveryBoxShape(t *testing.T) {
	shapes := []struct {
		name   string
		fw, fh int
		box    Box
	}{
		{"1080p side column", 1920, 1080, Box{X: 1300, Y: 40, W: 560, H: 480}},
		{"4K side column", 3840, 2160, Box{X: 2600, Y: 80, W: 1120, H: 960}},
		{"portrait wide strip", 1080, 1920, Box{X: 30, Y: 1200, W: 1020, H: 300}},
		{"a narrow sliver", 1080, 1920, Box{X: 30, Y: 30, W: 160, H: 600}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			c, img, ctx := elapsedFixture(t, s.fw, s.fh)
			p := HeartRate().Prepare(ctx, s.box)
			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 188}})

			in := inkCount(img, s.box, c.Theme)
			if in == 0 {
				t.Fatal("nothing drawn")
			}
			if whole := inkCount(img, Box{W: float64(s.fw), H: float64(s.fh)}, c.Theme); whole != in {
				t.Errorf("%d pixels of ink escaped the box", whole-in)
			}
		})
	}
}

// TestLayouts_CloseUpWhenAPanelDeclines is the end-to-end proof that the
// layout model's central claim holds with real panels rather than fixtures.
//
// Same layout, same frame, two activities: one carrying power and one not. The
// with-power render places three panels; the without-power render places two,
// and they are BIGGER. If closing up needed a redistribution special case, this
// is where the special case would be wrong.
func TestLayouts_CloseUpWhenAPanelDeclines(t *testing.T) {
	const w, h = 1920, 1080

	withPower := ctxWith(inspect.MetricHeartRate, inspect.MetricPower)
	withPower.Width, withPower.Height = w, h
	withoutPower := ctxWith(inspect.MetricHeartRate)
	withoutPower.Width, withoutPower.Height = w, h

	l, err := SelectLayout(LayoutAuto, w, h)
	if err != nil {
		t.Fatal(err)
	}
	keep := func(ctx *Context) func(Panel) bool {
		return func(p Panel) bool { return p.Accepts(ctx) }
	}

	three, err := l.Resolve(w, h, keep(withPower))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	two, err := l.Resolve(w, h, keep(withoutPower))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(three) != 3 {
		t.Fatalf("an activity with power placed %d panels, want 3", len(three))
	}
	if len(two) != 2 {
		t.Fatalf("an activity without power placed %d panels, want 2", len(two))
	}

	hrBefore := boxOf(t, three, HeartRate().Name())
	hrAfter := boxOf(t, two, HeartRate().Name())
	if !(hrAfter.H > hrBefore.H) {
		t.Errorf("heart rate is %g tall with power and %g without; the survivor must grow into the gap",
			hrBefore.H, hrAfter.H)
	}
	// Derived, not observed: heart rate and power split a column evenly, so
	// losing power doubles what heart rate gets.
	if ratio := hrAfter.H / hrBefore.H; ratio < 1.9 || ratio > 2.1 {
		t.Errorf("heart rate grew by %.2fx, want about 2x (it had an equal-weight sibling)", ratio)
	}
	// And no hole is left behind. Exact coverage is not the assertion here --
	// this layout has a frame margin and per-slot padding, so the panels
	// legitimately cover less than the frame. What must hold is that they do
	// not overlap, and that losing a panel INCREASED the area drawn rather
	// than leaving a void where it was.
	coveredByTwo := assertNoOverlap(t, two)
	coveredByThree := assertNoOverlap(t, three)
	if !(coveredByTwo > coveredByThree) {
		t.Errorf("two panels cover %g and three cover %g; the survivors did not take up the slack",
			coveredByTwo, coveredByThree)
	}
}

// TestSelectLayout_PicksATreePerOrientation pins that portrait is a different
// arrangement rather than the landscape one rescaled.
func TestSelectLayout_PicksATreePerOrientation(t *testing.T) {
	pick := func(w, h int) string {
		l, err := SelectLayout(LayoutAuto, w, h)
		if err != nil {
			t.Fatal(err)
		}
		return l.Name
	}
	if got := pick(1920, 1080); got != "landscape" {
		t.Errorf("1920x1080 selected %q", got)
	}
	if got := pick(1080, 1920); got != "portrait" {
		t.Errorf("1080x1920 selected %q", got)
	}
	// Square is not portrait: nothing about it forces a column.
	if got := pick(1000, 1000); got != "landscape" {
		t.Errorf("a square frame selected %q, want landscape", got)
	}

	// A named layout overrides the frame's shape, which is the whole point of
	// being able to name one.
	forced, err := SelectLayout("landscape", 1080, 1920)
	if err != nil {
		t.Fatal(err)
	}
	if forced.Name != "landscape" {
		t.Errorf("--layout landscape on a portrait frame selected %q", forced.Name)
	}
	if _, err := SelectLayout("sideways", 1920, 1080); err == nil {
		t.Error("an unknown layout name was accepted")
	}

	ctx := ctxWith(inspect.MetricHeartRate, inspect.MetricPower)
	keep := func(p Panel) bool { return p.Accepts(ctx) }
	for _, s := range []struct {
		name string
		w, h int
	}{{"landscape", 1920, 1080}, {"portrait", 1080, 1920}, {"4K", 3840, 2160}} {
		layout, err := SelectLayout(LayoutAuto, s.w, s.h)
		if err != nil {
			t.Fatal(err)
		}
		placed, err := layout.Resolve(s.w, s.h, keep)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if len(placed) != 3 {
			t.Errorf("%s placed %d panels, want 3", s.name, len(placed))
		}
		covered := assertNoOverlap(t, placed)
		// The margin and pads take a slice, but a layout covering less than
		// half its frame has a geometry bug rather than a tasteful gap.
		if frac := covered / (float64(s.w) * float64(s.h)); frac < 0.75 {
			t.Errorf("%s: panels cover only %.0f%% of the frame", s.name, frac*100)
		}
	}
}

// TestReadout_RowsClearOneAnother pins the vertical spacing.
//
// Text is drawn centred on its y, so each row occupies roughly its own size
// either side of that point. Picking three fractions that look separated is
// not enough: the first version of this panel put the value at 0.42 of the box
// centred at 0.55 and the unit at 0.82, and the value's descenders printed
// straight across the unit label -- "bpm" over the bottom of "160". No test
// caught it, because every assertion was about ink being inside the BOX.
//
// The rows are checked at several box shapes, since the sizes are fractions of
// the box's smaller dimension while the positions are fractions of its height:
// the two scale differently, so a shape that clears at one aspect ratio can
// collide at another.
func TestReadout_RowsClearOneAnother(t *testing.T) {
	shapes := []struct {
		name   string
		fw, fh int
		box    Box
	}{
		{"the landscape side column", 960, 540, Box{X: 640, Y: 20, W: 298, H: 243}},
		{"a 1080p side column", 1920, 1080, Box{X: 1300, Y: 40, W: 560, H: 480}},
		{"a portrait wide strip", 1080, 1920, Box{X: 30, Y: 1200, W: 1020, H: 300}},
		{"a narrow sliver", 1080, 1920, Box{X: 30, Y: 30, W: 160, H: 600}},
		{"a small box", 640, 360, Box{X: 10, Y: 10, W: 200, H: 150}},
		// The shape that exposed the stretching: a narrow column three times
		// taller than it is wide, which is what a readout inherits when its
		// neighbour declines.
		{"a column left by a declining neighbour", 960, 540, Box{X: 640, Y: 20, W: 298, H: 500}},
		{"an extremely tall column", 1080, 1920, Box{X: 30, Y: 30, W: 240, H: 1800}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			_, _, ctx := elapsedFixture(t, s.fw, s.fh)
			p := HeartRate().Prepare(ctx, s.box).(*readoutPainter)

			// Half-heights, since each row is centred on its y.
			_, labelH, err := ctx.Fonts.Measure(p.label, p.labelPx)
			if err != nil {
				t.Fatal(err)
			}
			_, valueH, err := ctx.Fonts.Measure("888", p.valuePx)
			if err != nil {
				t.Fatal(err)
			}
			_, unitH, err := ctx.Fonts.Measure(p.unit, p.unitPx)
			if err != nil {
				t.Fatal(err)
			}

			labelBottom := p.labelY + labelH/2
			valueTop, valueBottom := p.valueY-valueH/2, p.valueY+valueH/2
			unitTop := p.unitY - unitH/2

			if labelBottom > valueTop {
				t.Errorf("the label reaches %g and the value starts at %g; they collide", labelBottom, valueTop)
			}
			if valueBottom > unitTop {
				t.Errorf("the value reaches %g and the unit starts at %g; they collide", valueBottom, unitTop)
			}

			// The rows must also stay TOGETHER. Spacing them at fractions of
			// the box's HEIGHT spreads the group as the box grows, which is
			// what happened when the power panel declined and heart rate
			// inherited a column twice as tall: the label floated to the top,
			// the unit sank to the bottom, and the reading was marooned
			// between them. Bounding the group against the box's smaller
			// dimension is what keeps it a group.
			unit := s.box.H
			if s.box.W < unit {
				unit = s.box.W
			}
			if extent := (p.unitY + unitH/2) - (p.labelY - labelH/2); extent > unit {
				t.Errorf("the three rows span %g in a box whose smaller dimension is %g; "+
					"the group is being stretched by the box rather than sized against it", extent, unit)
			}
		})
	}
}

// TestPanelNames_AreConsistentAndDistinct keeps the render summary readable.
//
// These names are printed to the user, so a mix of styles -- "elapsed" beside
// "Heart rate" -- reads as two different kinds of thing rather than a list of
// panels. And two panels sharing a name would make the summary ambiguous about
// which one declined.
func TestPanelNames_AreConsistentAndDistinct(t *testing.T) {
	panels := []Panel{ElapsedPanel{}, HeartRate(), Power(), Cadence()}

	seen := map[string]bool{}
	for _, p := range panels {
		n := p.Name()
		if n == "" {
			t.Errorf("%T has an empty name", p)
			continue
		}
		if seen[n] {
			t.Errorf("two panels are both named %q", n)
		}
		seen[n] = true
		for _, r := range n {
			if !(r >= 'a' && r <= 'z') && r != '-' {
				t.Errorf("panel name %q contains %q; names are lower-case with hyphens so the summary reads as one list", n, r)
				break
			}
		}
	}
}

// TestPower_FollowsTheSelectedSource pins that the flag actually selects a
// sensor, on a fixture carrying BOTH -- which is the only case where the
// choice is observable, and the case the real recording presents.
//
// The two sources disagree by design, by enough at the same instant to change
// the number on screen. Showing one when the
// user asked for the other is not a formatting slip; it is displaying a
// different instrument's measurement under the label they chose.
func TestPower_FollowsTheSelectedSource(t *testing.T) {
	both := trackWith(inspect.MetricPower, strydPower)
	sample := both.Samples[0]

	native, ok := sample.ResolvedPower(fitactivity.PowerNative)
	if !ok {
		t.Fatal("fixture has no native power")
	}
	stryd, ok := sample.ResolvedPower(fitactivity.PowerStryd)
	if !ok {
		t.Fatal("fixture has no footpod power")
	}
	if native == stryd {
		t.Fatalf("the fixture's two sources both read %v; it cannot distinguish them", native)
	}

	cases := []struct {
		src  fitactivity.PowerSource
		want float64
	}{
		{fitactivity.PowerNative, native},
		{fitactivity.PowerStryd, stryd},
		// Auto prefers the footpod when it is there.
		{fitactivity.PowerAuto, stryd},
	}
	for _, c := range cases {
		t.Run(c.src.String(), func(t *testing.T) {
			ctx := &Context{Track: both, Report: inspect.Build(both), PowerSource: c.src}
			p := Power().Prepare(ctx, Box{W: 200, H: 200}).(*readoutPainter)
			got, ok := p.value(sample)
			if !ok {
				t.Fatal("no reading resolved")
			}
			if got != c.want {
				t.Errorf("source %v read %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// TestPower_AutoFallsBackToNative covers the other half of auto: an activity
// with a power meter but no footpod still shows power.
func TestPower_AutoFallsBackToNative(t *testing.T) {
	nativeOnly := ctxWith(inspect.MetricPower)
	nativeOnly.PowerSource = fitactivity.PowerAuto
	if !Power().Accepts(nativeOnly) {
		t.Fatal("auto declined an activity carrying native power")
	}
	p := Power().Prepare(nativeOnly, Box{W: 200, H: 200}).(*readoutPainter)
	if _, ok := p.value(nativeOnly.Track.Samples[0]); !ok {
		t.Error("auto resolved no reading from native power")
	}
}

// TestPower_AForcedSourceDeclinesRatherThanSubstituting is the strictness that
// makes the flag mean anything.
//
// Asking for the footpod on an activity that has only a power meter must
// DECLINE -- so the layout closes up and the summary says power was declined --
// rather than quietly showing the other sensor's number under the same label.
// A silent substitution would make the flag look like it worked while
// displaying the thing it was used to avoid.
func TestPower_AForcedSourceDeclinesRatherThanSubstituting(t *testing.T) {
	nativeOnly := ctxWith(inspect.MetricPower)
	nativeOnly.PowerSource = fitactivity.PowerStryd
	if Power().Accepts(nativeOnly) {
		t.Error("--power-source stryd accepted an activity with only native power")
	}

	strydOnly := ctxWith(strydPower)
	strydOnly.PowerSource = fitactivity.PowerNative
	if Power().Accepts(strydOnly) {
		t.Error("--power-source native accepted an activity with only footpod power")
	}
	// ...and auto takes it.
	strydOnly.PowerSource = fitactivity.PowerAuto
	if !Power().Accepts(strydOnly) {
		t.Error("auto declined an activity carrying footpod power")
	}
}

// TestPower_AcceptsCannotBeAnsweredByTheReport documents why this panel is the
// one exception to panels asking the coverage report.
//
// Both sources are called "Power", so a report lookup by name finds the native
// row and answers about a sensor the user may not have selected. Here is a
// fixture where the report says power is present while the SELECTED source has
// none -- and the panel must decline.
func TestPower_AcceptsCannotBeAnsweredByTheReport(t *testing.T) {
	nativeOnly := trackWith(inspect.MetricPower)
	rep := inspect.Build(nativeOnly)

	if !rep.Carries(inspect.MetricPower) {
		t.Fatal("precondition: the report should say this activity carries power")
	}
	ctx := &Context{Track: nativeOnly, Report: rep, PowerSource: fitactivity.PowerStryd}
	if Power().Accepts(ctx) {
		t.Error("the panel agreed with the report rather than with the selected source")
	}
}

// TestFormatPace_DerivesMinutesPerKilometre works every expectation out from
// the speed rather than pinning what the function returns.
func TestFormatPace_DerivesMinutesPerKilometre(t *testing.T) {
	// maxPaceSeconds("88:88") = 99*60+59 = 5999 s/km, so 1000/5999 m/s is the
	// slowest speed the template can still hold, and anything even a hair
	// under it needs a sixth character the template has no room for.
	const boundarySpeed = 1000.0 / 5999
	cases := []struct {
		name  string
		speed float64 // m/s
		want  string
	}{
		// 1000 m at 5 m/s is 200 s = 3:20.
		{"a fast runner", 5, "3:20"},
		// 1000 / 3.0864 = 324 s = 5:24, the pace on the reference recording.
		{"an ordinary run", 1000.0 / 324, "5:24"},
		// A brisk walk: 1000 / 1.4 = 714 s = 11:54.
		{"walking", 1.4, "11:54"},
		// Exactly a round number, to catch a seconds field that should be
		// padded and is not.
		{"four minutes flat", 1000.0 / 240, "4:00"},
		{"just over a minute boundary", 1000.0 / 241, "4:01"},
		// Standing still has NO pace. Zero is a real reading and its
		// reciprocal does not exist; printing "0:00" would say infinitely
		// fast, and any figure at all would be invented.
		{"stopped", 0, PacePlaceholder},
		{"a negative speed cannot happen and must not divide", -1, PacePlaceholder},
		// Exactly at the template's own ceiling: 99:59, the widest real pace
		// "88:88" has room for.
		{"the slowest pace the template can hold", boundarySpeed, "99:59"},
		// A hair slower needs a sixth character ("100:0x"), which the
		// template does not have -- this is the bug: a smoothed mean this
		// small is a real, positive speed, and its naive reciprocal would be
		// "859:06" or worse, six characters where five were reserved.
		{"just too slow for the template", boundarySpeed * 0.999, PacePlaceholder},
		// The exact speed a mostly-stationary smoothing window produces in
		// TestSmoothSample_StationaryOpeningYieldsATinyPositiveSpeed
		// (internal/render/smooth_test.go): nineteen stationary seconds and
		// one at 3.0 m/s, averaged over twenty, is 0.15 m/s -- a real,
		// positive speed whose naive reciprocal is 6667 s = 111:07, which
		// "88:88" cannot show at all.
		{"a smoothed near-stationary mean", 3.0 / 20, PacePlaceholder},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatPace(c.speed); got != c.want {
				t.Errorf("FormatPace(%v) = %q, want %q", c.speed, got, c.want)
			}
		})
	}
}

// TestMaxPaceSeconds_DerivesFromTheTemplatesOwnShape pins the arithmetic
// maxPaceSeconds does its work with, independent of the pace template's
// current literal value -- so a change to the template (a wider or narrower
// minutes column) is checked against the SAME rule rather than against a
// number copied out of today's template by hand.
func TestMaxPaceSeconds_DerivesFromTheTemplatesOwnShape(t *testing.T) {
	cases := []struct {
		template string
		want     int
	}{
		// Two digits of minutes (up to 99), plus 59 seconds: 99*60+59.
		{"88:88", 99*60 + 59},
		// One digit of minutes (up to 9): 9*60+59 = 599.
		{"8:88", 9*60 + 59},
		// Three digits of minutes (up to 999): 999*60+59.
		{"888:88", 999*60 + 59},
	}
	for _, c := range cases {
		t.Run(c.template, func(t *testing.T) {
			if got := maxPaceSeconds(c.template); got != c.want {
				t.Errorf("maxPaceSeconds(%q) = %d, want %d", c.template, got, c.want)
			}
		})
	}
}

// TestPace_StoppedIsAbsentNotZero pins where the decision lives.
//
// A speed of zero is present data whose DERIVED value does not exist, which is
// the absent-data rule reached from an unusual direction. The judgement is made
// in the value accessor -- "there is no reading here" -- rather than as a
// formatting special case downstream, so the panel's ordinary placeholder path
// handles it and nothing has to know pace is peculiar.
func TestPace_StoppedIsAbsentNotZero(t *testing.T) {
	moving := fitactivity.Sample{HasSpeed: true, Speed: 3.2}
	stopped := fitactivity.Sample{HasSpeed: true, Speed: 0}
	noReading := fitactivity.Sample{}

	p := Pace()
	if _, ok := p.value(moving); !ok {
		t.Error("a moving runner resolved no pace")
	}
	if _, ok := p.value(stopped); ok {
		t.Error("a stopped runner resolved a pace; standing still is not infinitely slow")
	}
	if _, ok := p.value(noReading); ok {
		t.Error("a sample with no speed resolved a pace")
	}
}

// TestPace_SmoothedNearStationarySpeedIsAbsentNotANumber is reachable from
// real data: 0.15 m/s is exactly what
// TestSmoothSample_StationaryOpeningYieldsATinyPositiveSpeed
// (internal/render/smooth_test.go) shows the render loop's own smoothing
// produces for nineteen stationary seconds followed by one at a running
// pace -- a real, positive speed a bare "speed > 0" guard would accept, whose
// naive reciprocal pace is 6667 s = 111:07, six characters where the
// template ("88:88") reserves five.
//
// This is the bug this fix closes, on the panel side of the fix: the value
// accessor must treat this speed exactly like a stopped runner's zero,
// answering "no reading" rather than handing format a number it cannot
// print.
//
// Verified load bearing by hand: reverting the guard to
// `s.HasSpeed && s.Speed > 0` (the code before this fix) makes this test
// fail, resolving ok=true for 0.15 m/s.
func TestPace_SmoothedNearStationarySpeedIsAbsentNotANumber(t *testing.T) {
	smoothed := fitactivity.Sample{HasSpeed: true, Speed: 3.0 / 20}
	p := Pace()
	if v, ok := p.value(smoothed); ok {
		t.Errorf("a smoothed speed of %v m/s (pace 111:07) resolved a pace of %v; "+
			"the reciprocal cannot fit the \"88:88\" template", smoothed.Speed, v)
	}
}

// TestPace_ValueAcceptsExactlyWhatTheTemplateCanHoldAndNothingSlower pins the
// boundary itself, on both sides, so the guard is not merely "rejects one
// known-bad value" but actually agrees with maxPaceSeconds/minPaceSpeed at
// the edge.
func TestPace_ValueAcceptsExactlyWhatTheTemplateCanHoldAndNothingSlower(t *testing.T) {
	minSpeed := minPaceSpeed(paceTemplate)
	p := Pace()

	if _, ok := p.value(fitactivity.Sample{HasSpeed: true, Speed: minSpeed}); !ok {
		t.Errorf("the slowest speed the template can render (%v m/s, 99:59) was rejected", minSpeed)
	}
	justSlower := minSpeed * 0.999
	if _, ok := p.value(fitactivity.Sample{HasSpeed: true, Speed: justSlower}); ok {
		t.Errorf("a speed just below the template's floor (%v m/s) resolved a pace", justSlower)
	}
}

// TestPace_DynamicDrawsThePlaceholderNotAConfidentOverflowForASmoothedSpeed
// is the pixel-level half: the value-accessor test above proves the
// PRESENCE decision, this proves the DRAWING follows it -- the smoothed
// near-stationary speed must draw in Theme.Absent, never Theme.Foreground,
// and the drawn text must never be wider than the template it was sized
// against.
func TestPace_DynamicDrawsThePlaceholderNotAConfidentOverflowForASmoothedSpeed(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 0, Y: 0, W: 400, H: 300}
	p := Pace().Prepare(ctx, box)

	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{HasSample: true, Sample: fitactivity.Sample{HasSpeed: true, Speed: 3.0 / 20}})

	if inkCount(img, box, c.Theme) == 0 {
		t.Fatal("nothing was drawn for a smoothed near-stationary speed; the box would be an unexplained hole")
	}
	if !containsColor(img, c.Theme.Absent) {
		t.Error("a smoothed near-stationary speed was not drawn in Theme.Absent; it would read as a confident measurement")
	}
	if containsColor(img, c.Theme.Foreground) {
		t.Error("a smoothed near-stationary speed was drawn in Theme.Foreground, as though 111:07 min/km were a real reading")
	}
}

// TestPace_FormattedTextNeverExceedsTheTemplateWidth pins the second
// symptom directly: whatever FormatPace returns, across every speed the
// value accessor can actually let through PLUS the boundary values just
// outside that range, its rendered width must never exceed paceTemplate's
// own rendered width. Before this fix, a speed of 0.02 m/s (a plausible
// smoothed mean) formatted as "859:06" -- six characters -- against a
// five-character template.
func TestPace_FormattedTextNeverExceedsTheTemplateWidth(t *testing.T) {
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	tmplW, _, err := faces.Measure(paceTemplate, 40)
	if err != nil {
		t.Fatal(err)
	}

	minSpeed := minPaceSpeed(paceTemplate)
	speeds := []float64{
		0.30, // a very fast runner
		5, 1000.0 / 324, 1.4, 1000.0 / 240,
		minSpeed * 1.5, minSpeed, // just inside the floor, and exactly at it
		minSpeed * 0.5, minSpeed * 0.1, 0.02, 0.001, // the overflow region
		0, -1, // no pace at all
	}
	for _, speed := range speeds {
		text := FormatPace(speed)
		w, _, err := faces.Measure(text, 40)
		if err != nil {
			t.Fatal(err)
		}
		if w > tmplW {
			t.Errorf("FormatPace(%v) = %q measures %g but the template %q only reserves %g; it would overflow the box",
				speed, text, w, paceTemplate, tmplW)
		}
	}
}

// TestDistance_IsAlwaysKilometres pins the unit's constancy, which matters
// more than it looks.
//
// The unit is drawn in the STATIC layer, rasterized once for the whole render.
// A readout that began in metres and crossed into kilometres would leave "m"
// burned in under a figure that had become kilometres, and nothing would report
// it -- the static layer cannot know the reading changed shape.
func TestDistance_IsAlwaysKilometres(t *testing.T) {
	d := Distance()
	if d.unit != "km" {
		t.Errorf("unit is %q; it must not vary with magnitude, because it is drawn once", d.unit)
	}
	cases := []struct {
		metres float64
		want   string
	}{
		{0, "0.00"},
		{340, "0.34"},
		{1000, "1.00"},
		{3049, "3.05"},
		{42195, "42.20"},
	}
	for _, c := range cases {
		v, ok := d.value(fitactivity.Sample{HasDistance: true, Distance: c.metres})
		if !ok {
			t.Fatalf("%v m resolved no distance", c.metres)
		}
		if got := d.format(v); got != c.want {
			t.Errorf("%v m rendered as %q, want %q", c.metres, got, c.want)
		}
	}
	if _, ok := d.value(fitactivity.Sample{}); ok {
		t.Error("a sample with no distance resolved one")
	}
}

// TestCadence_DoublesForRunningButNotForCycling pins the sport-dependent unit.
//
// FIT stores cadence as revolutions per minute: crank revolutions on a bike,
// which is what a cyclist reads, and revolutions PER LEG on a run, where the
// figure the runner recognises is twice it. Showing a run's 87 where the watch
// said 174 spm is not wrong so much as unrecognisable.
func TestCadence_DoublesForRunningButNotForCycling(t *testing.T) {
	sample := fitactivity.Sample{HasCadence: true, Cadence: 87}

	cases := []struct {
		sport     string
		wantValue float64
		wantUnit  string
	}{
		{"running", 174, "spm"},
		{"walking", 174, "spm"},
		{"hiking", 174, "spm"},
		// Case is the device's vocabulary, not this program's.
		{"Running", 174, "spm"},
		{"cycling", 87, "rpm"},
		{"swimming", 87, "rpm"},
		// An unknown or absent sport keeps the recorded number under its
		// recorded unit: the answer that cannot be wrong, where a guessed
		// doubling would silently halve or double somebody's cadence.
		{"", 87, "rpm"},
		{"some_new_sport", 87, "rpm"},
	}
	for _, c := range cases {
		name := c.sport
		if name == "" {
			name = "(no sport recorded)"
		}
		t.Run(name, func(t *testing.T) {
			track := trackWith(inspect.MetricCadence)
			track.Sport = c.sport
			ctx := &Context{Track: track, Report: inspect.Build(track)}

			bound := Cadence().Prepare(ctx, Box{W: 200, H: 200}).(*readoutPainter)
			if bound.unit != c.wantUnit {
				t.Errorf("unit = %q, want %q", bound.unit, c.wantUnit)
			}
			got, ok := bound.value(sample)
			if !ok {
				t.Fatal("no cadence resolved")
			}
			if got != c.wantValue {
				t.Errorf("cadence = %v, want %v", got, c.wantValue)
			}
		})
	}
}

// TestReadouts_SizeAgainstTheirOwnWidestString pins that each readout measures
// a template that can actually hold its readings.
//
// The default template is "888", which suits a three-digit integer and nothing
// else: a pace of "12:34" and a distance of "42.20" are both wider, and a
// readout sized against "888" prints them past the edge of its box.
func TestReadouts_SizeAgainstTheirOwnWidestString(t *testing.T) {
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		r        Readout
		examples []string
	}{
		{HeartRate(), []string{"48", "152", "205"}},
		{Power(), []string{"0", "285", "499"}},
		{Cadence(), []string{"84", "168"}},
		{Distance(), []string{"0.00", "3.05", "42.20"}},
		{Pace(), []string{"3:20", "5:45", "11:54", PacePlaceholder}},
	}
	for _, c := range cases {
		t.Run(c.r.Name(), func(t *testing.T) {
			tmplW, _, err := faces.Measure(c.r.valueTemplate(), 40)
			if err != nil {
				t.Fatal(err)
			}
			for _, ex := range c.examples {
				w, _, err := faces.Measure(ex, 40)
				if err != nil {
					t.Fatal(err)
				}
				if w > tmplW {
					t.Errorf("%q measures %g but the template %q only reserves %g; it would overflow its box",
						ex, w, c.r.valueTemplate(), tmplW)
				}
			}
		})
	}
}

// TestDistance_CoarsensOnAHeavilyCompressedRender pins the other half of the
// readability problem.
//
// Distance is never averaged -- averaging an accumulator buys nothing -- but it
// still churns under compression. At 480x a frame advances sixteen seconds,
// something like eighty metres, so hundredths of a kilometre change both
// decimals every single frame. One decimal changes far less often and drops
// precision nobody could read at that speed anyway.
func TestDistance_CoarsensOnAHeavilyCompressedRender(t *testing.T) {
	epochT := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	sample := fitactivity.Sample{HasDistance: true, Distance: 12345}

	cases := []struct {
		name    string
		speedup float64
		want    string
	}{
		// A frame covers 1/30 s of activity: hundredths track it fine.
		{"real time", 1, "12.35"},
		// 60x is 2 s per frame, still fine.
		{"sixty times", 60, "12.35"},
		// 150x is 5 s per frame, the threshold.
		{"at the threshold", 150, "12.3"},
		// 480x is 16 s per frame -- both decimals would change every frame.
		{"four hours into thirty seconds", 480, "12.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl, err := NewTimeline(epochT, 4*time.Hour, 30, c.speedup)
			if err != nil {
				t.Fatal(err)
			}
			track := trackWith(inspect.MetricDistance)
			ctx := &Context{Track: track, Report: inspect.Build(track), Timeline: tl}

			p := Distance().Prepare(ctx, Box{W: 300, H: 200}).(*readoutPainter)
			v, ok := p.value(sample)
			if !ok {
				t.Fatal("no distance resolved")
			}
			if got := p.format(v); got != c.want {
				t.Errorf("at %vx distance rendered as %q, want %q", c.speedup, got, c.want)
			}
			// The template must carry the same number of DECIMALS as the
			// output, or the readout is sized against a shape it will never
			// print. Comparing lengths would be wrong: the template is the
			// widest case ("888.8", up to 999.9 km) while a reading is
			// whatever it happens to be.
			decimals := func(s string) int {
				if i := strings.IndexByte(s, '.'); i >= 0 {
					return len(s) - i - 1
				}
				return 0
			}
			if got, want := decimals(p.valueTemplate()), decimals(c.want); got != want {
				t.Errorf("template %q has %d decimals but the reading %q has %d",
					p.valueTemplate(), got, c.want, want)
			}
		})
	}
}

// TestDistance_CoarsensFromTheCoarsestSegmentNotTheBase pins the property
// TestDistance_CoarsensOnAHeavilyCompressedRender never had the machinery to
// try: MaxSpeedup, not BaseSpeedup, decides distance precision, and it does
// so ONCE in Prepare -- before any Frame exists -- so the same precision
// applies for the whole render whether a reading came from the base-rate
// stretch or from inside the highlight that made the render coarsen. Naming
// only one call site ("bindDistancePrecision, readout.go") in the design was
// exactly the risk this test locks down: MaxSpeedup was renamed FROM
// BaseSpeedup deliberately (see Timeline's own doc comment) so a stale call
// site keeping the old name would fail to compile, but nothing stopped a
// call site that compiled fine while reading the WRONG one of the two.
//
// The base alone stays fine at two decimals (60x, 2s/frame -- the same case
// TestDistance_CoarsensOnAHeavilyCompressedRender already pins at 60x); one
// highlight running far faster (480x, 16s/frame) must coarsen the WHOLE
// readout, not just the highlight's own stretch, because the unit and the
// decimal count are drawn in the static layer and there is no per-frame
// re-Prepare to vary it.
func TestDistance_CoarsensFromTheCoarsestSegmentNotTheBase(t *testing.T) {
	track := trackWith(inspect.MetricDistance)
	sample := fitactivity.Sample{HasDistance: true, Distance: 12345}

	plain, err := NewTimeline(epoch, time.Hour, 30, 60)
	if err != nil {
		t.Fatal(err)
	}
	plainCtx := &Context{Track: track, Report: inspect.Build(track), Timeline: plain}
	plainP := Distance().Prepare(plainCtx, Box{W: 300, H: 200}).(*readoutPainter)
	v, ok := plainP.value(sample)
	if !ok {
		t.Fatal("no distance resolved")
	}
	if got, want := plainP.format(v), "12.35"; got != want {
		t.Fatalf("precondition: at the base rate alone (60x), format = %q, want %q", got, want)
	}

	fast := Highlight{From: 10 * time.Minute, To: 11 * time.Minute, RateFactor: 480}
	tl, err := NewSegmentedTimeline(epoch, time.Hour, 30, 60, []Highlight{fast})
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Track: track, Report: inspect.Build(track), Timeline: tl}
	p := Distance().Prepare(ctx, Box{W: 300, H: 200}).(*readoutPainter)
	if got, want := p.format(v), "12.3"; got != want {
		t.Errorf("with a 480x highlight added on top of the same 60x base, format = %q, want %q -- "+
			"MaxSpeedup (the highlight's own coarser rate), not BaseSpeedup, must decide precision", got, want)
	}
	if p.valueTemplate() != "888.8" {
		t.Errorf("valueTemplate() = %q, want the one-decimal template", p.valueTemplate())
	}
}

// distanceTrack builds a track whose Distance metric genuinely reaches
// maxMetres, for the tests of distanceLayout's magnitude-driven half below.
// trackWith deliberately does not do this -- its branches mark a metric
// present or absent across every sample, not carry a real accumulating
// value, and no other test needed one until this one.
func distanceTrack(maxMetres float64) *fitactivity.Track {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	return &fitactivity.Track{Samples: []fitactivity.Sample{
		{Time: base, HasDistance: true, Distance: 0},
		{Time: base.Add(time.Hour), HasDistance: true, Distance: maxMetres},
	}}
}

// TestDistanceLayout_TradesADecimalForTheExtraDigitPastNinetyNineKilometres
// works every case out from distanceLayout's own stated rule rather than
// pinning today's numbers: the digit budget is four (from distanceTemplate,
// "88.88"), decimals are whatever that budget has left once the integer
// digits are spent (see distanceDecimalsFor, and the dedicated test of it
// below), and the integer digits grow by one each time the ROUNDED reading
// reaches the next power of ten.
func TestDistanceLayout_TradesADecimalForTheExtraDigitPastNinetyNineKilometres(t *testing.T) {
	cases := []struct {
		name          string
		maxKm         float64
		coarse        bool
		wantIntDigits int
		wantDecimals  int
	}{
		{"no distance data at all", 0, false, 2, 2},
		{"a marathon", 42.195, false, 2, 2},
		{"just under the two-decimal ceiling", 99.99, false, 2, 2},
		{"a hundred-kilometre ride", 100, false, 3, 1},
		{"just under the one-decimal ceiling", 999.94, false, 3, 1},
		// Rounds UP to 1000.0 at one decimal, which needs a fourth integer
		// digit that the raw 999.96 would not, by its own magnitude, have
		// asked for -- this is the boundary the rounded-value check exists
		// for.
		{"rounds across the next power of ten", 999.96, false, 4, 0},
		{"a thousand kilometres", 1000, false, 4, 0},
		// Past a thousand the decimal is gone entirely rather than the
		// reserved width growing: at that scale a tenth of a kilometre is
		// noise, and dropping it keeps four integer digits inside the same
		// four-digit budget the template always had. The width is only
		// threatened again beyond 99999 km, which is not an activity anyone
		// records.
		{"a multi-day ride", 12345.6, false, 5, 0},
		// Compression alone reserves one extra digit over the base template,
		// matching what this readout always did before magnitude was
		// considered, regardless of how short the activity actually is.
		{"a short ride, heavily compressed", 5, true, 3, 1},
		// Magnitude already asked for at least as much as compression would;
		// compression must not narrow it back down.
		{"a long ride, heavily compressed", 1500, true, 4, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotInt, gotDec := distanceLayout(c.maxKm, c.coarse)
			if gotInt != c.wantIntDigits || gotDec != c.wantDecimals {
				t.Errorf("distanceLayout(%v, %v) = (%d, %d), want (%d, %d)",
					c.maxKm, c.coarse, gotInt, gotDec, c.wantIntDigits, c.wantDecimals)
			}
		})
	}
}

// TestDistanceDecimalsFor_IsTheDigitBudgetMinusIntegerDigits pins the general
// rule directly, independent of any of the ladder's particular thresholds:
// decimals is exactly the digit budget minus however many integer digits are
// spent, floored at zero once the integer part alone reaches the budget.
// Deriving "budget" from distanceTemplate here, the same way
// distanceDecimalsFor itself does, is what keeps this test from silently
// passing against a stale copy of a number that moved.
func TestDistanceDecimalsFor_IsTheDigitBudgetMinusIntegerDigits(t *testing.T) {
	budget := distanceDigitCount(distanceTemplate)
	for intDigits := 0; intDigits <= budget+3; intDigits++ {
		want := budget - intDigits
		if want < 0 {
			want = 0
		}
		if got := distanceDecimalsFor(intDigits); got != want {
			t.Errorf("distanceDecimalsFor(%d) = %d, want %d (budget %d)", intDigits, got, want, budget)
		}
	}
}

// TestDistance_WidensPastNinetyNineKilometres is the integration half: an
// activity whose REPORT says it reaches past 99.99 km must widen its
// template rather than let the readout overflow it, on an ordinary,
// uncompressed render where compression cannot be the explanation.
//
// Before this fix, the template was fixed at "88.88" regardless of the
// activity, and formatting a hundred-kilometre reading at two decimals
// ("100.00") drew six characters into a box reserved for five.
func TestDistance_WidensPastNinetyNineKilometres(t *testing.T) {
	track := distanceTrack(150_000) // a 150 km ride
	tl, err := NewTimeline(epoch, 6*time.Hour, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Track: track, Report: inspect.Build(track), Timeline: tl}
	p := Distance().Prepare(ctx, Box{W: 300, H: 200}).(*readoutPainter)

	if got, want := p.valueTemplate(), "888.8"; got != want {
		t.Errorf("valueTemplate() = %q, want %q -- a 150 km activity needs a third integer digit", got, want)
	}
	v, ok := p.value(fitactivity.Sample{HasDistance: true, Distance: 150_000})
	if !ok {
		t.Fatal("no distance resolved")
	}
	if got, want := p.format(v), "150.0"; got != want {
		t.Errorf("format = %q, want %q -- a real 150 km reading, not hidden behind a placeholder", got, want)
	}

	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	tmplW, _, err := faces.Measure(p.valueTemplate(), 40)
	if err != nil {
		t.Fatal(err)
	}
	textW, _, err := faces.Measure(p.format(v), 40)
	if err != nil {
		t.Fatal(err)
	}
	if textW > tmplW {
		t.Errorf("%q measures %g but the template %q only reserves %g; it overflows the box",
			p.format(v), textW, p.valueTemplate(), tmplW)
	}
}

// TestDistance_FormattedTextNeverExceedsTheTemplateWidth follows the shape of
// TestPace_FormattedTextNeverExceedsTheTemplateWidth: across a sweep of
// activity sizes and render speeds, whatever distanceLayout resolves must
// never draw past its own template's width -- and, unlike pace, must never
// fall back to a placeholder, because a long real activity is not a stopped
// runner and always has a genuine reading to show.
func TestDistance_FormattedTextNeverExceedsTheTemplateWidth(t *testing.T) {
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		maxKm  float64
		coarse bool
	}{
		{0, false}, {0.34, false}, {42.195, false}, {99.99, false},
		{100, false}, {999.94, false}, {999.96, false}, {1000, false},
		{12345.6, false}, // an implausible but not impossible multi-day track
		{5, true}, {99.99, true}, {150, true}, {1500, true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%vkm coarse=%v", c.maxKm, c.coarse), func(t *testing.T) {
			intDigits, decimals := distanceLayout(c.maxKm, c.coarse)
			template := distanceTemplateFor(intDigits, decimals)
			tmplW, _, err := faces.Measure(template, 40)
			if err != nil {
				t.Fatal(err)
			}

			text := fmt.Sprintf("%.*f", decimals, c.maxKm)
			textW, _, err := faces.Measure(text, 40)
			if err != nil {
				t.Fatal(err)
			}
			if textW > tmplW {
				t.Errorf("%q measures %g but the template %q only reserves %g; it would overflow the box",
					text, textW, template, tmplW)
			}
			if text == ReadoutPlaceholder {
				t.Errorf("a distance of %v km rendered as the placeholder; a long real activity is not a stopped runner", c.maxKm)
			}
		})
	}
}
