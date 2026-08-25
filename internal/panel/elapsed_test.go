package panel

import (
	"image"
	"testing"
	"time"
)

// TestFormatClock_DerivesEachFieldFromTheDuration works every expected string
// out from its inputs rather than pinning what the function returns.
func TestFormatClock_DerivesEachFieldFromTheDuration(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0:00:00"},
		{"one second", time.Second, "0:00:01"},
		{"one minute", time.Minute, "0:01:00"},
		{"one hour", time.Hour, "1:00:00"},
		{"an ordinary run", 25*time.Minute + 53*time.Second, "0:25:53"},
		{"a long ride", 4*time.Hour + 33*time.Minute + 23*time.Second, "4:33:23"},
		// Truncated, not rounded: a stopwatch shows the second it is in, not
		// the one it is nearest. Rounding would put the readout half a second
		// ahead of the video it is burned into.
		{"most of a second", 999 * time.Millisecond, "0:00:00"},
		{"a second and most of another", 1999 * time.Millisecond, "0:00:01"},
		// Rollovers, where an off-by-one in the arithmetic shows.
		{"just before a minute", 59 * time.Second, "0:00:59"},
		{"exactly a minute", 60 * time.Second, "0:01:00"},
		{"just before an hour", 3599 * time.Second, "0:59:59"},
		{"exactly an hour", 3600 * time.Second, "1:00:00"},
		// Hours are not padded, so the width changes only here -- past any
		// activity this targets.
		{"ten hours", 10 * time.Hour, "10:00:00"},
		// Unreachable in practice, since both clocks clamp at the start; a
		// negative readout would be a stranger failure than none.
		{"negative", -time.Minute, "0:00:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatClock(c.d); got != c.want {
				t.Errorf("FormatClock(%v) = %q, want %q", c.d, got, c.want)
			}
		})
	}
}

// TestClockPlaceholder_MatchesTheShapeItReplaces keeps the placeholder from
// shifting the layout when it appears.
func TestClockPlaceholder_MatchesTheShapeItReplaces(t *testing.T) {
	if got, want := len(ClockPlaceholder), len(FormatClock(time.Hour)); got != want {
		t.Errorf("the placeholder is %d characters and a clock is %d; in a monospace face they must match or the readout jumps",
			got, want)
	}
}

// --- drawing ----------------------------------------------------------------

func elapsedFixture(t *testing.T, w, h int) (*Canvas, *image.RGBA, *Context) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	faces, err := NewFaceCache()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCanvas(img, float64(h)*0.05, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Width: w, Height: h, FontScale: 0.05, Fonts: faces}
	return c, img, ctx
}

func inkCount(img *image.RGBA, b Box, th Theme) int {
	br, bg, bb, _ := th.Background.RGBA()
	n := 0
	for y := int(b.Y); y < int(b.Y+b.H) && y < img.Bounds().Dy(); y++ {
		for x := int(b.X); x < int(b.X+b.W) && x < img.Bounds().Dx(); x++ {
			if x < 0 || y < 0 {
				continue
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != br || g != bg || bl != bb {
				n++
			}
		}
	}
	return n
}

// TestElapsedPanel_StaticDrawsChromeAndDynamicDrawsTheClock is the "did it
// actually draw" test.
//
// A panel's product is pixels and Draw returns nothing, so a guard that
// early-returned, a size that collapsed to zero, or a colour equal to the
// background would all leave the box empty while every call succeeded. Both
// passes are therefore asserted to put ink in the box -- and to put it only
// there.
func TestElapsedPanel_StaticDrawsChromeAndDynamicDrawsTheClock(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 50, Y: 40, W: 300, H: 220}
	p := ElapsedPanel{}.Prepare(ctx, box)

	c.Fill(c.Theme.Background)
	p.Static(c)
	static := inkCount(img, box, c.Theme)
	if static == 0 {
		t.Fatal("Static drew nothing; the label and rule are the static layer's whole content here")
	}
	if whole := inkCount(img, Box{W: 400, H: 300}, c.Theme); whole != static {
		t.Errorf("Static put %d pixels of ink outside its box", whole-static)
	}

	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{Elapsed: time.Hour, Active: time.Hour, HasTimerEvents: true})
	dynamic := inkCount(img, box, c.Theme)
	if dynamic == 0 {
		t.Fatal("Dynamic drew nothing")
	}
	if whole := inkCount(img, Box{W: 400, H: 300}, c.Theme); whole != dynamic {
		t.Errorf("Dynamic put %d pixels of ink outside its box", whole-dynamic)
	}
}

// TestElapsedPanel_ClockChangesWithTheFrame guards the failure a "did it draw"
// test cannot see: a panel that draws the same thing every frame.
//
// The static layer would still be right, the video would be the right length,
// and the dashboard would simply be frozen -- which is exactly the shape of
// bug that survives a frame count.
func TestElapsedPanel_ClockChangesWithTheFrame(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 0, Y: 0, W: 400, H: 300}
	p := ElapsedPanel{}.Prepare(ctx, box)

	render := func(d time.Duration) []byte {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{Elapsed: d, Active: d, HasTimerEvents: true})
		out := make([]byte, len(img.Pix))
		copy(out, img.Pix)
		return out
	}

	a := render(1 * time.Second)
	b := render(2 * time.Second)
	same := render(1 * time.Second)

	if string(a) == string(b) {
		t.Error("one second and two seconds render identically; the clock is not moving")
	}
	if string(a) != string(same) {
		t.Error("the same elapsed time rendered differently twice; the panel is not deterministic")
	}
}

// TestElapsedPanel_MissingTimerEventsShowsAPlaceholder pins the honesty
// requirement.
//
// Without timer events, Active equals Elapsed for want of anything to
// subtract. Printing that number would be printing a measurement that was
// never made -- the same confident lie as rendering a missing heart rate as
// zero, and it must look different from a real reading, not merely be
// documented as different.
func TestElapsedPanel_MissingTimerEventsShowsAPlaceholder(t *testing.T) {
	c, img, ctx := elapsedFixture(t, 400, 300)
	box := Box{X: 0, Y: 0, W: 400, H: 300}
	p := ElapsedPanel{}.Prepare(ctx, box)

	shot := func(hasEvents bool) []byte {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{Elapsed: time.Hour, Active: time.Hour, HasTimerEvents: hasEvents})
		out := make([]byte, len(img.Pix))
		copy(out, img.Pix)
		return out
	}

	measured := shot(true)
	absent := shot(false)
	if string(measured) == string(absent) {
		t.Fatal("a file with no timer events renders identically to one with them; " +
			"an unmeasured active time is being shown as though it were measured")
	}

	// And the placeholder must be drawn in the absent colour, so a viewer can
	// tell it from a reading without knowing which is which in advance.
	c.Fill(c.Theme.Background)
	p.Dynamic(c, Frame{Elapsed: time.Hour, Active: time.Hour, HasTimerEvents: false})
	if !containsColor(img, c.Theme.Absent) {
		t.Error("the placeholder is not drawn in Theme.Absent; it would read as a live value")
	}
}

func containsColor(img *image.RGBA, want interface {
	RGBA() (uint32, uint32, uint32, uint32)
}) bool {
	wr, wg, wb, _ := want.RGBA()
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r == wr && g == wg && b == wb {
				return true
			}
		}
	}
	return false
}

// TestElapsedPanel_FitsEveryBoxShape is the resolution and aspect-ratio check
// at the panel level.
//
// A Box is a rectangle the panel must fit, not an anchor it can grow away
// from, so the readout has to stay inside boxes of very different shapes --
// including the narrow one a portrait layout produces, where the width
// collapses and a size derived from the height would overflow.
func TestElapsedPanel_FitsEveryBoxShape(t *testing.T) {
	shapes := []struct {
		name   string
		frameW int
		frameH int
		box    Box
	}{
		{"1080p, a third of the width", 1920, 1080, Box{X: 40, Y: 40, W: 600, H: 1000}},
		{"4K, the same layout", 3840, 2160, Box{X: 80, Y: 80, W: 1200, H: 2000}},
		{"portrait, a wide short strip", 1080, 1920, Box{X: 30, Y: 30, W: 1020, H: 300}},
		{"a very narrow column", 1080, 1920, Box{X: 30, Y: 30, W: 220, H: 900}},
		{"a small box", 640, 360, Box{X: 10, Y: 10, W: 200, H: 120}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			c, img, ctx := elapsedFixture(t, s.frameW, s.frameH)
			p := ElapsedPanel{}.Prepare(ctx, s.box)
			c.Fill(c.Theme.Background)
			p.Static(c)
			p.Dynamic(c, Frame{Elapsed: 4*time.Hour + 33*time.Minute, Active: 4 * time.Hour, HasTimerEvents: true})

			inBox := inkCount(img, s.box, c.Theme)
			if inBox == 0 {
				t.Fatal("nothing drawn")
			}
			whole := inkCount(img, Box{W: float64(s.frameW), H: float64(s.frameH)}, c.Theme)
			if whole != inBox {
				t.Errorf("%d pixels of ink escaped the box", whole-inBox)
			}
		})
	}
}

// TestElapsedPanel_AcceptsEveryActivity pins the policy declaration. Every
// activity has a duration, so there is no activity-level absence to decline
// over; what can be missing is active time, and that is a placeholder.
func TestElapsedPanel_AcceptsEveryActivity(t *testing.T) {
	if !(ElapsedPanel{}).Accepts(&Context{}) {
		t.Error("ElapsedPanel declined; it has no activity-level absence to decline over")
	}
}
