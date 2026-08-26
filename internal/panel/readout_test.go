package panel

import (
	"strconv"
	"testing"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// reportWith builds a report in which exactly the named metrics have coverage.
func reportWith(present ...string) inspect.Report {
	r := inspect.Report{Samples: 100}
	for _, name := range []string{
		inspect.MetricHeartRate, inspect.MetricPower, inspect.MetricCadence,
	} {
		m := inspect.Metric{Name: name}
		for _, p := range present {
			if p == name {
				m.Present = 100
			}
		}
		r.Metrics = append(r.Metrics, m)
	}
	return r
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
	withPower := &Context{Report: reportWith(inspect.MetricHeartRate, inspect.MetricPower)}
	withoutPower := &Context{Report: reportWith(inspect.MetricHeartRate)}

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
	partial := inspect.Report{Samples: 100, Metrics: []inspect.Metric{
		{Name: inspect.MetricPower, Present: 4},
	}}
	if !Power().Accepts(&Context{Report: partial}) {
		t.Error("Power declined at 4% coverage; partial data is a dropout, not an absence")
	}
}

// TestReadout_DrawsTheReadingAndThePlaceholder is the "did it actually draw"
// test, and the check that absence looks different from a value.
func TestReadout_DrawsTheReadingAndThePlaceholder(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 0, Y: 0, W: 400, H: 300}
	p := HeartRate().Prepare(ctx, box)

	shot := func(f Frame) []byte {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, f)
		out := make([]byte, len(img.Pix))
		copy(out, img.Pix)
		return out
	}

	live := shot(Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 148}})
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
	// and it is the direction a panel is most likely to get wrong.
	zero := shot(Frame{HasSample: true, Sample: fitactivity.Sample{HasHeartRate: true, HeartRate: 0}})
	if string(zero) == string(absent) {
		t.Error("a heart rate of 0 renders as the placeholder; a real zero is data")
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

	withPower := &Context{
		Report: reportWith(inspect.MetricHeartRate, inspect.MetricPower),
		Width:  w, Height: h,
	}
	withoutPower := &Context{
		Report: reportWith(inspect.MetricHeartRate),
		Width:  w, Height: h,
	}

	l := SelectLayout(w, h)
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
	if got := SelectLayout(1920, 1080).Name; got != "landscape" {
		t.Errorf("1920x1080 selected %q", got)
	}
	if got := SelectLayout(1080, 1920).Name; got != "portrait" {
		t.Errorf("1080x1920 selected %q", got)
	}
	// Square is not portrait: nothing about it forces a column.
	if got := SelectLayout(1000, 1000).Name; got != "landscape" {
		t.Errorf("a square frame selected %q, want landscape", got)
	}

	ctx := &Context{Report: reportWith(inspect.MetricHeartRate, inspect.MetricPower)}
	keep := func(p Panel) bool { return p.Accepts(ctx) }
	for _, s := range []struct {
		name string
		w, h int
	}{{"landscape", 1920, 1080}, {"portrait", 1080, 1920}, {"4K", 3840, 2160}} {
		placed, err := SelectLayout(s.w, s.h).Resolve(s.w, s.h, keep)
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
