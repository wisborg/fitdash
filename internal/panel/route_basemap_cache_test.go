package panel

import (
	"context"
	"image"
	"image/color"
	"math/rand"
	"testing"
	"time"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// patternedBasemap hands back a DIFFERENT, noisy image on every call.
//
// Both properties are needed to say anything about caching. A solid fill
// cannot distinguish a correctly reused image from a stale one, or a copied
// image from a resampled one, because every wrong answer is the same colour
// as the right one. And a cache that handed back the whole-course view where
// the zoomed one belongs would be invisible if the two were the same colour,
// which is what the existing fakeBasemap is for and why it is not used here.
type patternedBasemap struct {
	calls int
	// oversample is how much larger than the requested box the returned
	// image is, mirroring the real provider: planStatic rounds its zoom up,
	// so imagery arrives bigger than the panel and has to be scaled down.
	// At 1 it arrives at size and Canvas.Image copies instead of scaling,
	// which is a different code path and not the one a render takes.
	oversample float64
}

func (f *patternedBasemap) Image(_ context.Context, v tilemap.View) (image.Image, error) {
	f.calls++
	over := f.oversample
	if over <= 0 {
		over = 1
	}
	w, h := int(float64(v.Width)*over), int(float64(v.Height)*over)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(int64(f.calls)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rnd.Intn(256)), G: uint8(rnd.Intn(256)), B: uint8(rnd.Intn(256)), A: 255,
			})
		}
	}
	return img, nil
}

func (f *patternedBasemap) Attribution() string { return "Maps © Somebody, Data © Somebody" }
func (f *patternedBasemap) Name() string        { return "patterned/style" }

// TestRoutePanel_ASettledFrameDoesNotDependOnWhereTheViewportHasBeen is the
// whole contract of the resampled-image cache, stated as the property a
// viewer would notice if it broke.
//
// Each basemapView keeps ONE resampled image, for the size it was last drawn
// at, and a zoom changes that size twice a highlight in each direction. So
// every settled frame is reached with an entry built for some other size
// still in hand, and the render either throws it away or draws the wrong
// picture -- scaled a second time, from the wrong number of pixels, which
// reads as a map that went soft for no reason.
//
// Nothing about "the cache was hit" is asserted; a cache is allowed to miss.
// What is asserted is that the frame does not depend on the frames before it.
// The comparison is against a SEPARATE painter that drew only the frame in
// question, because a painter compared against its own earlier output agrees
// with itself even when both are wrong.
func TestRoutePanel_ASettledFrameDoesNotDependOnWhereTheViewportHasBeen(t *testing.T) {
	const w, h = 800, 800

	// The two views a render settles on: the whole course, well clear of the
	// highlight, and the highlight at full zoom.
	settled := map[string]Frame{
		"whole course": {Interval: NoHighlight},
		"full zoom":    {Interval: 0, IntervalWeight: 1},
	}

	// approach is the ramp a real render walks in and out of the zoom
	// through, and it is what leaves a wrongly-sized entry behind: partway
	// through, both views are drawn at sizes neither settles on.
	approach := []float64{0, 0.2, 0.45, 0.7, 0.9, 1, 0.9, 0.7, 0.45, 0.2, 0}

	draw := func(warmUp bool, f Frame) *image.RGBA {
		ctx, box := zoomFixture(t, true, w, h)
		ctx.Basemap = &patternedBasemap{oversample: 1.45}
		ctx.BasemapDim = 0.65
		img, c := zoomCanvas(t, w, h)
		p := RoutePanel{}.Prepare(ctx, box)

		at := ctx.Timeline.Start().Add(100 * time.Second)
		if warmUp {
			for _, weight := range approach {
				c.Fill(c.Theme.Background)
				p.Static(c)
				p.Dynamic(c, Frame{At: at, Interval: 0, IntervalWeight: weight})
			}
		}
		f.At = at
		if f.Interval == NoHighlight {
			f.At = ctx.Timeline.Start().Add(900 * time.Second)
		}
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, f)
		return img
	}

	for name, f := range settled {
		t.Run(name, func(t *testing.T) {
			fresh, warmed := draw(false, f), draw(true, f)
			if n := countDifferingPixels(fresh, warmed); n != 0 {
				t.Errorf("%d pixels differ from the same frame drawn without a zoom before it; the cached imagery did not follow the viewport", n)
			}
		})
	}
}

// TestRoutePanel_RepeatedFramesAreIdenticalWithABasemap says the cache cannot
// drift: drawing the same frame twice must put the same pixels in the frame,
// whether the imagery was resampled for it or fetched from the memo.
//
// This is the cheap, broad version of the test above, run at several points
// of the ramp so both the cached rectangle (a view filling the box) and the
// uncached ones (a view magnified past it) are covered.
func TestRoutePanel_RepeatedFramesAreIdenticalWithABasemap(t *testing.T) {
	const w, h = 640, 640
	ctx, box := zoomFixture(t, true, w, h)
	ctx.Basemap = &patternedBasemap{oversample: 1.45}
	ctx.BasemapDim = 0.65

	img, c := zoomCanvas(t, w, h)
	p := RoutePanel{}.Prepare(ctx, box)

	for _, weight := range []float64{0, 0.4, 1} {
		f := Frame{At: ctx.Timeline.Start().Add(100 * time.Second), Interval: 0, IntervalWeight: weight}

		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, f)
		first := cloneRGBA(img)

		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, f)

		if n := countDifferingPixels(first, img); n != 0 {
			t.Errorf("weight %v: the same frame drawn twice differs in %d pixels", weight, n)
		}
	}
}

// TestRoutePanel_ZoomWithABasemapKeepsEveryPixelInsideItsBox guards the
// attribution's move out of the clip.
//
// The credit is drawn after Clipped returns, because gg composites a string
// across the whole frame when a clip mask is set and that was half the cost
// of a zooming frame. What kept it inside the panel was never the clip --
// drawCredit fits the text to the box and anchors it to the box's corner --
// but "it fits by construction" is the claim this checks, since the clip is
// no longer there to make it true.
//
// Every non-background pixel is counted, not just the imagery's, so the text
// and its plate are included; the existing containment tests count one
// colour and would not see a credit drawn past the edge.
func TestRoutePanel_ZoomWithABasemapKeepsEveryPixelInsideItsBox(t *testing.T) {
	const w, h = 800, 800
	for _, weight := range []float64{0, 0.35, 1} {
		ctx, box := zoomFixture(t, true, w, h)
		ctx.Basemap = &patternedBasemap{oversample: 1.45}
		ctx.BasemapDim = 0.65

		img, c := zoomCanvas(t, w, h)
		p := RoutePanel{}.Prepare(ctx, box)
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{
			At:       ctx.Timeline.Start().Add(100 * time.Second),
			Interval: 0, IntervalWeight: weight,
		})

		in := inkCount(img, box, c.Theme)
		if in == 0 {
			t.Fatalf("weight %v: the panel drew nothing, so containment proves nothing", weight)
		}
		if whole := inkCount(img, Box{W: w, H: h}, c.Theme); whole != in {
			t.Errorf("weight %v: %d pixels drew outside the box", weight, whole-in)
		}
	}
}

// TestBasemapView_HidesOnlyWhatItActuallyCovers pins the three conditions
// drawBasemap relies on before it skips the whole-course image entirely.
//
// Skipping is the difference between one composite a frame and two, and the
// second one is at the zoom's own magnification -- but every condition that
// makes it safe can stop holding. A cross-fade midway leaves the zoomed image
// translucent; an image with transparency in it shows what is beneath it; a
// viewport that is not exactly the zoomed view's own rectangle leaves an edge
// of the box uncovered. Each of those must put the whole-course map back, and
// the cost of getting it wrong is a hole in the map rather than an error.
func TestBasemapView_HidesOnlyWhatItActuallyCovers(t *testing.T) {
	box := Box{X: 10, Y: 20, W: 300, H: 200}

	// A view whose ground is exactly the viewport below, so it lands on the
	// box edge to edge.
	opaque := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for i := range opaque.Pix {
		opaque.Pix[i] = 255
	}
	covering := &basemapView{img: opaque, opaque: true, minX: 0.5, minY: 0.25, spanX: 0.01, spanY: 0.01}

	if !covering.hides(box, 0.5, 0.25, 0.01, 0.01, 1) {
		t.Error("an opaque image drawn at full weight over the whole box does not report that it hides what is under it; the whole-course map would be composited on every zoomed frame for nothing")
	}

	// Partway through the cross-fade.
	if covering.hides(box, 0.5, 0.25, 0.01, 0.01, 0.5) {
		t.Error("a half-faded image claims to hide what is under it; the whole-course map would vanish mid-ramp and the zoom would fade in from bare background")
	}

	// The viewport moved, so the image no longer reaches the box's edges.
	if covering.hides(box, 0.49, 0.24, 0.03, 0.03, 1) {
		t.Error("an image covering part of the box claims to hide all of it; the uncovered edge would show no map at all")
	}

	// Same geometry, but the image has transparency in it.
	transparent := image.NewRGBA(image.Rect(0, 0, 300, 200))
	seeThrough := &basemapView{img: transparent, opaque: isOpaque(transparent), minX: 0.5, minY: 0.25, spanX: 0.01, spanY: 0.01}
	if seeThrough.hides(box, 0.5, 0.25, 0.01, 0.01, 1) {
		t.Error("an image with transparency claims to hide what is under it; the whole-course map would be dropped and show through as background")
	}

	// Nothing at all.
	var none *basemapView
	if none.hides(box, 0.5, 0.25, 0.01, 0.01, 1) {
		t.Error("a missing zoomed view claims to hide the whole-course one, which is the fallback it exists to leave showing")
	}
}

// TestBasemapView_NeverCachesAnImageLargerThanTheBox is the cache's memory
// bound, and it needs its own test because nothing on screen can show it.
//
// A zoomed viewport magnifies the whole-course image well past the panel on
// its way in and out, and a cache that kept those would hold a picture many
// times the size of the box -- hundreds of megabytes at 4K on a tight zoom --
// to answer a question asked once. The bound is invisible in every frame, so
// an unbounded cache passes every pixel test in this file and shows up only
// as a render that runs out of memory on somebody else's machine.
func TestBasemapView_NeverCachesAnImageLargerThanTheBox(t *testing.T) {
	box := Box{X: 0, Y: 0, W: 200, H: 150}
	src := image.NewRGBA(image.Rect(0, 0, 300, 225))
	v := &basemapView{img: src, opaque: true, minX: 0, minY: 0, spanX: 1, spanY: 1}

	// A viewport a twentieth of the image's ground: the image is drawn at
	// twenty times the box, which is exactly the frame a zoom ramps through.
	dst, ok := v.dest(box, 0.4, 0.4, 0.05, 0.05)
	if !ok {
		t.Fatal("dest declined a viewport it should have placed")
	}
	if dst.W <= box.W {
		t.Fatalf("fixture is wrong: the destination is %.0f wide against a %.0f box, so nothing here is oversized", dst.W, box.W)
	}
	if got := v.resampled(dst, box); got != image.Image(src) {
		t.Error("a magnified draw was resampled into the cache instead of being scaled straight from the source")
	}
	if v.scaled != nil {
		t.Errorf("cached a %v image for a %.0fx%.0f box", v.scaled.Bounds().Size(), box.W, box.H)
	}

	// The settled view, which is the one worth keeping.
	dst, ok = v.dest(box, 0, 0, 1, 1)
	if !ok {
		t.Fatal("dest declined the whole-course viewport")
	}
	if v.resampled(dst, box) == image.Image(src) {
		t.Fatal("the box-sized draw was not cached, so the cache never does anything")
	}
	if got := v.scaled.Bounds().Size(); got.X > int(box.W) || got.Y > int(box.H) {
		t.Errorf("cached a %v image for a %.0fx%.0f box", got, box.W, box.H)
	}
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	out := image.NewRGBA(src.Bounds())
	copy(out.Pix, src.Pix)
	return out
}

func countDifferingPixels(a, b *image.RGBA) int {
	if a.Bounds() != b.Bounds() {
		return -1
	}
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] ||
			a.Pix[i+2] != b.Pix[i+2] || a.Pix[i+3] != b.Pix[i+3] {
			n++
		}
	}
	return n
}
