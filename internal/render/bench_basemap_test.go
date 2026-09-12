package render

import (
	"context"
	"image"
	"image/color"
	"math/rand"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"
	"github.com/wisborg/fitactivity/fittest"

	"github.com/wisborg/fitdash/internal/inspect"
	"github.com/wisborg/fitdash/internal/panel"
	"github.com/wisborg/fitdash/internal/tilemap"
)

// stubProvider stands in for a map service, so the benchmark measures what
// fitdash does with imagery rather than what a network does.
//
// It returns an image LARGER than the requested box, which is what the real
// provider now does: planStatic rounds its zoom up, so the canvas that comes
// back overshoots the panel by up to a factor of two on each axis. The
// oversize is the whole reason the per-frame rescale is expensive, so a stub
// returning an exactly-sized image would measure a cost nobody pays.
type stubProvider struct {
	oversample float64
	calls      int
}

func (s *stubProvider) Image(_ context.Context, v tilemap.View) (image.Image, error) {
	s.calls++
	w, h := int(float64(v.Width)*s.oversample), int(float64(v.Height)*s.oversample)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Noise, not a flat fill: a resampling filter on a constant image is not
	// the same work as one on real map detail, and a flat source would also
	// let a caching bug pass unnoticed.
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), 255})
		}
	}
	return img, nil
}

func (s *stubProvider) Attribution() string { return "Maps (c) Nobody" }
func (s *stubProvider) Name() string        { return "stub" }

// basemapContext is benchContext with imagery and, optionally, a highlight
// that zooms -- the combination the visual gate measured at a fifth of the
// throughput of a plain render.
func basemapContext(b testing.TB, w, h int, zoom bool) *panel.Context {
	b.Helper()
	opts := fittest.DefaultOptions()
	opts.Count = 600
	opts.PowerWatts = 240

	path := b.TempDir() + "/activity.fit"
	if err := fittest.WriteFile(path, opts); err != nil {
		b.Fatalf("generating fixture: %v", err)
	}
	track, err := fitactivity.Decode(path)
	if err != nil {
		b.Fatalf("Decode: %v", err)
	}
	timer := fitactivity.BuildTimerModel(track)

	hs := []panel.Highlight{{
		Name: "climb",
		From: 100 * time.Second,
		To:   300 * time.Second,
		Zoom: zoom,
	}}
	tl, err := panel.NewTimelineForActivityWithHighlights(timer, 30, 1, hs)
	if err != nil {
		b.Fatalf("NewTimelineForActivityWithHighlights: %v", err)
	}
	return &panel.Context{
		Track: track, Report: inspect.Build(track), Timer: timer, Timeline: tl,
		Width: w, Height: h, FontScale: 0.05, Fonts: mustFaces(b),
		Elevation:  panel.BuildElevation(track, panel.DefaultElevationTuning(track)),
		Highlights: hs,
		Basemap:    &stubProvider{oversample: 1.45},
		BasemapDim: 0.65,
	}
}

func benchBasemapFrame(b *testing.B, w, h int, zoom bool, layout panel.Layout) {
	b.Helper()
	ctx := basemapContext(b, w, h, zoom)
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

// The route-only pair isolates the panel; the full-layout pair is what a user
// actually renders, where the route has a fifth of the frame rather than all
// of it. Both are here because they answer different questions, and reading
// either as the other overstates or understates the cost by about five times.
func BenchmarkBasemap_NoZoom1080p(b *testing.B) {
	benchBasemapFrame(b, 1920, 1080, false, routeOnly())
}
func BenchmarkBasemap_Zoom1080p(b *testing.B) { benchBasemapFrame(b, 1920, 1080, true, routeOnly()) }
func BenchmarkBasemap_NoZoom4K(b *testing.B)  { benchBasemapFrame(b, 3840, 2160, false, routeOnly()) }
func BenchmarkBasemap_Zoom4K(b *testing.B)    { benchBasemapFrame(b, 3840, 2160, true, routeOnly()) }

func BenchmarkBasemapFull_NoZoom1080p(b *testing.B) {
	benchBasemapFrame(b, 1920, 1080, false, panel.LandscapeLayout())
}
func BenchmarkBasemapFull_Zoom1080p(b *testing.B) {
	benchBasemapFrame(b, 1920, 1080, true, panel.LandscapeLayout())
}
func BenchmarkBasemapFull_NoZoom4K(b *testing.B) {
	benchBasemapFrame(b, 3840, 2160, false, panel.LandscapeLayout())
}
func BenchmarkBasemapFull_Zoom4K(b *testing.B) {
	benchBasemapFrame(b, 3840, 2160, true, panel.LandscapeLayout())
}
