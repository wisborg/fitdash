package tilemap

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // registered so a service answering with JPEG still decodes
	_ "image/png"  // the format actually requested
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ThunderforestProvider is the name a key file uses for this service.
const ThunderforestProvider = "thunderforest"

// ThunderforestStyles are the map styles this provider offers, in the order a
// help message should list them.
//
// outdoors leads because it is the one this program exists for: it draws
// contours, paths and trail furniture, which is what a run or a ride actually
// happened on. A general-purpose city style renders a forest trail as blank
// green.
var ThunderforestStyles = []string{
	"outdoors", "landscape", "cycle", "transport", "transport-dark", "atlas",
	"spinal-map", "pioneer", "mobile-atlas", "neighbourhood",
}

// thunderforestMaxPixels is the largest image the static endpoint will
// return on either axis.
const thunderforestMaxPixels = 2560

// thunderforestMaxZoom is the deepest zoom the service serves.
const thunderforestMaxZoom = 22

// Thunderforest fetches imagery from api.thunderforest.com.
//
// It uses the STATIC endpoint -- one request for one view -- rather than
// assembling a mosaic of z/x/y tiles. That is a fit for how this is used
// rather than a general preference: a caller here resolves every view it will
// ever draw before the first frame, so a view is a single fixed rectangle,
// and one request beats the nine a tile mosaic would take to cover the same
// box. It also means no seam handling and no tile cache to reason about.
type Thunderforest struct {
	Key   Key
	Style string

	// Client is the HTTP client to use. A nil Client uses a package default
	// with a timeout: a render must not hang forever on a map service that
	// has stopped answering, since the basemap is decoration and the render
	// is the product.
	Client *http.Client

	// Scale requests double-resolution imagery when true, which is worth it
	// for a large panel and wasteful for a small one.
	Scale2x bool
}

// Attribution is what must appear in any frame this imagery is drawn into.
//
// It is on the Provider interface rather than left to the caller because
// there is no version of using this service that does not owe it: Thunderforest's
// terms require credit to both the renderer and the data, and the data's
// ODbL requires the second half regardless of who rendered it. A provider
// that could be added without supplying this would be a provider whose
// obligations nobody had read.
func (t *Thunderforest) Attribution() string {
	return "Maps © Thunderforest, Data © OpenStreetMap contributors"
}

// Name identifies this provider AND its style.
//
// The style is part of the identity rather than a detail beside it, because
// the name is what a cache keys on: two styles of one service are two
// different pictures of the same ground, and a name that omitted the style
// would serve a Landscape render out of an Outdoors cache.
func (t *Thunderforest) Name() string {
	style := t.Style
	if style == "" {
		style = ThunderforestStyles[0]
	}
	return ThunderforestProvider + "/" + style
}

// Image fetches imagery covering v.
//
// The static endpoint takes a CENTRE and an integer zoom, not a bounding box,
// so the view is resolved the other way round: pick the deepest integer zoom
// whose rendering of v still fits inside the endpoint's own pixel ceiling,
// ask for a canvas large enough to cover v at that zoom, and let the caller
// crop. Rounding the zoom DOWN rather than up is deliberate -- one zoom too
// deep can need four times the pixels and be refused, while one too shallow
// is merely softer than it could be.
func (t *Thunderforest) Image(ctx context.Context, v View) (image.Image, error) {
	if t.Key.Empty() {
		return nil, fmt.Errorf("thunderforest: no API key")
	}
	style := t.Style
	if style == "" {
		style = ThunderforestStyles[0]
	}

	req, err := t.request(ctx, v, style)
	if err != nil {
		return nil, err
	}

	res, err := t.client().Do(req)
	if err != nil {
		// NEVER %w a transport error here. net/http wraps failures in a
		// *url.Error that carries the whole URL, and the URL carries the key
		// -- so passing it through would print the secret into the render
		// summary the first time a map service was unreachable. redact takes
		// the message and puts [redacted] where the key was.
		return nil, fmt.Errorf("thunderforest: fetching %s imagery: %s", style, redact(err.Error(), t.Key))
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thunderforest: fetching %s imagery: %s", style, describeStatus(res.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxImageBytes))
	if err != nil {
		return nil, fmt.Errorf("thunderforest: reading %s imagery: %s", style, redact(err.Error(), t.Key))
	}

	// The DIMENSIONS are checked before the pixels are allocated, and the
	// byte ceiling above is not a substitute for it. Compressed size and
	// decoded size are only loosely related: a PNG of one flat colour a few
	// kilobytes long can declare a width and height whose product is
	// billions of pixels, and the standard decoders enforce no maximum, so
	// image.Decode would faithfully try to allocate it. That turns any party
	// able to answer this request -- a spoofed response, a proxy in front of
	// the service -- into one that can exhaust this process's memory.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("thunderforest: decoding %s imagery: %w", style, err)
	}
	if cfg.Width > maxImageEdge || cfg.Height > maxImageEdge || cfg.Width*cfg.Height > maxImagePixels {
		return nil, fmt.Errorf("thunderforest: %s imagery came back %dx%d, past anything this endpoint can legitimately return",
			style, cfg.Width, cfg.Height)
	}

	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("thunderforest: decoding %s imagery: %w", style, err)
	}
	return img, nil
}

// maxImageEdge and maxImagePixels bound what will be decoded.
//
// The endpoint's own ceiling is 2560 on each axis, doubled by @2x, so
// anything past that is not imagery this program asked for. The area bound is
// the belt to that pair of braces: it is what stops a long thin image --
// within both edge limits on one axis and enormous on the other -- from
// allocating what the edges alone would allow.
const (
	maxImageEdge   = 2 * thunderforestMaxPixels
	maxImagePixels = maxImageEdge * maxImageEdge
)

// maxImageBytes bounds a response. 2560x2560 RGBA compresses well below this
// even as PNG; anything larger is not imagery this program asked for.
const maxImageBytes = 32 << 20

func (t *Thunderforest) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return defaultClient
}

var defaultClient = &http.Client{Timeout: 20 * time.Second}

// request builds the static-map request for v.
func (t *Thunderforest) request(ctx context.Context, v View, style string) (*http.Request, error) {
	plan, err := planStatic(v, thunderforestMaxPixels, thunderforestMaxZoom)
	if err != nil {
		return nil, fmt.Errorf("thunderforest: %w", err)
	}

	// Doubled when a single-scale canvas would still be smaller than the box
	// it has to fill -- which happens whenever the endpoint's own pixel
	// ceiling forces the zoom below what the panel wants, and is the only
	// case a deeper zoom cannot fix. @2x returns twice the pixels for the
	// same requested width and height, so it buys back exactly the detail
	// the ceiling took away, and the ceiling itself is unaffected because it
	// applies to what is asked for rather than to what comes back.
	//
	// Scale2x forces it on regardless, for a caller that wants the sharper
	// image whatever the arithmetic says.
	scale := ""
	if t.Scale2x || plan.Width < v.Width || plan.Height < v.Height {
		scale = "@2x"
	}
	// The path carries centre, zoom and size; the key is a query parameter,
	// which is the whole reason redact exists.
	raw := fmt.Sprintf("%s/static/%s/%.6f,%.6f,%d/%dx%d%s.png",
		thunderforestBase, style, plan.CentreLon, plan.CentreLat, plan.Zoom, plan.Width, plan.Height, scale)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("thunderforest: building the request: %w", err)
	}
	q := u.Query()
	q.Set("apikey", t.Key.reveal())
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("thunderforest: building the request: %s", redact(err.Error(), t.Key))
	}
	req.Header.Set("User-Agent", UserAgent)
	return req, nil
}

var thunderforestBase = "https://api.thunderforest.com"

// UserAgent identifies this program to a map service.
//
// A distinctive one is not politeness. Tile services block generic HTTP
// client defaults outright -- the OpenStreetMap Foundation's policy names
// "okhttp/x.y" as an example of what it refuses -- so a program that did not
// set one would be indistinguishable from the traffic those rules exist to
// stop.
var UserAgent = "fitdash (+https://github.com/wisborg/fitdash)"

// redact replaces every occurrence of the key in s, so a message built by
// somebody else's code can still be reported.
//
// Substring replacement rather than URL parsing on purpose: the message may
// not be a URL, may be a URL inside prose, or may be several. What matters is
// that the key does not survive, whatever shape the text is.
//
// THE ESCAPED FORMS MATTER AS MUCH AS THE RAW ONE. The key goes into the URL
// through url.Values.Encode, which percent-encodes it -- so a key containing
// a plus, a slash, an equals or a space appears in a *url.Error's message in
// its ENCODED form, and a literal match against the raw value would sail past
// it and print the secret. Thunderforest's own keys are hexadecimal and would
// never encode differently, which is exactly why this is easy to get wrong
// and impossible to notice: it would leak only for a service, or a future
// key format, that uses a character outside the unreserved set.
func redact(s string, k Key) string {
	if k.Empty() {
		return s
	}
	raw := k.reveal()
	s = strings.ReplaceAll(s, raw, redacted)
	for _, escaped := range []string{url.QueryEscape(raw), url.PathEscape(raw)} {
		if escaped != raw {
			s = strings.ReplaceAll(s, escaped, redacted)
		}
	}
	return s
}

// describeStatus turns an HTTP status into something a user can act on. The
// two that matter get their own words, because "403" does not tell somebody
// their key is wrong and "429" does not tell them to wait.
func describeStatus(code int) string {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("the service refused the API key (HTTP %d) -- check the key file, and that the plan covers static maps", code)
	case http.StatusTooManyRequests:
		return "the service is rate-limiting this key (HTTP 429)"
	default:
		return fmt.Sprintf("HTTP %d", code)
	}
}

// staticPlan is a resolved request: where to centre, how deep to zoom, and
// how large a canvas to ask for.
type staticPlan struct {
	CentreLat, CentreLon float64
	Zoom                 int
	Width, Height        int
}

// planStatic resolves v into a request the static endpoint will accept.
//
// The canvas is sized to cover v at the chosen zoom and then rounded UP, so
// the caller always has at least the rectangle it asked for to crop from --
// an image a pixel short leaves a transparent edge along the panel, which is
// far more visible than one pixel of waste.
func planStatic(v View, maxPixels, maxZoom int) (staticPlan, error) {
	if v.Width <= 0 || v.Height <= 0 {
		return staticPlan{}, fmt.Errorf("a view with no size (%dx%d)", v.Width, v.Height)
	}
	west, east := v.West, v.East
	north, south := v.North, v.South
	if east <= west || north <= south {
		return staticPlan{}, fmt.Errorf("a view with no extent")
	}

	x0, y0 := Project(north, west)
	x1, y1 := Project(south, east)
	spanX, spanY := x1-x0, y1-y0
	if spanX <= 0 || spanY <= 0 {
		return staticPlan{}, fmt.Errorf("a view with no extent once projected")
	}

	// The zoom that would render v at the caller's own pixel size, on
	// whichever axis needs the most detail.
	zx, okx := ZoomFor(spanX, v.Width)
	zy, oky := ZoomFor(spanY, v.Height)
	if !okx || !oky {
		return staticPlan{}, fmt.Errorf("a view with no extent once projected")
	}
	// Rounded UP, then stepped back down by the loop below until the canvas
	// fits. Rounding down instead -- which this did, and which reads as the
	// safe direction -- guarantees a canvas SMALLER than the box it will
	// fill, by anything up to half on each axis, so the map is upscaled and
	// visibly soft. It was measured at 0.70 of the requested size on an
	// ordinary run: a 1368-pixel-wide panel drawn from a 953-pixel image.
	//
	// Up costs bytes on one request and nothing else, and the step-down loop
	// already handles the case where it does not fit. The zoom that fits is
	// the zoom that fits; there was never a reason to start below it.
	z := int(math.Ceil(math.Min(zx, zy)))

	// Step back until the canvas fits the endpoint's ceiling. Each step down
	// halves both axes, so this terminates quickly and at worst at zoom 0.
	for ; z > 0; z-- {
		w, h := canvasFor(spanX, spanY, z)
		if w <= maxPixels && h <= maxPixels {
			break
		}
	}
	if z < 0 {
		z = 0
	}
	if z > maxZoom {
		z = maxZoom
	}

	w, h := canvasFor(spanX, spanY, z)
	if w > maxPixels {
		w = maxPixels
	}
	if h > maxPixels {
		h = maxPixels
	}

	lat, lon := Unproject((x0+x1)/2, (y0+y1)/2)
	return staticPlan{CentreLat: lat, CentreLon: lon, Zoom: z, Width: w, Height: h}, nil
}

// canvasFor is the pixel size a span occupies at a zoom, rounded up.
func canvasFor(spanX, spanY float64, z int) (int, int) {
	world := tileSize * math.Exp2(float64(z))
	return int(math.Ceil(spanX * world)), int(math.Ceil(spanY * world))
}

// View is a geographic rectangle and the pixel size it is wanted at.
type View struct {
	North, West, South, East float64 // degrees
	Width, Height            int     // pixels
}

// Provider is a source of map imagery.
//
// Deliberately small, and deliberately free of anything fitdash knows about:
// degrees in, an image out, plus the credit that must be shown for it. A
// second provider is a second implementation of these three methods and
// nothing else.
type Provider interface {
	Image(ctx context.Context, v View) (image.Image, error)
	Attribution() string
	Name() string
}

var _ Provider = (*Thunderforest)(nil)
