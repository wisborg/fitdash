package tilemap

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// pngBytes is a tiny valid PNG a fake service can answer with.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeService stands in for api.thunderforest.com, recording what was asked
// of it so a test can assert on the request rather than only the reply.
func fakeService(t *testing.T, handler http.HandlerFunc) (*Thunderforest, *[]*http.Request) {
	t.Helper()
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(context.Background()))
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	old := thunderforestBase
	thunderforestBase = srv.URL
	t.Cleanup(func() { thunderforestBase = old })

	return &Thunderforest{Key: Key(fakeKey), Style: "outdoors", Client: srv.Client()}, &got
}

func aView() View {
	return View{North: 55.71, West: 12.50, South: 55.69, East: 12.54, Width: 400, Height: 300}
}

// TestThunderforest_Image fetches one view and checks the request is the one
// the service documents: centre, integer zoom, size, style and the key.
func TestThunderforest_Image(t *testing.T) {
	tf, reqs := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 8, 8))
	})

	img, err := tf.Image(context.Background(), aView())
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	if img == nil {
		t.Fatal("no image")
	}
	if len(*reqs) != 1 {
		t.Fatalf("made %d requests for one view, want exactly 1", len(*reqs))
	}

	r := (*reqs)[0]
	if !strings.Contains(r.URL.Path, "/static/outdoors/") {
		t.Errorf("path %q does not name the static endpoint and the style", r.URL.Path)
	}
	if got := r.URL.Query().Get("apikey"); got != fakeKey {
		t.Errorf("apikey = %q, want the key", got)
	}
	if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "fitdash") {
		t.Errorf("User-Agent = %q; tile services block generic client defaults outright", ua)
	}
}

// TestThunderforest_ErrorsNeverCarryTheKey is the security test this whole
// design exists for.
//
// The key is a QUERY PARAMETER, so it is inside the URL -- and net/http wraps
// transport failures in a *url.Error that carries the whole URL. Wrapping one
// with %w prints the secret into the render summary the first time a map
// service is unreachable, which is the most ordinary failure there is.
func TestThunderforest_ErrorsNeverCarryTheKey(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		close   bool
	}{
		{"the service refuses the key", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }, false},
		{"the service rate-limits", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, false},
		{"the service answers with rubbish", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not an image")) }, false},
		{"the service is unreachable", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.handler
			if h == nil {
				h = func(w http.ResponseWriter, r *http.Request) {}
			}
			tf, _ := fakeService(t, h)
			if c.close {
				// Point at a port nothing is listening on, so the failure is
				// a real transport error carrying a real URL.
				thunderforestBase = "http://127.0.0.1:1"
			}

			_, err := tf.Image(context.Background(), aView())
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), fakeKey) {
				t.Errorf("the error carries the API key: %v", err)
			}
		})
	}
}

// TestThunderforest_StatusMessagesAreActionable pins that the two failures a
// user can actually do something about say what to do. "HTTP 403" does not
// tell somebody their key is wrong.
func TestThunderforest_StatusMessagesAreActionable(t *testing.T) {
	for _, c := range []struct {
		code int
		want string
	}{
		{http.StatusForbidden, "key"},
		{http.StatusUnauthorized, "key"},
		{http.StatusTooManyRequests, "rate-limit"},
	} {
		tf, _ := fakeService(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(c.code) })
		_, err := tf.Image(context.Background(), aView())
		if err == nil {
			t.Fatalf("HTTP %d produced no error", c.code)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("HTTP %d says %q, which does not mention %q", c.code, err, c.want)
		}
	}
}

// TestThunderforest_NoKeyFailsBeforeAnyRequest keeps a keyless run from
// bothering the service at all.
func TestThunderforest_NoKeyFailsBeforeAnyRequest(t *testing.T) {
	tf, reqs := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 8, 8))
	})
	tf.Key = ""

	if _, err := tf.Image(context.Background(), aView()); err == nil {
		t.Fatal("a keyless provider fetched anyway")
	}
	if len(*reqs) != 0 {
		t.Errorf("made %d requests without a key", len(*reqs))
	}
}

// TestPlanStatic_ChoosesAZoomThatFits pins the arithmetic that turns a
// bounding box into the centre-and-zoom request the endpoint takes.
//
// Rounding the zoom DOWN is the load-bearing part: one zoom too deep needs
// four times the pixels and can be refused outright, while one too shallow is
// merely softer than it could be.
func TestPlanStatic_ChoosesAZoomThatFits(t *testing.T) {
	cases := []struct {
		name string
		view View
	}{
		{"a city block", View{North: 55.6805, West: 12.5700, South: 55.6795, East: 12.5720, Width: 400, Height: 300}},
		{"a long run", View{North: 55.80, West: 12.40, South: 55.60, East: 12.70, Width: 800, Height: 600}},
		{"a whole country", View{North: 58.0, West: 8.0, South: 54.5, East: 15.5, Width: 1200, Height: 900}},
		{"a tall narrow box", View{North: 55.80, West: 12.50, South: 55.60, East: 12.52, Width: 200, Height: 900}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := planStatic(c.view, thunderforestMaxPixels, thunderforestMaxZoom)
			if err != nil {
				t.Fatalf("planStatic: %v", err)
			}
			if plan.Width <= 0 || plan.Height <= 0 {
				t.Fatalf("planned a %dx%d canvas", plan.Width, plan.Height)
			}
			if plan.Width > thunderforestMaxPixels || plan.Height > thunderforestMaxPixels {
				t.Errorf("planned %dx%d, past the endpoint's %d ceiling", plan.Width, plan.Height, thunderforestMaxPixels)
			}
			if plan.Zoom < 0 || plan.Zoom > thunderforestMaxZoom {
				t.Errorf("planned zoom %d, outside 0..%d", plan.Zoom, thunderforestMaxZoom)
			}
			// The centre must be inside the view it was computed from.
			if plan.CentreLat > c.view.North || plan.CentreLat < c.view.South ||
				plan.CentreLon < c.view.West || plan.CentreLon > c.view.East {
				t.Errorf("centre (%v, %v) lies outside the view", plan.CentreLat, plan.CentreLon)
			}
		})
	}
}

// TestPlanStatic_CanvasCoversTheView is the property a crop depends on: the
// image must be at least as large as the rectangle asked for, or the panel
// gets a transparent edge where the map ran out.
func TestPlanStatic_CanvasCoversTheView(t *testing.T) {
	v := aView()
	plan, err := planStatic(v, thunderforestMaxPixels, thunderforestMaxZoom)
	if err != nil {
		t.Fatalf("planStatic: %v", err)
	}
	x0, y0 := Project(v.North, v.West)
	x1, y1 := Project(v.South, v.East)
	world := tileSize * math.Exp2(float64(plan.Zoom))

	if wantW := (x1 - x0) * world; float64(plan.Width) < wantW-1e-9 {
		t.Errorf("canvas is %d wide, short of the %v the view needs", plan.Width, wantW)
	}
	if wantH := (y1 - y0) * world; float64(plan.Height) < wantH-1e-9 {
		t.Errorf("canvas is %d tall, short of the %v the view needs", plan.Height, wantH)
	}
}

// TestPlanStatic_RefusesAViewWithNoExtent covers the degenerate inputs, which
// must not become a request.
func TestPlanStatic_RefusesAViewWithNoExtent(t *testing.T) {
	base := aView()
	cases := map[string]View{
		"no pixels":       {North: 55.7, West: 12.5, South: 55.6, East: 12.6},
		"reversed bounds": {North: 55.6, West: 12.5, South: 55.7, East: 12.6, Width: 100, Height: 100},
		"a single point":  {North: 55.7, West: 12.5, South: 55.7, East: 12.5, Width: 100, Height: 100},
	}
	for name, v := range cases {
		if _, err := planStatic(v, thunderforestMaxPixels, thunderforestMaxZoom); err == nil {
			t.Errorf("%s: planStatic accepted %+v", name, v)
		}
	}
	if _, err := planStatic(base, thunderforestMaxPixels, thunderforestMaxZoom); err != nil {
		t.Errorf("planStatic refused an ordinary view: %v", err)
	}
}

// TestRedact covers the helper the error path leans on, including the shapes
// a URL is not.
func TestRedact(t *testing.T) {
	k := Key(fakeKey)
	in := "Get \"https://api.thunderforest.com/static/outdoors/1,2,3/4x5.png?apikey=" + fakeKey + "\": dial tcp: refused"
	got := redact(in, k)
	if strings.Contains(got, fakeKey) {
		t.Errorf("redact left the key in: %s", got)
	}
	if !strings.Contains(got, redacted) {
		t.Errorf("redact removed the key without marking it: %s", got)
	}
	// Twice in one message, which a retrying transport can produce.
	twice := in + " (and again: apikey=" + fakeKey + ")"
	if strings.Contains(redact(twice, k), fakeKey) {
		t.Error("redact replaced only the first occurrence")
	}
	// An empty key must not turn every message into redactions.
	if got := redact("nothing secret here", ""); got != "nothing secret here" {
		t.Errorf("redact with no key changed the message: %s", got)
	}
}

// TestRedact_HandlesTheEncodedFormsToo covers the leak the plain-hex fixture
// used by every other test here cannot reach.
//
// The key goes into the URL through url.Values.Encode, which percent-encodes
// it. A key containing a plus, a slash, an equals or a space therefore appears
// in a *url.Error's message in its ENCODED form -- and a redactor matching
// only the raw value sails straight past it and prints the secret.
// Thunderforest's own keys are hexadecimal and encode to themselves, which is
// exactly why this is easy to get wrong and impossible to notice: it would
// leak only for a different key format, or a different service.
func TestRedact_HandlesTheEncodedFormsToo(t *testing.T) {
	awkward := Key("ab+cd/ef=gh ij")
	raw := awkward.reveal()
	encoded := url.QueryEscape(raw)
	if encoded == raw {
		t.Fatal("this fixture's key encodes to itself, so the test proves nothing")
	}

	msg := `Get "https://api.thunderforest.com/static/outdoors/1,2,3/4x5.png?apikey=` + encoded + `": dial tcp: refused`
	got := redact(msg, awkward)
	if strings.Contains(got, encoded) {
		t.Errorf("redact left the percent-encoded key in: %s", got)
	}
	if strings.Contains(got, raw) {
		t.Errorf("redact left the raw key in: %s", got)
	}
}

// TestThunderforest_AwkwardKeyNeverLeaks is the same property end to end,
// through the real request path where the encoding actually happens.
func TestThunderforest_AwkwardKeyNeverLeaks(t *testing.T) {
	tf, _ := fakeService(t, func(w http.ResponseWriter, r *http.Request) {})
	tf.Key = Key("ab+cd/ef=gh ij")
	thunderforestBase = "http://127.0.0.1:1" // unreachable, so the error carries a URL

	_, err := tf.Image(context.Background(), aView())
	if err == nil {
		t.Fatal("no error")
	}
	raw := tf.Key.reveal()
	if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), url.QueryEscape(raw)) {
		t.Errorf("the error carries the API key: %v", err)
	}
}

// TestThunderforest_RefusesAnImageLargerThanTheEndpointCanProduce closes a
// decompression bomb.
//
// The response body is capped in BYTES, but compressed size and decoded size
// are only loosely related: a PNG of one flat colour a few kilobytes long can
// declare a width and height whose product is billions of pixels, and Go's
// decoders enforce no maximum. Without a dimension check, image.Decode would
// faithfully try to allocate it -- so anything able to answer this request
// could exhaust the process.
func TestThunderforest_RefusesAnImageLargerThanTheEndpointCanProduce(t *testing.T) {
	// A huge, almost entirely empty PNG: tiny on the wire, enormous decoded.
	//
	// The size is a LITERAL rather than maxImageEdge+1, so that weakening the
	// bound is a mutation this test can actually catch. Deriving the fixture
	// from the constant under test moves the fixture with it -- and in this
	// case moves it to a seventeen-gigabyte allocation, which is its own
	// answer but not the one being asked for.
	const oversized = 8000 // comfortably past 2 * thunderforestMaxPixels
	huge := image.NewRGBA(image.Rect(0, 0, oversized, 4))
	var body bytes.Buffer
	if err := png.Encode(&body, huge); err != nil {
		t.Fatal(err)
	}
	t.Logf("declared %dx%d in %d compressed bytes", huge.Bounds().Dx(), huge.Bounds().Dy(), body.Len())

	tf, _ := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body.Bytes())
	})

	_, err := tf.Image(context.Background(), aView())
	if err == nil {
		t.Fatal("an oversized image was accepted")
	}
	if !strings.Contains(err.Error(), "past anything this endpoint can legitimately return") {
		t.Errorf("error does not explain the refusal: %v", err)
	}

	// The ordinary case must still pass, or the bound is simply a ban.
	ok, _ := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 64, 64))
	})
	if _, err := ok.Image(context.Background(), aView()); err != nil {
		t.Errorf("an ordinary image was refused: %v", err)
	}
}

// viewFor builds a View the way the panel builds one: a rectangle whose
// PROJECTED aspect matches the pixel box it will fill.
//
// That property is not incidental, it is guaranteed by route.Projection's
// CoverBox, and a test that hand-writes geographic bounds instead is testing
// a shape this program never produces -- one whose two axes want different
// zooms, where any choice is short on one of them.
func viewFor(lat, lon, spanX float64, w, h int) View {
	cx, cy := Project(lat, lon)
	spanY := spanX * float64(h) / float64(w)
	north, west := Unproject(cx-spanX/2, cy-spanY/2)
	south, east := Unproject(cx+spanX/2, cy+spanY/2)
	return View{North: north, West: west, South: south, East: east, Width: w, Height: h}
}

// TestPlanStatic_NeverAsksForLessDetailThanTheBoxNeeds pins the fix for a
// map that was drawn visibly soft.
//
// The zoom was rounded DOWN, which reads as the safe direction and is not:
// it guarantees a canvas smaller than the box it fills, by up to half on
// each axis, so the imagery is upscaled. Measured at 0.70 of the requested
// size on an ordinary run -- a 1368-pixel-wide panel drawn from a
// 953-pixel image -- with nothing the user could see or change.
func TestPlanStatic_NeverAsksForLessDetailThanTheBoxNeeds(t *testing.T) {
	const oneDegree = 1.0 / 360.0
	cases := []struct {
		name string
		view View
	}{
		{"a short run in a wide panel", viewFor(55.69, 12.53, oneDegree/60, 1368, 336)},
		{"the same at 4K", viewFor(55.69, 12.53, oneDegree/60, 2736, 672)},
		{"a long activity", viewFor(55.70, 12.50, oneDegree/3, 1368, 336)},
		{"a portrait panel", viewFor(55.69, 12.52, oneDegree/40, 995, 480)},
		{"a square panel", viewFor(55.69, 12.53, oneDegree/50, 900, 900)},
		{"a tiny panel", viewFor(55.69, 12.53, oneDegree/200, 200, 150)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := planStatic(c.view, thunderforestMaxPixels, thunderforestMaxZoom)
			if err != nil {
				t.Fatalf("planStatic: %v", err)
			}
			// Either the canvas already covers the box, or the endpoint's
			// own ceiling stopped it -- in which case @2x is what buys the
			// detail back, and the plan must be AT that ceiling rather than
			// short of it for no reason.
			short := plan.Width < c.view.Width || plan.Height < c.view.Height
			atCeiling := plan.Width*2 > thunderforestMaxPixels || plan.Height*2 > thunderforestMaxPixels
			if short && !atCeiling {
				t.Errorf("planned a %dx%d canvas for a %dx%d box with the %d ceiling nowhere near: the map would be "+
					"upscaled for no reason", plan.Width, plan.Height, c.view.Width, c.view.Height, thunderforestMaxPixels)
			}
		})
	}
}

// TestThunderforest_DoublesTheScaleWhenTheCeilingForcesASmallCanvas covers
// the other half: where a deeper zoom cannot fit, @2x returns twice the
// pixels for the same requested size, so the ceiling costs no detail.
func TestThunderforest_DoublesTheScaleWhenTheCeilingForcesASmallCanvas(t *testing.T) {
	huge := viewFor(55.5, 12.5, 1.0/360.0, 2400, 2400)
	plan, err := planStatic(huge, thunderforestMaxPixels, thunderforestMaxZoom)
	if err != nil {
		t.Fatalf("planStatic: %v", err)
	}
	if !(plan.Width < huge.Width || plan.Height < huge.Height) {
		t.Skip("this fixture no longer hits the endpoint's ceiling, so it cannot exercise the doubling")
	}

	tf, reqs := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 8, 8))
	})
	if _, err := tf.Image(context.Background(), huge); err != nil {
		t.Fatalf("Image: %v", err)
	}
	if got := (*reqs)[0].URL.Path; !strings.Contains(got, "@2x") {
		t.Errorf("path %q does not ask for @2x, so the ceiling costs detail the endpoint would have given", got)
	}

	// And a view the ceiling does not bind must NOT pay for the extra bytes.
	small := viewFor(55.695, 12.51, 1.0/360.0/200, 400, 300)
	tf2, reqs2 := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 8, 8))
	})
	if _, err := tf2.Image(context.Background(), small); err != nil {
		t.Fatalf("Image: %v", err)
	}
	if got := (*reqs2)[0].URL.Path; strings.Contains(got, "@2x") {
		t.Errorf("path %q asks for @2x on a view that already has the detail it needs", got)
	}
}
