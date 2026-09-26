package panel

import (
	"context"
	"image"
	"image/color"
	"math"
	"sync"
	"testing"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// recordingBasemap answers every view with a plain image of the size asked
// for, and keeps the views.
type recordingBasemap struct {
	mu    sync.Mutex
	views []tilemap.View
}

func (r *recordingBasemap) Image(_ context.Context, v tilemap.View) (image.Image, error) {
	r.mu.Lock()
	r.views = append(r.views, v)
	r.mu.Unlock()
	img := image.NewRGBA(image.Rect(0, 0, v.Width, v.Height))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.RGBA{A: 255})
	return img, nil
}

func (r *recordingBasemap) Attribution() string { return "Maps © Somebody" }
func (r *recordingBasemap) Name() string        { return "recording/style" }

// Every view the route panel asks a map for has EXACTLY the aspect ratio of
// the image it asks for, so the image spans exactly the rectangle it is
// drawn over.
//
// It did not. The rectangle came from the box's fractional size and the
// image's size from that size truncated to whole pixels, so the two differed
// by a fraction of a pixel along each axis -- a fraction of a percent of the
// shape. The renderer had to reconcile them: osmbase up to v0.9 cropped the
// longer axis and from v0.10 extends the shorter one, and the panel stretched
// either result over the rectangle it had asked about, so the map sat up to
// a pixel or two beside the route line, in opposite directions on the two
// versions. Measured on a real render: 5% of the frame's pixels changed
// between them. With the aspect exact there is nothing to reconcile and the
// renderer's rule cannot matter.
//
// Asked on a box of fractional size, since that is where it went wrong, and
// the image must still cover the whole box.
func TestRoutePanel_ABasemapViewHasItsImagesExactShape(t *testing.T) {
	ctx, _ := zoomFixture(t, true, 800, 800)
	rec := &recordingBasemap{}
	ctx.Basemap = rec
	box := Box{X: 40.3, Y: 40.7, W: 700.6, H: 523.4}
	RoutePanel{}.Prepare(ctx, box)

	if len(rec.views) == 0 {
		t.Fatal("the panel asked for no map at all, so this proves nothing")
	}
	for _, v := range rec.views {
		x0, y0 := tilemap.Project(v.North, v.West)
		x1, y1 := tilemap.Project(v.South, v.East)
		world := (x1 - x0) / (y1 - y0)
		pixels := float64(v.Width) / float64(v.Height)
		if math.Abs(world/pixels-1) > 1e-9 {
			t.Errorf("a %dx%d image was asked for a rectangle of aspect %.6f, not its own %.6f", v.Width, v.Height, world, pixels)
		}
	}
	// Every view is drawn to fill the box when it is the one in use, so each
	// image must be at least the box's size in both directions -- rounded
	// up, never down, or a strip along an edge is left without a map.
	for _, v := range rec.views {
		if float64(v.Width) < box.W || float64(v.Height) < box.H {
			t.Errorf("an image of %dx%d was asked for a %.1fx%.1f box", v.Width, v.Height, box.W, box.H)
		}
	}
}

// The whole-course view settles on a destination a fraction of a pixel
// larger than the box -- its image is asked for in whole pixels, rounded up
// -- and that one is still worth keeping resampled. It is the view most
// frames draw, and resampling was measured at three quarters of a frame. The
// cache's bound is there to refuse a magnified view many times the box, not a
// view that overhangs it by the rounding of its own size.
func TestBasemapView_CachesAViewOverhangingTheBoxByUnderAPixel(t *testing.T) {
	box := Box{X: 40.3, Y: 40.7, W: 700.6, H: 523.4}
	dst := Box{X: box.X, Y: box.Y, W: 701, H: 524}
	if pixelRect(dst).Size().X <= pixelRect(box).Size().X {
		t.Fatal("precondition: the destination is no wider in pixels than the box, so this proves nothing")
	}
	v := &basemapView{img: image.NewRGBA(image.Rect(0, 0, 1402, 1048))}
	v.resampled(dst, box)
	if v.scaled == nil {
		t.Error("a view overhanging its box by under a pixel was not kept resampled; every frame would resample it again")
	}
}
