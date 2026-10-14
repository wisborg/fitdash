package panel

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/perspective"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// flatFlyer is a basemap that can fly: a pyramid of tiles of one colour
// over level ground -- the real pictures and the real cameras, without a
// store.
type flatFlyer struct{ ground color.RGBA }

func (flatFlyer) Image(context.Context, tilemap.View) (image.Image, error) {
	return nil, errors.New("flatFlyer draws only in perspective")
}
func (flatFlyer) Attribution() string { return "© Test map" }
func (flatFlyer) Name() string        { return "flyer/test" }

func (f flatFlyer) Flight(context.Context, slice.Bounds, float64) (tilemap.FlightMap, error) {
	tiles, err := perspective.NewTiles(func(context.Context, uint8, uint32, uint32) (*perspective.Tile, error) {
		img := image.NewRGBA(image.Rect(0, 0, perspective.TileSize, perspective.TileSize))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = f.ground.R, f.ground.G, f.ground.B, 0xff
		}
		return &perspective.Tile{Image: img}, nil
	}, 10, 16, 0)
	return &flatFlight{tiles: tiles}, err
}

type flatFlight struct{ tiles *perspective.Tiles }

func (*flatFlight) PlanNames(context.Context, []perspective.Camera, perspective.Options, int, func(int, int)) error {
	return nil
}

func (f *flatFlight) Picture(ctx context.Context, _ int, cam perspective.Camera, o perspective.Options) (*perspective.Picture, error) {
	v := cam.MapView(cam.MapBounds(float64(o.Width)/float64(o.Height)), o.Height, 512)
	return perspective.RenderContext(ctx, perspective.Scene{Tiles: f.tiles, View: v, Heights: perspective.Level}, cam, o)
}
func (*flatFlight) Ground(osm.Coord) (float64, bool)      { return 0, false }
func (*flatFlight) Leveled() bool                         { return true }
func (*flatFlight) Attribution() string                   { return "© Test map | Test elevation" }
func (*flatFlight) NameCountries([]osm.Coord) (int, bool) { return 0, false }

func flyoverContext(t *testing.T, base time.Time, basemap tilemap.Provider) *Context {
	t.Helper()
	ctx := route3DContext(t, base, basemap)
	ctx.Route3D.Flyover, ctx.Route3D.Distance = true, 300
	// The 400 s square in 20 s of video.
	tl, err := NewTimeline(base, 400*time.Second, 30, 20)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Timeline = tl
	return ctx
}

// The flyover draws a picture each frame, the route fills in over it, and
// the camera follows the activity: mid-flight its position is near the
// middle of the box. A frame drawn twice is drawn the same; the credit is
// the flight's own, elevation and all.
func TestFlyover_FollowsTheActivityAndTheRouteFillsIn(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ground := color.RGBA{R: 0x50, G: 0x80, B: 0x50, A: 0xff}
	ctx := flyoverContext(t, base, flatFlyer{ground: ground})
	box := Box{W: 600, H: 400}
	p := RoutePanel{}.Prepare(ctx, box)
	pf, ok := p.(*flyoverPainter)
	if !ok {
		t.Fatalf("Prepare returned a %T, not the flyover; note: %q", p, p.(BasemapReporter).BasemapNote())
	}
	if note := pf.BasemapNote(); !strings.Contains(note, "flown over in 3d") || !strings.Contains(note, "drawn level") {
		t.Errorf("note %q", note)
	}
	if pf.mapCredit != "© Test map | Test elevation" {
		t.Errorf("the credit is %q, want the flight's own", pf.mapCredit)
	}

	img := image.NewRGBA(image.Rect(0, 0, 600, 400))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	p.Static(c)
	frame := func(i int) *image.RGBA {
		c.Fill(c.Theme.Background)
		p.Dynamic(c, Frame{Index: i, At: ctx.Timeline.At(i)})
		return cloneRGBA(img)
	}
	n := ctx.Timeline.Frames()
	mid := frame(n / 2)
	if got := countNear(mid, ground, 40); got < 600*400/4 {
		t.Errorf("only %d pixels of the map's colour; the picture is not filling the box", got)
	}
	// The dot, near the middle: the camera looks at where the activity is.
	var sx, sy, k float64
	for y := 0; y < 400; y++ {
		for x := 0; x < 600; x++ {
			if countNearInBox(mid, Box{X: float64(x), Y: float64(y), W: 1, H: 1}, c.Theme.Accent, 12) == 1 {
				sx, sy, k = sx+float64(x), sy+float64(y), k+1
			}
		}
	}
	if k == 0 {
		t.Fatal("no position dot mid-flight")
	}
	if dx, dy := sx/k-300, sy/k-200; math.Hypot(dx, dy) > 60 {
		t.Errorf("mid-flight the dot is at (%.0f, %.0f), not near the middle (300, 200)", sx/k, sy/k)
	}
	if again := frame(n / 2); countDifferingPixels(mid, again) != 0 {
		t.Error("the same frame drawn twice came out different")
	}
	early, late := countNear(frame(n/4), c.Theme.Foreground, 12), countNear(frame(3*n/4), c.Theme.Foreground, 12)
	if early == 0 || late == 0 {
		t.Errorf("covered route pixels %d at a quarter, %d at three quarters; want some of each", early, late)
	}
}

// A basemap that cannot fly draws the flat route, and says why.
func TestFlyover_FallsBackToFlatSayingWhy(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	p := RoutePanel{}.Prepare(flyoverContext(t, base, &patternedBasemap{}), Box{W: 600, H: 400})
	flat, ok := p.(*routePainter)
	if !ok {
		t.Fatalf("Prepare returned a %T, not the flat painter", p)
	}
	if note := flat.BasemapNote(); !strings.HasPrefix(note, "drawn flat: a flyover needs") {
		t.Errorf("note %q", note)
	}
}

// Suppressed, the flyover draws nothing: its whole box is map, which the
// pause notice's placement does not count as content.
func TestFlyover_SuppressedDrawsNothing(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx := flyoverContext(t, base, flatFlyer{ground: color.RGBA{R: 0x50, G: 0x80, B: 0x50, A: 0xff}})
	p := RoutePanel{}.Prepare(ctx, Box{W: 600, H: 400})
	s, ok := p.(BasemapSuppressor)
	if !ok {
		t.Fatalf("the flyover cannot be suppressed")
	}
	img := image.NewRGBA(image.Rect(0, 0, 600, 400))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	c.Fill(c.Theme.Background)
	blank := cloneRGBA(img)
	s.SuppressBasemap(true)
	p.Dynamic(c, Frame{Index: ctx.Timeline.Frames() / 2, At: ctx.Timeline.At(ctx.Timeline.Frames() / 2)})
	if d := countDifferingPixels(blank, img); d != 0 {
		t.Errorf("suppressed, the flyover drew %d pixels", d)
	}
}
