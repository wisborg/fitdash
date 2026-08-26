package render

import (
	"image"
	"testing"

	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/panel"
)

// The benchmarks here measure ONE FRAME, not a whole render.
//
// A whole-render benchmark is the wrong unit twice over. It is unusably slow
// -- an 18,000-frame render per iteration, and Go wants several iterations --
// and it answers a question nobody asks, because render time is frames times
// per-frame cost and the frame count is fixed by the activity and the frame
// rate. What can actually be changed is the cost of a frame, so that is what
// is measured, and comparing two layouts differing by one panel says what that
// panel costs.
//
// Static rasterization is deliberately outside the timed loop: it happens once
// per render, so including it would let an expensive static layer hide inside
// a per-frame number.
func benchFrame(b *testing.B, w, h int, layout panel.Layout) {
	b.Helper()
	opts := fittest.DefaultOptions()
	opts.Count = 600
	opts.PowerWatts = 240

	ctx := benchContext(b, opts, w, h)
	r, err := New(ctx, layout, panel.DefaultTheme())
	if err != nil {
		b.Fatal(err)
	}

	base := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := r.RenderStatic(base); err != nil {
		b.Fatal(err)
	}
	buf := image.NewRGBA(image.Rect(0, 0, w, h))
	frames := r.Frames()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf.Pix, base.Pix)
		if err := r.RenderDynamic(buf, r.Frame(i%frames)); err != nil {
			b.Fatal(err)
		}
	}
}

// textOnly is the shipping landscape layout with the route panel removed.
//
// Beware reading the difference between this and the full layout as "what the
// route costs". It is not, and measuring it was instructive: text-only came
// out at 19.1 ms a frame against the full layout's 7.2 ms -- SLOWER, while
// drawing strictly less. Removing the route lets its column-mate grow, so the
// clock is drawn at roughly two and a half times the size, and glyph
// rasterization scales with area. What the route actually costs is
// BenchmarkFrame_RouteOnly1080p, and it is 0.17 ms.
//
// The lesson generalises: in this renderer the dominant per-frame cost is the
// SIZE of the text, not the number of panels, so a comparison that changes the
// layout is mostly measuring the layout.
func textOnly() panel.Layout {
	l := panel.LandscapeLayout()
	l.Name = "text-only"
	l.Root = panel.Slot{Dir: panel.Row, Children: []panel.Slot{
		{Panel: panel.ElapsedPanel{}, Weight: 2, Pad: 0.01},
		{Dir: panel.Col, Weight: 1, Children: []panel.Slot{
			{Panel: panel.HeartRate(), Pad: 0.01},
			{Panel: panel.Power(), Pad: 0.01},
		}},
	}}
	return l
}

func routeOnly() panel.Layout {
	l := panel.LandscapeLayout()
	l.Name = "route-only"
	l.Root = panel.Slot{Panel: panel.RoutePanel{}, Pad: 0.01}
	return l
}

func BenchmarkFrame_Full1080p(b *testing.B)      { benchFrame(b, 1920, 1080, panel.LandscapeLayout()) }
func BenchmarkFrame_TextOnly1080p(b *testing.B)  { benchFrame(b, 1920, 1080, textOnly()) }
func BenchmarkFrame_RouteOnly1080p(b *testing.B) { benchFrame(b, 1920, 1080, routeOnly()) }
func BenchmarkFrame_Full4K(b *testing.B)         { benchFrame(b, 3840, 2160, panel.LandscapeLayout()) }

// BenchmarkFrameCopy measures the static-base restore alone: the memmove every
// frame pays before anything is drawn. It is the floor the rest sits on, and
// at 4K it is not negligible.
func BenchmarkFrameCopy_1080p(b *testing.B) { benchCopy(b, 1920, 1080) }
func BenchmarkFrameCopy_4K(b *testing.B)    { benchCopy(b, 3840, 2160) }

func benchCopy(b *testing.B, w, h int) {
	base := image.NewRGBA(image.Rect(0, 0, w, h))
	buf := image.NewRGBA(image.Rect(0, 0, w, h))
	b.SetBytes(int64(len(base.Pix)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf.Pix, base.Pix)
	}
}

// TestRender_ConsecutiveFramesAreMostlyIdentical measures the redundancy in
// the render, which is the number that decides whether caching frames is worth
// building.
//
// The activity's data arrives at 1 Hz and the clock advances once a second, so
// at 30 fps roughly thirty consecutive frames show exactly the same thing. It
// is reported rather than asserted tightly: the point is to know the figure,
// and pinning it would turn any future panel that animates continuously into a
// test failure rather than a design decision.
func TestRender_ConsecutiveFramesAreMostlyIdentical(t *testing.T) {
	opts := fittest.DefaultOptions()
	opts.Count = 60
	opts.PowerWatts = 240

	ctx := benchContext(t, opts, 640, 360)
	r, err := New(ctx, panel.LandscapeLayout(), panel.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}

	const n = 300 // ten seconds at 30 fps
	base := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	if err := r.RenderStatic(base); err != nil {
		t.Fatal(err)
	}
	cur := image.NewRGBA(image.Rect(0, 0, ctx.Width, ctx.Height))
	prev := make([]byte, len(cur.Pix))

	changed := 0
	for i := 0; i < n; i++ {
		copy(cur.Pix, base.Pix)
		if err := r.RenderDynamic(cur, r.Frame(i)); err != nil {
			t.Fatal(err)
		}
		if i > 0 && !bytesEqual(prev, cur.Pix) {
			changed++
		}
		copy(prev, cur.Pix)
	}

	t.Logf("%d of %d consecutive frames differ (%.0f%%) at 30 fps over 1 Hz data",
		changed, n-1, float64(changed)/float64(n-1)*100)
	if changed >= n-1 {
		t.Error("every frame differs from the last; there is no redundancy to exploit and the figure above is wrong")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
