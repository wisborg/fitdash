package panel

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/perspective"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// flatDraper is a basemap that drapes: a map of one colour, over level
// ground -- enough to place the route through a real camera without a store.
type flatDraper struct{ ground color.RGBA }

func (flatDraper) Image(context.Context, tilemap.View) (image.Image, error) {
	return nil, errors.New("flatDraper draws only in perspective")
}
func (flatDraper) Attribution() string { return "© Test map" }
func (flatDraper) Name() string        { return "flat/test" }

func (d flatDraper) Drape(_ context.Context, cam perspective.Camera, o perspective.Options, ex float64) (*perspective.Picture, bool, error) {
	v := cam.MapView(cam.MapBounds(float64(o.Width)/float64(o.Height)), o.Height, 512)
	img := image.NewRGBA(image.Rect(0, 0, v.Width, v.Height))
	draw.Draw(img, img.Bounds(), image.NewUniform(d.ground), image.Point{}, draw.Src)
	pic, err := perspective.Render(perspective.Scene{Map: img, View: v, Heights: perspective.Level, Exaggeration: ex}, cam, o)
	return pic, true, err
}

func route3DContext(t *testing.T, base time.Time, basemap tilemap.Provider) *Context {
	t.Helper()
	ctx := routeContext(t, squareTrack(base, 400), 600, 400)
	ctx.Basemap = basemap
	ctx.Route3D = &Route3D{AutoHeading: true, Pitch: 35, Exaggeration: 1, Air: color.RGBA{A: 0xff}}
	return ctx
}

// In 3d the draped map fills the box, the route over it fills in as the
// activity goes, the dot is drawn, and a frame drawn twice is drawn the same
// -- Dynamic leaves nothing behind it. The summary's note says where the
// camera stood, and that the ground is level for want of elevation.
func TestRoute3D_DrawsTheDrapedMapAndARouteThatFillsIn(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ground := color.RGBA{R: 0x50, G: 0x80, B: 0x50, A: 0xff}
	ctx := route3DContext(t, base, flatDraper{ground: ground})
	box := Box{W: 600, H: 400}
	p := RoutePanel{}.Prepare(ctx, box)
	p3, ok := p.(*route3DPainter)
	if !ok {
		t.Fatalf("Prepare returned a %T, not the 3d painter; note: %q", p, p.(BasemapReporter).BasemapNote())
	}
	if note := p3.BasemapNote(); !strings.Contains(note, "drawn in 3d, looking") || !strings.Contains(note, "drawn level") {
		t.Errorf("note %q", note)
	}

	img := image.NewRGBA(image.Rect(0, 0, 600, 400))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	frame := func(offset time.Duration) *image.RGBA {
		c.Fill(c.Theme.Background)
		p.Static(c)
		p.Dynamic(c, Frame{At: base.Add(offset)})
		return cloneRGBA(img)
	}

	first := frame(0)
	if n := countNear(first, ground, 40); n < 600*400/4 {
		t.Errorf("only %d pixels of the draped map's colour; the map is not filling the box", n)
	}
	var bright []int
	for _, s := range []time.Duration{1, 100, 200, 300, 399} {
		bright = append(bright, countNear(frame(s*time.Second), c.Theme.Foreground, 12))
	}
	for i := 1; i < len(bright); i++ {
		if bright[i] <= bright[i-1] {
			t.Errorf("covered pixels went %d -> %d; the route is not filling in", bright[i-1], bright[i])
		}
	}
	mid := frame(200 * time.Second)
	if countNear(mid, c.Theme.Accent, 12) == 0 {
		t.Error("no position dot")
	}
	if again := frame(200 * time.Second); countDifferingPixels(mid, again) != 0 {
		t.Error("the same frame drawn twice came out different")
	}
}

// A basemap that cannot drape -- a tile service's flat pictures carry no
// ground -- draws the flat route, and its note says why, so a 3d render that
// came out flat is explained in the summary rather than looking like a fault.
func TestRoute3D_FallsBackToFlatSayingWhy(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx := route3DContext(t, base, &patternedBasemap{})
	p := RoutePanel{}.Prepare(ctx, Box{W: 600, H: 400})
	flat, ok := p.(*routePainter)
	if !ok {
		t.Fatalf("Prepare returned a %T, not the flat painter", p)
	}
	if note := flat.BasemapNote(); !strings.HasPrefix(note, "drawn flat: a 3d route needs") {
		t.Errorf("note %q", note)
	}
}

// A stretch of route a hill hides is drawn, faintly, rather than left out --
// left out, the line breaks off at the ridge as if the recording stopped --
// and a stretch outside the picture is not drawn at all.
func TestRoute3D_HiddenStretchIsDrawnFaintly(t *testing.T) {
	ink := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	p := &route3DPainter{
		xs:   []float64{20, 100, 180, 260, 340, 420, 500},
		ys:   []float64{100, 100, 100, 100, 100, 100, 100},
		in:   []bool{true, true, true, true, true, false, false},
		seen: []bool{true, true, true, false, false, false, false},
	}
	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	faces, _ := NewFaceCache()
	c, err := NewCanvas(img, 20, DefaultTheme(), faces)
	if err != nil {
		t.Fatal(err)
	}
	c.Fill(color.Black)
	p.stroke(c, 0, len(p.xs)-1, 6, ink, 1)

	brightness := func(x int) uint8 { return img.RGBAAt(x, 100).R }
	seen, hidden, outside := brightness(60), brightness(300), brightness(460)
	if seen < 0xf0 {
		t.Errorf("a seen stretch is drawn at %d, not full strength", seen)
	}
	if hidden == 0 || hidden >= seen/2 {
		t.Errorf("a hidden stretch is drawn at %d against %d seen: want faint, but there", hidden, seen)
	}
	if outside != 0 {
		t.Errorf("a stretch outside the picture is drawn, at %d", outside)
	}
}
