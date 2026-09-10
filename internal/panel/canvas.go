package panel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
)

// Theme is the palette every panel draws from.
//
// A panel never names a colour literal, for the same reason it never names a
// pixel constant: eight panels each picking their own grey is eight programs
// in one frame. Anything a panel wants to say -- this is a reading, this is
// chrome, this is missing -- it says by choosing the role, and the theme
// decides what the role looks like.
type Theme struct {
	// Name identifies the theme for --theme and for the render summary.
	Name string

	// Background fills the frame. There is no source video beneath it: in
	// fitdash the background IS the product, which is also what lets a
	// declining panel leave clean background rather than a hole.
	Background color.Color

	// Foreground is a live reading -- the numbers the dashboard exists to
	// show.
	Foreground color.Color

	// Dim is chrome: axes, rules, labels, the un-covered part of a route.
	// Present, but not what the eye should land on.
	Dim color.Color

	// Accent marks the current position -- the route dot, a playhead.

	Accent color.Color

	// Absent is the colour of a placeholder: the "--" a panel draws where a
	// reading would be if the sensor had one.
	//
	// It is a distinct role rather than a reuse of Dim because the two say
	// different things. Dim means "this is chrome"; Absent means "there is no
	// data here", which is a fact about the activity rather than about the
	// drawing. A viewer has to be able to tell a missing heart rate from an
	// axis label without knowing which is which in advance, so this must stay
	// visibly different from BOTH Foreground and Background.
	Absent color.Color

	// Highlight marks a --highlight range: the highlight strip's blocks draw
	// with it, and the margin border the render loop overlays on top of a
	// render will too. A distinct role from Accent rather than a reuse of it,
	// because the two say different things and can be on screen together --
	// the strip's playhead (Accent, "the render is here") sweeps across the
	// strip's own blocks (Highlight, "a range the user chose") -- and
	// conflating them would make the current position indistinguishable from
	// a configured one.
	Highlight color.Color
}

// DefaultTheme is the palette used when none is chosen.
func DefaultTheme() Theme { return DarkTheme() }

// DarkTheme is a dark dashboard with a warm accent.
func DarkTheme() Theme {
	return Theme{
		Name:       "dark",
		Background: color.RGBA{0x0E, 0x0E, 0x10, 0xFF},
		Foreground: color.RGBA{0xF2, 0xF2, 0xF4, 0xFF},
		Dim:        color.RGBA{0x6E, 0x6E, 0x78, 0xFF},
		Accent:     color.RGBA{0xFF, 0x5A, 0x36, 0xFF},
		Absent:     color.RGBA{0x4A, 0x4A, 0x54, 0xFF},
		Highlight:  color.RGBA{0x9B, 0x6B, 0xFF, 0xFF},
	}
}

// LightTheme is the same dashboard on paper.
//
// Not the dark palette inverted. Inverting it would give a light grey chrome
// on white, which vanishes, and the dark theme's accent -- a bright
// orange chosen to glow against near-black -- reads as washed out on white
// rather than as the one thing the eye should find. Each role is picked again
// for its background, and ThemesAreLegible checks the result rather than
// trusting that it was done carefully.
func LightTheme() Theme {
	return Theme{
		Name:       "light",
		Background: color.RGBA{0xF5, 0xF4, 0xF1, 0xFF},
		Foreground: color.RGBA{0x16, 0x16, 0x1A, 0xFF},
		Dim:        color.RGBA{0x7A, 0x7A, 0x84, 0xFF},
		Accent:     color.RGBA{0xC4, 0x37, 0x14, 0xFF},
		Absent:     color.RGBA{0xBE, 0xBE, 0xC6, 0xFF},
		Highlight:  color.RGBA{0x5B, 0x3D, 0xE0, 0xFF},
	}
}

// Themes are the palettes --theme can choose, in the order they are offered.
func Themes() []Theme { return []Theme{DarkTheme(), LightTheme()} }

// SelectTheme returns the named palette.
//
// An unknown name is refused where the user typed it rather than falling back
// to the default: a typo would otherwise render an entire video in a palette
// nobody asked for, and a render takes long enough that discovering it at the
// end is a real cost.
func SelectTheme(name string) (Theme, error) {
	for _, t := range Themes() {
		if t.Name == name {
			return t, nil
		}
	}
	var names []string
	for _, t := range Themes() {
		names = append(names, t.Name)
	}
	return Theme{}, fmt.Errorf("panel: unknown theme %q; use %s", name, strings.Join(names, " or "))
}

// ContrastRatio is the WCAG 2 contrast ratio between a and b, in the range
// [1, 21] -- 1 for identical colours, 21 for pure black against pure white.
//
// This is the one legibility number in this package that is not a judgement
// call the way highlightWashStrength or highlightRestAlpha are: 4.5:1 is
// WCAG 2's own published threshold for normal text, defensible because it is
// not this project's own invention. TestThemesAreLegible uses it to check
// every shipped theme's Foreground against its Background, and
// cmd/highlight.go's --highlight background= warning uses it to check a
// user-typed colour against Theme.Foreground, Theme.Dim and Theme.Absent.
func ContrastRatio(a, b color.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance is WCAG 2's formula: each sRGB channel is linearized
// (undoing the gamma curve a display applies), then combined with the
// standard luminance weights -- green dominates human perception of
// brightness and blue barely registers, which is why the weights are not
// equal.
func relativeLuminance(c color.Color) float64 {
	nc := color.NRGBAModel.Convert(c).(color.NRGBA)
	r := linearizeSRGB(float64(nc.R) / 255)
	g := linearizeSRGB(float64(nc.G) / 255)
	b := linearizeSRGB(float64(nc.B) / 255)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// linearizeSRGB undoes sRGB's gamma encoding for one channel, per WCAG 2's
// own definition of relative luminance.
func linearizeSRGB(c float64) float64 {
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// FaceCache holds rasterized font faces, keyed by pixel size.
//
// Building a face is expensive and a render asks for the same handful of sizes
// on every one of its frames -- 45,000 times for a 25-minute activity at 30
// fps -- so the cache is not an optimisation so much as the difference between
// a render finishing and not.
//
// It is NOT goroutine-safe by contract, and the mutex below is belt and braces
// rather than a licence to share one across goroutines. Nothing in a render
// writes to it concurrently: a Painter's Dynamic method must not mutate, and
// the two Canvases of one render are used one at a time.
type FaceCache struct {
	mu    sync.Mutex
	font  *opentype.Font
	faces map[int]font.Face
}

// NewFaceCache returns a cache over the embedded monospace face.
//
// Monospace on purpose: a dashboard's numbers change every frame, and in a
// proportional face the readout jitters horizontally as digits swap width.
// That movement reads as instability in the data rather than in the typography.
func NewFaceCache() (*FaceCache, error) {
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		return nil, fmt.Errorf("panel: parsing the embedded font: %w", err)
	}
	return &FaceCache{font: f, faces: map[int]font.Face{}}, nil
}

// face returns a face at px pixels, rounded to a whole pixel.
//
// Rounding is what makes the cache effective: a size derived from a fractional
// box dimension is a different float64 on almost every call, so keying on the
// raw value would cache nothing while growing without bound. A sub-pixel
// difference in text size is not visible; the unbounded map is.
func (c *FaceCache) face(px float64) (font.Face, error) {
	if px < 1 {
		px = 1
	}
	key := int(math.Round(px))

	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.faces[key]; ok {
		return f, nil
	}
	// DPI 72 makes one point exactly one pixel, so a caller asking for a size
	// in pixels gets one -- every dimension in this package is in pixels, and
	// a points-versus-pixels conversion hiding here would silently rescale
	// every layout.
	f, err := opentype.NewFace(c.font, &opentype.FaceOptions{
		Size: float64(key), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("panel: building a %dpx font face: %w", key, err)
	}
	c.faces[key] = f
	return f, nil
}

// Canvas is the drawing surface a panel is handed.
//
// It carries the frame's dimensions, the base text size and the theme, and
// NOTHING that varies from frame to frame. That is deliberate and is one of
// the four things keeping the static layer honest: if a Canvas carried the
// per-frame state, a panel's Static method could reach frame data through the
// drawing surface, which is the same leak the phase split exists to close,
// wearing a different hat.
type Canvas struct {
	// W, H are the frame's pixel dimensions.
	W, H int

	// BasePx is the layout's base text size in pixels, already resolved from
	// its FontScale. A panel sizes its own text relative to this rather than
	// choosing pixels, so one layout reads the same at 1080p and 4K.
	BasePx float64

	// Theme is the palette. See Theme.
	Theme Theme

	// clip is the region Clipped has confined drawing to, or the zero
	// rectangle when there is none.
	//
	// gg's own clip is a mask it applies to ITS operations, and Image does
	// not go through gg -- it composites with golang.org/x/image/draw
	// straight into the frame. So a clip set for the drawing context does
	// not constrain imagery unless it is also honoured here, which is a
	// distinction with no visible symptom until something is drawn larger
	// than its box: a zoomed basemap is exactly that, and it painted over
	// the panels beside it.
	clip image.Rectangle

	// img is the frame being drawn into, kept alongside the drawing context
	// because scaled image compositing goes through golang.org/x/image/draw
	// rather than through gg -- gg can only place an image at 1:1 or under a
	// whole-context transform, and a basemap has to land in one panel's box
	// at its own scale without moving anything else.
	img *image.RGBA

	dc    *gg.Context
	faces *FaceCache
}

// NewCanvas wraps img for drawing.
//
// gg draws directly into the image's own pixels rather than into a buffer it
// copies out afterwards, so the frame a panel draws on is the frame the
// encoder pipes -- no copy per frame, which at 45,000 frames of 4K is the
// difference between a render and a memory profile.
func NewCanvas(img *image.RGBA, basePx float64, theme Theme, faces *FaceCache) (*Canvas, error) {
	if img == nil {
		return nil, fmt.Errorf("panel: no image to draw on")
	}
	if faces == nil {
		return nil, fmt.Errorf("panel: no font cache")
	}
	if basePx <= 0 {
		return nil, fmt.Errorf("panel: base text size must be positive, got %v", basePx)
	}
	b := img.Bounds()
	return &Canvas{
		W: b.Dx(), H: b.Dy(),
		BasePx: basePx,
		Theme:  theme,
		img:    img,
		dc:     gg.NewContextForRGBA(img),
		faces:  faces,
	}, nil
}

// Fill paints the whole frame.
//
// The renderer calls this once per frame before any panel draws, which is what
// makes a declining panel's absence read as clean background rather than as
// whatever the previous frame left there.
func (c *Canvas) Fill(col color.Color) {
	c.dc.SetColor(col)
	c.dc.Clear()
}

// Rect fills b.
func (c *Canvas) Rect(b Box, col color.Color) {
	c.dc.SetColor(col)
	c.dc.DrawRectangle(b.X, b.Y, b.W, b.H)
	c.dc.Fill()
}

// Image draws src scaled to fill b, at the given opacity.
//
// Scaled with CatmullRom rather than a nearest or bilinear sampler: this runs
// once per view in the ordinary case, the source is map imagery with fine
// detail and text in it, and a soft basemap is the one thing that would make
// the route panel look worse for having a map under it.
//
// opacity exists for the cross-fade a zooming route needs -- two images of
// the same ground at different scales, blended as the view moves between
// them. At 1 the image is drawn opaque; at 0 nothing is drawn at all.
func (c *Canvas) Image(src image.Image, b Box, opacity float64) {
	if src == nil || b.W <= 0 || b.H <= 0 || opacity <= 0 {
		return
	}
	dst := image.Rect(int(b.X), int(b.Y), int(b.X+b.W), int(b.Y+b.H))
	if dst.Empty() {
		return
	}

	// Confined to the clip by drawing into a SUB-IMAGE of the frame rather
	// than by shrinking dst: dst is where the imagery lands, and a zoomed
	// view deliberately puts most of it outside the box. Shrinking dst would
	// squash the picture into the box instead of showing the part of it the
	// box is looking at.
	target := c.img
	if !c.clip.Empty() {
		visible := dst.Intersect(c.clip)
		if visible.Empty() {
			return
		}
		target = c.img.SubImage(visible).(*image.RGBA)
	}

	if opacity >= 1 {
		xdraw.CatmullRom.Scale(target, dst, src, src.Bounds(), xdraw.Over, nil)
		return
	}
	xdraw.CatmullRom.Scale(target, dst, src, src.Bounds(), xdraw.Over, &xdraw.Options{
		SrcMask: image.NewUniform(color.Alpha{A: uint8(opacity*255 + 0.5)}),
	})
}

// Clipped runs draw with every drawing operation confined to b.
//
// A panel is trusted to stay inside its own box, and every panel here does so
// by construction -- it is handed a Box and computes its coordinates from it.
// The route panel's zoom breaks that: a zoomed view deliberately shows a
// fraction of the course at a scale where the rest of it falls outside the
// box, so the polyline runs off across whatever panels sit next to it. Nothing
// about the geometry can prevent that, because the overflow IS the zoom.
//
// The clip is RESET afterwards rather than restored to whatever was set
// before, and that is a real limitation rather than an oversight: gg's own
// Push/Pop deliberately does not save the mask, so there is nothing to
// restore it from without reaching past the Canvas API. Nothing in this
// program nests clips or sets a mask by any other route, so "reset" and
// "restore" are the same thing here -- but a second clip inside draw would
// leave the outer one cleared on the way out, so do not add one without
// fixing this first.
func (c *Canvas) Clipped(b Box, draw func()) {
	prev := c.clip
	c.clip = image.Rect(int(b.X), int(b.Y), int(math.Ceil(b.X+b.W)), int(math.Ceil(b.Y+b.H)))
	c.dc.DrawRectangle(b.X, b.Y, b.W, b.H)
	c.dc.Clip()
	defer func() {
		c.dc.ResetClip()
		c.clip = prev
	}()
	draw()
}

// Polyline strokes the path through the given points, which must be the same
// length. Fewer than two points draws nothing -- a single point is not a line,
// and a route with one GPS fix is a real input.
//
// Of the three conditions, only two are load-bearing and that was measured
// rather than assumed. The length-mismatch check prevents an index panic and
// its removal fails the tests. The `< 2` bound does NOT change behaviour:
// relaxing it to `< 1` still guards the empty case, and a single point becomes
// a MoveTo with no LineTo, which the underlying context strokes as nothing.
// It is kept because it states the precondition a caller should reason about
// -- a line needs two points -- rather than relying on a library's tolerance
// for a degenerate path, but no test can distinguish it and none pretends to.
func (c *Canvas) Polyline(xs, ys []float64, width float64, col color.Color) {
	if len(xs) != len(ys) || len(xs) < 2 || width <= 0 {
		return
	}
	c.dc.SetColor(col)
	c.dc.SetLineWidth(width)
	c.dc.MoveTo(xs[0], ys[0])
	for i := 1; i < len(xs); i++ {
		c.dc.LineTo(xs[i], ys[i])
	}
	c.dc.Stroke()
}

// Polygon fills the closed area bounded by the given points, which must be
// the same length. Fewer than three points draws nothing -- two points have
// no interior, and a degenerate call (a profile with no span, say) should
// cost nothing rather than fill a sliver by accident.
//
// Mirrors Polyline's guards rather than composing on top of it: a filled
// area and a stroked path are different primitives (Fill versus Stroke on
// the same path), not one built from repeated calls to the other. A column
// of 1px Rects was considered and rejected -- it needs no new primitive, but
// costs on the order of a plot's pixel width in draw calls every frame, and
// hides "fill the area under a curve" inside whichever panel wants it
// instead of putting it where drawing primitives live.
func (c *Canvas) Polygon(xs, ys []float64, col color.Color) {
	if len(xs) != len(ys) || len(xs) < 3 {
		return
	}
	c.dc.SetColor(col)
	c.dc.MoveTo(xs[0], ys[0])
	for i := 1; i < len(xs); i++ {
		c.dc.LineTo(xs[i], ys[i])
	}
	c.dc.ClosePath()
	c.dc.Fill()
}

// Circle fills a disc of radius r centred at (x, y).
func (c *Canvas) Circle(x, y, r float64, col color.Color) {
	if r <= 0 {
		return
	}
	c.dc.SetColor(col)
	c.dc.DrawCircle(x, y, r)
	c.dc.Fill()
}

// Arc strokes the circular arc of radius r centred at (cx, cy), from
// startAngle to endAngle in radians, in the standard x = r*cos(theta),
// y = r*sin(theta) convention against the image's own y-DOWN axes -- see
// dialAngle's own doc comment (gauge.go) for what that convention means for
// an angle drawn "up" on screen versus "down".
//
// The one caller today (GaugeStyleDial -- see that type's own doc comment,
// gauge.go) always draws a half-circle, but this takes
// explicit start/end angles rather than hard-coding a semicircle, on the
// same principle as Polyline and Polygon taking arbitrary point lists rather
// than a fixed shape: the primitive is the shape gg already knows how to
// stroke, not a policy about what a caller draws with it.
func (c *Canvas) Arc(cx, cy, r, startAngle, endAngle, width float64, col color.Color) {
	if r <= 0 || width <= 0 {
		return
	}
	c.dc.SetColor(col)
	c.dc.SetLineWidth(width)
	c.dc.DrawArc(cx, cy, r, startAngle, endAngle)
	c.dc.Stroke()
}

// Text draws s at px pixels, anchored at (x, y).
//
// ax and ay place the anchor within the text's own box: 0 is left/top, 0.5 is
// centre, 1 is right/bottom. A panel positioning a readout against the right
// edge of its box passes ax=1 and the box's right edge, and does not have to
// measure the string first -- which matters because the string's width changes
// every frame as the digits do.
func (c *Canvas) Text(s string, x, y, ax, ay, px float64, col color.Color) error {
	if s == "" {
		return nil
	}
	face, err := c.faces.face(px)
	if err != nil {
		return err
	}
	c.dc.SetFontFace(face)
	c.dc.SetColor(col)
	c.dc.DrawStringAnchored(s, x, y, ax, ay)
	return nil
}

// Measure returns the pixel extent s occupies at px.
//
// It lives on the cache rather than on the Canvas because a panel resolves its
// text sizes in Prepare, which has no Canvas -- and it must, for two reasons.
// A size recomputed per frame changes as the digits do, so the readout jitters
// horizontally. And a size resolved during drawing is per-frame state
// influencing what is drawn, which is exactly the kind of thing the static
// layer cannot see: chrome laid out against one size while the value drawn
// over it used another.
func (c *FaceCache) Measure(s string, px float64) (w, h float64, err error) {
	face, err := c.face(px)
	if err != nil {
		return 0, 0, err
	}
	adv := font.MeasureString(face, s)
	m := face.Metrics()
	return float64(adv) / 64, float64(m.Ascent+m.Descent) / 64, nil
}

// FitSize returns the largest size no greater than px at which s fits within
// maxW.
//
// Panels need this because a Box is a rectangle they must fit rather than an
// anchor they can grow away from: a readout comfortable at 1080p can overflow
// the same box in a portrait frame, where the width collapses.
//
// A panel should call this in Prepare with a TEMPLATE string -- the widest the
// readout can get, "88:88:88" rather than the current value -- so the size is
// fixed for the render. Sizing to the actual value would shrink and grow the
// text as the activity ran.
//
// Scaling by the measured ratio rather than searching: width is very nearly
// linear in size for a monospace face, so one measurement and one correction
// land within a pixel, where a binary search would cost a dozen measurements.
// The result never goes below one pixel -- a box too small for that is a
// layout problem, and Resolve already refuses a frame too small for its
// arrangement.
func (c *FaceCache) FitSize(s string, maxW, px float64) (float64, error) {
	if s == "" || maxW <= 0 || px <= 0 {
		return px, nil
	}
	w, _, err := c.Measure(s, px)
	if err != nil {
		return 0, err
	}
	if w <= maxW {
		return px, nil
	}
	scaled := px * maxW / w
	if scaled < 1 {
		scaled = 1
	}
	// Rounding to a whole-pixel face can push the result back over the limit,
	// and a readout one pixel wider than its box has a clipped digit.
	for scaled > 1 {
		w, _, err := c.Measure(s, scaled)
		if err != nil {
			return 0, err
		}
		if w <= maxW {
			break
		}
		scaled--
	}
	return scaled, nil
}

// Fonts exposes the face cache, for a panel measuring inside Prepare.
func (c *Canvas) Fonts() *FaceCache { return c.faces }

// MeasureText is Fonts().Measure, for a panel already holding a Canvas.
func (c *Canvas) MeasureText(s string, px float64) (w, h float64, err error) {
	return c.faces.Measure(s, px)
}

// FitTextSize is Fonts().FitSize, for a panel already holding a Canvas.
func (c *Canvas) FitTextSize(s string, maxW, px float64) (float64, error) {
	return c.faces.FitSize(s, maxW, px)
}
