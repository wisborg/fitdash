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

// TestRender_FrameRedundancy REPORTS how many consecutive frames differ, per
// layout. It asserts nothing about the figure, deliberately.
//
// The number is an input to a design decision -- whether caching unchanged
// frames is worth building -- and not a contract. An earlier version did
// assert that redundancy existed, on the strength of a measurement taken when
// every panel drew text or snapped to 1 Hz data: 9 of 299 frames changed, 3%.
// Adding the elevation profile took it to 100%, and the assertion failed
// against a panel working exactly as intended.
//
// That is the finding, not a problem with the test. The playhead is placed
// from an INTERPOLATED distance, so it moves a fraction of a pixel every
// frame, and it should: a playhead that jumped once a second would look broken
// beside a clock that ticks. Smooth motion and frame redundancy are in direct
// tension, and which one a panel chooses is a panel's business. Pinning the
// ratio here would make the next smoothly-animating panel look like a
// regression.
func TestRender_FrameRedundancy(t *testing.T) {
	opts := fittest.DefaultOptions()
	opts.Count = 60
	opts.PowerWatts = 240

	layouts := []struct {
		name string
		l    panel.Layout
	}{
		{"full landscape", panel.LandscapeLayout()},
		{"text only", textOnly()},
		{"route only", routeOnly()},
		{"elevation only", elevationOnly()},
	}
	for _, lay := range layouts {
		ctx := benchContext(t, opts, 640, 360)
		r, err := New(ctx, lay.l, panel.DefaultTheme())
		if err != nil {
			t.Fatalf("%s: %v", lay.name, err)
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
		t.Logf("%-16s %3d of %d consecutive frames differ (%.0f%%)",
			lay.name, changed, n-1, float64(changed)/float64(n-1)*100)
	}
}

func elevationOnly() panel.Layout {
	l := panel.LandscapeLayout()
	l.Name = "elevation-only"
	l.Root = panel.Slot{Panel: panel.ElevationPanel{}, Pad: 0.01}
	return l
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
