package panel

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/route"
)

// The pins' own inks, as osmbase draws them.
var (
	pinGreen = color.RGBA{R: 0x1E, G: 0x9E, B: 0x4A, A: 0xff}
	pinRed   = color.RGBA{R: 0xD6, G: 0x28, B: 0x3A, A: 0xff}
)

// The route view marks its start and finish with pins unless told not to,
// and a route that ends where it began -- the square does -- with one pin,
// green and red; the pins stay inside the panel's box.
func TestRoutePanel_EndPins(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		ends RouteEnds
		want bool
	}{{EndsPin, true}, {EndsNone, false}} {
		ctx := routeContext(t, squareTrack(base, 400), 800, 800)
		ctx.RouteEnds = c.ends
		img := image.NewRGBA(image.Rect(0, 0, 800, 800))
		faces, _ := NewFaceCache()
		cv, _ := NewCanvas(img, 20, DefaultTheme(), faces)
		box := Box{X: 100, Y: 100, W: 400, H: 400}
		p := RoutePanel{}.Prepare(ctx, box)
		cv.Fill(cv.Theme.Background)
		p.Dynamic(cv, Frame{At: base.Add(50 * time.Second)})
		green, red := countNear(img, pinGreen, 12), countNear(img, pinRed, 12)
		if got := green > 0 && red > 0; got != c.want {
			t.Errorf("ends %d: %d green and %d red pixels, want pins %v", c.ends, green, red, c.want)
		}
		inBox := countNearInBox(img, box, pinGreen, 12) + countNearInBox(img, box, pinRed, 12)
		if inBox != green+red {
			t.Errorf("ends %d: %d pin pixels outside the panel's box", c.ends, green+red-inBox)
		}
	}
}

// The direction an arrow or a plane points is the file's own course where
// it records one, the route's from its fixes otherwise, turned into the
// picture -- here one with north up -- and none where nothing moved.
func TestTravelHeading(t *testing.T) {
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	var east, still []route.Point
	for i := range 120 {
		at := base.Add(time.Duration(i) * time.Second)
		east = append(east, route.Point{Lat: 55, Lon: 12 + float64(i)*0.0002, Time: at})
		still = append(still, route.Point{Lat: 55, Lon: 12, Time: at})
	}
	northUp := func(p route.Point) (float64, float64, bool) { return p.Lon * 1e5, -p.Lat * 1e5 * 1.74, true }
	at := base.Add(time.Minute)
	pos := east[60]
	for _, c := range []struct {
		name   string
		f      Frame
		fixes  []route.Point
		want   float64
		wantOK bool
	}{
		{"from the fixes", Frame{At: at}, east, 90, true},
		{"the file's own course", Frame{At: at, HasSample: true, Sample: fitactivity.Sample{HasCourse: true, Course: 180}}, east, 180, true},
		{"standing still", Frame{At: at}, still, 0, false},
	} {
		got, ok := travelHeading(c.f, c.fixes, pos, northUp)
		if ok != c.wantOK || ok && (got-c.want > 2 || c.want-got > 2) {
			t.Errorf("%s: %.1f° (ok %v), want %v° (ok %v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestParseRouteMarks(t *testing.T) {
	for _, c := range []struct {
		s      string
		flight bool
		want   RouteIcon
	}{
		{"auto", true, IconPlane}, {"auto", false, IconDot},
		{"dot", true, IconDot}, {"arrow", false, IconArrow}, {"plane", false, IconPlane},
	} {
		if got, err := ParseRouteIcon(c.s, c.flight); err != nil || got != c.want {
			t.Errorf("--route-position %s (flight %v): %v %v, want %v", c.s, c.flight, got, err, c.want)
		}
	}
	if _, err := ParseRouteIcon("rocket", false); err == nil {
		t.Error("--route-position rocket accepted")
	}
	if e, err := ParseRouteEnds("pin"); err != nil || e != EndsPin {
		t.Errorf("--route-ends pin: %v %v", e, err)
	}
	if e, err := ParseRouteEnds("none"); err != nil || e != EndsNone {
		t.Errorf("--route-ends none: %v %v", e, err)
	}
	if _, err := ParseRouteEnds("flag"); err == nil {
		t.Error("--route-ends flag accepted")
	}
}

// With pins, the whole route's view is framed with room above it for a pin's
// height -- the camera stands further back -- and without, as before; a
// tall picture's pins are smaller against its height and need less.
func TestRoute3DCameraKeepsRoomForPins(t *testing.T) {
	var pts []route.Point
	for i := range 50 {
		pts = append(pts, route.Point{Lat: 55 + float64(i)*0.002, Lon: 12})
	}
	r := Route3D{Pitch: 35, Heading: 90}
	plain, err := r.Camera(pts, 16.0/9)
	if err != nil {
		t.Fatal(err)
	}
	r.PinRoom = true
	roomy, err := r.Camera(pts, 16.0/9)
	if err != nil {
		t.Fatal(err)
	}
	if !(roomy.Distance > plain.Distance) {
		t.Errorf("framed with room for pins from %.0f m, without from %.0f m; want further back", roomy.Distance, plain.Distance)
	}
	if wide, tall := pinRoom(16.0/9), pinRoom(9.0/16); !(wide > tall) || wide <= 0 {
		t.Errorf("room for pins %.3f wide and %.3f tall; want some, and less for a tall picture", wide, tall)
	}
}
